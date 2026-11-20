package drive

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// TestResumableUploadRetry checks that a chunk which fails with a 5xx is
// retried successfully. The chunk buffer is pool-backed and implements
// io.Closer, so if it reaches the http transport unwrapped the transport
// closes it after the failed attempt, returning its pages to the pool, and
// the retry then reads a freed buffer.
func TestResumableUploadRetry(t *testing.T) {
	content := bytes.Repeat([]byte("resumable"), 512)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		assert.Equal(t, content, body, "retried chunk should re-send the full chunk")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fake-id","name":"remote"}`))
	}))
	defer server.Close()

	f := &Fs{
		pacer:  fs.NewPacer(context.Background(), pacer.NewGoogleDrive()),
		client: server.Client(),
	}
	f.opt.ChunkSize = fs.SizeSuffix(len(content))
	rx := &resumableUpload{
		f:             f,
		remote:        "remote",
		URI:           server.URL,
		Media:         bytes.NewReader(content),
		MediaType:     "application/octet-stream",
		ContentLength: int64(len(content)),
	}
	info, err := rx.Upload(context.Background())
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "fake-id", info.Id)
	assert.Equal(t, int32(2), attempts.Load(), "expected exactly one failed attempt and one retry")
}

func TestDriveAPIEndpoint(t *testing.T) {
	for _, test := range []struct {
		in, ver, want string
	}{
		{"", "v3", ""},
		{"https://googleapis.example.com", "v3", "https://googleapis.example.com/drive/v3/"},
		{"https://googleapis.example.com/", "v3", "https://googleapis.example.com/drive/v3/"},
		{"https://googleapis.example.com/drive/v3/", "v3", "https://googleapis.example.com/drive/v3/"},
		{"https://googleapis.example.com/drive/v3", "v3", "https://googleapis.example.com/drive/v3/"},
		{"googleapis.example.com", "v3", "https://googleapis.example.com/drive/v3/"},
		{"https://proxy.example.com/google", "v3", "https://proxy.example.com/google/drive/v3/"},
		{"https://proxy.example.com/google/", "v3", "https://proxy.example.com/google/drive/v3/"},
		{"http://localhost:8080", "v3", "http://localhost:8080/drive/v3/"},
		{"https://googleapis.example.com", "v2", "https://googleapis.example.com/drive/v2/"},
		{"https://googleapis.example.com/drive/v2/", "v3", "https://googleapis.example.com/drive/v2/"},
	} {
		assert.Equal(t, test.want, driveAPIEndpoint(test.in, test.ver), test.in+" "+test.ver)
	}
}

func TestDriveAPIEndpointGoogleClient(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"Login Required"}}`))
	}))
	defer server.Close()

	ctx := context.Background()
	rawSvc, err := drive.NewService(ctx,
		option.WithoutAuthentication(),
		option.WithHTTPClient(server.Client()),
		option.WithEndpoint(server.URL),
	)
	require.NoError(t, err)
	_, _ = rawSvc.About.Get().Do()
	assert.Equal(t, "/about", gotPath, "origin-only WithEndpoint is used as BasePath verbatim")

	normSvc, err := drive.NewService(ctx,
		option.WithoutAuthentication(),
		option.WithHTTPClient(server.Client()),
		option.WithEndpoint(driveAPIEndpoint(server.URL, "v3")),
	)
	require.NoError(t, err)
	_, _ = normSvc.About.Get().Do()
	assert.Equal(t, "/drive/v3/about", gotPath)
}

func TestDriveUploadURL(t *testing.T) {
	assert.Equal(t, defaultResumableUploadURL, driveUploadURL(""))
	assert.Equal(t, defaultResumableUploadURL, driveUploadURL("https://www.googleapis.com/drive/v3/"))
	assert.Equal(t, "https://googleapis.example.com/upload/drive/v3/files", driveUploadURL("https://googleapis.example.com/drive/v3/"))
	assert.Equal(t, "https://proxy.example.com/google/upload/drive/v3/files", driveUploadURL("https://proxy.example.com/google/drive/v3/"))
	assert.Equal(t, "https://proxy.example.com/upload/drive/v3/files", driveUploadURL("https://proxy.example.com/"))
}

func TestRewriteGoogleAPILocation(t *testing.T) {
	official := "https://www.googleapis.com/upload/drive/v3/files?upload_id=abc"
	assert.Equal(t, official, rewriteGoogleAPILocation(official, "https://www.googleapis.com/drive/v3/"))
	assert.Equal(t, "https://googleapis.example.com/upload/drive/v3/files?upload_id=abc", rewriteGoogleAPILocation(official, "https://googleapis.example.com/drive/v3/"))
	assert.Equal(t, "https://proxy.example.com/google/upload/drive/v3/files?upload_id=abc", rewriteGoogleAPILocation(official, "https://proxy.example.com/google/drive/v3/"))
	assert.Equal(t, "https://already.example.com/upload/drive/v3/files?upload_id=abc", rewriteGoogleAPILocation("https://already.example.com/upload/drive/v3/files?upload_id=abc", "https://googleapis.example.com/drive/v3/"))
}
