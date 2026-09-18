package yun139

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

func TestNewFsMissingAuth(t *testing.T) {
	_, err := NewFs(context.Background(), "x", "", configmap.Simple{})
	if err == nil {
		t.Fatal("expected auth error")
	}
}

func TestMockListAndDownload(t *testing.T) {
	const payload = "hello-139"
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/orchestration/personalCloud/catalog/v1.0/getDisk":
			_, _ = io.WriteString(w, `{"success":true,"code":"S_OK","data":{"getDiskResult":{"nodeCount":2,"catalogList":[{"catalogID":"10","catalogName":"docs","updateTime":"2024-01-02 03:04:05"}],"contentList":[{"contentID":"20","contentName":"readme.txt","contentSize":10,"digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","updateTime":"2024-01-02 03:04:05"}]}}}`)
		case "/orchestration/personalCloud/uploadAndDownload/v1.0/downloadRequest":
			_, _ = io.WriteString(w, `{"success":true,"code":"S_OK","data":{"downloadURL":"`+ts.URL+`/dl/readme"}}`)
		case "/dl/readme":
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"success":false,"message":"not found"}`)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "test139", "", configmap.Simple{
		"authorization": "Basic xxx",
		"account":        "13800000000",
		"endpoint":       ts.URL,
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
