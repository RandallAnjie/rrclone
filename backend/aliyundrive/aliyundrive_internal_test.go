package aliyundrive

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/aliyundrive/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/fstest/fstests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// fakeAli is an in memory Aliyun Drive open platform. Like the real
// one it lets a folder hold two files of one name, upload URLs expire
// and every 11th API call is rate limited.
type fakeAli struct {
	t         *testing.T
	srv       *httptest.Server
	mu        sync.Mutex
	files     map[string]*fakeFile
	uploads   map[string]*fakeUpload
	nextID    int
	access    string
	refresh   string
	calls     int
	refreshes int
	expired   int    // upload URLs refused as expired
	expireAt  int    // refuse the upload of this part number once
	badName   string // refuse renames to this name
}

type fakeFile struct {
	api.File
	data []byte
}

type fakeUpload struct {
	file  api.File
	parts map[int][]byte
	gen   int
}

const (
	testClientID     = "app1"
	testClientSecret = "secret1"
	driveID          = "drive-default"
)

func newFakeAli(t *testing.T) *fakeAli {
	s := &fakeAli{t: t, files: map[string]*fakeFile{}, uploads: map[string]*fakeUpload{}, access: "acc0", refresh: "ref0", expireAt: 2}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fakeAli) reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *fakeAli) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": "fake " + code})
}

func (s *fakeAli) id() string {
	s.nextID++
	return "f" + strconv.Itoa(s.nextID)
}

func (s *fakeAli) live(parent, name string, typ string) *fakeFile {
	for _, f := range s.files {
		if f.ParentFileID == parent && f.Name == name && (typ == "" || f.Type == typ) {
			return f
		}
	}
	return nil
}

func (s *fakeAli) exists(id string) bool {
	_, ok := s.files[id]
	return id == rootID || ok
}

func (s *fakeAli) removeTree(id string) {
	delete(s.files, id)
	for cid, f := range s.files {
		if f.ParentFileID == id {
			s.removeTree(cid)
		}
	}
}

func (s *fakeAli) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/dl/") {
		f, ok := s.files[strings.TrimPrefix(r.URL.Path, "/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, f.Name, time.Time{}, bytes.NewReader(f.data))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/up/") {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/up/"), "/")
		up, ok := s.uploads[parts[0]]
		n, _ := strconv.Atoi(parts[1])
		gen, _ := strconv.Atoi(r.URL.Query().Get("gen"))
		if !ok || gen != up.gen || r.Method != "PUT" {
			s.fail(w, http.StatusForbidden, "AccessDenied")
			return
		}
		if n == s.expireAt && s.expired == 0 {
			s.expired++
			s.fail(w, http.StatusForbidden, "AccessDenied") // URL expired
			return
		}
		data, _ := io.ReadAll(r.Body)
		up.parts[n] = data
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	str := func(k string) string { v, _ := body[k].(string); return v }

	if r.URL.Path == "/oauth/access_token" {
		if str("client_id") != testClientID || str("client_secret") != testClientSecret {
			s.fail(w, http.StatusBadRequest, "InvalidClient")
			return
		}
		switch {
		case str("grant_type") == "authorization_code" && str("code") == "good-code":
		case str("grant_type") == "refresh_token" && str("refresh_token") == s.refresh:
		default:
			s.fail(w, http.StatusBadRequest, "InvalidGrant")
			return
		}
		s.refreshes++
		s.access, s.refresh = fmt.Sprintf("acc%d", s.refreshes), fmt.Sprintf("ref%d", s.refreshes)
		s.reply(w, api.TokenResponse{TokenType: "Bearer", AccessToken: s.access, RefreshToken: s.refresh, ExpiresIn: 7200})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.access {
		s.fail(w, http.StatusUnauthorized, api.CodeAccessTokenInvalid)
		return
	}
	s.calls++
	if s.calls%11 == 0 {
		s.fail(w, http.StatusTooManyRequests, api.CodeTooManyRequests)
		return
	}
	if str("drive_id") != driveID && !strings.HasPrefix(r.URL.Path, "/adrive/v1.0/user/") {
		s.fail(w, http.StatusBadRequest, "InvalidParameter.DriveId")
		return
	}

	switch r.URL.Path {
	case "/adrive/v1.0/user/getDriveInfo":
		s.reply(w, api.DriveInfo{UserID: "u1", DefaultDriveID: driveID, ResourceDriveID: "drive-res"})
	case "/adrive/v1.0/user/getSpaceInfo":
		var used int64
		for _, f := range s.files {
			used += f.Size
		}
		var sp api.SpaceInfo
		sp.PersonalSpaceInfo.TotalSize, sp.PersonalSpaceInfo.UsedSize = 1<<40, used
		s.reply(w, sp)
	case "/adrive/v1.0/openFile/list":
		parent := str("parent_file_id")
		if !s.exists(parent) {
			s.fail(w, http.StatusNotFound, api.CodeNotFoundFile)
			return
		}
		var kids []*fakeFile
		for _, f := range s.files {
			if f.ParentFileID == parent {
				kids = append(kids, f)
			}
		}
		sort.Slice(kids, func(i, j int) bool { return kids[i].FileID < kids[j].FileID })
		start, _ := strconv.Atoi(str("marker"))
		limit := int(body["limit"].(float64))
		end := min(start+limit, len(kids))
		res := api.ListResponse{Items: []api.File{}}
		for _, f := range kids[min(start, len(kids)):end] {
			res.Items = append(res.Items, f.File)
		}
		if end < len(kids) {
			res.NextMarker = strconv.Itoa(end)
		}
		s.reply(w, res)
	case "/adrive/v1.0/openFile/create":
		parent, name := str("parent_file_id"), str("name")
		if !s.exists(parent) {
			s.fail(w, http.StatusNotFound, api.CodeNotFoundFile)
			return
		}
		if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
			s.fail(w, http.StatusBadRequest, "InvalidParameter.Name")
			return
		}
		if str("type") == api.TypeFolder {
			if f := s.live(parent, name, ""); f != nil && str("check_name_mode") == api.CheckRefuse {
				s.reply(w, api.CreateResponse{FileID: f.FileID, Exist: true})
				return
			}
			id := s.id()
			s.files[id] = &fakeFile{File: api.File{DriveID: driveID, FileID: id, ParentFileID: parent, Name: name, Type: api.TypeFolder, UpdatedAt: time.Now()}}
			s.reply(w, api.CreateResponse{FileID: id})
			return
		}
		// check_name_mode ignore: a file of the same name may exist
		id := s.id()
		size, _ := body["size"].(float64)
		up := &fakeUpload{file: api.File{DriveID: driveID, FileID: id, ParentFileID: parent, Name: name, Type: api.TypeFile, Size: int64(size)}, parts: map[int][]byte{}}
		s.uploads[id] = up
		partList, ok := body["part_info_list"].([]any)
		if !ok {
			s.fail(w, http.StatusBadRequest, "InvalidParameter.PartInfoList")
			return
		}
		s.reply(w, api.CreateResponse{FileID: id, UploadID: "u" + id, PartInfoList: s.partURLs(id, up.gen, len(partList))})
	case "/adrive/v1.0/openFile/getUploadUrl":
		up, ok := s.uploads[str("file_id")]
		if !ok {
			s.fail(w, http.StatusNotFound, api.CodeNotFoundFile)
			return
		}
		up.gen++
		s.reply(w, api.CreateResponse{FileID: up.file.FileID, UploadID: str("upload_id"), PartInfoList: s.partURLs(up.file.FileID, up.gen, len(body["part_info_list"].([]any)))})
	case "/adrive/v1.0/openFile/complete":
		up, ok := s.uploads[str("file_id")]
		if !ok || str("upload_id") != "u"+up.file.FileID {
			s.fail(w, http.StatusNotFound, api.CodeNotFoundFile)
			return
		}
		var data []byte
		for i := 1; i <= len(up.parts); i++ {
			data = append(data, up.parts[i]...)
		}
		if int64(len(data)) != up.file.Size {
			s.fail(w, http.StatusBadRequest, "PartNotSequential")
			return
		}
		sum := sha1.Sum(data)
		f := &fakeFile{File: up.file, data: data}
		f.ContentHash = strings.ToUpper(hex.EncodeToString(sum[:]))
		f.UpdatedAt = time.Now()
		s.files[f.FileID] = f
		delete(s.uploads, f.FileID)
		s.reply(w, f.File)
	case "/adrive/v1.0/openFile/update":
		f, ok := s.files[str("file_id")]
		if !ok {
			s.fail(w, http.StatusNotFound, api.CodeNotFoundFile)
			return
		}
		if c := s.live(f.ParentFileID, str("name"), ""); c != nil && c != f {
			s.fail(w, http.StatusConflict, api.CodeAlreadyExist)
			return
		}
		if s.badName != "" && str("name") == s.badName {
			s.fail(w, http.StatusBadRequest, "InvalidParameter.Name")
			return
		}
		f.Name = str("name")
		s.reply(w, f.File)
	case "/adrive/v1.0/openFile/move":
		f, ok := s.files[str("file_id")]
		to := str("to_parent_file_id")
		if !ok || !s.exists(to) {
			s.fail(w, http.StatusNotFound, api.CodeNotFoundFile)
			return
		}
		name := f.Name
		if nn := str("new_name"); nn != "" {
			name = nn
		}
		if c := s.live(to, name, ""); c != nil {
			s.reply(w, api.MoveCopyResponse{FileID: c.FileID, Exist: true})
			return
		}
		f.ParentFileID, f.Name = to, name
		s.reply(w, api.MoveCopyResponse{FileID: f.FileID})
	case "/adrive/v1.0/openFile/copy":
		f, ok := s.files[str("file_id")]
		to := str("to_parent_file_id")
		if !ok || !s.exists(to) || f.Type != api.TypeFile {
			s.fail(w, http.StatusNotFound, api.CodeNotFoundFile)
			return
		}
		c := *f
		c.FileID, c.ParentFileID, c.data = s.id(), to, bytes.Clone(f.data)
		s.files[c.FileID] = &c // may duplicate a name, like the real one
		s.reply(w, api.MoveCopyResponse{FileID: c.FileID})
	case "/adrive/v1.0/openFile/recyclebin/trash", "/adrive/v1.0/openFile/delete":
		if _, ok := s.files[str("file_id")]; !ok {
			s.fail(w, http.StatusNotFound, api.CodeNotFoundFile)
			return
		}
		s.removeTree(str("file_id"))
		s.reply(w, map[string]string{"file_id": str("file_id")})
	case "/adrive/v1.0/openFile/getDownloadUrl":
		if _, ok := s.files[str("file_id")]; !ok {
			s.fail(w, http.StatusNotFound, api.CodeNotFoundFile)
			return
		}
		s.reply(w, api.DownloadURLResponse{URL: s.srv.URL + "/dl/" + str("file_id")})
	default:
		s.t.Errorf("fake aliyundrive: unexpected %s %s", r.Method, r.URL)
		s.fail(w, http.StatusNotFound, "NotFound")
	}
}

func (s *fakeAli) partURLs(id string, gen, n int) []api.PartInfo {
	parts := make([]api.PartInfo, n)
	for i := range parts {
		parts[i] = api.PartInfo{PartNumber: i + 1, UploadURL: fmt.Sprintf("%s/up/%s/%d?gen=%d", s.srv.URL, id, i+1, gen)}
	}
	return parts
}

// noLimits removes the rate limits and makes parts small for the tests
func noLimits(t *testing.T) {
	oldList, oldLink, oldOther := listRate, linkRate, otherRate
	listRate, linkRate, otherRate = rate.Inf, rate.Inf, rate.Inf
	t.Cleanup(func() { listRate, linkRate, otherRate = oldList, oldLink, oldOther })
}

func (s *fakeAli) config() configmap.Simple {
	return configmap.Simple{
		"client_id":     testClientID,
		"client_secret": obscure.MustObscure(testClientSecret),
		"refresh_token": s.refresh,
		"api_url":       s.srv.URL,
		"drive_type":    "default",
	}
}

// TestFakeIntegration runs the generic tests on a remote in a config
// file, so the refresh token Aliyun Drive rotates on each refresh is
// saved and shared between the Fs the tests make, as in real use.
func TestFakeIntegration(t *testing.T) {
	noLimits(t)
	s := newFakeAli(t)
	conf := filepath.Join(t.TempDir(), "rclone.conf")
	ini := fmt.Sprintf("[TestAliFake]\ntype = aliyundrive\nclient_id = %s\nclient_secret = %s\nrefresh_token = %s\napi_url = %s\n",
		testClientID, obscure.MustObscure(testClientSecret), s.refresh, s.srv.URL)
	require.NoError(t, os.WriteFile(conf, []byte(ini), 0o600))
	t.Setenv("RCLONE_CONFIG", conf)
	fstests.Run(t, &fstests.Opt{
		RemoteName:      "TestAliFake:rclone-test",
		NilObject:       (*Object)(nil),
		SkipInvalidUTF8: true,
	})
}

func newTestFs(t *testing.T, s *fakeAli, m configmap.Simple) *Fs {
	f, err := NewFs(context.Background(), "test", "", m)
	require.NoError(t, err)
	return f.(*Fs)
}

// TestCopyOverExisting checks a copy never leaves two files of one name.
// The copy API can't refuse or replace an existing file, so a copy onto
// one returns fs.ErrorCantCopy and rclone uploads a new version instead.
func TestCopyOverExisting(t *testing.T) {
	noLimits(t)
	ctx := context.Background()
	s := newFakeAli(t)
	f := newTestFs(t, s, s.config())
	put := func(remote, content string) fs.Object {
		o, err := f.Put(ctx, strings.NewReader(content), object.NewStaticObjectInfo(remote, time.Now(), int64(len(content)), true, nil, nil))
		require.NoError(t, err)
		return o
	}
	o := put("a/file.txt", "source")
	put("b/file.txt", "same name")
	put("c/other.txt", "other name")

	for _, remote := range []string{"b/file.txt", "c/other.txt"} {
		_, err := f.Copy(ctx, o, remote)
		assert.ErrorIs(t, err, fs.ErrorCantCopy, remote)
	}
	for dir, want := range map[string]string{"b": "b/file.txt", "c": "c/other.txt"} {
		entries, err := f.List(ctx, dir)
		require.NoError(t, err)
		require.Len(t, entries, 1, dir)
		assert.Equal(t, want, entries[0].Remote())
	}

	// A copy which can't be renamed into place is removed again
	s.mu.Lock()
	s.badName = "renamed.txt"
	s.mu.Unlock()
	_, err := f.Copy(ctx, o, "d/renamed.txt")
	require.Error(t, err)
	entries, err := f.List(ctx, "d")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestUploadPartsAndReplace(t *testing.T) {
	noLimits(t)
	old := partSizeOverride
	partSizeOverride = 1000
	t.Cleanup(func() { partSizeOverride = old })
	ctx := context.Background()
	s := newFakeAli(t)
	f := newTestFs(t, s, s.config())

	data := bytes.Repeat([]byte("ali"), 1200) // 4 parts
	o, err := f.Put(ctx, bytes.NewReader(data), object.NewStaticObjectInfo("d/file.bin", time.Now(), int64(len(data)), true, nil, nil))
	require.NoError(t, err)
	sum := sha1.Sum(data)
	h, _ := o.Hash(ctx, hash.SHA1)
	assert.Equal(t, hex.EncodeToString(sum[:]), h, "lower case SHA1")
	s.mu.Lock()
	assert.Equal(t, 1, s.expired, "an expired upload URL was renewed")
	s.mu.Unlock()

	// Put over the existing file replaces it rather than leaving two
	_, err = f.Put(ctx, strings.NewReader("new"), object.NewStaticObjectInfo("d/file.bin", time.Now(), 3, true, nil, nil))
	require.NoError(t, err)
	entries, err := f.List(ctx, "d")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, int64(3), entries[0].Size())

	// Copy into a folder holding the source's name renames the new copy only
	src, err := f.NewObject(ctx, "d/file.bin")
	require.NoError(t, err)
	_, err = f.Put(ctx, strings.NewReader("keep me"), object.NewStaticObjectInfo("e/file.bin", time.Now(), 7, true, nil, nil))
	require.NoError(t, err)
	c, err := f.Copy(ctx, src, "e/other.bin")
	require.NoError(t, err)
	assert.Equal(t, "e/other.bin", c.Remote())
	kept, err := f.NewObject(ctx, "e/file.bin")
	require.NoError(t, err)
	assert.Equal(t, int64(7), kept.Size(), "existing file untouched")

	rc, err := c.Open(ctx)
	require.NoError(t, err)
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, "new", string(got))
}

func TestTokenRefreshAndConfig(t *testing.T) {
	noLimits(t)
	ctx := context.Background()
	s := newFakeAli(t)

	// Authorise with a code during config
	m := configmap.Simple{"client_id": testClientID, "client_secret": obscure.MustObscure(testClientSecret), "api_url": s.srv.URL}
	out, err := configure(ctx, "test", m, fs.ConfigIn{})
	require.NoError(t, err)
	assert.Equal(t, "code", out.State)
	assert.Contains(t, out.Option.Help, s.srv.URL+"/oauth/authorize?")
	assert.Contains(t, out.Option.Help, "redirect_uri=oob")
	_, err = configure(ctx, "test", m, fs.ConfigIn{State: "code", Result: "bad"})
	assert.Error(t, err)
	_, err = configure(ctx, "test", m, fs.ConfigIn{State: "code", Result: " good-code "})
	require.NoError(t, err)
	assert.Contains(t, m["token"], "access_token")

	f := newTestFs(t, s, m)
	require.NoError(t, f.Mkdir(ctx, "x"))

	// Token revoked: refreshed with the rotated refresh token
	s.mu.Lock()
	before := s.refreshes
	s.access = "revoked"
	s.mu.Unlock()
	_, err = f.List(ctx, "")
	require.NoError(t, err)
	s.mu.Lock()
	assert.Equal(t, before+1, s.refreshes)
	s.mu.Unlock()
}

func TestPartSize(t *testing.T) {
	assert.Equal(t, int64(20<<20), partSize(1<<30))
	assert.Equal(t, int64(40<<20), partSize(300<<30), "under 10,000 parts")
	_ = fs.ErrorObjectNotFound
}
