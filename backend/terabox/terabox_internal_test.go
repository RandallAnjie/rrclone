package terabox

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
	const payload = "hello-backend"
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/api/check/login"):
			_, _ = io.WriteString(w, `{"errno":0}`)
		case strings.HasSuffix(r.URL.Path, "/api/list"):
			_, _ = io.WriteString(w, `{"errno":0,"list":[{"fs_id":20,"path":"/readme.txt","server_filename":"readme.txt","isdir":0,"size":13,"server_mtime":1700000001}]}`)
		case strings.HasSuffix(r.URL.Path, "/api/filemetas"):
			_, _ = io.WriteString(w, `{"errno":0,"info":[{"dlink":"`+ts.URL+`/dl/readme"}]}`)
		case strings.HasSuffix(r.URL.Path, "/dl/readme"):
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"errno":-1}`)
		}

	}))
	defer ts.Close()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "testremote", "", configmap.Simple{"cookie": "a=b", "endpoint": ts.URL})
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
