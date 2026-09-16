package openlist

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

func TestNewFsMissingURL(t *testing.T) {
	_, err := NewFs(context.Background(), "x", "", configmap.Simple{})
	if err == nil {
		t.Fatal("expected url error")
	}
}

func TestMockListAndDownload(t *testing.T) {
	const payload = "hello-alist"
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			_, _ = io.WriteString(w, `{"code":200,"data":{"token":"tok"}}`)
		case "/api/fs/list":
			_, _ = io.WriteString(w, `{"code":200,"data":{"content":[{"name":"docs","is_dir":true,"modified":"2024-01-02T03:04:05Z"},{"name":"readme.txt","is_dir":false,"size":11,"modified":"2024-01-02T03:04:05Z","hash_info":{"md5":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]}}`)
		case "/api/fs/get":
			_, _ = io.WriteString(w, `{"code":200,"data":{"name":"readme.txt","is_dir":false,"size":11,"raw_url":"`+ts.URL+`/dl/readme"}}`)
		case "/dl/readme":
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":404,"message":"not found"}`)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "testol", "", configmap.Simple{
		"url":      ts.URL,
		"username": "u",
		"password": "p",
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
