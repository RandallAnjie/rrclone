package accountrotation

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestParseFiles(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "accounts")
	require.NoError(t, os.Mkdir(sub, 0o700))
	writeFile(t, filepath.Join(sub, "b.json"), "{}")
	writeFile(t, filepath.Join(sub, "a.json"), "{}")
	writeFile(t, filepath.Join(sub, "notes.txt"), "x")
	single := filepath.Join(dir, "single.tok")
	writeFile(t, single, "{}")

	files, err := ParseFiles(single + ",\n" + sub + "\n" + filepath.Join(sub, "*.json"))
	require.NoError(t, err)
	assert.Equal(t, []string{single, filepath.Join(sub, "a.json"), filepath.Join(sub, "b.json")}, files)

	files, err = ParseFiles("  ")
	require.NoError(t, err)
	assert.Empty(t, files)

	_, err = ParseFiles(filepath.Join(dir, "missing.json"))
	assert.Error(t, err)
	_, err = ParseFiles(filepath.Join(dir, "*.nothing"))
	assert.Error(t, err)
}

func TestTokenFileMapperStructured(t *testing.T) {
	file := filepath.Join(t.TempDir(), "acct.json")
	writeFile(t, file, `{"client_id":"id1","client_secret":"sec1","token":{"access_token":"a","expiry":"0001-01-01T00:00:00Z"}}`)
	base := configmap.Simple{"client_id": "base-id", "token": "base-token", "other": "x"}
	m := &TokenFileMapper{Base: base, File: file}

	v, ok := m.Get("client_id")
	assert.True(t, ok)
	assert.Equal(t, "id1", v)
	v, _ = m.Get("client_secret")
	assert.Equal(t, "sec1", v)
	v, _ = m.Get("other")
	assert.Equal(t, "x", v)
	v, _ = m.Get("token")
	assert.Contains(t, v, `"expiry":"1970-01-01T00:00:00Z"`, "zero expiry forces a refresh")

	m.Set("token", `{"access_token":"b", "expiry":"2030-01-01T00:00:00Z"}`)
	clientID, clientSecret, tok, structured, err := ReadFile(file)
	require.NoError(t, err)
	assert.True(t, structured)
	assert.Equal(t, "id1", clientID)
	assert.Equal(t, "sec1", clientSecret)
	assert.Equal(t, `{"access_token":"b","expiry":"2030-01-01T00:00:00Z"}`, tok)
	assert.Equal(t, "base-token", base["token"], "base token untouched")

	m.Set("other", "y")
	assert.Equal(t, "y", base["other"])
}

func TestTokenFileMapperLegacy(t *testing.T) {
	file := filepath.Join(t.TempDir(), "acct.json")
	writeFile(t, file, `{"access_token":"a","expiry":"2030-01-01T00:00:00Z"}`+"\n")
	m := &TokenFileMapper{Base: configmap.Simple{"client_id": "base-id"}, File: file}

	v, _ := m.Get("client_id")
	assert.Equal(t, "base-id", v, "legacy files fall through for client_id")
	v, _ = m.Get("token")
	assert.Equal(t, `{"access_token":"a","expiry":"2030-01-01T00:00:00Z"}`, v)

	m.Set("token", `{"access_token":"b"}`)
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, `{"access_token":"b"}`, string(data))

	_, ok := (&TokenFileMapper{Base: configmap.Simple{}, File: file + ".missing"}).Get("token")
	assert.False(t, ok)
}

func TestPoolCandidates(t *testing.T) {
	p := NewPool([]string{"a", "b", "", "c", "a"})
	assert.Equal(t, 3, p.Len())
	now := time.Now()

	got, wait := p.candidates("b", now)
	assert.Equal(t, []string{"c", "a"}, got, "starts after current and wraps")
	assert.Zero(t, wait)

	p.coolDown("c", now.Add(time.Minute))
	got, wait = p.candidates("b", now)
	assert.Equal(t, []string{"a"}, got)
	assert.Equal(t, time.Minute, wait)
	assert.True(t, p.coolingDown("c", now))

	p.coolDown("c", time.Time{})
	assert.False(t, p.coolingDown("c", now))
}

// accountTransport tags requests with the account they were sent from
type accountTransport struct{ name string }

func (a accountTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("X-Account", a.name)
	return http.DefaultTransport.RoundTrip(req)
}

func clientFor(name string) *http.Client {
	return &http.Client{Transport: accountTransport{name}}
}

// rateLimited classifies 429 as rotate
func rateLimited(resp *http.Response, err error) Verdict {
	if err == nil && resp.StatusCode == http.StatusTooManyRequests {
		return Verdict{Rotate: true, Reason: "rate limited"}
	}
	return Verdict{}
}

// newTestServer rate limits every account in limited and echoes the
// account and request body otherwise
func newTestServer(t *testing.T, limited ...string) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		acct := r.Header.Get("X-Account")
		for _, l := range limited {
			if acct == l {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, "too_many_requests")
				return
			}
		}
		body, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, acct+":"+string(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestRotatorReplaysOnNextAccount(t *testing.T) {
	srv, hits := newTestServer(t, "a")
	var made []string
	r := NewRotator("test", NewPool([]string{"a", "b", "c"}), time.Minute, "a", clientFor("a"), rateLimited,
		func(ctx context.Context, file string) (*http.Client, error) {
			made = append(made, file)
			return clientFor(file), nil
		})
	client := &http.Client{Transport: r}

	resp, err := client.Post(srv.URL, "text/plain", strings.NewReader("hello"))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "b:hello", string(body), "body replayed on the new account")
	assert.Equal(t, []string{"b"}, made)
	assert.Equal(t, "b", r.Current())
	assert.True(t, r.Healthy())
	assert.Equal(t, int32(2), hits.Load())
}

func TestRotatorSkipsBrokenAccounts(t *testing.T) {
	srv, _ := newTestServer(t, "a")
	r := NewRotator("test", NewPool([]string{"a", "b", "c"}), time.Minute, "a", clientFor("a"), rateLimited,
		func(ctx context.Context, file string) (*http.Client, error) {
			if file == "b" {
				return nil, errors.New("revoked")
			}
			return clientFor(file), nil
		})
	resp, err := (&http.Client{Transport: r}).Get(srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "c", r.Current())
	assert.True(t, r.pool.coolingDown("b", time.Now()), "broken account cools down")
}

func TestRotatorAllLimited(t *testing.T) {
	srv, hits := newTestServer(t, "a", "b")
	r := NewRotator("test", NewPool([]string{"a", "b"}), time.Minute, "a", clientFor("a"), rateLimited,
		func(ctx context.Context, file string) (*http.Client, error) { return clientFor(file), nil })
	client := &http.Client{Transport: r}

	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "gives up after trying every account")
	assert.Equal(t, int32(2), hits.Load())
	assert.Equal(t, "b", r.Current())
	assert.False(t, r.Healthy())

	// Both are cooling down now so no further rotation happens
	resp, err = client.Get(srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, int32(3), hits.Load())
	assert.Equal(t, "b", r.Current())
}

func TestRotatorCooldownZeroDisables(t *testing.T) {
	srv, _ := newTestServer(t, "a")
	r := NewRotator("test", NewPool([]string{"a", "b"}), 0, "a", clientFor("a"), rateLimited,
		func(ctx context.Context, file string) (*http.Client, error) { return clientFor(file), nil })
	resp, err := (&http.Client{Transport: r}).Get(srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "a", r.Current())
}

func TestRotatorUnreplayableBody(t *testing.T) {
	srv, hits := newTestServer(t, "a")
	r := NewRotator("test", NewPool([]string{"a", "b"}), time.Minute, "a", clientFor("a"), rateLimited,
		func(ctx context.Context, file string) (*http.Client, error) { return clientFor(file), nil })
	// A reader http.NewRequest can't rewind
	req, err := http.NewRequest("POST", srv.URL, io.MultiReader(strings.NewReader("data")))
	require.NoError(t, err)
	require.Nil(t, req.GetBody)
	resp, err := (&http.Client{Transport: r}).Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "caller must retry")
	assert.Equal(t, int32(1), hits.Load())
	assert.Equal(t, "b", r.Current(), "but the next try uses the new account")
	assert.True(t, r.Healthy())
}

func TestRotatorConcurrentRotatesOnce(t *testing.T) {
	srv, _ := newTestServer(t, "a")
	var made atomic.Int32
	r := NewRotator("test", NewPool([]string{"a", "b", "c"}), time.Minute, "a", clientFor("a"), rateLimited,
		func(ctx context.Context, file string) (*http.Client, error) {
			made.Add(1)
			return clientFor(file), nil
		})
	client := &http.Client{Transport: r}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			resp, err := client.Get(srv.URL)
			if assert.NoError(t, err) {
				_ = resp.Body.Close()
				assert.Equal(t, http.StatusOK, resp.StatusCode)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), made.Load(), "failures from the same account rotate once")
	assert.Equal(t, "b", r.Current())
}

func TestRotatorServerCooldownWins(t *testing.T) {
	srv, _ := newTestServer(t, "a")
	classify := func(resp *http.Response, err error) Verdict {
		v := rateLimited(resp, err)
		v.Cooldown = time.Hour
		return v
	}
	r := NewRotator("test", NewPool([]string{"a", "b"}), time.Minute, "a", clientFor("a"), classify,
		func(ctx context.Context, file string) (*http.Client, error) { return clientFor(file), nil })
	resp, err := (&http.Client{Transport: r}).Get(srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.True(t, r.pool.coolingDown("a", time.Now().Add(30*time.Minute)))
}
