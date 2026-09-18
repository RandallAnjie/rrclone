package baidu

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/hash"
)

func TestNewFsMissingCredentials(t *testing.T) {
	_, err := NewFs(context.Background(), "x", "", configmap.Simple{})
	if err == nil {
		t.Fatal("expected credentials error")
	}
}

func TestMockListAndDownload(t *testing.T) {
	const payload = "hello-baidu"
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/oauth/2.0/token"):
			_, _ = io.WriteString(w, `{"access_token":"tok","refresh_token":"ref2","expires_in":2592000}`)
		case r.URL.Path == "/xpan/file" && r.URL.Query().Get("method") == "list":
			_, _ = io.WriteString(w, `{"errno":0,"list":[{"fs_id":10,"path":"/docs","server_filename":"docs","isdir":1,"server_mtime":1700000000},{"fs_id":20,"path":"/readme.txt","server_filename":"readme.txt","isdir":0,"size":11,"md5":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","server_mtime":1700000001}]}`)
		case r.URL.Path == "/xpan/multimedia":
			_, _ = io.WriteString(w, `{"errno":0,"list":[{"fs_id":20,"dlink":"`+ts.URL+`/dl/readme"}]}`)
		case r.URL.Path == "/api/quota":
			_, _ = io.WriteString(w, `{"errno":0,"total":1000,"used":10}`)
		case r.URL.Path == "/dl/readme":
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errno":-1}`)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "testbaidu", "", configmap.Simple{
		"access_token": "tok",
		"endpoint":     ts.URL,
	})
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
	got, err := obj.Hash(ctx, hash.MD5)
	if err != nil || got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("hash=%s err=%v", got, err)
	}
	in, err := obj.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	body, err := io.ReadAll(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != payload {
		t.Fatalf("got %q", body)
	}
}
