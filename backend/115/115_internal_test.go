package _115

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/115/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
)

func TestParseCookies(t *testing.T) {
	uid, cid, seid, kid, err := parseCookies("UID=u1; CID=c1; SEID=s1; KID=k1", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if uid != "u1" || cid != "c1" || seid != "s1" || kid != "k1" {
		t.Fatalf("got %s %s %s %s", uid, cid, seid, kid)
	}

	uid, cid, seid, kid, err = parseCookies("", "u2", "c2", "s2", "k2")
	if err != nil {
		t.Fatal(err)
	}
	if uid != "u2" || cid != "c2" || seid != "s2" || kid != "k2" {
		t.Fatalf("split cookies failed")
	}

	_, _, _, _, err = parseCookies("UID=only", "", "", "", "")
	if err == nil {
		t.Fatal("expected error for incomplete cookies")
	}
}

func TestCookieHeader(t *testing.T) {
	got := cookieHeader("u", "c", "s", "k")
	if got != "UID=u; CID=c; SEID=s; KID=k" {
		t.Fatalf("got %q", got)
	}
	got = cookieHeader("u", "c", "s", "")
	if strings.Contains(got, "KID") {
		t.Fatalf("unexpected kid: %q", got)
	}
}

func TestFileInfoIsDir(t *testing.T) {
	dir := api.FileInfo{CategoryID: "123", Name: "folder"}
	if !dir.IsDir() || dir.ID() != "123" {
		t.Fatalf("dir: isDir=%v id=%s", dir.IsDir(), dir.ID())
	}
	file := api.FileInfo{FileID: "456", CategoryID: "123", Name: "a.txt", Size: 12, Sha1: "abc", PickCode: "pc"}
	if file.IsDir() || file.ID() != "456" || file.Parent() != "123" {
		t.Fatalf("file: isDir=%v id=%s parent=%s", file.IsDir(), file.ID(), file.Parent())
	}
}

func TestFlexJSON(t *testing.T) {
	var info api.FileInfo
	raw := []byte(`{"fid":123,"cid":"0","n":"x","s":"42","sha":"AB","pc":"p","te":"1700000000"}`)
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	if info.ID() != "123" || info.Size.Int64() != 42 {
		t.Fatalf("%+v", info)
	}
	if info.ModTime().IsZero() {
		t.Fatal("expected modtime")
	}
}

func TestMockListAndDownload(t *testing.T) {
	const payload = "hello-115"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/files/index_info":
			_, _ = io.WriteString(w, `{"state":true,"data":{"space_info":{"all_total":{"size":1000},"all_use":{"size":100},"all_remain":{"size":900}}}}`)
		case r.URL.Path == "/files" && r.URL.Query().Get("cid") == "0":
			_, _ = io.WriteString(w, `{"state":true,"count":2,"offset":0,"cid":"0","data":[{"cid":"10","n":"docs","pid":"0","te":"1700000000"},{"fid":"20","cid":"0","n":"readme.txt","s":"9","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","pc":"pick1","te":"1700000001"}]}`)
		case r.URL.Path == "/files/download" && r.URL.Query().Get("pickcode") == "pick1":
			_, _ = io.WriteString(w, `{"state":true,"file_url":"`+tsURL(r, "/dl/readme")+`"}`)
		case r.URL.Path == "/dl/readme":
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"state":false,"error":"not found","errno":404}`)
		}
	}))
	defer ts.Close()

	m := configmap.Simple{
		"cookie":   "UID=u; CID=c; SEID=s; KID=k",
		"endpoint": ts.URL,
	}
	ctx := context.Background()
	fsi, err := NewFs(ctx, "test115", "", m)
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
	if obj.Size() != 9 {
		t.Fatalf("size=%d", obj.Size())
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

func tsURL(r *http.Request, path string) string {
	scheme := "http"
	return scheme + "://" + r.Host + path
}

func TestNewFsMissingCookies(t *testing.T) {
	_, err := NewFs(context.Background(), "x", "", configmap.Simple{})
	if err == nil {
		t.Fatal("expected cookie error")
	}
}

func TestPrecision(t *testing.T) {
	var f Fs
	if f.Precision() != fs.ModTimeNotSupported {
		t.Fatal("expected modtime not supported")
	}
}
