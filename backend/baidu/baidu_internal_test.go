package baidu

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
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/baidu/api"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/fstest/fstests"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBaidu is an in memory Baidu Netdisk. Every 9th API call is
// refused with frequency control.
type fakeBaidu struct {
	t         *testing.T
	srv       *httptest.Server
	mu        sync.Mutex
	nodes     map[string]*fakeNode // by absolute path, "/" is the root
	uploads   map[string]*fakeUpload
	nextID    int64
	access    string
	refresh   string
	calls     int
	refreshes int
	rapid     int
}

type fakeNode struct {
	id    int64
	isDir bool
	data  []byte
	mtime int64
}

type fakeUpload struct {
	path   string
	size   int64
	blocks []string
	got    map[int][]byte
	mtime  int64
}

func newFakeBaidu(t *testing.T) *fakeBaidu {
	s := &fakeBaidu{t: t, nodes: map[string]*fakeNode{"/": {id: 1, isDir: true}}, uploads: map[string]*fakeUpload{}, nextID: 1, access: "a1", refresh: "r1"}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fakeBaidu) json(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *fakeBaidu) fail(w http.ResponseWriter, errno int) {
	s.json(w, map[string]any{"errno": errno, "errmsg": "fake error " + strconv.Itoa(errno)})
}

func (s *fakeBaidu) fileJSON(p string, n *fakeNode) api.File {
	f := api.File{FsID: n.id, Path: p, ServerFilename: path.Base(p), ServerMtime: n.mtime, LocalMtime: n.mtime}
	if n.isDir {
		f.IsDir = 1
	} else {
		f.Size = int64(len(n.data))
	}
	return f
}

func (s *fakeBaidu) add(p string, isDir bool, data []byte, mtime int64) *fakeNode {
	s.nextID++
	n := &fakeNode{id: s.nextID, isDir: isDir, data: data, mtime: mtime}
	s.nodes[p] = n
	return n
}

func validName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `\?|"><:*`) && strings.TrimSpace(name) == name
}

func (s *fakeBaidu) moveTree(src, dst string, copy bool) {
	for q, n := range s.nodes {
		if q != src && !strings.HasPrefix(q, src+"/") {
			continue
		}
		nq := dst + strings.TrimPrefix(q, src)
		if copy {
			c := *n
			s.nextID++
			c.id = s.nextID
			c.data = bytes.Clone(n.data)
			s.nodes[nq] = &c
		} else {
			delete(s.nodes, q)
			s.nodes[nq] = n
		}
	}
}

func (s *fakeBaidu) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := r.URL.Query()
	_ = r.ParseForm()

	if r.URL.Path == "/oauth/2.0/token" {
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != s.refresh || r.Form.Get("client_id") != "cid" {
			w.WriteHeader(http.StatusBadRequest)
			s.json(w, map[string]string{"error": "invalid_grant"})
			return
		}
		s.refreshes++
		s.access = fmt.Sprintf("a%d", s.refreshes+1)
		s.refresh = fmt.Sprintf("r%d", s.refreshes+1)
		s.json(w, map[string]any{"access_token": s.access, "refresh_token": s.refresh, "expires_in": 2592000, "token_type": "Bearer"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/dl/") {
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/dl/"), 10, 64)
		if r.Header.Get("User-Agent") != userAgent || q.Get("access_token") != s.access {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		for _, n := range s.nodes {
			if n.id == id {
				http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(n.data))
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	if q.Get("access_token") != s.access {
		s.fail(w, api.ErrnoTokenInvalid)
		return
	}
	s.calls++
	if s.calls%9 == 0 {
		s.fail(w, api.ErrnoFrequency)
		return
	}
	method := q.Get("method")
	switch {
	case r.URL.Path == "/rest/2.0/xpan/nas" && method == "uinfo":
		s.json(w, map[string]any{"errno": 0, "vip_type": 0})
	case r.URL.Path == "/api/quota":
		var used int64
		for _, n := range s.nodes {
			used += int64(len(n.data))
		}
		s.json(w, map[string]any{"errno": 0, "total": int64(1) << 40, "used": used})
	case r.URL.Path == "/rest/2.0/xpan/file" && method == "list":
		dir := path.Clean(q.Get("dir"))
		if n, ok := s.nodes[dir]; !ok || !n.isDir {
			s.fail(w, api.ErrnoNotExist)
			return
		}
		var kids []string
		for p := range s.nodes {
			if p != "/" && path.Dir(p) == dir {
				kids = append(kids, p)
			}
		}
		sort.Strings(kids)
		start, _ := strconv.Atoi(q.Get("start"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		start = min(start, len(kids))
		end := min(start+limit, len(kids))
		list := []api.File{}
		for _, p := range kids[start:end] {
			list = append(list, s.fileJSON(p, s.nodes[p]))
		}
		s.json(w, map[string]any{"errno": 0, "list": list})
	case r.URL.Path == "/rest/2.0/xpan/file" && method == "create":
		p := path.Clean(r.Form.Get("path"))
		if !validName(path.Base(p)) {
			s.fail(w, api.ErrnoBadName)
			return
		}
		if parent, ok := s.nodes[path.Dir(p)]; !ok || !parent.isDir {
			s.fail(w, api.ErrnoNotExist)
			return
		}
		if r.Form.Get("isdir") == "1" {
			if _, ok := s.nodes[p]; ok {
				s.fail(w, api.ErrnoExist)
				return
			}
			n := s.add(p, true, nil, time.Now().Unix())
			s.json(w, map[string]any{"errno": 0, "fs_id": n.id, "path": p, "isdir": 1})
			return
		}
		up, ok := s.uploads[r.Form.Get("uploadid")]
		if !ok || up.path != p {
			s.fail(w, api.ErrnoParam)
			return
		}
		var data []byte
		for i := range up.blocks {
			blk, ok := up.got[i]
			if !ok {
				s.fail(w, 31363) // block missing
				return
			}
			data = append(data, blk...)
		}
		if int64(len(data)) != up.size {
			s.fail(w, api.ErrnoParam)
			return
		}
		delete(s.uploads, r.Form.Get("uploadid"))
		mtime, _ := strconv.ParseInt(r.Form.Get("local_mtime"), 10, 64)
		n := s.add(p, false, data, mtime)
		s.json(w, map[string]any{"errno": 0, "fs_id": n.id, "path": p, "size": len(data)})
	case r.URL.Path == "/rest/2.0/xpan/file" && method == "precreate":
		p := path.Clean(r.Form.Get("path"))
		if !validName(path.Base(p)) {
			s.fail(w, api.ErrnoBadName)
			return
		}
		var blocks []string
		if err := json.Unmarshal([]byte(r.Form.Get("block_list")), &blocks); err != nil || len(blocks) == 0 {
			s.fail(w, api.ErrnoParam)
			return
		}
		size, _ := strconv.ParseInt(r.Form.Get("size"), 10, 64)
		mtime, _ := strconv.ParseInt(r.Form.Get("local_mtime"), 10, 64)
		// Rapid upload when the content is already stored
		for _, n := range s.nodes {
			if sum := md5.Sum(n.data); !n.isDir && int64(len(n.data)) == size && size > 0 && hex.EncodeToString(sum[:]) == r.Form.Get("content-md5") {
				s.rapid++
				c := s.add(p, false, bytes.Clone(n.data), mtime)
				s.json(w, map[string]any{"errno": 0, "return_type": 2, "info": s.fileJSON(p, c)})
				return
			}
		}
		id := fmt.Sprintf("up%d", len(s.uploads)+100)
		s.uploads[id] = &fakeUpload{path: p, size: size, blocks: blocks, got: map[int][]byte{}, mtime: mtime}
		todo := []int{}
		for i := range blocks {
			todo = append(todo, i)
		}
		s.json(w, map[string]any{"errno": 0, "return_type": 1, "uploadid": id, "block_list": todo})
	case r.URL.Path == "/rest/2.0/pcs/superfile2":
		up, ok := s.uploads[q.Get("uploadid")]
		if !ok {
			s.fail(w, api.ErrnoParam)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			s.fail(w, api.ErrnoParam)
			return
		}
		data, _ := io.ReadAll(file)
		n, _ := strconv.Atoi(q.Get("partseq"))
		sum := md5.Sum(data)
		if n >= len(up.blocks) || hex.EncodeToString(sum[:]) != up.blocks[n] {
			s.fail(w, 31352) // block md5 mismatch
			return
		}
		up.got[n] = data
		s.json(w, map[string]any{"errno": 0, "md5": hex.EncodeToString(sum[:])})
	case r.URL.Path == "/rest/2.0/xpan/file" && method == "filemanager":
		s.manage(w, q.Get("opera"), r.Form.Get("filelist"))
	case r.URL.Path == "/rest/2.0/xpan/multimedia" && method == "filemetas":
		var ids []int64
		_ = json.Unmarshal([]byte(q.Get("fsids")), &ids)
		for _, n := range s.nodes {
			if len(ids) == 1 && n.id == ids[0] {
				s.json(w, map[string]any{"errno": 0, "list": []map[string]any{{"fs_id": n.id, "dlink": fmt.Sprintf("%s/dl/%d?sign=x", s.srv.URL, n.id)}}})
				return
			}
		}
		s.fail(w, api.ErrnoNotFoundMeta)
	default:
		s.t.Errorf("fake baidu: unexpected %s %s", r.Method, r.URL)
		s.fail(w, api.ErrnoParam)
	}
}

func (s *fakeBaidu) manage(w http.ResponseWriter, opera, filelist string) {
	itemFail := func(p string, errno int) {
		s.json(w, map[string]any{"errno": 12, "info": []map[string]any{{"errno": errno, "path": p}}})
	}
	if opera == "delete" {
		var paths []string
		_ = json.Unmarshal([]byte(filelist), &paths)
		for _, p := range paths {
			if _, ok := s.nodes[p]; !ok {
				itemFail(p, api.ErrnoNotExist)
				return
			}
			for q := range s.nodes {
				if q == p || strings.HasPrefix(q, p+"/") {
					delete(s.nodes, q)
				}
			}
		}
		s.json(w, map[string]any{"errno": 0, "info": []any{}})
		return
	}
	var items []api.ManageItem
	_ = json.Unmarshal([]byte(filelist), &items)
	for _, it := range items {
		if _, ok := s.nodes[it.Path]; !ok {
			itemFail(it.Path, api.ErrnoNotExist)
			return
		}
		dest := it.Dest
		if opera == "rename" {
			dest = path.Dir(it.Path)
		}
		if n, ok := s.nodes[dest]; !ok || !n.isDir {
			itemFail(it.Path, api.ErrnoNotExist)
			return
		}
		if !validName(it.Newname) {
			itemFail(it.Path, api.ErrnoBadName)
			return
		}
		dst := path.Join(dest, it.Newname)
		if _, ok := s.nodes[dst]; ok {
			itemFail(it.Path, api.ErrnoExist)
			return
		}
		s.moveTree(it.Path, dst, opera == "copy")
	}
	s.json(w, map[string]any{"errno": 0, "info": []any{}})
}

func (s *fakeBaidu) config() configmap.Simple {
	tok := fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"token_type":"Bearer","expiry":%q}`, s.access, s.refresh, time.Now().Add(time.Hour).Format(time.RFC3339))
	return configmap.Simple{
		"client_id": "cid", "client_secret": "csec", "token": tok,
		"api_url": s.srv.URL, "upload_url": s.srv.URL, "oauth_url": s.srv.URL,
		"block_size": "1Ki",
	}
}

func (s *fakeBaidu) remote() string {
	var parts []string
	for k, v := range s.config() {
		parts = append(parts, fmt.Sprintf("%s='%s'", k, strings.ReplaceAll(v, "'", "''")))
	}
	sort.Strings(parts)
	return ":baidu," + strings.Join(parts, ",") + ":"
}

func TestFakeIntegration(t *testing.T) {
	s := newFakeBaidu(t)
	fstests.Run(t, &fstests.Opt{
		RemoteName:      s.remote() + "rclone-test",
		NilObject:       (*Object)(nil),
		SkipInvalidUTF8: true,
	})
}

func newTestFs(t *testing.T, s *fakeBaidu, m configmap.Simple) *Fs {
	f, err := NewFs(context.Background(), "test", "", m)
	require.NoError(t, err)
	return f.(*Fs)
}

func TestUploadBlocksRapidAndToken(t *testing.T) {
	ctx := context.Background()
	s := newFakeBaidu(t)
	m := s.config()
	f := newTestFs(t, s, m)
	data := bytes.Repeat([]byte("baidu!"), 1000) // 6 blocks of 1 KiB
	modTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	o, err := f.Put(ctx, bytes.NewReader(data), object.NewStaticObjectInfo("x/y/z.bin", modTime, -1, true, nil, nil))
	require.NoError(t, err, "unknown size works since uploads are spooled")
	assert.Equal(t, int64(len(data)), o.Size())
	assert.True(t, modTime.Equal(o.ModTime(ctx)))

	c, err := f.Copy(ctx, o, "copy.bin")
	require.NoError(t, err)
	rc, err := c.Open(ctx)
	require.NoError(t, err)
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, data, got)

	// Same content again is a rapid upload
	_, err = f.Put(ctx, bytes.NewReader(data), object.NewStaticObjectInfo("again.bin", modTime, int64(len(data)), true, nil, nil))
	require.NoError(t, err)
	s.mu.Lock()
	assert.Equal(t, 1, s.rapid)
	// Expire the access token: refreshed with the refresh token
	s.access = "gone"
	s.mu.Unlock()
	_, err = f.List(ctx, "")
	require.NoError(t, err)
	s.mu.Lock()
	assert.Equal(t, 1, s.refreshes)
	s.mu.Unlock()
	assert.Contains(t, m["token"], `"refresh_token":"r2"`, "rotated refresh token saved")
}

func TestSpool(t *testing.T) {
	data := bytes.Repeat([]byte{7}, 2500)
	sp, err := spool(bytes.NewReader(data), 1000)
	require.NoError(t, err)
	defer sp.close()
	assert.Equal(t, int64(2500), sp.size)
	require.Len(t, sp.blocks, 3)
	last := md5.Sum(data[2000:])
	assert.Equal(t, hex.EncodeToString(last[:]), sp.blocks[2])
	whole := md5.Sum(data)
	assert.Equal(t, hex.EncodeToString(whole[:]), sp.contentMD5)

	empty, err := spool(bytes.NewReader(nil), 1000)
	require.NoError(t, err)
	defer empty.close()
	assert.Equal(t, []string{"d41d8cd98f00b204e9800998ecf8427e"}, empty.blocks)
}

func TestPathsAndBlockSize(t *testing.T) {
	f := &Fs{root: "a b", opt: Options{RootFolder: "/apps/my app", Enc: encoder.Base | encoder.EncodeWin}, blockSize: 4 << 20}
	assert.Equal(t, "/apps/my app/a b/c？d", f.absPath("c?d"), "root folder kept, names encoded")
	f.opt.RootFolder = ""
	f.root = ""
	assert.Equal(t, "/", f.absPath(""))
	assert.Equal(t, int64(4<<20), f.blockSizeFor(1<<30))
	assert.Equal(t, int64(8<<20), f.blockSizeFor(10<<30), "grows to keep under 2048 blocks")
	_ = url.Values{}
}
