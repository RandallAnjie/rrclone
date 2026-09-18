package cloud189

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
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
	link, err := fsi.(*Fs).PublicLink(ctx, "readme.txt", fs.DurationOff, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "/dl/readme") {
		t.Fatalf("direct link = %q", link)
	}
}

func TestPasswordLogin(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.StdEncoding.EncodeToString(der)
	const payload = "hello-189-login"
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/portal/loginUrl.action":
			http.Redirect(w, r, ts.URL+"/oauth?lt=LT&reqId=RID&appId=cloud", http.StatusFound)
		case r.URL.Path == "/oauth":
			_, _ = io.WriteString(w, `ok`)
		case r.URL.Path == "/api/logbox/oauth2/appConf.do":
			_, _ = io.WriteString(w, `{"result":"0","data":{"accountType":"02","returnUrl":"https://cloud.189.cn/","mailSuffix":"@189.cn","clientType":1,"paramId":"p"}}`)
		case r.URL.Path == "/api/logbox/config/encryptConf.do":
			_, _ = io.WriteString(w, `{"result":0,"data":{"pre":"100","pubKey":"`+pub+`"}}`)
		case r.URL.Path == "/api/logbox/oauth2/loginSubmit.do":
			http.SetCookie(w, &http.Cookie{Name: "COOKIE_LOGIN_USER", Value: "sess"})
			_, _ = io.WriteString(w, `{"result":0,"msg":"ok","toUrl":"`+ts.URL+`/sso"}`)
		case r.URL.Path == "/sso":
			http.SetCookie(w, &http.Cookie{Name: "COOKIE_LOGIN_USER", Value: "sess"})
			_, _ = io.WriteString(w, `ok`)
		case r.URL.Path == "/api/portal/getUserSizeInfo.action":
			_, _ = io.WriteString(w, `{"res_code":0,"cloudCapacityInfo":{"freeSize":900,"totalSize":1000,"usedSize":100}}`)
		case r.URL.Path == "/api/open/file/listFiles.action":
			_, _ = io.WriteString(w, `{"res_code":0,"fileListAO":{"count":1,"folderList":[],"fileList":[{"id":20,"name":"readme.txt","size":10,"lastOpTime":"2024-01-02 03:04:05"}]}}`)
		case r.URL.Path == "/api/portal/getFileInfo.action":
			_, _ = io.WriteString(w, `{"res_code":0,"fileDownloadUrl":"`+ts.URL+`/dl/readme"}`)
		case r.URL.Path == "/dl/readme":
			http.ServeContent(w, r, "readme.txt", time.Unix(1700000001, 0), strings.NewReader(payload))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"res_code":404}`)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	fsi, err := NewFs(ctx, "test189pw", "", configmap.Simple{
		"user":          "13800000000",
		"pass":          "secret",
		"endpoint":      ts.URL,
		"auth_endpoint": ts.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fsi.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%d", len(entries))
	}
}

func TestRSAEncryptHex(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.StdEncoding.EncodeToString(der)
	got, err := rsaEncryptHex(pub, []byte("user"))
	if err != nil || got == "" {
		t.Fatalf("encrypt %q %v", got, err)
	}
}
