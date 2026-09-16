package alipan

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/alipan/api"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/hash"
)

func TestFileIsDir(t *testing.T) {
	dir := api.File{Type: "folder", FileID: "d1", Name: "docs"}
	if !dir.IsDir() {
		t.Fatal("expected dir")
	}
	file := api.File{Type: "file", FileID: "f1", Name: "a.txt"}
	if file.IsDir() {
		t.Fatal("expected file")
	}
}

func TestNewFsMissingCredentials(t *testing.T) {
	_, err := NewFs(context.Background(), "x", "", configmap.Simple{})
	if err == nil {
		t.Fatal("expected credentials error")
	}
}

func TestMockListAndDownload(t *testing.T) {
	const payload = "hello-ali"
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/access_token":
			_, _ = io.WriteString(w, `{"access_token":"tok","refresh_token":"ref2","expires_in":7200}`)
		case "/adrive/v1.0/user/getDriveInfo":
			_, _ = io.WriteString(w, `{"default_drive_id":"drv"}`)
		case "/adrive/v1.0/user/getSpaceInfo":
			_, _ = io.WriteString(w, `{"personal_space_info":{"used_size":10,"total_size":1000}}`)
		case "/adrive/v1.0/openFile/list":
			_, _ = io.WriteString(w, `{"items":[{"drive_id":"drv","file_id":"10","parent_file_id":"root","name":"docs","type":"folder","updated_at":"2024-01-02T03:04:05Z"},{"drive_id":"drv","file_id":"20","parent_file_id":"root","name":"readme.txt","type":"file","size":9,"content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","updated_at":"2024-01-02T03:04:05Z"}],"next_marker":""}`)
		case "/adrive/v1.0/openFile/getDownloadUrl":
			_, _ = io.WriteString(w, `{"url":"`+ts.URL+`/dl/readme"}`)
		case "/dl/readme":
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"NotFound","message":"not found"}`)
		}
	}))
	defer ts.Close()

	m := configmap.Simple{
		"refresh_token": "ref",
		"client_id":     "cid",
		"client_secret": "sec",
		"endpoint":      ts.URL,
	}
	ctx := context.Background()
	fsi, err := NewFs(ctx, "testali", "", m)
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
	got, err := obj.Hash(ctx, hash.SHA1)
	if err != nil || got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
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
