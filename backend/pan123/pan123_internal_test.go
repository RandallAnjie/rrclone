package pan123

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/pan123/api"
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

// fake123 is an in memory 123 Pan open platform. It is deliberately
// awkward: moves which clash rename the file like the web client,
// listings include trashed files, uploads complete on the second
// poll and every 7th request is rate limited.
type fake123 struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	files    map[int64]*fakeFile
	sessions map[string]*fakeSession
	nextID   int64
	token    string
	requests int
	tokens   int
	reuses   int
}

type fakeFile struct {
	api.File
	data []byte
}

type fakeSession struct {
	file     api.File
	slices   map[int][]byte
	polls    int
	sliceLen int64
}

const (
	testClientID     = "cid"
	testClientSecret = "csecret"
)

func newFake123(t *testing.T) *fake123 {
	s := &fake123{t: t, files: map[int64]*fakeFile{}, sessions: map[string]*fakeSession{}, nextID: 100}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fake123) reply(w http.ResponseWriter, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg, "data": data, "x-traceID": "t"})
}

func (s *fake123) live(parent int64, name string) *fakeFile {
	for _, f := range s.files {
		if f.ParentFileID == parent && f.FileName == name && f.Trashed == 0 {
			return f
		}
	}
	return nil
}

func (s *fake123) add(parent int64, name string, typ int, data []byte) *fakeFile {
	s.nextID++
	sum := md5.Sum(data)
	f := &fakeFile{File: api.File{
		FileID: s.nextID, FileName: name, Type: typ, ParentFileID: parent,
		Size: int64(len(data)), UpdateAt: api.Time(time.Now()), CreateAt: api.Time(time.Now()),
	}, data: data}
	if typ == api.TypeFile {
		f.Etag = hex.EncodeToString(sum[:])
	}
	s.files[f.FileID] = f
	return f
}

func validName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `\/:*?"<>|`)
}

func (s *fake123) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/dl/") {
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/dl/"), 10, 64)
		f, ok := s.files[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, f.FileName, time.Time{}, bytes.NewReader(f.data))
		return
	}
	if r.Header.Get("Platform") != "open_platform" {
		s.reply(w, 400, "missing platform", nil)
		return
	}
	if r.URL.Path == "/api/v1/access_token" {
		var req api.AccessTokenRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ClientID != testClientID || req.ClientSecret != testClientSecret {
			s.reply(w, 401, "invalid client", nil)
			return
		}
		s.tokens++
		s.token = fmt.Sprintf("tok%d", s.tokens)
		s.reply(w, 0, "ok", api.AccessToken{AccessToken: s.token, ExpiredAt: time.Now().Add(30 * 24 * time.Hour)})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		s.reply(w, 401, "token expired", nil)
		return
	}
	s.requests++
	if s.requests%7 == 0 {
		s.reply(w, 429, "too many requests", nil)
		return
	}
	var body map[string]any
	if r.Method != "GET" && !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	num := func(k string) int64 { v, _ := body[k].(float64); return int64(v) }
	str := func(k string) string { v, _ := body[k].(string); return v }
	ids := func() []int64 {
		var out []int64
		for _, v := range body["fileIDs"].([]any) {
			out = append(out, int64(v.(float64)))
		}
		return out
	}

	switch r.URL.Path {
	case "/api/v2/file/list":
		q := r.URL.Query()
		parent, _ := strconv.ParseInt(q.Get("parentFileId"), 10, 64)
		last, _ := strconv.ParseInt(q.Get("lastFileId"), 10, 64)
		limit, _ := strconv.Atoi(q.Get("limit"))
		var kids []*fakeFile
		for _, f := range s.files {
			if f.ParentFileID == parent && f.FileID > last {
				kids = append(kids, f) // trashed ones too, like the real API
			}
		}
		sort.Slice(kids, func(i, j int) bool { return kids[i].FileID < kids[j].FileID })
		res := api.FileList{LastFileID: -1, FileList: []api.File{}}
		if len(kids) > limit {
			kids = kids[:limit]
			res.LastFileID = kids[len(kids)-1].FileID
		}
		for _, f := range kids {
			res.FileList = append(res.FileList, f.File)
		}
		s.reply(w, 0, "ok", res)
	case "/upload/v1/file/mkdir":
		parentID, _ := strconv.ParseInt(fmt.Sprint(body["parentID"]), 10, 64)
		if !validName(str("name")) {
			s.reply(w, 1, "bad name", nil)
			return
		}
		if s.live(parentID, str("name")) != nil {
			s.reply(w, 1, "该目录下已经有同名文件夹", nil)
			return
		}
		d := s.add(parentID, str("name"), api.TypeFolder, nil)
		s.reply(w, 0, "ok", api.MkdirResponse{DirID: d.FileID})
	case "/api/v1/file/name":
		f, ok := s.files[num("fileId")]
		if !ok || !validName(str("fileName")) {
			s.reply(w, 1, "bad rename", nil)
			return
		}
		if c := s.live(f.ParentFileID, str("fileName")); c != nil && c != f {
			s.reply(w, 1, "same name exists", nil)
			return
		}
		f.FileName = str("fileName")
		s.reply(w, 0, "ok", nil)
	case "/api/v1/file/move":
		to := num("toParentFileID")
		for _, id := range ids() {
			f := s.files[id]
			// Clashes are renamed, the worst case for rclone
			if s.live(to, f.FileName) != nil {
				f.FileName += " (1)"
			}
			f.ParentFileID = to
		}
		s.reply(w, 0, "ok", nil)
	case "/api/v1/file/trash":
		for _, id := range ids() {
			s.files[id].Trashed = 1
		}
		s.reply(w, 0, "ok", nil)
	case "/api/v1/file/download_info":
		id, _ := strconv.ParseInt(r.URL.Query().Get("fileId"), 10, 64)
		s.reply(w, 0, "ok", api.DownloadInfo{DownloadURL: fmt.Sprintf("%s/dl/%d", s.srv.URL, id)})
	case "/api/v1/user/info":
		var used int64
		for _, f := range s.files {
			used += f.Size
		}
		s.reply(w, 0, "ok", api.UserInfo{SpaceUsed: used, SpacePermanent: 1 << 40})
	case "/upload/v2/file/create":
		parent := num("parentFileID")
		name := str("filename")
		if !validName(name) {
			s.reply(w, 1, "bad name", nil)
			return
		}
		if old := s.live(parent, name); old != nil {
			old.Trashed = 1 // duplicate=2 overwrites
		}
		for _, f := range s.files {
			if f.Type == api.TypeFile && f.Etag == str("etag") && f.Size == num("size") {
				s.reuses++
				nf := s.add(parent, name, api.TypeFile, bytes.Clone(f.data))
				s.reply(w, 0, "ok", api.CreateResponse{FileID: nf.FileID, Reuse: true})
				return
			}
		}
		id := fmt.Sprintf("pre%d", len(s.sessions)+1)
		s.sessions[id] = &fakeSession{
			file:     api.File{FileName: name, ParentFileID: parent, Size: num("size"), Etag: str("etag")},
			slices:   map[int][]byte{},
			sliceLen: 1000,
		}
		s.reply(w, 0, "ok", api.CreateResponse{PreuploadID: id, SliceSize: 1000, Servers: []string{s.srv.URL}})
	case "/upload/v2/file/slice":
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			s.reply(w, 1, err.Error(), nil)
			return
		}
		sess, ok := s.sessions[r.FormValue("preuploadID")]
		if !ok {
			s.reply(w, 1, "no session", nil)
			return
		}
		file, _, err := r.FormFile("slice")
		if err != nil {
			s.reply(w, 1, err.Error(), nil)
			return
		}
		data, _ := io.ReadAll(file)
		if sum := md5.Sum(data); hex.EncodeToString(sum[:]) != r.FormValue("sliceMD5") {
			s.reply(w, 1, "slice md5 mismatch", nil)
			return
		}
		n, _ := strconv.Atoi(r.FormValue("sliceNo"))
		sess.slices[n] = data
		s.reply(w, 0, "ok", nil)
	case "/upload/v2/file/upload_complete":
		sess, ok := s.sessions[str("preuploadID")]
		if !ok {
			s.reply(w, 1, "no session", nil)
			return
		}
		sess.polls++
		if sess.polls < 2 {
			s.reply(w, 0, "ok", api.CompleteResponse{Completed: false})
			return
		}
		var data []byte
		for i := 1; i <= len(sess.slices); i++ {
			data = append(data, sess.slices[i]...)
		}
		sum := md5.Sum(data)
		if int64(len(data)) != sess.file.Size || hex.EncodeToString(sum[:]) != sess.file.Etag {
			s.reply(w, 1, "content doesn't match etag", nil)
			return
		}
		f := s.add(sess.file.ParentFileID, sess.file.FileName, api.TypeFile, data)
		delete(s.sessions, str("preuploadID"))
		s.reply(w, 0, "ok", api.CompleteResponse{Completed: true, FileID: f.FileID})
	default:
		s.t.Errorf("fake 123pan: unexpected %s %s", r.Method, r.URL)
		s.reply(w, 404, "not found", nil)
	}
}

func noLimits(t *testing.T) {
	oldQPS, oldWait := apiQPS, completeWait
	apiQPS, completeWait = map[string]float64{}, time.Millisecond
	t.Cleanup(func() { apiQPS, completeWait = oldQPS, oldWait })
}

func (s *fake123) remote() string {
	return fmt.Sprintf(":123pan,client_id=%s,client_secret='%s',api_url='%s':", testClientID, obscure.MustObscure(testClientSecret), s.srv.URL)
}

func TestFakeIntegration(t *testing.T) {
	noLimits(t)
	s := newFake123(t)
	fstests.Run(t, &fstests.Opt{
		RemoteName:      s.remote() + "rclone-test",
		NilObject:       (*Object)(nil),
		SkipInvalidUTF8: true,
	})
}

func newTestFs(t *testing.T, s *fake123) *Fs {
	m := configmap.Simple{"client_id": testClientID, "client_secret": obscure.MustObscure(testClientSecret), "api_url": s.srv.URL}
	f, err := NewFs(context.Background(), "test", "", m)
	require.NoError(t, err)
	return f.(*Fs)
}

func TestUploadAndCopyByHash(t *testing.T) {
	noLimits(t)
	ctx := context.Background()
	s := newFake123(t)
	f := newTestFs(t, s)
	data := bytes.Repeat([]byte("0123456789"), 350) // 4 slices
	// No hash from the source: rclone buffers to hash it
	src := object.NewStaticObjectInfo("a/b.bin", time.Now(), int64(len(data)), true, nil, nil)
	o, err := f.Put(ctx, bytes.NewReader(data), src)
	require.NoError(t, err)
	sum := md5.Sum(data)
	h, _ := o.Hash(ctx, hash.MD5)
	assert.Equal(t, hex.EncodeToString(sum[:]), h)

	c, err := f.Copy(ctx, o, "c/copy.bin")
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), c.Size())
	s.mu.Lock()
	assert.Equal(t, 1, s.reuses, "copied by instant upload")
	s.mu.Unlock()
	rc, err := c.Open(ctx)
	require.NoError(t, err)
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, data, got)

	// A move clashing with an existing name mustn't be renamed by 123 Pan
	_, err = f.Put(ctx, strings.NewReader("x"), object.NewStaticObjectInfo("c/b.bin", time.Now(), 1, true, nil, nil))
	require.NoError(t, err)
	m, err := f.Move(ctx, o, "c/moved.bin")
	require.NoError(t, err)
	assert.Equal(t, "c/moved.bin", m.Remote())
	entries, err := f.List(ctx, "c")
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Remote())
	}
	assert.ElementsMatch(t, []string{"c/copy.bin", "c/b.bin", "c/moved.bin"}, names)

	usage, err := f.About(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1<<40), *usage.Total)
}

func TestTokenRenewal(t *testing.T) {
	noLimits(t)
	ctx := context.Background()
	s := newFake123(t)
	m := configmap.Simple{"client_id": testClientID, "client_secret": obscure.MustObscure(testClientSecret), "api_url": s.srv.URL}
	f, err := NewFs(ctx, "test", "", m)
	require.NoError(t, err)
	require.NoError(t, f.Mkdir(ctx, "d"))
	assert.Contains(t, m["token"], "access_token", "token saved for next time")

	// Revoked server side: renewed with the credentials
	s.mu.Lock()
	s.token = "revoked"
	s.mu.Unlock()
	_, err = f.List(ctx, "")
	require.NoError(t, err)
	s.mu.Lock()
	assert.Equal(t, 2, s.tokens)
	s.mu.Unlock()

	// A new Fs reuses the saved token
	f2, err := NewFs(ctx, "test", "", m)
	require.NoError(t, err)
	_, err = f2.List(ctx, "")
	require.NoError(t, err)
	s.mu.Lock()
	assert.Equal(t, 2, s.tokens)
	s.mu.Unlock()
}

func TestRateLimiter(t *testing.T) {
	f := &Fs{st: &state{limiters: map[string]*rate.Limiter{}}}
	assert.Nil(t, f.limiter("/upload/v2/file/upload_complete"), "unlimited endpoint")
	l := f.limiter("/api/v1/file/move")
	require.NotNil(t, l)
	assert.Same(t, l, f.limiter("/api/v1/file/move"))
	start := time.Now()
	for range 3 {
		require.NoError(t, l.Wait(context.Background()))
	}
	assert.GreaterOrEqual(t, time.Since(start), 1900*time.Millisecond, "1 QPS")
}

func TestTime(t *testing.T) {
	var tm api.Time
	require.NoError(t, json.Unmarshal([]byte(`"2025-03-04 05:06:07"`), &tm))
	assert.Equal(t, "2025-03-03T21:06:07Z", time.Time(tm).UTC().Format(time.RFC3339))
	require.NoError(t, json.Unmarshal([]byte(`""`), &tm))
	assert.True(t, time.Time(tm).IsZero())
	_ = fs.ErrorObjectNotFound
}
