package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

func TestDirectRoute(t *testing.T) {
	r := &directRouter{
		relay:       &url.URL{Scheme: "https", Host: "relay.example.com"},
		relayPrefix: "/google",
	}
	for _, test := range []struct {
		in        string
		official  string
		relayable bool
		ok        bool
	}{
		{"https://relay.example.com/google/drive/v3/files?q=x", "https://www.googleapis.com/drive/v3/files?q=x", true, true},
		{"https://relay.example.com/google/upload/drive/v3/files?upload_id=1", "https://www.googleapis.com/upload/drive/v3/files?upload_id=1", true, true},
		{"https://relay.example.com/token", "", false, false},
		{"https://relay.example.com/drive/v3/files", "", false, false},
		{"https://www.googleapis.com/drive/v3/files", "https://www.googleapis.com/drive/v3/files", true, true},
		{"https://doc-0k-4c-docs.googleusercontent.com/x", "https://doc-0k-4c-docs.googleusercontent.com/x", false, true},
		{"https://oauth2.googleapis.com/token", "", false, false},
	} {
		u, err := url.Parse(test.in)
		require.NoError(t, err)
		rt, ok := r.route(u)
		assert.Equal(t, test.ok, ok, test.in)
		if !ok {
			continue
		}
		assert.Equal(t, test.official, rt.official.String(), test.in)
		assert.Equal(t, test.relayable, rt.relayable, test.in)
		if rt.relayable {
			assert.Equal(t, strings.Replace(test.official, "https://www.googleapis.com", "https://relay.example.com/google", 1), r.relayURL(rt.official).String())
		}
	}

	// Without a relay, official Drive URLs can't be relayed.
	r = &directRouter{}
	u, _ := url.Parse("https://www.googleapis.com/drive/v3/files")
	rt, ok := r.route(u)
	assert.True(t, ok)
	assert.False(t, rt.relayable)
}

func TestDirectOrder(t *testing.T) {
	now := time.Now()
	r := &directRouter{
		bad: map[string]*directBackoff{},
		cache: directCache{
			Endpoints: []directEndpoint{
				{IP: "a", LatencyMs: 10},
				{IP: "b", LatencyMs: 20},
				{IP: "c", LatencyMs: 30},
			},
			Normal: directEndpoint{IP: directNormal, LatencyMs: 100},
		},
	}
	ips := []string{"a", "b", "c"}

	// No speed test: latency order, normal last.
	assert.Equal(t, []string{"a", "b", "c", directNormal}, r.order(ips, directDown, true, now))

	// Directions are ranked separately; unmeasured and failed paths go after measured ones.
	r.cache.Speeds = map[string]directSpeed{
		"a":          {DownBps: 12 << 20, UpBps: 100 << 10},
		"b":          {DownError: "x", UpError: "x"},
		directNormal: {DownBps: 3 << 20, UpBps: 800 << 10},
	}
	assert.Equal(t, []string{"a", directNormal, "c", "b"}, r.order(ips, directDown, true, now))
	assert.Equal(t, []string{directNormal, "a", "c", "b"}, r.order(ips, directUp, true, now))

	// A request the relay can't serve keeps the normal path last.
	assert.Equal(t, []string{"a", "c", "b", directNormal}, r.order(ips, directUp, false, now))

	// Backing off paths are skipped, but the normal path stays as a last resort.
	r.bad["a"] = &directBackoff{until: now.Add(time.Minute)}
	r.bad[directNormal] = &directBackoff{until: now.Add(time.Minute)}
	assert.Equal(t, []string{"c", "b", directNormal}, r.order(ips, directDown, true, now))

	// Everything backing off: try them all anyway.
	r.bad["b"] = &directBackoff{until: now.Add(time.Minute)}
	r.bad["c"] = &directBackoff{until: now.Add(time.Minute)}
	assert.Equal(t, []string{"a", directNormal, "c", "b"}, r.order(ips, directDown, true, now))

	// Once the backoff ends the fast path is used again.
	assert.Equal(t, []string{"a", directNormal, "c", "b"}, r.order(ips, directDown, true, now.Add(2*time.Minute)))
}

func TestDirectBackoff(t *testing.T) {
	r := &directRouter{bad: map[string]*directBackoff{}}
	r.markBad("a", errors.New("x"))
	first := r.bad["a"].until
	// More failures from requests already in flight don't extend it.
	r.markBad("a", errors.New("x"))
	assert.Equal(t, first, r.bad["a"].until)
	assert.Equal(t, 1, r.bad["a"].fails)

	// After the backoff, a failure doubles it; a success clears it.
	r.bad["a"].until = time.Now().Add(-time.Second)
	r.markBad("a", errors.New("x"))
	assert.InDelta(t, float64(2*directBackoffMin), float64(time.Until(r.bad["a"].until)), float64(time.Second))
	r.markOK("a", directDown) // still backing off: kept
	assert.NotNil(t, r.bad["a"])
	r.bad["a"].until = time.Now().Add(-time.Second)
	r.markOK("a", directDown)
	assert.Nil(t, r.bad["a"])
}

type blockingReader struct {
	ctx context.Context
}

func (b blockingReader) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b blockingReader) Close() error { return nil }

func TestDirectBodyStall(t *testing.T) {
	// A slow reader isn't a stall: only time inside Read counts.
	ctx, cancel := context.WithCancel(context.Background())
	b := newDirectBody(io.NopCloser(strings.NewReader("hello")), 20*time.Millisecond, cancel, func() { t.Error("not a stall") })
	time.Sleep(50 * time.Millisecond)
	data, err := io.ReadAll(b)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
	require.NoError(t, b.Close())
	assert.Error(t, ctx.Err(), "close cancels the request")

	// A read that waits too long is aborted.
	ctx, cancel = context.WithCancel(context.Background())
	stalled := false
	b = newDirectBody(blockingReader{ctx}, 20*time.Millisecond, cancel, func() { stalled = true })
	_, err = b.Read(make([]byte, 10))
	assert.ErrorIs(t, err, errDirectStalled)
	assert.True(t, stalled)
}

// fakeDrive serves the bits of the Drive API the speed test uses.
type fakeDrive struct {
	host      string        // the official host, for Location headers
	delayDown time.Duration // before sending a download
	delayUp   time.Duration // before answering an upload

	mu    sync.Mutex
	hits  []string
	stall chan struct{}
}

func (d *fakeDrive) handle(prefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, prefix)
		d.mu.Lock()
		d.hits = append(d.hits, r.Method+" "+path)
		d.mu.Unlock()
		switch {
		case path == "/drive/v3/about":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":401}}`)
		case path == "/drive/v3/files" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"files":[{"id":"big","size":"2097152"}]}`)
		case path == "/drive/v3/files/stall":
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-d.stall:
			}
		case path == "/drive/v3/files/big":
			n := int64(2 << 20)
			if rng := r.Header.Get("Range"); rng != "" {
				end, _ := strconv.ParseInt(strings.TrimPrefix(rng, "bytes=0-"), 10, 64)
				n = end + 1
			}
			w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(make([]byte, n/2))
			w.(http.Flusher).Flush()
			time.Sleep(d.delayDown)
			_, _ = w.Write(make([]byte, n-n/2))
		case path == "/upload/drive/v3/files" && r.Method == http.MethodPost:
			w.Header().Set("Location", "https://"+d.host+"/upload/drive/v3/files?uploadType=resumable&upload_id=x")
		case path == "/upload/drive/v3/files" && r.Method == http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			time.Sleep(d.delayUp)
			w.WriteHeader(statusResumeIncomplete)
		case path == "/upload/drive/v3/files" && r.Method == http.MethodDelete:
			w.WriteHeader(499)
		default:
			_, _ = fmt.Fprintf(w, "%s %s", prefix, path)
		}
	}
}

func (d *fakeDrive) count(hit string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, h := range d.hits {
		if h == hit {
			n++
		}
	}
	return n
}

// newDirectRelayTest starts a fake Google front end on 127.0.0.1 and a
// fake relay, and returns options using both.
func newDirectRelayTest(t *testing.T) (context.Context, *Options, *fakeDrive, *fakeDrive) {
	ctx, s := newDirectTestServer(t)
	fs.GetConfig(ctx).HTTPProxy = ""
	oldAPI := directAPIURL
	directAPIURL = &url.URL{Scheme: "https", Host: "www.googleapis.com:" + s.port}
	t.Cleanup(func() { directAPIURL = oldAPI })

	google := &fakeDrive{host: directAPIURL.Host, stall: make(chan struct{})}
	s.Config.Handler = google.handle("")
	relay := &fakeDrive{host: directAPIURL.Host, stall: make(chan struct{})}
	rs := httptest.NewServer(relay.handle("/google"))
	t.Cleanup(func() { closeTestServer(rs) })
	t.Cleanup(func() { close(google.stall); close(relay.stall) })

	opt := &Options{
		Direct:             true,
		DirectIPs:          fs.CommaSepList{"127.0.0.1", "127.0.0.2"},
		Endpoint:           rs.URL + "/google",
		DirectSpeedTest:    1 << 20,
		DirectStallTimeout: fs.Duration(200 * time.Millisecond),
	}
	return ctx, opt, google, relay
}

func doDirect(t *testing.T, client *http.Client, ctx context.Context, method, u string) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

func TestDirectFailover(t *testing.T) {
	ctx, opt, google, relay := newDirectRelayTest(t)
	r := getDirectRouter(opt)
	_, err := r.scan(ctx)
	require.NoError(t, err)
	r.mu.Lock()
	require.True(t, r.cache.Normal.ok(), "the relay passes the probe: %s", r.cache.Normal.Error)
	// 127.0.0.2 refuses connections; pretend it scanned well.
	r.cache.Endpoints = []directEndpoint{{IP: "127.0.0.2", LatencyMs: 1}, {IP: "127.0.0.1", LatencyMs: 2}}
	r.cache.Speeds = map[string]directSpeed{
		"127.0.0.2":  {DownBps: 30, UpBps: 1},
		"127.0.0.1":  {DownBps: 20, UpBps: 2},
		directNormal: {DownBps: 10, UpBps: 3},
	}
	r.mu.Unlock()
	client := getClient(ctx, opt)
	relayAPI := strings.TrimSuffix(driveAPIEndpoint(opt.Endpoint, "v3"), "/drive/v3/")

	// A download fails over from the refusing IP to the next one.
	body, err := doDirect(t, client, ctx, http.MethodGet, relayAPI+"/drive/v3/files/x")
	require.NoError(t, err)
	assert.Equal(t, " /drive/v3/files/x", body, "served by Google directly")
	r.mu.Lock()
	assert.True(t, r.cooling("127.0.0.2", time.Now()))
	r.mu.Unlock()

	// Uploads prefer the relay.
	body, err = doDirect(t, client, ctx, http.MethodPost, relayAPI+"/upload/drive/v3/x?uploadType=multipart")
	require.NoError(t, err)
	assert.Equal(t, "/google /upload/drive/v3/x", body)

	// A download that stalls marks the path bad...
	_, err = doDirect(t, client, ctx, http.MethodGet, relayAPI+"/drive/v3/files/stall?alt=media")
	assert.ErrorIs(t, err, errDirectStalled)
	assert.Equal(t, 1, google.count("GET /drive/v3/files/stall"))

	// ...so the retry goes through the relay.
	body, err = doDirect(t, client, ctx, http.MethodGet, relayAPI+"/drive/v3/files/y")
	require.NoError(t, err)
	assert.Equal(t, "/google /drive/v3/files/y", body)

	// Token refresh on the relay host isn't touched.
	body, err = doDirect(t, client, ctx, http.MethodPost, strings.TrimSuffix(relayAPI, "/google")+"/token")
	require.NoError(t, err)
	assert.Equal(t, "/google /token", body)
	assert.Equal(t, 1, relay.count("POST /token"))
}

func TestDirectFailoverNotRetryingWrites(t *testing.T) {
	ctx, opt, _, relay := newDirectRelayTest(t)
	r := getDirectRouter(opt)
	r.mu.Lock()
	r.cache = directCache{
		ScannedAt: time.Now(),
		Endpoints: []directEndpoint{{IP: "127.0.0.2", LatencyMs: 1}},
		Normal:    directEndpoint{IP: directNormal, LatencyMs: 50},
	}
	r.mu.Unlock()
	client := getClient(ctx, opt)
	relayAPI := strings.TrimSuffix(driveAPIEndpoint(opt.Endpoint, "v3"), "/drive/v3/")

	// The write fails rather than being sent twice...
	_, err := doDirect(t, client, ctx, http.MethodPost, relayAPI+"/drive/v3/files")
	require.Error(t, err)
	assert.Equal(t, 0, relay.count("POST /drive/v3/files"))

	// ...and the caller's retry takes the next path.
	body, err := doDirect(t, client, ctx, http.MethodPost, relayAPI+"/drive/v3/files")
	require.NoError(t, err)
	assert.Equal(t, "/google /drive/v3/files", body)
}

func TestDirectSpeedTest(t *testing.T) {
	ctx, opt, google, relay := newDirectRelayTest(t)
	opt.DirectIPs = fs.CommaSepList{"127.0.0.1"}
	// Google is fast down and slow up; the relay the other way round.
	google.delayUp = 150 * time.Millisecond
	relay.delayDown = 150 * time.Millisecond

	f := &Fs{opt: *opt, rootFolderID: "root"}
	f.client = getClient(ctx, opt)
	var err error
	f.svc, err = drive.NewService(ctx, option.WithHTTPClient(f.client), option.WithEndpoint(driveAPIEndpoint(opt.Endpoint, "v3")))
	require.NoError(t, err)

	r := getDirectRouter(opt)
	require.NoError(t, r.runSpeedTest(ctx, f))

	r.mu.Lock()
	speeds := r.cache.Speeds
	r.mu.Unlock()
	require.Len(t, speeds, 2)
	for p, s := range speeds {
		assert.Empty(t, s.DownError, p)
		assert.Empty(t, s.UpError, p)
		assert.Positive(t, s.DownBps, p)
		assert.Positive(t, s.UpBps, p)
	}
	// Each path did its own transfer, and the upload sessions were cancelled.
	for _, d := range []*fakeDrive{google, relay} {
		assert.Equal(t, 1, d.count("GET /drive/v3/files/big"))
		assert.Equal(t, 1, d.count("PUT /upload/drive/v3/files"))
		assert.Equal(t, 1, d.count("DELETE /upload/drive/v3/files"))
	}

	status, err := f.directCommand(ctx, map[string]string{})
	require.NoError(t, err)
	st := status.(directStatus)
	require.Len(t, st.Downloads, 2)
	assert.Equal(t, "127.0.0.1", st.Downloads[0].Path)
	assert.Equal(t, directNormal, st.Uploads[0].Path)

	// The results are cached for the next run.
	directRoutersMu.Lock()
	clear(directRouters)
	directRoutersMu.Unlock()
	r2 := getDirectRouter(opt)
	assert.Len(t, r2.cache.Speeds, 2)
}
