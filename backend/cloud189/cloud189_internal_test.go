package cloud189

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs/config/configmap"
)

func TestNewFsMissingCookie(t *testing.T) {
	_, err := NewFs(context.Background(), "x", "", configmap.Simple{})
	if err == nil {
		t.Fatal("expected cookie error")
	}
}

func TestMockListAndDownload(t *testing.T) {
	const payload = "hello-189"
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/portal/getUserSizeInfo.action":
			_, _ = io.WriteString(w, `{"res_code":0,"cloudCapacityInfo":{"freeSize":900,"totalSize":1000,"usedSize":100}}`)
		case "/api/open/file/listFiles.action":
			_, _ = io.WriteString(w, `{"res_code":0,"fileListAO":{"count":2,"folderList":[{"id":10,"name":"docs","lastOpTime":"2024-01-02 03:04:05"}],"fileList":[{"id":20,"name":"readme.txt","size":10,"lastOpTime":"2024-01-02 03:04:05"}]}}`)
		case "/api/portal/getFileInfo.action":
			_, _ = io.WriteString(w, `{"res_code":0,"fileDownloadUrl":"`+ts.URL+`/dl/readme"}`)
		case "/dl/readme":
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"res_code":404,"res_message":"not found"}`)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "test189", "", configmap.Simple{"cookie": "a=b", "endpoint": ts.URL})
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
}
