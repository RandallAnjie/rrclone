package drive

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestSignedDownloadURL(t *testing.T) {
	got := signedDownloadURL("https://www.googleapis.com/drive/v3/", "FILEID", "tok", false, "")
	want := "https://www.googleapis.com/drive/v3/files/FILEID?access_token=tok&alt=media&supportsAllDrives=true"
	if got != want {
		t.Fatalf("default: got %q want %q", got, want)
	}

	got = signedDownloadURL("", "FILEID", "tok", false, "")
	if got != want {
		t.Fatalf("empty base: got %q want %q", got, want)
	}

	got = signedDownloadURL("https://drive-proxy.example.com/drive/v3/", "FILEID", "tok+/=", true, "rkey")
	u, err := url.Parse(got)
	require.NoError(t, err)
	if u.Host != "drive-proxy.example.com" {
		t.Fatalf("custom endpoint host: got %q", u.Host)
	}
	if u.Path != "/drive/v3/files/FILEID" {
		t.Fatalf("custom endpoint path: got %q", u.Path)
	}
	q := u.Query()
	if q.Get("alt") != "media" || q.Get("supportsAllDrives") != "true" {
		t.Fatalf("media query: got %q", u.RawQuery)
	}
	if q.Get("access_token") != "tok+/=" {
		t.Fatalf("access_token: got %q", q.Get("access_token"))
	}
	if q.Get("acknowledgeAbuse") != "true" {
		t.Fatalf("acknowledgeAbuse: got %q", q.Get("acknowledgeAbuse"))
	}
	if q.Get("resourceKey") != "rkey" {
		t.Fatalf("resourceKey: got %q", q.Get("resourceKey"))
	}
	if q.Has("export") {
		t.Fatalf("must not be a share/uc URL: %q", got)
	}
}

func TestSignedDownloadURLFetchWithoutAuthHeader(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("download must not require Authorization, got %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/drive/v3/files/FILEID" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("access_token") != "tok" || r.URL.Query().Get("alt") != "media" {
			http.Error(w, "bad query", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("file-bytes"))
	}))
	defer ts.Close()

	link := signedDownloadURL(ts.URL+"/drive/v3/", "FILEID", "tok", false, "")
	resp, err := http.Get(link)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "file-bytes", string(body))
}

func TestPublicLinkURL(t *testing.T) {
	const id = "FILEID"
	got := publicLinkURL(id, "video/mp4", false)
	want := "https://drive.google.com/uc?export=download&confirm=t&id=FILEID"
	if got != want {
		t.Fatalf("file: got %q want %q", got, want)
	}
	got = publicLinkURL(id, "application/vnd.google-apps.document", false)
	want = "https://drive.google.com/open?id=FILEID"
	if got != want {
		t.Fatalf("gdoc: got %q want %q", got, want)
	}
	got = publicLinkURL(id, "video/mp4", true)
	if got != want {
		t.Fatalf("folder: got %q want %q", got, want)
	}
}

func TestPublicLinkUnlinkUnsupported(t *testing.T) {
	f := &Fs{}
	_, err := f.PublicLink(context.Background(), "file.bin", fs.DurationOff, true)
	require.EqualError(t, err, "drive: signed download URLs expire with the OAuth token; unlink is not supported")
}

func TestOauthAccessToken(t *testing.T) {
	expiry := time.Now().Add(time.Hour)
	f := &Fs{client: oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "abc",
		Expiry:      expiry,
	}))}
	tok, err := f.oauthAccessToken()
	require.NoError(t, err)
	require.Equal(t, "abc", tok.AccessToken)

	f = &Fs{client: &http.Client{}}
	_, err = f.oauthAccessToken()
	require.Error(t, err)

	f = &Fs{client: oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{}))}
	_, err = f.oauthAccessToken()
	require.Error(t, err)
}
