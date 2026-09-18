package quark

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/object"
)

func TestNewFsMissingCookie(t *testing.T) {
	_, err := NewFs(context.Background(), "x", "", configmap.Simple{})
	if err == nil {
		t.Fatal("expected cookie error")
	}
}

func TestMockListAndDownload(t *testing.T) {
	const payload = "hello-quark"
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/config":
			_, _ = io.WriteString(w, `{"status":200,"code":0}`)
		case "/file/sort":
			_, _ = io.WriteString(w, `{"status":200,"code":0,"data":{"list":[{"fid":"10","file_name":"docs","pdir_fid":"0","file":false,"updated_at":1700000000000},{"fid":"20","file_name":"readme.txt","pdir_fid":"0","file":true,"size":11,"updated_at":1700000001000}]},"metadata":{"_page":1,"_size":50,"_total":2}}`)
		case "/file/download":
			_, _ = io.WriteString(w, `{"status":200,"code":0,"data":[{"fid":"20","download_url":"`+ts.URL+`/dl/readme"}]}`)
		case "/dl/readme":
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"status":404,"message":"not found"}`)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "testquark", "", configmap.Simple{"cookie": "a=b", "endpoint": ts.URL})
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
	obj, err := fsi.NewObject(ctx, "readme.txt")
	if err != nil {
		t.Fatal(err)
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
	link, err := fsi.(*Fs).PublicLink(ctx, "readme.txt", fs.DurationOff, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "/dl/readme") {
		t.Fatalf("direct link = %q", link)
	}
}

func TestOSSUpload(t *testing.T) {
	const payload = "hello-oss"
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/config":
			_, _ = io.WriteString(w, `{"status":200,"code":0}`)
		case r.URL.Path == "/file/sort":
			_, _ = io.WriteString(w, `{"status":200,"code":0,"data":{"list":[]},"metadata":{"_page":1,"_size":50,"_total":0}}`)
		case r.URL.Path == "/file/upload/pre":
			_, _ = io.WriteString(w, `{"status":200,"code":0,"data":{"task_id":"t1","finish":false,"upload_id":"u1","obj_key":"obj","upload_url":"`+ts.URL+`","bucket":"","auth_info":"auth","callback":{"callbackUrl":"http://cb","callbackBody":"b"}},"metadata":{"part_size":4096}}`)
		case r.URL.Path == "/file/update/hash":
			_, _ = io.WriteString(w, `{"status":200,"code":0,"data":{"finish":false}}`)
		case r.URL.Path == "/file/upload/auth":
			_, _ = io.WriteString(w, `{"status":200,"code":0,"data":{"auth_key":"OSS auth"}}`)
		case r.URL.Path == "/obj" || strings.HasPrefix(r.URL.Path, "/obj"):
			w.Header().Set("ETag", `"etag1"`)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/file/upload/finish":
			_, _ = io.WriteString(w, `{"status":200,"code":0,"data":{"fid":"99","file_name":"hello.txt","size":9}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"status":404,"message":"`+r.URL.Path+`"}`)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "testquarkoss", "", configmap.Simple{"cookie": "a=b", "endpoint": ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	src := object.NewStaticObjectInfo("hello.txt", time.Now(), int64(len(payload)), true, nil, nil)
	newObj, err := fsi.Put(ctx, strings.NewReader(payload), src)
	if err != nil {
		t.Fatal(err)
	}
	if newObj.Size() != int64(len(payload)) {
		t.Fatalf("uploaded size=%d", newObj.Size())
	}
}
