package bunny

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

		if strings.HasSuffix(r.URL.Path, "/dl/readme") {
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"errno":0,"status":0,"items":[{"id":"20","name":"readme.txt","size":13,"url":"`+ts.URL+`/dl/readme"}],"list":[{"id":"20","name":"readme.txt","size":13,"url":"`+ts.URL+`/dl/readme"}],"files":[{"id":"20","name":"readme.txt","size":13,"url":"`+ts.URL+`/dl/readme"}],"data":{"list":[{"id":"20","name":"readme.txt","size":13,"url":"`+ts.URL+`/dl/readme"}],"content":[{"id":"20","name":"readme.txt","size":13,"url":"`+ts.URL+`/dl/readme"}],"objects":[{"id":"20","name":"readme.txt","size":13,"url":"`+ts.URL+`/dl/readme"}]}}`)

	}))
	defer ts.Close()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "testremote", "", configmap.Simple{"token": "tok", "endpoint": ts.URL})
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
