package clouddrive2

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/clouddrive2/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/hash"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type mockNode struct {
	name    string
	isDir   bool
	data    []byte
	modTime time.Time
}

type mockSrv struct {
	api.UnimplementedCloudDriveFileSrvServer
	mu       sync.Mutex
	nodes    map[string]*mockNode
	handles  map[uint64]string
	nextH    uint64
	download string
}

func newMockSrv(download string) *mockSrv {
	s := &mockSrv{
		nodes:    map[string]*mockNode{"/": {name: "", isDir: true, modTime: time.Unix(1700000000, 0)}},
		handles:  map[uint64]string{},
		download: download,
	}
	s.nodes["/docs"] = &mockNode{name: "docs", isDir: true, modTime: time.Unix(1700000000, 0)}
	s.nodes["/readme.txt"] = &mockNode{name: "readme.txt", isDir: false, data: []byte("hello-cd2"), modTime: time.Unix(1700000001, 0)}
	return s
}

func parentOf(p string) string {
	p = strings.TrimSuffix(p, "/")
	if p == "" || p == "/" {
		return "/"
	}
	d := path.Dir(p)
	if d == "." || d == "" {
		return "/"
	}
	return d
}

func (s *mockSrv) GetToken(_ context.Context, req *api.GetTokenRequest) (*api.JWTToken, error) {
	if req.GetUserName() == "user" && req.GetPassword() == "pass" {
		return &api.JWTToken{Success: true, Token: "test-jwt"}, nil
	}
	return &api.JWTToken{Success: false, ErrorMessage: "bad credentials"}, nil
}

func toPB(p string, n *mockNode) *api.CloudDriveFile {
	info := &api.CloudDriveFile{
		Id:           p,
		Name:         n.name,
		FullPathName: p,
		Size:         int64(len(n.data)),
		IsDirectory:  n.isDir,
		WriteTime:    timestamppb.New(n.modTime),
	}
	if n.isDir {
		info.FileType = api.CloudDriveFile_Directory
	} else {
		info.FileType = api.CloudDriveFile_File
	}
	return info
}

func (s *mockSrv) GetSubFiles(req *api.ListSubFileRequest, stream grpc.ServerStreamingServer[api.SubFilesReply]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := joinAPI(req.GetPath())
	if _, ok := s.nodes[dir]; !ok {
		return status.Error(codes.NotFound, "not found")
	}
	var files []*api.CloudDriveFile
	for p, n := range s.nodes {
		if p == dir {
			continue
		}
		if parentOf(p) == dir {
			files = append(files, toPB(p, n))
		}
	}
	return stream.Send(&api.SubFilesReply{SubFiles: files})
}

func (s *mockSrv) FindFileByPath(_ context.Context, req *api.FindFileByPathRequest) (*api.CloudDriveFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := joinAPI(req.GetParentPath(), req.GetPath())
	n, ok := s.nodes[p]
	if !ok {
		return nil, status.Error(codes.NotFound, "not found")
	}
	return toPB(p, n), nil
}

func (s *mockSrv) CreateFolder(_ context.Context, req *api.CreateFolderRequest) (*api.CreateFolderResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := joinAPI(req.GetParentPath(), req.GetFolderName())
	n := &mockNode{name: req.GetFolderName(), isDir: true, modTime: time.Now()}
	s.nodes[p] = n
	return &api.CreateFolderResult{
		FolderCreated: toPB(p, n),
		Result:        &api.FileOperationResult{Success: true},
	}, nil
}

func (s *mockSrv) DeleteFile(_ context.Context, req *api.FileRequest) (*api.FileOperationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := joinAPI(req.GetPath())
	for p := range s.nodes {
		if p == target || strings.HasPrefix(p, target+"/") {
			delete(s.nodes, p)
		}
	}
	return &api.FileOperationResult{Success: true}, nil
}

func (s *mockSrv) CreateFile(_ context.Context, req *api.CreateFileRequest) (*api.CreateFileResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := joinAPI(req.GetParentPath(), req.GetFileName())
	s.nextH++
	h := s.nextH
	s.handles[h] = p
	s.nodes[p] = &mockNode{name: req.GetFileName(), isDir: false, modTime: time.Now()}
	return &api.CreateFileResult{FileHandle: h}, nil
}

func (s *mockSrv) WriteToFile(_ context.Context, req *api.WriteFileRequest) (*api.WriteFileResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.handles[req.GetFileHandle()]
	if !ok {
		return nil, status.Error(codes.NotFound, "bad handle")
	}
	n := s.nodes[p]
	end := int(req.GetStartPos()) + len(req.GetBuffer())
	if cap(n.data) < end || len(n.data) < end {
		buf := make([]byte, end)
		copy(buf, n.data)
		n.data = buf
	}
	copy(n.data[req.GetStartPos():], req.GetBuffer())
	if req.GetCloseFile() {
		delete(s.handles, req.GetFileHandle())
	}
	return &api.WriteFileResult{BytesWritten: uint64(len(req.GetBuffer()))}, nil
}

func (s *mockSrv) CloseFile(_ context.Context, req *api.CloseFileRequest) (*api.FileOperationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.handles, req.GetFileHandle())
	return &api.FileOperationResult{Success: true}, nil
}

func (s *mockSrv) GetDownloadUrlPath(_ context.Context, req *api.GetDownloadUrlPathRequest) (*api.DownloadUrlPathInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := joinAPI(req.GetPath())
	n, ok := s.nodes[p]
	if !ok || n.isDir {
		return nil, status.Error(codes.NotFound, "not found")
	}
	u := s.download + "/dl?path=" + urlQuery(p)
	return &api.DownloadUrlPathInfo{DirectUrl: &u}, nil
}

func (s *mockSrv) GetSpaceInfo(context.Context, *api.FileRequest) (*api.SpaceInfo, error) {
	return &api.SpaceInfo{TotalSpace: 1000, UsedSpace: 9, FreeSpace: 991}, nil
}

func (s *mockSrv) RenameFile(_ context.Context, req *api.RenameFileRequest) (*api.FileOperationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := joinAPI(req.GetTheFilePath())
	n, ok := s.nodes[src]
	if !ok {
		return &api.FileOperationResult{Success: false, ErrorMessage: "not found"}, nil
	}
	dst := joinAPI(parentOf(src), req.GetNewName())
	delete(s.nodes, src)
	n.name = req.GetNewName()
	s.nodes[dst] = n
	return &api.FileOperationResult{Success: true}, nil
}

func (s *mockSrv) MoveFile(_ context.Context, req *api.MoveFileRequest) (*api.FileOperationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, src := range req.GetTheFilePaths() {
		src = joinAPI(src)
		n, ok := s.nodes[src]
		if !ok {
			return &api.FileOperationResult{Success: false, ErrorMessage: "not found"}, nil
		}
		dst := joinAPI(req.GetDestPath(), n.name)
		delete(s.nodes, src)
		s.nodes[dst] = n
	}
	return &api.FileOperationResult{Success: true}, nil
}

func (s *mockSrv) CopyFile(_ context.Context, req *api.CopyFileRequest) (*api.FileOperationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, src := range req.GetTheFilePaths() {
		src = joinAPI(src)
		n, ok := s.nodes[src]
		if !ok {
			return &api.FileOperationResult{Success: false, ErrorMessage: "not found"}, nil
		}
		cp := *n
		data := append([]byte(nil), n.data...)
		cp.data = data
		s.nodes[joinAPI(req.GetDestPath(), n.name)] = &cp
	}
	return &api.FileOperationResult{Success: true}, nil
}

func urlQuery(p string) string {
	return strings.ReplaceAll(p, "/", "%2F")
}

func startMock(t *testing.T) (addr string, srv *mockSrv, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mock *mockSrv
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		p = strings.ReplaceAll(p, "%2F", "/")
		if p == "" {
			http.NotFound(w, r)
			return
		}
		mock.mu.Lock()
		n := mock.nodes[p]
		mock.mu.Unlock()
		if n == nil || n.isDir {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, n.name, n.modTime, bytes.NewReader(n.data))
	}))
	mock = newMockSrv(httpSrv.URL)
	gs := grpc.NewServer()
	api.RegisterCloudDriveFileSrvServer(gs, mock)
	go func() { _ = gs.Serve(ln) }()
	return ln.Addr().String(), mock, func() {
		gs.Stop()
		_ = ln.Close()
		httpSrv.Close()
	}
}

func TestNewFsMissingConfig(t *testing.T) {
	_, err := NewFs(context.Background(), "x", "", configmap.Simple{})
	if err == nil {
		t.Fatal("expected config error")
	}
}

func TestMockListDownloadUpload(t *testing.T) {
	addr, mock, stop := startMock(t)
	defer stop()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "testcd2", "", configmap.Simple{
		"address":  addr,
		"username": "user",
		"password": "pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fsi.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("entries=%d", len(entries))
	}
	obj, err := fsi.NewObject(ctx, "readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	in, err := obj.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(in)
	_ = in.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello-cd2" {
		t.Fatalf("got %q", got)
	}
	usage, err := fsi.(fs.Abouter).About(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Used == nil || *usage.Used != 9 {
		t.Fatalf("used=%v", usage.Used)
	}
	src := &memObj{remote: "new.txt", data: []byte("uploaded")}
	dst, err := fsi.Put(ctx, bytes.NewReader(src.data), src)
	if err != nil {
		t.Fatal(err)
	}
	in, err = dst.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(in)
	_ = in.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "uploaded" {
		t.Fatalf("upload got %q", got)
	}
	if err := fsi.Mkdir(ctx, "docs/sub"); err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	_, ok := mock.nodes["/docs/sub"]
	mock.mu.Unlock()
	if !ok {
		t.Fatal("mkdir missing")
	}
}

type memObj struct {
	remote string
	data   []byte
}

func (m *memObj) String() string                    { return m.remote }
func (m *memObj) Remote() string                    { return m.remote }
func (m *memObj) ModTime(context.Context) time.Time { return time.Unix(1700000002, 0) }
func (m *memObj) Size() int64                       { return int64(len(m.data)) }
func (m *memObj) Fs() fs.Info                       { return nil }
func (m *memObj) Hash(context.Context, hash.Type) (string, error) {
	return "", hash.ErrUnsupported
}
func (m *memObj) Storable() bool { return true }
