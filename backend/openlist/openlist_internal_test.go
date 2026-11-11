package openlist

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
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

	"github.com/rclone/rclone/backend/openlist/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/fstest/fstests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOpenList is an in memory OpenList server. Top level folders act
// as separate storages: copies and moves between them run as tasks.
type fakeOpenList struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	nodes    map[string]*fakeNode // absolute paths, "/" is the root
	tasks    map[string]*fakeTask
	nextID   int
	user     string
	password string
	tokens   map[string]bool
	logins   int
	hashes   bool
}

type fakeNode struct {
	isDir   bool
	data    []byte
	modTime time.Time
}

type fakeTask struct {
	polls int
	run   func()
	name  string
}

func newFakeOpenList(t *testing.T) *fakeOpenList {
	s := &fakeOpenList{
		t:        t,
		nodes:    map[string]*fakeNode{"/": {isDir: true}},
		tasks:    map[string]*fakeTask{},
		user:     "admin",
		password: "pass",
		hashes:   true,
		tokens:   map[string]bool{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fakeOpenList) remote(extra string) string {
	return fmt.Sprintf(":openlist,url='%s',username='%s',password='%s'%s:", s.srv.URL, s.user, obscure.MustObscure(s.password), extra)
}

func (s *fakeOpenList) reply(w http.ResponseWriter, code int, msg string, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg, "data": data})
}

func clean(p string) string {
	return path.Clean("/" + p)
}

func (s *fakeOpenList) mkdirAll(p string) bool {
	if n, ok := s.nodes[p]; ok {
		return n.isDir
	}
	if !s.mkdirAll(path.Dir(p)) {
		return false
	}
	s.nodes[p] = &fakeNode{isDir: true, modTime: time.Now()}
	return true
}

func (s *fakeOpenList) obj(p string, n *fakeNode) api.Object {
	o := api.Object{Name: path.Base(p), IsDir: n.isDir, Modified: n.modTime, Sign: "sig-" + p}
	if !n.isDir {
		o.Size = int64(len(n.data))
		if s.hashes {
			m, s1, s2 := md5.Sum(n.data), sha1.Sum(n.data), sha256.Sum256(n.data)
			o.HashInfo = map[string]string{"md5": hex.EncodeToString(m[:]), "sha1": hex.EncodeToString(s1[:]), "sha256": hex.EncodeToString(s2[:])}
		}
	}
	return o
}

// storage returns the top level folder, which stands for a mount
func storage(p string) string {
	return strings.SplitN(strings.TrimPrefix(p, "/"), "/", 2)[0]
}

// moveTree moves or copies src and everything below it to dst
func (s *fakeOpenList) moveTree(src, dst string, copy bool) {
	for q, n := range s.nodes {
		if q != src && !strings.HasPrefix(q, src+"/") {
			continue
		}
		nq := dst + strings.TrimPrefix(q, src)
		if copy {
			c := *n
			c.data = bytes.Clone(n.data)
			s.nodes[nq] = &c
		} else {
			delete(s.nodes, q)
			s.nodes[nq] = n
		}
	}
}

func (s *fakeOpenList) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	if strings.HasPrefix(r.URL.Path, "/d/") {
		p := clean(strings.TrimPrefix(r.URL.Path, "/d"))
		n, ok := s.nodes[p]
		if !ok || n.isDir || r.URL.Query().Get("sign") != "sig-"+p {
			http.Error(w, "bad sign or path", http.StatusUnauthorized)
			return
		}
		w.Header().Del("Content-Type")
		http.ServeContent(w, r, path.Base(p), n.modTime, bytes.NewReader(n.data))
		return
	}
	if r.URL.Path == "/api/auth/login" {
		var req api.LoginRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Username != s.user || req.Password != s.password {
			s.reply(w, 400, "password is incorrect", nil)
			return
		}
		s.logins++
		s.nextID++
		token := "tok" + strconv.Itoa(s.nextID)
		s.tokens[token] = true
		s.reply(w, 200, "success", api.LoginResponse{Token: token})
		return
	}
	if !s.tokens[r.Header.Get("Authorization")] {
		s.reply(w, 401, "token is expired", nil)
		return
	}

	var body map[string]any
	if r.Method == "POST" {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	str := func(k string) string { v, _ := body[k].(string); return v }
	names := func() []string {
		var out []string
		for _, v := range body["names"].([]any) {
			out = append(out, v.(string))
		}
		return out
	}

	switch r.URL.Path {
	case "/api/fs/get":
		p := clean(str("path"))
		n, ok := s.nodes[p]
		if !ok {
			s.reply(w, 500, "failed get objs: object not found", nil)
			return
		}
		s.reply(w, 200, "success", api.GetResponse{Object: s.obj(p, n)})
	case "/api/fs/list":
		p := clean(str("path"))
		n, ok := s.nodes[p]
		if !ok || !n.isDir {
			s.reply(w, 500, "failed get objs: object not found", nil)
			return
		}
		var kids []string
		for q := range s.nodes {
			if q != "/" && path.Dir(q) == p {
				kids = append(kids, q)
			}
		}
		sort.Strings(kids)
		page, per := int(body["page"].(float64)), int(body["per_page"].(float64))
		start := min((page-1)*per, len(kids))
		end := min(start+per, len(kids))
		res := api.ListResponse{Total: int64(len(kids)), Content: []api.Object{}}
		for _, q := range kids[start:end] {
			res.Content = append(res.Content, s.obj(q, s.nodes[q]))
		}
		s.reply(w, 200, "success", res)
	case "/api/fs/mkdir":
		if !s.mkdirAll(clean(str("path"))) {
			s.reply(w, 500, "not a folder", nil)
			return
		}
		s.reply(w, 200, "success", nil)
	case "/api/fs/rename":
		p := clean(str("path"))
		if _, ok := s.nodes[p]; !ok {
			s.reply(w, 500, "object not found", nil)
			return
		}
		dst := path.Join(path.Dir(p), str("name"))
		if _, ok := s.nodes[dst]; ok {
			s.reply(w, 403, "file ["+str("name")+"] exists", nil)
			return
		}
		s.moveTree(p, dst, false)
		s.reply(w, 200, "success", nil)
	case "/api/fs/move", "/api/fs/copy":
		copy := r.URL.Path == "/api/fs/copy"
		srcDir, dstDir := clean(str("src_dir")), clean(str("dst_dir"))
		var tasks []api.TaskInfo
		for _, name := range names() {
			src, dst := path.Join(srcDir, name), path.Join(dstDir, name)
			if _, ok := s.nodes[src]; !ok {
				s.reply(w, 500, "object not found", nil)
				return
			}
			if _, ok := s.nodes[dst]; ok {
				s.reply(w, 403, "file ["+name+"] exists", nil)
				return
			}
			if storage(src) == storage(dst) {
				s.moveTree(src, dst, copy)
				continue
			}
			s.nextID++
			id := "task" + strconv.Itoa(s.nextID)
			s.tasks[id] = &fakeTask{name: name, run: func() { s.moveTree(src, dst, copy) }}
			tasks = append(tasks, api.TaskInfo{ID: id, Name: name, State: api.TaskPending})
		}
		if len(tasks) > 0 {
			s.reply(w, 200, "success", api.TasksResponse{Tasks: tasks})
			return
		}
		s.reply(w, 200, "success", map[string]string{"message": "done"})
	case "/api/task/move/info", "/api/task/copy/info":
		t, ok := s.tasks[r.URL.Query().Get("tid")]
		if !ok {
			s.reply(w, 500, "task not found", nil)
			return
		}
		t.polls++
		state := api.TaskRunning
		if t.polls >= 2 {
			if t.run != nil {
				t.run()
				t.run = nil
			}
			state = api.TaskSucceeded
		}
		s.reply(w, 200, "success", api.TaskInfo{ID: r.URL.Query().Get("tid"), Name: t.name, State: state})
	case "/api/fs/remove":
		dir := clean(str("dir"))
		for _, name := range names() {
			p := path.Join(dir, name)
			if _, ok := s.nodes[p]; !ok {
				s.reply(w, 500, "object not found", nil)
				return
			}
			for q := range s.nodes {
				if q == p || strings.HasPrefix(q, p+"/") {
					delete(s.nodes, q)
				}
			}
		}
		s.reply(w, 200, "success", nil)
	case "/api/fs/put":
		raw, err := url.PathUnescape(r.Header.Get("File-Path"))
		if err != nil || r.Method != "PUT" {
			s.reply(w, 400, "bad path", nil)
			return
		}
		p := clean(raw)
		data, _ := io.ReadAll(r.Body)
		if r.ContentLength != int64(len(data)) {
			s.reply(w, 400, "short body", nil)
			return
		}
		if md := r.Header.Get("X-File-Md5"); md != "" {
			if sum := md5.Sum(data); md != hex.EncodeToString(sum[:]) {
				s.reply(w, 400, "md5 mismatch", nil)
				return
			}
		}
		if !s.mkdirAll(path.Dir(p)) {
			s.reply(w, 500, "not a folder", nil)
			return
		}
		ms, _ := strconv.ParseInt(r.Header.Get("Last-Modified"), 10, 64)
		s.nodes[p] = &fakeNode{data: data, modTime: time.UnixMilli(ms)}
		s.reply(w, 200, "success", nil)
	default:
		s.t.Errorf("fake openlist: unexpected %s %s", r.Method, r.URL)
		s.reply(w, 404, "not found", nil)
	}
}

// TestFakeIntegration runs the generic backend tests against the fake
// server, within one storage and with modification times kept.
func TestFakeIntegration(t *testing.T) {
	old := listPageSize
	listPageSize = 3 // exercise paging
	t.Cleanup(func() { listPageSize = old })
	s := newFakeOpenList(t)
	fstests.Run(t, &fstests.Opt{
		RemoteName:      s.remote(",preserve_mod_time=true") + "mount/test",
		NilObject:       (*Object)(nil),
		SkipInvalidUTF8: true,
	})
}

func newTestFs(t *testing.T, s *fakeOpenList, root string) *Fs {
	t.Helper()
	m := configmap.Simple{"url": s.srv.URL, "username": s.user, "password": obscure.MustObscure(s.password)}
	f, err := NewFs(context.Background(), "test", root, m)
	require.NoError(t, err)
	return f.(*Fs)
}

func put(t *testing.T, f *Fs, remote, content string) fs.Object {
	t.Helper()
	src := object.NewStaticObjectInfo(remote, time.Now(), int64(len(content)), true, nil, nil)
	o, err := f.Put(context.Background(), strings.NewReader(content), src)
	require.NoError(t, err)
	return o
}

// TestCrossStorageTasks checks copies and moves between mounts wait
// for OpenList's background tasks.
func TestCrossStorageTasks(t *testing.T) {
	ctx := context.Background()
	s := newFakeOpenList(t)
	a := newTestFs(t, s, "a")
	b := newTestFs(t, s, "b")
	o := put(t, a, "dir/file.txt", "hello")

	c, err := b.Copy(ctx, o, "copied/renamed.txt")
	require.NoError(t, err)
	assert.Equal(t, int64(5), c.Size())
	s.mu.Lock()
	assert.Equal(t, "hello", string(s.nodes["/b/copied/renamed.txt"].data))
	assert.Contains(t, s.nodes, "/a/dir/file.txt", "copy keeps the source")
	s.mu.Unlock()

	m, err := b.Move(ctx, o, "moved.txt")
	require.NoError(t, err)
	assert.Equal(t, "moved.txt", m.Remote())
	s.mu.Lock()
	assert.NotContains(t, s.nodes, "/a/dir/file.txt")
	assert.Contains(t, s.nodes, "/b/moved.txt")
	s.mu.Unlock()

	require.NoError(t, b.DirMove(ctx, a, "dir", "dir2"))
	s.mu.Lock()
	assert.Contains(t, s.nodes, "/b/dir2")
	s.mu.Unlock()
}

func TestHashesAndRelogin(t *testing.T) {
	ctx := context.Background()
	s := newFakeOpenList(t)
	f := newTestFs(t, s, "")
	o := put(t, f, "x/y.bin", "abc")
	md, err := o.Hash(ctx, hash.MD5)
	require.NoError(t, err)
	assert.Equal(t, "900150983cd24fb0d6963f7d28e17f72", md)

	// Session expires: rclone logs in again
	s.mu.Lock()
	clear(s.tokens)
	s.mu.Unlock()
	_, err = f.List(ctx, "x")
	require.NoError(t, err)
	s.mu.Lock()
	assert.Equal(t, 2, s.logins)
	s.mu.Unlock()

	// Storages without hashes give empty ones
	s.mu.Lock()
	s.hashes = false
	s.mu.Unlock()
	o2, err := f.NewObject(ctx, "x/y.bin")
	require.NoError(t, err)
	md, err = o2.Hash(ctx, hash.MD5)
	require.NoError(t, err)
	assert.Equal(t, "", md)
	assert.Equal(t, fs.ModTimeNotSupported, f.Precision(), "mod times not trusted by default")

	rc, err := o2.Open(ctx)
	require.NoError(t, err)
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, "abc", string(got))
}

func TestTokenExpiry(t *testing.T) {
	enc := func(claims string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".s"
	}
	assert.True(t, tokenExpiry("admin-token-not-jwt").IsZero())
	assert.Equal(t, int64(1700000000), tokenExpiry(enc(`{"exp":1700000000}`)).Unix())
	assert.True(t, tokenExpiry(enc(`{}`)).IsZero())
}

func TestPaths(t *testing.T) {
	f := &Fs{root: "mount/sub dir", siteURL: "https://al.example.com"}
	assert.Equal(t, "/mount/sub dir/a#b", f.fullPath("a#b"))
	assert.Equal(t, "/mount/sub%20dir/a%23b", escapePath(f.fullPath("a#b")))
	assert.Equal(t, "https://al.example.com/d/mount/sub%20dir/x?sign=s%3D1", f.downloadURL(f.fullPath("x"), "s=1"))
	f.root = ""
	assert.Equal(t, "/", f.fullPath(""))
}
