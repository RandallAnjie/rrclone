package cloudreve

// An in memory Cloudreve v4 server for tests.
//
// It follows the behaviour of the real server closely enough for the
// generic backend tests: the JSON envelope with errors in "code",
// per item errors of batch operations, cloudreve:// URIs, cursor and
// offset pagination, file name validation, modification times reset by
// moves and copies, metadata, upload sessions which lock a placeholder
// file, several storage policy types and AES-256-CTR encryption of
// uploads.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
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

	"github.com/rclone/rclone/backend/cloudreve/api"
)

// emptyEntity is the primary_entity of a file with no content
const emptyEntity = "entity-empty"

func init() {
	upyunPollInterval = 5 * time.Millisecond
	upyunPollTimeout = 5 * time.Second
}

type fakeNode struct {
	id       string
	isDir    bool
	data     []byte
	modTime  time.Time
	entity   string
	metadata map[string]string
}

// setMeta sets a metadata key of n
func (n *fakeNode) setMeta(key, value string) {
	if n.metadata == nil {
		n.metadata = map[string]string{}
	}
	n.metadata[key] = value
}

type fakeSession struct {
	id          string
	path        string
	size        int64
	modTime     time.Time
	chunks      map[int][]byte
	parts       map[int][]byte // part number -> data for multipart policies
	odData      []byte
	encrypt     *api.EncryptMetadata
	secret      string
	policy      string
	complete    bool
	placeholder bool // the session created the file at path
}

type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	nodes    map[string]*fakeNode // "" is the root
	sessions map[string]*fakeSession
	nextID   int

	// Configuration
	email, password string
	totp            string // 2FA code required if set
	policyType      string
	relay           bool
	encrypt         bool
	chunkSize       int64
	accessTTL       time.Duration
	pageSize        int   // server side page size cap
	offsetPaging    bool  // page by number rather than by cursor
	ossCallbackFail bool  // OSS can't reach Cloudreve when completing
	odLoseReply     bool  // OneDrive takes half of a later chunk then fails
	failCodes       []int // codes to fail the next API calls with

	// State
	access, refresh  string
	accessExpires    time.Time
	logins           int
	encryptedUploads int // finished uploads which were encrypted client side
	serverWrites     int // files written through the Cloudreve server
	refreshes        int
	twoFASessions    map[string]bool
}

func newFakeServer(t *testing.T) *fakeServer {
	s := &fakeServer{
		t:             t,
		nodes:         map[string]*fakeNode{"": {id: "root", isDir: true, modTime: time.Now()}},
		sessions:      map[string]*fakeSession{},
		email:         "user@example.com",
		password:      "secret-pass",
		policyType:    api.PolicyLocal,
		chunkSize:     64 * 1024,
		accessTTL:     time.Hour,
		pageSize:      7,
		twoFASessions: map[string]bool{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fakeServer) newID() string {
	s.nextID++
	return "id" + strconv.Itoa(s.nextID)
}

func (s *fakeServer) reply(w http.ResponseWriter, code int, data any, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": data, "msg": msg})
}

func (s *fakeServer) ok(w http.ResponseWriter, data any) { s.reply(w, 0, data, "") }

func (s *fakeServer) fail(w http.ResponseWriter, code int, msg string) { s.reply(w, code, nil, msg) }

// failBatch reports that a batch operation failed for uri
func (s *fakeServer) failBatch(w http.ResponseWriter, uri string, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":             api.CodeBatchIncomplete,
		"msg":              "Not all operations are successful",
		"aggregated_error": map[string]any{uri: map[string]any{"code": code, "msg": msg}},
	})
}

// parseURI returns the path of a cloudreve://my URI
func parseURI(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "cloudreve" || u.Host != "my" {
		return "", fmt.Errorf("bad uri %q", raw)
	}
	return strings.Trim(u.Path, "/"), nil
}

// validName checks a name can be stored, including by a server on Windows
func validName(name string) bool {
	hasCtl := strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 })
	return name != "" && len(name) < 255 && !strings.ContainsAny(name, "\\/:*?\"<>|") && name != "." && name != ".." &&
		!hasCtl && !strings.HasSuffix(name, " ")
}

// mkdirAll makes p and its parents, returning false if a file is in the way
func (s *fakeServer) mkdirAll(p string) bool {
	if p == "" {
		return true
	}
	if n, ok := s.nodes[p]; ok {
		return n.isDir
	}
	if !s.mkdirAll(parentOf(p)) {
		return false
	}
	s.nodes[p] = &fakeNode{id: s.newID(), isDir: true, modTime: time.Now()}
	return true
}

func parentOf(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}

func (s *fakeServer) fileJSON(p string, n *fakeNode) api.File {
	f := api.File{ID: n.id, Name: path.Base(p), UpdatedAt: n.modTime, CreatedAt: n.modTime, Metadata: n.metadata}
	if n.isDir {
		f.Type = api.FileTypeFolder
	} else {
		f.Size = int64(len(n.data))
		f.PrimaryEntity = n.entity
	}
	return f
}

func (s *fakeServer) children(dir string) []string {
	var out []string
	for p := range s.nodes {
		if p != "" && parentOf(p) == dir {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func (s *fakeServer) authed(r *http.Request) bool {
	return s.access != "" && r.Header.Get("Authorization") == "Bearer "+s.access && time.Now().Before(s.accessExpires)
}

func (s *fakeServer) issue() api.Token {
	s.access = "access-" + s.newID()
	s.refresh = "refresh-" + s.newID()
	s.accessExpires = time.Now().Add(s.accessTTL)
	return api.Token{AccessToken: s.access, RefreshToken: s.refresh, AccessExpires: s.accessExpires, RefreshExpires: time.Now().Add(24 * time.Hour)}
}

func (s *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := r.URL.Path

	// Storage provider endpoints which don't use the envelope
	switch {
	case strings.HasPrefix(p, "/dl/"):
		s.download(w, r, strings.TrimPrefix(p, "/dl/"))
		return
	case strings.HasPrefix(p, "/s3/"):
		s.s3Part(w, r, strings.TrimPrefix(p, "/s3/"))
		return
	case strings.HasPrefix(p, "/s3complete/"):
		s.s3Complete(w, r, strings.TrimPrefix(p, "/s3complete/"))
		return
	case strings.HasPrefix(p, "/od/"):
		s.oneDrivePut(w, r, strings.TrimPrefix(p, "/od/"))
		return
	case strings.HasPrefix(p, "/slave/"):
		s.slaveChunk(w, r, strings.TrimPrefix(p, "/slave/"))
		return
	case strings.HasPrefix(p, "/qiniu/"):
		s.qiniu(w, r, strings.TrimPrefix(p, "/qiniu/"))
		return
	case strings.HasPrefix(p, "/upyun/"):
		s.upyun(w, r, strings.TrimPrefix(p, "/upyun/"))
		return
	}

	p = strings.TrimPrefix(p, "/api/v4")
	switch {
	case p == "/session/token" && r.Method == "POST":
		var req api.LoginRequest
		_ = decodeJSON(r, &req)
		if req.Email != s.email || req.Password != s.password {
			s.fail(w, api.CodeCredentialInvalid, "Wrong password or email address")
			return
		}
		s.logins++
		if s.totp != "" {
			id := s.newID()
			s.twoFASessions[id] = true
			s.reply(w, api.CodeNotFullySuccess, id, "")
			return
		}
		s.ok(w, api.LoginResponse{Token: s.issue()})
		return
	case p == "/session/token/2fa":
		var req api.TwoFARequest
		_ = decodeJSON(r, &req)
		if !s.twoFASessions[req.SessionID] || req.OTP != s.totp {
			s.fail(w, api.Code2FACodeErr, "Incorrect 2FA code")
			return
		}
		delete(s.twoFASessions, req.SessionID)
		s.ok(w, api.LoginResponse{Token: s.issue()})
		return
	case p == "/session/token/refresh":
		var req api.RefreshRequest
		_ = decodeJSON(r, &req)
		if req.RefreshToken == "" || req.RefreshToken != s.refresh {
			s.fail(w, api.CodeCredentialInvalid, "Failed to issue token pair")
			return
		}
		s.refreshes++
		s.ok(w, s.issue())
		return
	case strings.HasPrefix(p, "/callback/"):
		s.callback(w, r, strings.Split(strings.TrimPrefix(p, "/callback/"), "/"))
		return
	}

	if !s.authed(r) {
		s.fail(w, api.CodeCheckLogin, "Login required")
		return
	}
	if len(s.failCodes) > 0 {
		code := s.failCodes[0]
		s.failCodes = s.failCodes[1:]
		s.fail(w, code, "injected failure")
		return
	}

	switch {
	case p == "/file/info" && r.Method == "GET":
		fp, err := parseURI(r.URL.Query().Get("uri"))
		if err != nil {
			s.fail(w, api.CodeParamErr, err.Error())
			return
		}
		n, ok := s.nodes[fp]
		if !ok {
			s.fail(w, api.CodeParentNotExist, "Path not exist")
			return
		}
		s.ok(w, s.fileJSON(fp, n))
	case p == "/file" && r.Method == "GET":
		s.list(w, r)
	case p == "/file/create":
		var req api.CreateFileRequest
		_ = decodeJSON(r, &req)
		fp, err := parseURI(req.URI)
		if err != nil || !validName(path.Base(fp)) {
			s.fail(w, api.CodeParamErr, "invalid name")
			return
		}
		if n, ok := s.nodes[fp]; ok {
			if req.ErrOnConflict || n.isDir != (req.Type == "folder") {
				s.fail(w, api.CodeObjectExist, "Object existed")
				return
			}
			s.ok(w, s.fileJSON(fp, n))
			return
		}
		if !s.mkdirAll(parentOf(fp)) {
			s.fail(w, api.CodeObjectExist, "file in the way")
			return
		}
		n := &fakeNode{id: s.newID(), isDir: req.Type == "folder", modTime: time.Now(), entity: emptyEntity}
		s.nodes[fp] = n
		if !n.isDir {
			s.serverWrites++
		}
		s.ok(w, s.fileJSON(fp, n))
	case p == "/file/rename":
		var req api.RenameRequest
		_ = decodeJSON(r, &req)
		fp, err := parseURI(req.URI)
		if err != nil || !validName(req.NewName) {
			s.fail(w, api.CodeParamErr, "invalid name")
			return
		}
		if _, ok := s.nodes[fp]; !ok {
			s.fail(w, api.CodeParentNotExist, "Path not exist")
			return
		}
		dst := path.Join(parentOf(fp), req.NewName)
		if _, ok := s.nodes[dst]; ok {
			s.fail(w, api.CodeObjectExist, "Object existed")
			return
		}
		s.moveTree(fp, dst, false)
		s.ok(w, nil)
	case p == "/file/move":
		var req api.MoveRequest
		_ = decodeJSON(r, &req)
		dstDir, err := parseURI(req.Dst)
		if err != nil {
			s.fail(w, api.CodeParamErr, err.Error())
			return
		}
		if n, ok := s.nodes[dstDir]; !ok || !n.isDir {
			s.fail(w, api.CodeParentNotExist, "Parent not exist")
			return
		}
		for _, u := range req.URIs {
			fp, _ := parseURI(u)
			if _, ok := s.nodes[fp]; !ok {
				s.failBatch(w, u, api.CodeParentNotExist, "Path not exist")
				return
			}
			dst := path.Join(dstDir, path.Base(fp))
			if _, ok := s.nodes[dst]; ok {
				s.failBatch(w, u, api.CodeObjectExist, "Object existed")
				return
			}
			s.moveTree(fp, dst, req.Copy)
		}
		s.ok(w, nil)
	case p == "/file" && r.Method == "DELETE":
		var req api.DeleteRequest
		_ = decodeJSON(r, &req)
		for _, u := range req.URIs {
			fp, _ := parseURI(u)
			if _, ok := s.nodes[fp]; !ok || fp == "" {
				s.failBatch(w, u, api.CodeParentNotExist, "Path not exist")
				return
			}
			for q := range s.nodes {
				if q == fp || strings.HasPrefix(q, fp+"/") {
					delete(s.nodes, q)
				}
			}
		}
		s.ok(w, nil)
	case p == "/file/url":
		var req api.FileURLRequest
		_ = decodeJSON(r, &req)
		fp, _ := parseURI(req.URIs[0])
		n, ok := s.nodes[fp]
		if !ok || n.isDir {
			s.fail(w, api.CodeParentNotExist, "Path not exist")
			return
		}
		// Relative like local storage, which is resolved against the site
		s.ok(w, api.FileURLResponse{URLs: []api.EntityURL{{URL: "/dl/" + url.PathEscape(fp)}}})
	case p == "/file/content" && r.Method == "PUT":
		fp, err := parseURI(r.URL.Query().Get("uri"))
		if err != nil {
			s.fail(w, api.CodeParamErr, err.Error())
			return
		}
		data, _ := io.ReadAll(r.Body)
		s.serverWrites++
		if !s.mkdirAll(parentOf(fp)) {
			s.fail(w, api.CodeObjectExist, "file in the way")
			return
		}
		n, ok := s.nodes[fp]
		if !ok {
			n = &fakeNode{id: s.newID()}
			s.nodes[fp] = n
		}
		if n.metadata[api.MetaUploadSessionID] != "" {
			s.fail(w, api.CodeLockConflict, "Lock conflict")
			return
		}
		n.data, n.modTime, n.entity = data, time.Now(), s.newID()
		s.ok(w, s.fileJSON(fp, n))
	case p == "/file/metadata" && r.Method == "PATCH":
		s.patchMetadata(w, r)
	case p == "/file/upload" && r.Method == "PUT":
		s.createSession(w, r)
	case p == "/file/upload" && r.Method == "DELETE":
		var req api.DeleteUploadSessionRequest
		_ = decodeJSON(r, &req)
		s.cancelSession(req.ID)
		s.ok(w, nil)
	case strings.HasPrefix(p, "/file/upload/") && r.Method == "POST":
		parts := strings.Split(strings.TrimPrefix(p, "/file/upload/"), "/")
		index, _ := strconv.Atoi(parts[1])
		s.storeChunk(w, parts[0], index, r.Body)
	case p == "/user/capacity":
		var used int64
		for _, n := range s.nodes {
			used += int64(len(n.data))
		}
		s.ok(w, api.Capacity{Total: 1 << 40, Used: used})
	case p == "/share" && r.Method == "PUT":
		var req api.ShareCreateRequest
		_ = decodeJSON(r, &req)
		fp, _ := parseURI(req.URI)
		if _, ok := s.nodes[fp]; !ok {
			s.fail(w, api.CodeParentNotExist, "Path not exist")
			return
		}
		s.ok(w, s.srv.URL+"/s/"+s.newID())
	default:
		s.t.Errorf("fake cloudreve: unexpected %s %s", r.Method, r.URL)
		s.fail(w, api.CodeNotFound, "not found")
	}
}

func (s *fakeServer) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fp, err := parseURI(q.Get("uri"))
	if err != nil {
		s.fail(w, api.CodeParamErr, err.Error())
		return
	}
	n, ok := s.nodes[fp]
	if !ok {
		s.fail(w, api.CodeParentNotExist, "Path not exist")
		return
	}
	if !n.isDir {
		s.ok(w, api.ListResponse{Files: []api.File{s.fileJSON(fp, n)}, SingleFileView: true})
		return
	}
	size, _ := strconv.Atoi(q.Get("page_size"))
	if size <= 0 || size > s.pageSize {
		size = s.pageSize
	}
	all := s.children(fp)
	res := api.ListResponse{
		Pagination:    &api.Pagination{PageSize: size, IsCursor: true},
		StoragePolicy: &api.StoragePolicy{ID: "p1", Name: "Default", Type: s.policyType},
	}
	start, _ := strconv.Atoi(q.Get("next_page_token"))
	if s.offsetPaging {
		page, _ := strconv.Atoi(q.Get("page"))
		start = min(page*size, len(all))
		res.Pagination = &api.Pagination{Page: page, PageSize: size, TotalItems: len(all)}
	}
	end := min(start+size, len(all))
	for _, c := range all[start:end] {
		res.Files = append(res.Files, s.fileJSON(c, s.nodes[c]))
	}
	if end < len(all) && !s.offsetPaging {
		res.Pagination.NextPageToken = strconv.Itoa(end)
	}
	s.ok(w, res)
}

// patchMetadata sets metadata, which doesn't count as changing the file
func (s *fakeServer) patchMetadata(w http.ResponseWriter, r *http.Request) {
	var req api.PatchMetadataRequest
	_ = decodeJSON(r, &req)
	for _, patch := range req.Patches {
		// The WebDAV server panics on keys without a namespace
		key, isDav := strings.CutPrefix(patch.Key, "dav:")
		namespace, name, hasName := strings.Cut(key, "|")
		var prop struct {
			InnerXML []byte `json:"i"`
		}
		if !isDav || !hasName || namespace == "" || name == "" || json.Unmarshal([]byte(patch.Value), &prop) != nil {
			s.fail(w, api.CodeParamErr, "invalid metadata")
			return
		}
	}
	for _, u := range req.URIs {
		fp, _ := parseURI(u)
		n, ok := s.nodes[fp]
		if !ok {
			s.failBatch(w, u, api.CodeParentNotExist, "Path not exist")
			return
		}
		for _, patch := range req.Patches {
			n.setMeta(patch.Key, patch.Value)
		}
	}
	s.ok(w, nil)
}

// cancelSession drops a session and the lock it holds on its file
func (s *fakeServer) cancelSession(id string) {
	sess, ok := s.sessions[id]
	if !ok {
		return
	}
	delete(s.sessions, id)
	if sess.placeholder {
		delete(s.nodes, sess.path)
	} else if n, ok := s.nodes[sess.path]; ok {
		delete(n.metadata, api.MetaUploadSessionID)
	}
}

// moveTree moves or copies src and everything under it to dst. Like
// any change to a database row this resets the modification time of
// the rows it touches.
func (s *fakeServer) moveTree(src, dst string, copy bool) {
	for q, n := range s.nodes {
		if q != src && !strings.HasPrefix(q, src+"/") {
			continue
		}
		nq := dst + strings.TrimPrefix(q, src)
		if copy {
			c := *n
			c.id = s.newID()
			c.data = bytes.Clone(n.data)
			c.metadata = maps.Clone(n.metadata)
			c.modTime = time.Now()
			s.nodes[nq] = &c
		} else {
			delete(s.nodes, q)
			s.nodes[nq] = n
		}
	}
	s.nodes[dst].modTime = time.Now()
}

func (s *fakeServer) download(w http.ResponseWriter, r *http.Request, escaped string) {
	fp, _ := url.PathUnescape(escaped)
	n, ok := s.nodes[fp]
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, path.Base(fp), n.modTime, bytes.NewReader(n.data))
}

func (s *fakeServer) createSession(w http.ResponseWriter, r *http.Request) {
	var req api.UploadSessionRequest
	_ = decodeJSON(r, &req)
	fp, err := parseURI(req.URI)
	if err != nil || !validName(path.Base(fp)) {
		s.fail(w, api.CodeParamErr, "invalid name")
		return
	}
	n, exists := s.nodes[fp]
	if exists && n.metadata[api.MetaUploadSessionID] != "" {
		s.fail(w, api.CodeLockConflict, "Lock conflict")
		return
	}
	if exists && (req.EntityType != "version" || n.isDir) {
		s.fail(w, api.CodeObjectExist, "Object existed")
		return
	}
	if !s.mkdirAll(parentOf(fp)) {
		s.fail(w, api.CodeObjectExist, "file in the way")
		return
	}
	sess := &fakeSession{
		id:      s.newID(),
		path:    fp,
		size:    req.Size,
		modTime: time.UnixMilli(req.LastModified),
		chunks:  map[int][]byte{},
		parts:   map[int][]byte{},
		secret:  "secret" + s.newID(),
		policy:  s.policyType,
	}
	s.sessions[sess.id] = sess
	if !exists {
		n = &fakeNode{id: s.newID(), modTime: sess.modTime, entity: emptyEntity}
		s.nodes[fp] = n
		sess.placeholder = true
	}
	n.setMeta(api.MetaUploadSessionID, sess.id)
	res := api.UploadSession{
		SessionID:      sess.id,
		ChunkSize:      s.chunkSize,
		URI:            req.URI,
		CallbackSecret: sess.secret,
		StoragePolicy:  &api.StoragePolicy{ID: "p1", Type: s.policyType, Relay: s.relay, Encryption: s.encrypt},
	}
	if s.relay {
		res.ChunkSize = 0
	}
	if s.encrypt && !s.relay && len(req.EncryptionSupported) > 0 {
		key := make([]byte, 32)
		iv := make([]byte, 16)
		_, _ = rand.Read(key)
		_, _ = rand.Read(iv)
		// Start near a counter overflow to test the carry
		for i := 8; i < 16; i++ {
			iv[i] = 0xff
		}
		sess.encrypt = &api.EncryptMetadata{Algorithm: api.CipherAES256CTR, KeyPlainText: key, IV: iv}
		res.EncryptMetadata = sess.encrypt
	}
	if !s.relay {
		nChunks := 1
		if s.chunkSize > 0 {
			nChunks = max(1, int((req.Size+s.chunkSize-1)/s.chunkSize))
		}
		switch s.policyType {
		case api.PolicyS3, api.PolicyKS3, api.PolicyCOS, api.PolicyOBS, api.PolicyOSS:
			for i := range nChunks {
				res.UploadURLs = append(res.UploadURLs, fmt.Sprintf("%s/s3/%s/%d?X-Signature=abc", s.srv.URL, sess.id, i+1))
			}
			res.CompleteURL = fmt.Sprintf("%s/s3complete/%s?uploadId=u1", s.srv.URL, sess.id)
		case api.PolicyOneDrive:
			res.UploadURLs = []string{s.srv.URL + "/od/" + sess.id}
		case api.PolicyRemote:
			res.UploadURLs = []string{s.srv.URL + "/slave/" + sess.id}
			res.Credential = "Bearer slave-" + sess.id
		case api.PolicyQiniu:
			res.UploadURLs = []string{s.srv.URL + "/qiniu/" + sess.id}
			res.Credential = "qiniu-token-" + sess.id
		case api.PolicyUpyun:
			res.UploadURLs = []string{s.srv.URL + "/upyun/" + sess.id}
			res.Credential = "upyun-auth-" + sess.id
			res.UploadPolicy = "upyun-policy-" + sess.id
		}
	}
	s.ok(w, res)
}

// ctrDecrypt decrypts data the way the Cloudreve server does
func ctrDecrypt(m *api.EncryptMetadata, data []byte) []byte {
	block, _ := aes.NewCipher(m.KeyPlainText)
	counter := bytes.Clone(m.IV)
	out := make([]byte, len(data))
	cipher.NewCTR(block, counter).XORKeyStream(out, data)
	return out
}

// finish stores the uploaded data of sess
func (s *fakeServer) finish(sess *fakeSession, data []byte) error {
	if int64(len(data)) != sess.size {
		return fmt.Errorf("size mismatch: got %d want %d", len(data), sess.size)
	}
	if sess.encrypt != nil {
		data = ctrDecrypt(sess.encrypt, data)
		if len(data) > 0 {
			s.encryptedUploads++
		}
	}
	if !s.mkdirAll(parentOf(sess.path)) {
		return fmt.Errorf("file in the way")
	}
	n, ok := s.nodes[sess.path]
	if !ok {
		n = &fakeNode{id: s.newID()}
		s.nodes[sess.path] = n
	}
	n.data, n.modTime, n.entity = data, sess.modTime, s.newID()
	delete(n.metadata, api.MetaUploadSessionID)
	sess.complete = true
	delete(s.sessions, sess.id)
	return nil
}

func joinChunks(m map[int][]byte, first int) []byte {
	var buf bytes.Buffer
	for i := first; ; i++ {
		c, ok := m[i]
		if !ok {
			return buf.Bytes()
		}
		buf.Write(c)
	}
}

// storeChunk handles relay and slave chunks, finishing after the last
func (s *fakeServer) storeChunk(w http.ResponseWriter, id string, index int, body io.Reader) {
	sess, ok := s.sessions[id]
	if !ok {
		s.fail(w, api.CodeUploadSessionExpired, "Upload session expired or not exist")
		return
	}
	data, _ := io.ReadAll(body)
	sess.chunks[index] = data
	all := joinChunks(sess.chunks, 0)
	if int64(len(all)) == sess.size {
		if err := s.finish(sess, all); err != nil {
			s.fail(w, api.CodeParamErr, err.Error())
			return
		}
	}
	s.ok(w, nil)
}

func (s *fakeServer) slaveChunk(w http.ResponseWriter, r *http.Request, id string) {
	if r.Header.Get("Authorization") != "Bearer slave-"+id {
		s.fail(w, api.CodeCredentialInvalid, "bad slave signature")
		return
	}
	index, _ := strconv.Atoi(r.URL.Query().Get("chunk"))
	s.storeChunk(w, id, index, r.Body)
}

func (s *fakeServer) s3Part(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(rest, "/")
	sess, ok := s.sessions[parts[0]]
	if !ok || r.Method != "PUT" || r.URL.Query().Get("X-Signature") != "abc" {
		http.Error(w, "<Error>NoSuchUpload</Error>", http.StatusNotFound)
		return
	}
	if r.Header.Get("Content-Type") != octetStream {
		http.Error(w, "<Error>SignatureDoesNotMatch</Error>", http.StatusForbidden)
		return
	}
	n, _ := strconv.Atoi(parts[1])
	data, _ := io.ReadAll(r.Body)
	sess.parts[n] = data
	w.Header().Set("ETag", fmt.Sprintf("\"etag-%d\"", n))
}

func (s *fakeServer) s3Complete(w http.ResponseWriter, r *http.Request, id string) {
	sess, ok := s.sessions[id]
	if !ok || r.Method != "POST" {
		http.Error(w, "<Error>NoSuchUpload</Error>", http.StatusNotFound)
		return
	}
	if r.Header.Get("Content-Type") != octetStream {
		http.Error(w, "<Error>SignatureDoesNotMatch</Error>", http.StatusForbidden)
		return
	}
	switch sess.policy {
	case api.PolicyOSS:
		if r.Header.Get("x-oss-complete-all") != "yes" {
			_, _ = io.WriteString(w, "<Error>missing complete-all</Error>")
			return
		}
		if s.ossCallbackFail {
			w.WriteHeader(http.StatusNonAuthoritativeInfo)
			_, _ = io.WriteString(w, `{"Status":"CallbackFailed"}`)
			return
		}
	default:
		var c struct {
			Parts []struct {
				PartNumber int
				ETag       string
			} `xml:"Part"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := xml.Unmarshal(body, &c); err != nil || len(c.Parts) == 0 || len(c.Parts) != len(sess.parts) {
			_, _ = io.WriteString(w, "<Error>InvalidPart</Error>")
			return
		}
		for i, p := range c.Parts {
			etag := fmt.Sprintf("\"etag-%d\"", i+1)
			if p.PartNumber != i+1 || p.ETag != etag || !bytes.Contains(body, []byte("<ETag>"+etag+"</ETag>")) {
				_, _ = io.WriteString(w, "<Error>InvalidPart</Error>")
				return
			}
		}
		if sess.policy == api.PolicyCOS && r.Header.Get("x-cos-forbid-overwrite") != "true" {
			http.Error(w, "<Error>SignatureDoesNotMatch</Error>", http.StatusForbidden)
			return
		}
	}
	data := joinChunks(sess.parts, 1)
	switch sess.policy {
	case api.PolicyOSS, api.PolicyOBS:
		// These call Cloudreve back and relay its reply
		if err := s.finish(sess, data); err != nil {
			s.fail(w, api.CodeParamErr, err.Error())
			return
		}
		s.ok(w, nil)
	default:
		sess.odData = data // completed at the storage, waiting for the callback
		_, _ = io.WriteString(w, "<CompleteMultipartUploadResult></CompleteMultipartUploadResult>")
	}
}

func (s *fakeServer) oneDrivePut(w http.ResponseWriter, r *http.Request, id string) {
	sess, ok := s.sessions[id]
	if !ok {
		http.Error(w, "gone", http.StatusNotFound)
		return
	}
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.OneDriveStatus{NextExpectedRanges: []string{fmt.Sprintf("%d-", len(sess.odData))}})
		return
	}
	var start, end, total int64
	if _, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || start != int64(len(sess.odData)) || total != sess.size {
		http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	data, _ := io.ReadAll(r.Body)
	if s.odLoseReply && start > 0 {
		s.odLoseReply = false
		sess.odData = append(sess.odData, data[:len(data)/2]...)
		http.Error(w, "connection lost", http.StatusServiceUnavailable)
		return
	}
	sess.odData = append(sess.odData, data...)
	w.WriteHeader(http.StatusAccepted)
}

// upyun takes a form upload, then tells Cloudreve about it a little later
func (s *fakeServer) upyun(w http.ResponseWriter, r *http.Request, id string) {
	sess, ok := s.sessions[id]
	if !ok {
		http.Error(w, "gone", http.StatusNotFound)
		return
	}
	form, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fields := map[string]string{}
	var data []byte
	gotFile := false
	for {
		part, err := form.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil || gotFile {
			http.Error(w, "the file must be the last field", http.StatusBadRequest)
			return
		}
		value, _ := io.ReadAll(part)
		if part.FormName() == "file" {
			data, gotFile = value, true
		} else {
			fields[part.FormName()] = string(value)
		}
	}
	if !gotFile || fields["policy"] != "upyun-policy-"+id || fields["authorization"] != "upyun-auth-"+id {
		http.Error(w, "bad form", http.StatusUnauthorized)
		return
	}
	time.AfterFunc(20*time.Millisecond, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = s.finish(sess, data)
	})
}

func (s *fakeServer) qiniu(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(rest, "/")
	sess, ok := s.sessions[parts[0]]
	if !ok || r.Header.Get("Authorization") != "UpToken qiniu-token-"+parts[0] {
		http.Error(w, `{"error":"bad token"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "PUT" {
		n, _ := strconv.Atoi(parts[1])
		data, _ := io.ReadAll(r.Body)
		sess.parts[n] = data
		_ = json.NewEncoder(w).Encode(map[string]string{"etag": "q" + parts[1]})
		return
	}
	var c api.QiniuComplete
	_ = decodeJSON(r, &c)
	if len(c.Parts) != len(sess.parts) {
		http.Error(w, `{"error":"parts"}`, http.StatusBadRequest)
		return
	}
	if err := s.finish(sess, joinChunks(sess.parts, 1)); err != nil {
		s.fail(w, api.CodeParamErr, err.Error())
		return
	}
	s.ok(w, nil)
}

func (s *fakeServer) callback(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 3 {
		s.fail(w, api.CodeParamErr, "bad callback")
		return
	}
	sess, ok := s.sessions[parts[1]]
	if !ok || sess.secret != parts[2] || sess.policy != parts[0] {
		s.fail(w, api.CodeUploadSessionExpired, "Upload session expired or not exist")
		return
	}
	if err := s.finish(sess, sess.odData); err != nil {
		s.fail(w, api.CodeParamErr, err.Error())
		return
	}
	s.ok(w, nil)
}
