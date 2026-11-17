package onedrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/lib/accountrotation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func resp(status int, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: header}
}

func TestClassifyGraphResponse(t *testing.T) {
	v := classifyGraphResponse(resp(429, http.Header{"Retry-After": {"120"}}), nil)
	assert.True(t, v.Rotate)
	assert.Equal(t, 120*time.Second, v.Cooldown)

	assert.True(t, classifyGraphResponse(resp(401, nil), nil).Rotate, "revoked token")
	assert.False(t, classifyGraphResponse(resp(401, http.Header{"Www-Authenticate": {`Bearer error="invalid_token", error_description="expired_token"`}}), nil).Rotate,
		"expired tokens are refreshed instead")
	assert.False(t, classifyGraphResponse(resp(503, nil), nil).Rotate, "busy servers aren't the account's fault")
	assert.False(t, classifyGraphResponse(resp(200, nil), nil).Rotate)

	rErr := &oauth2.RetrieveError{Response: &http.Response{StatusCode: 400}}
	assert.True(t, classifyGraphResponse(nil, fmt.Errorf("refresh: %w", rErr)).Rotate)
	assert.False(t, classifyGraphResponse(nil, errors.New("connection reset")).Rotate)
}

// fakeGraph serves one drive. Account tokA is always throttled, tokB
// works and tokC can't reach the drive.
type fakeGraph struct {
	mu    sync.Mutex
	hits  map[string]int
	srv   *httptest.Server
	drive string
}

func newFakeGraph(t *testing.T) *fakeGraph {
	g := &fakeGraph{hits: map[string]int{}, drive: "D1"}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		g.mu.Lock()
		g.hits[tok]++
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch tok {
		case "tokA":
			w.Header().Set("Retry-After", "300")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"activityLimitReached","message":"throttled"}}`))
			return
		case "tokC":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"accessDenied","message":"no"}}`))
			return
		case "tokB":
		default:
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		base := "/v1.0/drives/" + g.drive
		switch {
		case r.URL.Path == base:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": g.drive, "driveType": "personal"})
		case r.URL.Path == base+"/root" || r.URL.Path == base+"/root:" || r.URL.Path == base+"/items/root":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ROOT", "name": "root", "folder": map[string]any{"childCount": 0}, "parentReference": map[string]any{"driveId": g.drive}})
		case strings.HasSuffix(r.URL.Path, "/children"):
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{}})
		default:
			t.Logf("fake graph: unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"itemNotFound","message":"not found"}}`))
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func writeToken(t *testing.T, dir, name, access string) string {
	file := filepath.Join(dir, name)
	tok := fmt.Sprintf(`{"access_token":%q,"token_type":"Bearer","refresh_token":"r-%s","expiry":%q}`, access, access, time.Now().Add(time.Hour).Format(time.RFC3339))
	require.NoError(t, accountrotation.WriteFile(file, "", "", tok))
	return file
}

func TestRotationSwitchesAccount(t *testing.T) {
	ctx := context.Background()
	g := newFakeGraph(t)
	dir := t.TempDir()
	a := writeToken(t, dir, "1-a.json", "tokA")
	c := writeToken(t, dir, "2-c.json", "tokC")
	b := writeToken(t, dir, "3-b.json", "tokB")

	m := configmap.Simple{
		"drive_id":               g.drive,
		"drive_type":             "personal",
		"endpoint":               g.srv.URL,
		"oauth_account_files":    strings.Join([]string{a, c, b}, ","),
		"oauth_account_cooldown": "5m",
		"client_id":              "cid",
		"client_secret":          "csecret",
	}
	// With the option defaults, as in real use
	ri, err := fs.Find("onedrive")
	require.NoError(t, err)
	f, err := NewFs(ctx, "odtest", "", fs.ConfigMap(ri.Prefix, ri.Options, "odtest", m))
	require.NoError(t, err, "the throttled first account is skipped at once")
	od := f.(*Fs)
	require.NotNil(t, od.rotator)
	assert.Equal(t, b, od.rotator.Current(), "the account which can't reach the drive was skipped")

	_, err = f.List(ctx, "")
	require.NoError(t, err)

	st := od.rotator.Status()
	assert.Equal(t, int64(1), st.Switches)
	assert.Equal(t, "throttled", st.LastReason)
	for _, acct := range st.Accounts {
		switch acct.File {
		case a:
			assert.True(t, acct.CoolingDown)
			assert.InDelta(t, 300, acct.CooldownLeft, 5, "the longer of the cooldown and Retry-After")
		case c:
			assert.True(t, acct.CoolingDown, "unusable account rests too")
		case b:
			assert.True(t, acct.Active)
		}
	}
	g.mu.Lock()
	assert.Positive(t, g.hits["tokB"])
	g.mu.Unlock()

	_, err = os.Stat(a)
	require.NoError(t, err)
}
