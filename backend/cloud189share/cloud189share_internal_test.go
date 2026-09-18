package cloud189share

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

func TestNewFsMissingConfig(t *testing.T) {
	_, err := NewFs(context.Background(), "x", "", configmap.Simple{})
	if err == nil {
		t.Fatal("expected config error")
	}
}

func TestMockListAndDownload(t *testing.T) {
	const payload = "hello-189share"
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "getShareInfoByCodeV2"):
			_, _ = io.WriteString(w, `{"res_code":0,"shareId":1,"fileId":11,"isFolder":true,"shareMode":1,"fileName":"share"}`)
		case strings.Contains(r.URL.Path, "listShareDir"):
			_, _ = io.WriteString(w, `{"res_code":0,"fileListAO":{"count":1,"fileList":[{"id":20,"name":"readme.txt","size":14,"lastOpTime":"2023-11-14 22:13:21"}],"folderList":[]}}`)
		case strings.Contains(r.URL.Path, "getFileDownloadUrl"):
			_, _ = io.WriteString(w, `{"res_code":0,"fileDownloadUrl":"`+ts.URL+`/dl/readme"}`)
		case strings.HasSuffix(r.URL.Path, "/dl/readme"):
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"res_code":1,"res_message":"not found"}`)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "test189share", "", configmap.Simple{"share_code": "abc", "endpoint": ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fsi.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 1 {
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
}
