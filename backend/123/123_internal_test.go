package _123

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/123/api"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/object"
)

func TestFileIsDirAndID(t *testing.T) {
	dir := api.File{FileName: "docs", Type: 1, FileID: 10, ParentFileID: 0}
	if !dir.IsDir() || dir.ID() != "10" {
		t.Fatalf("dir: isDir=%v id=%s", dir.IsDir(), dir.ID())
	}
	file := api.File{FileName: "a.txt", Type: 0, FileID: 20, ParentFileID: 10, Size: 9, Etag: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", UpdateAt: "2024-01-02 03:04:05"}
	if file.IsDir() || file.ID() != "20" || file.Parent() != "10" {
		t.Fatalf("file: isDir=%v id=%s parent=%s", file.IsDir(), file.ID(), file.Parent())
	}
	if file.ModTime().IsZero() {
		t.Fatal("expected modtime")
	}
}

func TestParseID(t *testing.T) {
	id, err := api.ParseID("")
	if err != nil || id != 0 {
		t.Fatalf("empty: %d %v", id, err)
	}
	id, err = api.ParseID("42")
	if err != nil || id != 42 {
		t.Fatalf("got %d %v", id, err)
	}
}

func TestNewFsMissingCredentials(t *testing.T) {
	_, err := NewFs(context.Background(), "x", "", configmap.Simple{})
	if err == nil {
		t.Fatal("expected credentials error")
	}
}

func TestMockListDownloadUpload(t *testing.T) {
	const payload = "hello-123"
	sum := md5.Sum([]byte(payload))
	etag := hex.EncodeToString(sum[:])
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/access_token":
			_, _ = io.WriteString(w, `{"code":0,"data":{"accessToken":"tok","expiredAt":"2099-01-01T00:00:00Z"}}`)
		case r.URL.Path == "/api/v1/user/info":
			if got := r.Header.Get("Authorization"); !strings.Contains(got, "Bearer tok") {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"code":401,"message":"no auth"}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"uid":1,"spaceUsed":10,"spacePermanent":1000,"spaceTemp":0}}`)
		case r.URL.Path == "/api/v2/file/list":
			parent := r.URL.Query().Get("parentFileId")
			if parent == "0" {
				_, _ = io.WriteString(w, `{"code":0,"data":{"lastFileId":-1,"fileList":[{"filename":"docs","fileId":10,"parentFileId":0,"type":1,"updateAt":"2024-01-02 03:04:05"},{"filename":"readme.txt","fileId":20,"parentFileId":0,"type":0,"size":9,"etag":"`+etag+`","updateAt":"2024-01-02 03:04:05"}]}}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"lastFileId":-1,"fileList":[]}}`)
		case r.URL.Path == "/api/v1/file/download_info":
			_, _ = io.WriteString(w, `{"code":0,"data":{"downloadUrl":"`+ts.URL+`/dl/readme"}}`)
		case r.URL.Path == "/dl/readme":
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		case r.URL.Path == "/upload/v2/file/sha1_reuse":
			_, _ = io.WriteString(w, `{"code":0,"data":{"fileID":0,"reuse":false}}`)
		case r.URL.Path == "/upload/v2/file/create":
			var req api.UploadCreateReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			if strings.EqualFold(req.Etag, etag) {
				_, _ = io.WriteString(w, `{"code":0,"data":{"fileID":30,"reuse":true}}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"fileID":0,"preuploadID":"pre1","reuse":false,"sliceSize":4,"servers":["`+ts.URL+`"]}}`)
		case r.URL.Path == "/upload/v2/file/slice":
			_, _ = io.WriteString(w, `{"code":0}`)
		case r.URL.Path == "/upload/v2/file/upload_complete":
			_, _ = io.WriteString(w, `{"code":0,"data":{"completed":true,"fileID":31}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":404,"message":"not found"}`)
		}
	}))
	defer ts.Close()

	m := configmap.Simple{
		"client_id":     "cid",
		"client_secret": "sec",
		"endpoint":      ts.URL,
	}
	ctx := context.Background()
	fsi, err := NewFs(ctx, "test123", "", m)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fsi.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%d", len(entries))
	}
	about, err := fsi.(*Fs).About(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if about.Total == nil || *about.Total != 1000 {
		t.Fatalf("about=%v", about)
	}
	obj, err := fsi.NewObject(ctx, "readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Size() != 9 {
		t.Fatalf("size=%d", obj.Size())
	}
	gotHash, err := obj.Hash(ctx, hash.MD5)
	if err != nil || gotHash != etag {
		t.Fatalf("hash=%s err=%v", gotHash, err)
	}
	in, err := obj.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	got, err := io.ReadAll(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("got %q", got)
	}

	src := object.NewStaticObjectInfo("hello.txt", time.Now(), int64(len(payload)), true, map[hash.Type]string{hash.MD5: etag}, nil)
	newObj, err := fsi.Put(ctx, bytes.NewReader([]byte(payload)), src)
	if err != nil {
		t.Fatal(err)
	}
	if newObj.Size() != int64(len(payload)) {
		t.Fatalf("uploaded size=%d", newObj.Size())
	}
}

func TestPrecision(t *testing.T) {
	var f Fs
	if f.Precision() != time.Second {
		t.Fatal("expected 1s precision")
	}
	if !f.Hashes().Contains(hash.MD5) || !f.Hashes().Contains(hash.SHA1) {
		t.Fatal("expected md5 and sha1")
	}
}
