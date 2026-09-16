package quarkshare

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
	const payload = "hello-qshare"
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/share/sharepage/token"):
			_, _ = io.WriteString(w, `{"status":200,"code":0,"data":{"stoken":"st"}}`)
		case strings.HasSuffix(r.URL.Path, "/share/sharepage/detail"):
			_, _ = io.WriteString(w, `{"status":200,"code":0,"data":{"list":[{"fid":"20","file_name":"readme.txt","pdir_fid":"0","file":true,"size":13,"updated_at":1700000001000}]},"metadata":{"_page":1,"_size":50,"_total":1}}`)
		case strings.HasSuffix(r.URL.Path, "/share/sharepage/download"):
			_, _ = io.WriteString(w, `{"status":200,"code":0,"data":[{"fid":"20","download_url":"`+ts.URL+`/dl/readme"}]}`)
		case strings.HasSuffix(r.URL.Path, "/dl/readme"):
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"status":404,"message":"not found"}`)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "testqshare", "", configmap.Simple{"share_key": "pwd", "cookie": "a=b", "endpoint": ts.URL})
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
