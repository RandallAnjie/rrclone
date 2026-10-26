// Direct connection mode: pin Drive traffic to scanned Google front-end IPs.
//
// The idea comes from gdirect (https://github.com/mizorewww/gdirect): when
// DNS for Google is poisoned or the default route is filtered, some Google
// front-end IPs are often still reachable. We probe candidates with a real
// Drive API request, keep the ones that answer, and dial those IPs for the
// Drive hosts while TLS is still verified against the real hostname.
// Token refresh (oauth2.googleapis.com) is left on the normal network.
//
// Each Drive request can take one of several paths: one of the fastest
// scanned IPs, or the "normal" path, which is the endpoint option (a relay)
// when set and the normal network otherwise. Paths are ranked separately
// for downloads and uploads by a periodic speed test, a path that fails or
// stalls is avoided for a while, and traffic goes back to it once that
// backoff ends.
//
// This is an independent Go implementation; no gdirect code is used.

package drive

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/fshttp"
	drive "google.golang.org/api/drive/v3"
)

const (
	directSNIOff        = "off"
	directNormal        = "normal" // path through the endpoint option or the normal network
	directProbeWorkers  = 8
	directProbeTimeout  = 5 * time.Second
	directScanTimeout   = 30 * time.Second
	directCooldown      = 5 * time.Minute // minimum time between scans or speed tests
	directBackoffMin    = time.Minute
	directBackoffMax    = 30 * time.Minute
	directMaxCandidates = 256
	directMaxIPPaths    = 3 // fastest scanned IPs used as paths
	directSpeedTimeout  = 20 * time.Second
	directSpeedMinBytes = 256 << 10
	directUploadChunk   = 256 << 10 // resumable upload chunks must be a multiple of this
)

// directSeedIPs are bootstrap candidates for networks where DNS for Google
// is poisoned. They are always probed and never assumed to work.
var directSeedIPs = []string{"183.56.143.147"}

// directResolveHosts are resolved with the system resolver to find more
// candidates. DNS answers are only candidates; each must pass the probe.
var directResolveHosts = []string{
	"www.googleapis.com",
	"drive.usercontent.google.com",
	"drive.google.com",
}

// directProbeURL must answer 401 with a Google JSON error when called
// without a token. Replaced in tests.
var directProbeURL = "https://www.googleapis.com/drive/v3/about?fields=user"

// directAPIURL is the origin of the Drive API. Replaced in tests.
var directAPIURL = &url.URL{Scheme: "https", Host: "www.googleapis.com"}

// directAllowIP filters candidates. Replaced in tests.
var directAllowIP = isPublicIP

// directRoutedHost reports whether host is served by Google front ends
// and carries Drive traffic, so it may be dialled on a scanned IP.
func directRoutedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	switch host {
	case "www.googleapis.com", "content.googleapis.com", "drive.googleapis.com",
		"drive.google.com", "drive.usercontent.google.com":
		return true
	}
	return strings.HasSuffix(host, ".googleusercontent.com")
}

// fakeIPRange is the benchmark range Clash/Mihomo and others use for fake-ip DNS.
var fakeIPRange = netip.MustParsePrefix("198.18.0.0/15")

func isPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return false
	}
	if ip.Is4() {
		if fakeIPRange.Contains(ip) || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
			return false
		}
		b := ip.As4()
		// 192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24, 100.64.0.0/10
		switch {
		case b[0] == 192 && b[1] == 0 && b[2] == 2,
			b[0] == 198 && b[1] == 51 && b[2] == 100,
			b[0] == 203 && b[1] == 0 && b[2] == 113,
			b[0] == 100 && b[1]&0xc0 == 64:
			return false
		}
	}
	return true
}

// directEndpoint is one probed IP, or the normal path.
type directEndpoint struct {
	IP        string    `json:"ip"`
	LatencyMs int64     `json:"latency_ms,omitempty"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

func (e directEndpoint) ok() bool { return e.Error == "" }

// directDir is the direction a request moves data in.
type directDir int

const (
	directDown directDir = iota // downloads and API calls
	directUp                    // uploads
)

func (d directDir) String() string {
	if d == directUp {
		return "uploads"
	}
	return "downloads"
}

// directSpeed is the speed test result for one path. A speed of 0 with no
// error means it wasn't measured.
type directSpeed struct {
	DownBps   int64     `json:"down_bps,omitempty"`
	DownError string    `json:"down_error,omitempty"`
	UpBps     int64     `json:"up_bps,omitempty"`
	UpError   string    `json:"up_error,omitempty"`
	TestedAt  time.Time `json:"tested_at"`
}

func (s directSpeed) get(dir directDir) (bps int64, errStr string) {
	if dir == directUp {
		return s.UpBps, s.UpError
	}
	return s.DownBps, s.DownError
}

type directCache struct {
	ScannedAt     time.Time              `json:"scanned_at"`
	Endpoints     []directEndpoint       `json:"endpoints"`
	Normal        directEndpoint         `json:"normal"`
	SpeedTestedAt time.Time              `json:"speed_tested_at,omitzero"`
	Speeds        map[string]directSpeed `json:"speeds,omitempty"`
}

// directBackoff tracks a path that failed.
type directBackoff struct {
	until time.Time
	fails int
}

// directRouter holds the scan and speed test results shared by all drive
// remotes with the same candidates, SNI and endpoint.
type directRouter struct {
	candidates  []string // user supplied candidates, empty for the defaults
	sni         string
	rescan      time.Duration
	speedSize   int64
	speedEvery  time.Duration
	stall       time.Duration
	relay       *url.URL // endpoint option origin, nil when unset
	relayPrefix string   // path before /drive/v3 on the endpoint
	cachePath   string

	mu         sync.Mutex
	cache      directCache
	bad        map[string]*directBackoff // path -> backoff
	scanning   chan struct{}             // closed when the running scan ends
	speedDone  chan struct{}             // closed when the running speed test ends
	speedTried time.Time
	speedFs    *Fs       // remote used to run speed tests
	current    [2]string // path last used per direction, for logging
}

var (
	directRoutersMu sync.Mutex
	directRouters   = map[string]*directRouter{}
)

// getDirectRouter returns the shared router for these options.
func getDirectRouter(opt *Options) *directRouter {
	candidates := slices.Clone([]string(opt.DirectIPs))
	slices.Sort(candidates)
	endpoint := driveAPIEndpoint(opt.Endpoint, "v3")
	key := strings.Join(candidates, ",") + "|" + opt.DirectSNI + "|" + endpoint
	directRoutersMu.Lock()
	defer directRoutersMu.Unlock()
	if r, ok := directRouters[key]; ok {
		return r
	}
	sum := sha256.Sum256([]byte(key))
	r := &directRouter{
		candidates: candidates,
		sni:        opt.DirectSNI,
		rescan:     time.Duration(opt.DirectRescan),
		speedSize:  int64(opt.DirectSpeedTest),
		speedEvery: time.Duration(opt.DirectSpeedTestInterval),
		stall:      time.Duration(opt.DirectStallTimeout),
		cachePath:  filepath.Join(config.GetCacheDir(), "drive-direct", hex.EncodeToString(sum[:6])+".json"),
		bad:        map[string]*directBackoff{},
	}
	if r.rescan <= 0 {
		r.rescan = 24 * time.Hour
	}
	if r.speedEvery <= 0 {
		r.speedEvery = time.Hour
	}
	if endpoint != "" {
		if u, err := url.Parse(endpoint); err == nil && u.Host != "" && !isOfficialGoogleAPIHost(strings.ToLower(u.Hostname())) {
			r.relay = &url.URL{Scheme: u.Scheme, Host: u.Host}
			r.relayPrefix = driveAPIPathPrefix(u.Path)
		}
	}
	r.load()
	directRouters[key] = r
	return r
}

func (r *directRouter) load() {
	data, err := os.ReadFile(r.cachePath)
	if err != nil {
		return
	}
	var c directCache
	if err := json.Unmarshal(data, &c); err != nil {
		fs.Debugf(nil, "drive direct: ignoring unreadable cache %q: %v", r.cachePath, err)
		return
	}
	r.cache = c
}

// save writes the cache. Call with r.mu held.
func (r *directRouter) save() {
	data, err := json.MarshalIndent(r.cache, "", "  ")
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(r.cachePath), 0o700); err == nil {
			err = os.WriteFile(r.cachePath, data, 0o600)
		}
	}
	if err != nil {
		fs.Debugf(nil, "drive direct: failed to save cache %q: %v", r.cachePath, err)
	}
}

// pathName describes a path for logs.
func (r *directRouter) pathName(p string) string {
	if p != directNormal {
		return "Google IP " + p
	}
	if r.relay != nil {
		return "endpoint " + r.relay.Host
	}
	return "normal network"
}

// okIPs returns the fastest working IPs. Call with r.mu held.
func (r *directRouter) okIPs() []string {
	var ips []string
	for _, e := range r.cache.Endpoints {
		if e.ok() && len(ips) < directMaxIPPaths {
			ips = append(ips, e.IP)
		}
	}
	return ips
}

// cooling reports whether path p is backing off. Call with r.mu held.
func (r *directRouter) cooling(p string, now time.Time) bool {
	b := r.bad[p]
	return b != nil && now.Before(b.until)
}

// ips returns the IP paths, scanning first when the cache is stale, empty,
// or every IP is backing off. With wait false a needed scan runs in the
// background and the current IPs are returned.
func (r *directRouter) ips(ctx context.Context, wait bool) []string {
	r.mu.Lock()
	now := time.Now()
	ips := r.okIPs()
	allCooling := len(ips) > 0
	for _, ip := range ips {
		if !r.cooling(ip, now) {
			allCooling = false
		}
	}
	age := now.Sub(r.cache.ScannedAt)
	// A scan within the cooldown is trusted even if it found nothing, so a
	// network with no reachable IP doesn't rescan on every request.
	fresh := age < directCooldown ||
		(age < r.rescan && len(ips) > 0 && !allCooling)
	if fresh || (r.scanning != nil && !wait) {
		r.mu.Unlock()
		return ips
	}
	if r.scanning == nil {
		done := make(chan struct{})
		r.scanning = done
		r.mu.Unlock()
		run := func() {
			// Detached from ctx so one cancelled request doesn't abort the
			// scan that other transfers are waiting for.
			scanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), directScanTimeout)
			_, err := r.scan(scanCtx)
			cancel()
			if err != nil {
				fs.Errorf(nil, "drive direct: %v", err)
			}
			r.mu.Lock()
			r.scanning = nil
			close(done)
			r.mu.Unlock()
		}
		if !wait {
			go run()
			return ips
		}
		run()
	} else {
		done := r.scanning
		r.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.okIPs()
}

// latency of path p in ms; unknown counts as the probe timeout. Call with
// r.mu held.
func (r *directRouter) latency(p string) int64 {
	unknown := directProbeTimeout.Milliseconds()
	if p == directNormal {
		if n := r.cache.Normal; n.ok() && n.LatencyMs > 0 {
			return n.LatencyMs
		}
		return unknown
	}
	for _, e := range r.cache.Endpoints {
		if e.IP == p && e.ok() {
			return e.LatencyMs
		}
	}
	return unknown
}

// rank orders paths for dir: measured speed first, then unmeasured by
// latency, then paths whose speed test failed. Lower is better. Call with
// r.mu held.
func (r *directRouter) rank(p string, dir directDir) (tier int, v int64) {
	if s, ok := r.cache.Speeds[p]; ok {
		bps, errStr := s.get(dir)
		if bps > 0 {
			return 0, -bps
		}
		if errStr != "" {
			return 2, r.latency(p)
		}
	}
	return 1, r.latency(p)
}

// order returns the paths to try, best first. The normal path is ranked
// with the rest when rankNormal is set and is otherwise kept as a last
// resort; backing off paths are only used when nothing else is left. Call
// with r.mu held.
func (r *directRouter) order(ips []string, dir directDir, rankNormal bool, now time.Time) []string {
	all := slices.Clone(ips)
	if rankNormal {
		all = append(all, directNormal)
	}
	slices.SortStableFunc(all, func(a, b string) int {
		ta, va := r.rank(a, dir)
		tb, vb := r.rank(b, dir)
		if ta != tb {
			return ta - tb
		}
		switch {
		case va < vb:
			return -1
		case va > vb:
			return 1
		}
		return 0
	})
	if !rankNormal {
		all = append(all, directNormal)
	}
	var active, cooling []string
	for _, p := range all {
		if r.cooling(p, now) {
			cooling = append(cooling, p)
		} else {
			active = append(active, p)
		}
	}
	if len(active) == 0 {
		return cooling
	}
	if slices.Contains(cooling, directNormal) {
		active = append(active, directNormal)
	}
	return active
}

// paths returns the paths to try for a request, best first.
func (r *directRouter) paths(ctx context.Context, dir directDir, rt directRoute) []string {
	// With a relay to fall back on, don't hold requests up for a scan.
	ips := r.ips(ctx, !rt.relayable)
	r.mu.Lock()
	defer r.mu.Unlock()
	// The normal path's speed is the relay's when there is one, which
	// says nothing about requests the relay can't serve.
	return r.order(ips, dir, rt.relayable || r.relay == nil, time.Now())
}

func (r *directRouter) markBad(p string, err error) {
	r.mu.Lock()
	now := time.Now()
	b := r.bad[p]
	if b == nil {
		b = &directBackoff{}
		r.bad[p] = b
	}
	if now.Before(b.until) {
		// Other requests in flight on this path failed too.
		r.mu.Unlock()
		return
	}
	d := min(directBackoffMin<<min(b.fails, 5), directBackoffMax)
	b.fails++
	b.until = now.Add(d)
	name := r.pathName(p)
	r.mu.Unlock()
	fs.Infof(nil, "drive direct: %s failed, avoiding it for %v: %v", name, d, err)
}

// markOK forgets the failures of p once its backoff is over.
func (r *directRouter) markOK(p string, dir directDir) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b := r.bad[p]; b != nil && !time.Now().Before(b.until) {
		delete(r.bad, p)
	}
	if r.current[dir] != p {
		old := r.current[dir]
		r.current[dir] = p
		if old != "" {
			fs.Infof(nil, "drive direct: %s now via %s (was %s)", dir, r.pathName(p), r.pathName(old))
		} else {
			fs.Debugf(nil, "drive direct: %s via %s", dir, r.pathName(p))
		}
	}
}

// relayURL maps an official Drive API URL onto the endpoint option.
func (r *directRouter) relayURL(o *url.URL) *url.URL {
	u := *o
	u.Scheme, u.Host = r.relay.Scheme, r.relay.Host
	u.Path = r.relayPrefix + o.Path
	if o.RawPath != "" {
		u.RawPath = r.relayPrefix + o.RawPath
	}
	return &u
}

// directRoute is where a Drive request may go.
type directRoute struct {
	official  *url.URL // the URL on the Google host
	relayable bool     // the endpoint option can serve it
}

// route works out whether u is Drive traffic. Requests already aimed at
// the endpoint option are mapped back to the Google URL so they can take
// any path.
func (r *directRouter) route(u *url.URL) (directRoute, bool) {
	if r.relay != nil && u.Scheme == r.relay.Scheme && strings.EqualFold(u.Host, r.relay.Host) {
		rest, found := strings.CutPrefix(u.Path, r.relayPrefix)
		if !found || !(strings.HasPrefix(rest, "/drive/") || strings.HasPrefix(rest, "/upload/drive/")) {
			return directRoute{}, false // e.g. token_url on the same host
		}
		o := *u
		o.Scheme, o.Host = directAPIURL.Scheme, directAPIURL.Host
		o.Path = rest
		o.RawPath = strings.TrimPrefix(u.RawPath, r.relayPrefix)
		return directRoute{official: &o, relayable: true}, true
	}
	if !directRoutedHost(u.Hostname()) {
		return directRoute{}, false
	}
	o := *u
	return directRoute{
		official:  &o,
		relayable: r.relay != nil && isOfficialGoogleAPIHost(strings.ToLower(u.Hostname())),
	}, true
}

// directPathFailed reports statuses that mean the path, not Drive, failed:
// gateway errors from a relay or a front end.
func directPathFailed(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusGatewayTimeout ||
		(status >= 520 && status <= 530) // Cloudflare
}

// directPathKey in a request context forces a path, for speed tests.
type directPathKey struct{}

// directTransport sends Drive requests down the best path and fails over
// to the next one.
type directTransport struct {
	r      *directRouter
	normal http.RoundTripper
	newIP  func(ip string) http.RoundTripper

	mu   sync.Mutex
	byIP map[string]http.RoundTripper // one pool per IP so connections aren't mixed
}

func newDirectTransport(ctx context.Context, opt *Options, normal http.RoundTripper, customize func(*http.Transport)) *directTransport {
	return &directTransport{
		r:      getDirectRouter(opt),
		normal: normal,
		newIP: func(ip string) http.RoundTripper {
			return fshttp.NewTransportCustom(ctx, func(t *http.Transport) {
				customize(t)
				pinDirectIP(t, ip, opt.DirectSNI)
			})
		},
		byIP: map[string]http.RoundTripper{},
	}
}

func (d *directTransport) ipTransport(ip string) http.RoundTripper {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.byIP[ip]
	if t == nil {
		t = d.newIP(ip)
		d.byIP[ip] = t
	}
	return t
}

// RoundTrip implements http.RoundTripper.
func (d *directTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt, ok := d.r.route(req.URL)
	if !ok {
		return d.normal.RoundTrip(req)
	}
	read := req.Method == http.MethodGet || req.Method == http.MethodHead
	dir := directDown
	if !read && strings.HasPrefix(rt.official.Path, "/upload/") {
		dir = directUp
	}
	ctx := req.Context()
	forced, _ := ctx.Value(directPathKey{}).(string)
	var paths []string
	if forced != "" {
		paths = []string{forced}
	} else {
		if dir == directUp || rt.official.Query().Get("alt") == "media" || !rt.relayable {
			// Only transfers start a speed test, not a quick listing.
			d.r.maybeSpeedTest(ctx)
		}
		paths = d.r.paths(ctx, dir, rt)
	}
	// Only requests that can't have changed anything are retried here;
	// the rest fail and the caller's retry picks the next path.
	retry := forced == "" && read && (req.Body == nil || req.Body == http.NoBody)
	var lastErr error
	for i, p := range paths {
		last := !retry || i == len(paths)-1
		resp, err := d.try(req, rt, p, forced == "")
		if err == nil && !directPathFailed(resp.StatusCode) {
			if forced == "" {
				d.r.markOK(p, dir)
			}
			return resp, nil
		}
		if ctx.Err() != nil {
			return resp, err
		}
		if err == nil {
			err = fmt.Errorf("HTTP %s", resp.Status)
			if forced == "" {
				d.r.markBad(p, err)
			}
			if last {
				return resp, nil
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
		} else if forced == "" {
			d.r.markBad(p, err)
		}
		lastErr = err
		if last {
			break
		}
	}
	return nil, lastErr
}

// try sends req down path p.
func (d *directTransport) try(req *http.Request, rt directRoute, p string, track bool) (*http.Response, error) {
	target := *rt.official
	t := d.normal
	if p == directNormal {
		if rt.relayable {
			target = *d.r.relayURL(rt.official)
		}
	} else {
		t = d.ipTransport(p)
	}
	if d.r.stall <= 0 {
		out := req.Clone(req.Context())
		out.URL, out.Host = &target, ""
		return t.RoundTrip(out)
	}
	ctx, cancel := context.WithCancel(req.Context())
	out := req.Clone(ctx)
	out.URL, out.Host = &target, ""
	resp, err := t.RoundTrip(out)
	if err != nil {
		cancel()
		return nil, err
	}
	var onStall func()
	if track {
		onStall = func() { d.r.markBad(p, errDirectStalled) }
	}
	resp.Body = newDirectBody(resp.Body, d.r.stall, cancel, onStall)
	return resp, nil
}

var errDirectStalled = errors.New("drive direct: no data received within direct_stall_timeout")

// directBody aborts a response body when a read waits longer than stall.
// Only time spent inside Read counts, so a slow reader isn't a stall.
type directBody struct {
	io.ReadCloser
	stall   time.Duration
	cancel  context.CancelFunc
	onStall func()
	timer   *time.Timer
	stalled atomic.Bool
	once    sync.Once
}

func newDirectBody(rc io.ReadCloser, stall time.Duration, cancel context.CancelFunc, onStall func()) *directBody {
	b := &directBody{ReadCloser: rc, stall: stall, cancel: cancel, onStall: onStall}
	b.timer = time.AfterFunc(time.Hour, func() {
		b.stalled.Store(true)
		b.cancel()
	})
	b.timer.Stop()
	return b
}

func (b *directBody) Read(p []byte) (int, error) {
	b.timer.Reset(b.stall)
	n, err := b.ReadCloser.Read(p)
	b.timer.Stop()
	if err != nil && err != io.EOF && b.stalled.Load() {
		b.once.Do(func() {
			if b.onStall != nil {
				b.onStall()
			}
		})
		return n, errDirectStalled
	}
	return n, err
}

func (b *directBody) Close() error {
	b.timer.Stop()
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// candidateList builds the IPs to probe: user candidates or the seeds plus
// DNS answers, plus every IP that worked before.
func (r *directRouter) candidateList(ctx context.Context) ([]netip.Addr, error) {
	seen := map[netip.Addr]bool{}
	var out []netip.Addr
	add := func(s string, strict bool) error {
		ip, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			if strict {
				return fmt.Errorf("invalid direct_ips entry %q: %w", s, err)
			}
			return nil
		}
		ip = ip.Unmap()
		if !directAllowIP(ip) {
			if strict {
				return fmt.Errorf("direct_ips entry %q is not a public IP", s)
			}
			return nil
		}
		if !seen[ip] && len(out) < directMaxCandidates {
			seen[ip] = true
			out = append(out, ip)
		}
		return nil
	}
	if len(r.candidates) > 0 {
		for _, s := range r.candidates {
			if err := add(s, true); err != nil {
				return nil, err
			}
		}
	} else {
		for _, s := range directSeedIPs {
			_ = add(s, false)
		}
		for _, host := range directResolveHosts {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				fs.Debugf(nil, "drive direct: resolving %s: %v", host, err)
				continue
			}
			for _, a := range addrs {
				_ = add(a.IP.String(), false)
			}
		}
		r.mu.Lock()
		old := slices.Clone(r.cache.Endpoints)
		r.mu.Unlock()
		for _, e := range old {
			if e.ok() {
				_ = add(e.IP, false)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no candidate IPs; set direct_ips")
	}
	return out, nil
}

// normalProbeURL is the probe URL on the normal path.
func (r *directRouter) normalProbeURL() string {
	if r.relay == nil {
		return directProbeURL
	}
	u, err := url.Parse(directProbeURL)
	if err != nil {
		return directProbeURL
	}
	return r.relayURL(u).String()
}

// scan probes every candidate and the normal path and stores the results.
func (r *directRouter) scan(ctx context.Context) ([]directEndpoint, error) {
	ips, err := r.candidateList(ctx)
	if err != nil {
		return nil, err
	}
	fs.Infof(nil, "drive direct: probing %d candidate IPs", len(ips))

	normal := directEndpoint{IP: directNormal, CheckedAt: time.Now()}
	var normalWG sync.WaitGroup
	normalWG.Go(func() {
		latency, err := probeDirect(ctx, r.normalProbeURL(), nil)
		if err != nil {
			normal.Error = err.Error()
		} else {
			normal.LatencyMs = max(latency.Milliseconds(), 1)
		}
	})

	jobs := make(chan netip.Addr)
	results := make([]directEndpoint, 0, len(ips))
	var resultsMu sync.Mutex
	var wg sync.WaitGroup
	for range min(directProbeWorkers, len(ips)) {
		wg.Go(func() {
			for ip := range jobs {
				e := directEndpoint{IP: ip.String(), CheckedAt: time.Now()}
				latency, err := probeDirect(ctx, directProbeURL, func(t *http.Transport) {
					pinDirectIP(t, ip.String(), r.sni)
				})
				if err != nil {
					e.Error = err.Error()
				} else {
					e.LatencyMs = max(latency.Milliseconds(), 1)
				}
				resultsMu.Lock()
				results = append(results, e)
				resultsMu.Unlock()
			}
		})
	}
	for _, ip := range ips {
		jobs <- ip
	}
	close(jobs)
	wg.Wait()
	normalWG.Wait()
	sortDirectEndpoints(results)

	r.mu.Lock()
	r.cache.ScannedAt = time.Now()
	r.cache.Endpoints = results
	r.cache.Normal = normal
	for p := range r.bad {
		if p != directNormal {
			delete(r.bad, p)
		}
	}
	r.save()
	r.mu.Unlock()

	if normal.ok() {
		fs.Infof(nil, "drive direct: %s answers in %d ms", r.pathName(directNormal), normal.LatencyMs)
	} else {
		fs.Infof(nil, "drive direct: %s failed the probe: %s", r.pathName(directNormal), normal.Error)
	}
	good := 0
	for _, e := range results {
		if e.ok() {
			good++
		}
	}
	if good == 0 {
		return results, fmt.Errorf("none of %d candidate IPs passed the Drive probe; a TUN proxy or fake-ip DNS may be intercepting them, or set direct_ips", len(results))
	}
	fs.Infof(nil, "drive direct: %d of %d IPs usable, best %s (%d ms)", good, len(results), results[0].IP, results[0].LatencyMs)
	return results, nil
}

// sortDirectEndpoints orders working IPs by latency, then failures.
func sortDirectEndpoints(es []directEndpoint) {
	slices.SortStableFunc(es, func(a, b directEndpoint) int {
		if a.ok() != b.ok() {
			if a.ok() {
				return -1
			}
			return 1
		}
		if a.LatencyMs != b.LatencyMs {
			if a.LatencyMs < b.LatencyMs {
				return -1
			}
			return 1
		}
		return strings.Compare(a.IP, b.IP)
	})
}

// pinDirectIP makes t dial ip for every address, without a proxy.
func pinDirectIP(t *http.Transport, ip, sni string) {
	t.Proxy = nil
	dial := t.DialContext
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		return dial(ctx, network, net.JoinHostPort(ip, port))
	}
	applyDirectSNI(t, sni)
}

// probeDirect makes an unauthenticated Drive API call to probeURL and
// checks Google answers it. Only Google's JSON 401 counts as success.
func probeDirect(ctx context.Context, probeURL string, customize func(*http.Transport)) (time.Duration, error) {
	t := fshttp.NewTransportCustom(ctx, func(t *http.Transport) {
		t.DisableKeepAlives = true
		if customize != nil {
			customize(t)
		}
	})
	client := &http.Client{
		Transport: t,
		Timeout:   directProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		var uerr interface{ Unwrap() error }
		if errors.As(err, &uerr) {
			err = uerr.Unwrap() // drop the URL, it's the same for every IP
		}
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		return 0, fmt.Errorf("unexpected Drive API status %d", resp.StatusCode)
	}
	var body struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil || body.Error.Code != http.StatusUnauthorized {
		return 0, errors.New("response is not a Drive API error")
	}
	return time.Since(start), nil
}

// applyDirectSNI replaces the TLS server name sent to Drive hosts with sni,
// or sends none when sni is "off". The certificate is still verified
// against the real hostname. Empty sni leaves t alone.
func applyDirectSNI(t *http.Transport, sni string) {
	if sni == "" {
		return
	}
	if strings.EqualFold(sni, directSNIOff) {
		sni = ""
	}
	base := t.TLSClientConfig
	if base == nil {
		base = &tls.Config{}
	}
	h2 := t.TLSNextProto == nil || t.TLSNextProto["h2"] != nil
	handshakeTimeout := t.TLSHandshakeTimeout
	dial := t.DialContext
	t.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		raw, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		cfg := base.Clone()
		if h2 {
			cfg.NextProtos = []string{"h2", "http/1.1"}
		} else {
			cfg.NextProtos = []string{"http/1.1"}
		}
		cfg.ServerName = host
		if directRoutedHost(host) {
			cfg.ServerName = sni
			if !base.InsecureSkipVerify {
				// ServerName no longer names the host, so verify by hand.
				cfg.InsecureSkipVerify = true
				cfg.VerifyConnection = func(cs tls.ConnectionState) error {
					return verifyDirectCert(cs, host, base.RootCAs)
				}
			}
		}
		if handshakeTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, handshakeTimeout)
			defer cancel()
		}
		conn := tls.Client(raw, cfg)
		if err := conn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return conn, nil
	}
}

func verifyDirectCert(cs tls.ConnectionState, host string, roots *x509.CertPool) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("drive direct: server sent no certificate")
	}
	opts := x509.VerifyOptions{
		DNSName:       host,
		Roots:         roots,
		Intermediates: x509.NewCertPool(),
	}
	for _, c := range cs.PeerCertificates[1:] {
		opts.Intermediates.AddCert(c)
	}
	_, err := cs.PeerCertificates[0].Verify(opts)
	return err
}

// setSpeedFs makes f the remote speed tests run on.
func (r *directRouter) setSpeedFs(f *Fs) {
	r.mu.Lock()
	r.speedFs = f
	r.mu.Unlock()
}

// maybeSpeedTest starts a background speed test when the results are
// older than the interval or than the last scan.
func (r *directRouter) maybeSpeedTest(ctx context.Context) {
	if r.speedSize <= 0 {
		return
	}
	r.mu.Lock()
	now := time.Now()
	f := r.speedFs
	due := f != nil && r.speedDone == nil && now.Sub(r.speedTried) >= directCooldown &&
		(now.Sub(r.cache.SpeedTestedAt) >= r.speedEvery || r.cache.ScannedAt.After(r.cache.SpeedTestedAt))
	if due {
		r.speedTried = now
	}
	r.mu.Unlock()
	if due {
		go func() {
			if err := r.runSpeedTest(context.WithoutCancel(ctx), f); err != nil {
				fs.Infof(nil, "drive direct: speed test: %v", err)
			}
		}()
	}
}

// runSpeedTest runs a speed test now, or waits for the one running.
func (r *directRouter) runSpeedTest(ctx context.Context, f *Fs) error {
	r.mu.Lock()
	if done := r.speedDone; done != nil {
		r.mu.Unlock()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan struct{})
	r.speedDone = done
	r.speedTried = time.Now()
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.speedDone = nil
		close(done)
		r.mu.Unlock()
	}()
	return r.speedTest(ctx, f)
}

// speedTest measures download and upload speed on every path, one path
// at a time so they don't compete with each other.
func (r *directRouter) speedTest(ctx context.Context, f *Fs) error {
	size := (max(r.speedSize, directUploadChunk) + directUploadChunk - 1) / directUploadChunk * directUploadChunk
	paths := append(r.ips(ctx, true), directNormal)
	r.mu.Lock()
	normal := r.cache.Normal
	r.mu.Unlock()
	fileID, n, fileErr := f.directSpeedTestFile(ctx, size)
	results := make(map[string]directSpeed, len(paths))
	for _, p := range paths {
		pctx := context.WithValue(ctx, directPathKey{}, p)
		s := directSpeed{TestedAt: time.Now()}
		if p == directNormal && !normal.ok() {
			// Don't wait for timeouts on a path that is known to be down.
			s.DownError = "failed the probe: " + normal.Error
			s.UpError = s.DownError
			results[p] = s
			continue
		}
		var err error
		if fileErr != nil {
			s.DownError = fileErr.Error()
		} else if s.DownBps, err = f.directDownloadTest(pctx, fileID, n); err != nil {
			s.DownError = err.Error()
		}
		if s.UpBps, err = f.directUploadTest(pctx, size); err != nil {
			s.UpError = err.Error()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		results[p] = s
		fs.Infof(nil, "drive direct: speed test %s: download %s, upload %s",
			r.pathName(p), directSpeedString(s.DownBps, s.DownError), directSpeedString(s.UpBps, s.UpError))
	}
	r.mu.Lock()
	r.cache.Speeds = results
	r.cache.SpeedTestedAt = time.Now()
	r.save()
	r.mu.Unlock()
	return nil
}

func directSpeedString(bps int64, errStr string) string {
	if errStr != "" {
		return "failed (" + errStr + ")"
	}
	return fs.SizeSuffix(bps).ByteRateUnit()
}

// directRate turns got bytes in d into a speed. Running out of time still
// gives a speed if enough arrived.
func directRate(ctx context.Context, got int64, d time.Duration, err error) (int64, error) {
	if err != nil && (ctx.Err() == nil || got < directSpeedMinBytes) {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("too slow: %s in %v", fs.SizeSuffix(got).ByteUnit(), d.Round(time.Second))
		}
		return 0, err
	}
	return int64(float64(got) / max(d.Seconds(), 1e-3)), nil
}

// directSpeedTestFile picks the largest file on the remote to download
// from, up to want bytes of it.
func (f *Fs) directSpeedTestFile(ctx context.Context, want int64) (id string, n int64, err error) {
	list := f.svc.Files.List().
		Q("trashed=false and mimeType!='" + driveFolderType + "'").
		OrderBy("quotaBytesUsed desc").
		PageSize(1).
		Fields("files(id,size)").
		SupportsAllDrives(true)
	if f.isTeamDrive {
		list.Corpora("drive").DriveId(f.opt.TeamDriveID).IncludeItemsFromAllDrives(true)
	}
	var files *drive.FileList
	files, err = list.Context(ctx).Do()
	if err != nil {
		return "", 0, fmt.Errorf("finding a file to download: %w", err)
	}
	if len(files.Files) == 0 || files.Files[0].Size < 4*directSpeedMinBytes {
		return "", 0, errors.New("no file of at least 1 MiB to download")
	}
	return files.Files[0].Id, min(want, files.Files[0].Size), nil
}

// directDownloadTest downloads the first n bytes of file id.
func (f *Fs) directDownloadTest(ctx context.Context, id string, n int64) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, directSpeedTimeout)
	defer cancel()
	u := directAPIURL.String() + "/drive/v3/files/" + url.PathEscape(id) + "?alt=media&supportsAllDrives=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("HTTP %s", resp.Status)
	}
	start := time.Now()
	got, err := io.CopyN(io.Discard, resp.Body, n)
	if err == io.EOF {
		err = nil
	}
	return directRate(ctx, got, time.Since(start), err)
}

// directUploadTest uploads n bytes to a resumable upload session and then
// cancels it, so no file is created.
func (f *Fs) directUploadTest(ctx context.Context, n int64) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, directSpeedTimeout)
	defer cancel()
	meta := map[string]any{"name": ".rrclone-direct-speedtest"}
	if f.rootFolderID != "" {
		meta["parents"] = []string{f.rootFolderID}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return 0, err
	}
	u := directAPIURL.String() + "/upload/drive/v3/files?uploadType=resumable&supportsAllDrives=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(metaJSON))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", "application/octet-stream")
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusOK || loc == "" {
		return 0, fmt.Errorf("starting upload: HTTP %s", resp.Status)
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), directProbeTimeout)
		defer cancel()
		if req, err := http.NewRequestWithContext(dctx, http.MethodDelete, loc, nil); err == nil {
			if resp, err := f.client.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
	}()

	data := make([]byte, n)
	_, _ = rand.Read(data) // random so nothing on the way can compress it
	body := &directCountingReader{r: bytes.NewReader(data)}
	req, err = http.NewRequestWithContext(ctx, http.MethodPut, loc, body)
	if err != nil {
		return 0, err
	}
	req.ContentLength = n
	req.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/*", n-1))
	start := time.Now()
	resp, err = f.client.Do(req)
	d := time.Since(start)
	if err != nil {
		return directRate(ctx, body.n.Load(), d, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != statusResumeIncomplete {
		return 0, fmt.Errorf("uploading: HTTP %s", resp.Status)
	}
	return directRate(ctx, n, d, nil)
}

type directCountingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *directCountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// directPathStatus is one path in the "direct" backend command output.
type directPathStatus struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	LatencyMs   int64  `json:"latency_ms,omitempty"`
	DownBps     int64  `json:"down_bps,omitempty"`
	DownError   string `json:"down_error,omitempty"`
	UpBps       int64  `json:"up_bps,omitempty"`
	UpError     string `json:"up_error,omitempty"`
	BackoffLeft string `json:"backoff_left,omitempty"`
}

// directStatus is the output of the "direct" backend command.
type directStatus struct {
	Enabled       bool               `json:"enabled"`
	SNI           string             `json:"sni,omitempty"`
	Endpoint      string             `json:"endpoint,omitempty"`
	ScannedAt     time.Time          `json:"scanned_at"`
	SpeedTestedAt time.Time          `json:"speed_tested_at,omitzero"`
	CacheFile     string             `json:"cache_file"`
	Downloads     []directPathStatus `json:"downloads"`
	Uploads       []directPathStatus `json:"uploads"`
	Normal        directEndpoint     `json:"normal"`
	Endpoints     []directEndpoint   `json:"endpoints"`
}

func (f *Fs) directCommand(ctx context.Context, opt map[string]string) (any, error) {
	r := getDirectRouter(&f.opt)
	if _, ok := opt["scan"]; ok {
		if _, err := r.scan(ctx); err != nil {
			return nil, err
		}
	}
	if _, ok := opt["speedtest"]; ok {
		if !f.opt.Direct {
			return nil, errors.New("speedtest needs direct mode on (--drive-direct)")
		}
		if err := r.runSpeedTest(ctx, f); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	status := directStatus{
		Enabled:       f.opt.Direct,
		SNI:           f.opt.DirectSNI,
		ScannedAt:     r.cache.ScannedAt,
		SpeedTestedAt: r.cache.SpeedTestedAt,
		CacheFile:     r.cachePath,
		Normal:        r.cache.Normal,
		Endpoints:     slices.Clone(r.cache.Endpoints),
	}
	if r.relay != nil {
		status.Endpoint = r.relay.String() + r.relayPrefix
	}
	ips := r.okIPs()
	for dir, out := range map[directDir]*[]directPathStatus{directDown: &status.Downloads, directUp: &status.Uploads} {
		for _, p := range r.order(ips, dir, true, now) {
			s := r.cache.Speeds[p]
			ps := directPathStatus{
				Path:      p,
				Name:      r.pathName(p),
				LatencyMs: r.latency(p),
				DownBps:   s.DownBps,
				DownError: s.DownError,
				UpBps:     s.UpBps,
				UpError:   s.UpError,
			}
			if b := r.bad[p]; b != nil && now.Before(b.until) {
				ps.BackoffLeft = b.until.Sub(now).Round(time.Second).String()
			}
			*out = append(*out, ps)
		}
	}
	return status, nil
}
