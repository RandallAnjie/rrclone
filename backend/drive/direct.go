// Direct connection mode: pin Drive traffic to scanned Google front-end IPs.
//
// The idea comes from gdirect (https://github.com/mizorewww/gdirect): when
// DNS for Google is poisoned or the default route is filtered, some Google
// front-end IPs are often still reachable. We probe candidates with a real
// Drive API request, keep the ones that answer, and dial those IPs for the
// Drive hosts while TLS is still verified against the real hostname.
// Token refresh (oauth2.googleapis.com) is left on the normal network.
//
// This is an independent Go implementation; no gdirect code is used.

package drive

import (
	"context"
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
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/fshttp"
)

const (
	directSNIOff        = "off"
	directProbeWorkers  = 8
	directProbeTimeout  = 5 * time.Second
	directScanTimeout   = 30 * time.Second
	directCooldown      = 5 * time.Minute
	directMaxCandidates = 256
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

// directEndpoint is one probed IP.
type directEndpoint struct {
	IP        string    `json:"ip"`
	LatencyMs int64     `json:"latency_ms,omitempty"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

func (e directEndpoint) ok() bool { return e.Error == "" }

type directCache struct {
	ScannedAt time.Time        `json:"scanned_at"`
	Endpoints []directEndpoint `json:"endpoints"`
}

// directRouter holds the scan results shared by all drive remotes with the
// same candidate list.
type directRouter struct {
	candidates []string // user supplied candidates, empty for the defaults
	sni        string
	rescan     time.Duration
	cachePath  string

	mu       sync.Mutex
	cache    directCache
	bad      map[string]time.Time // IP -> cooldown end
	scanning chan struct{}        // closed when the running scan ends
	warned   bool
}

var (
	directRoutersMu sync.Mutex
	directRouters   = map[string]*directRouter{}
)

// getDirectRouter returns the shared router for these options.
func getDirectRouter(opt *Options) *directRouter {
	candidates := slices.Clone([]string(opt.DirectIPs))
	slices.Sort(candidates)
	key := strings.Join(candidates, ",") + "|" + opt.DirectSNI
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
		cachePath:  filepath.Join(config.GetCacheDir(), "drive-direct", hex.EncodeToString(sum[:6])+".json"),
		bad:        map[string]time.Time{},
	}
	if r.rescan <= 0 {
		r.rescan = 24 * time.Hour
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

// usable returns the working IPs, best first, skipping those cooling down
// unless every one is. Call with r.mu held.
func (r *directRouter) usable(now time.Time) (ips []string, allCooling bool) {
	var cooling []string
	for _, e := range r.cache.Endpoints {
		if !e.ok() {
			continue
		}
		if now.Before(r.bad[e.IP]) {
			cooling = append(cooling, e.IP)
		} else {
			ips = append(ips, e.IP)
		}
	}
	if len(ips) == 0 && len(cooling) > 0 {
		return cooling, true
	}
	return ips, false
}

// endpoints returns IPs to try, scanning first when the cache is stale,
// empty, or every IP has recently failed.
func (r *directRouter) endpoints(ctx context.Context) []string {
	r.mu.Lock()
	now := time.Now()
	ips, allCooling := r.usable(now)
	age := now.Sub(r.cache.ScannedAt)
	// A scan within the cooldown is trusted even if it found nothing, so a
	// network with no reachable IP doesn't rescan on every dial.
	fresh := age < directCooldown ||
		(age < r.rescan && len(ips) > 0 && !allCooling)
	if fresh {
		r.mu.Unlock()
		return ips
	}
	if r.scanning == nil {
		done := make(chan struct{})
		r.scanning = done
		r.mu.Unlock()
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
		ips, _ = r.usable(time.Now())
		r.mu.Unlock()
		return ips
	}
	done := r.scanning
	r.mu.Unlock()
	select {
	case <-done:
		r.mu.Lock()
		ips, _ = r.usable(time.Now())
		r.mu.Unlock()
		return ips
	case <-ctx.Done():
		return nil
	}
}

func (r *directRouter) markBad(ip string, err error) {
	r.mu.Lock()
	r.bad[ip] = time.Now().Add(directCooldown)
	r.mu.Unlock()
	fs.Debugf(nil, "drive direct: %s failed, cooling down for %v: %v", ip, directCooldown, err)
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// dial connects to a scanned IP on the port from addr, falling back to the
// normal dial when no scanned IP works.
func (r *directRouter) dial(ctx context.Context, dial dialFunc, network, addr string) (net.Conn, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	for _, ip := range r.endpoints(ctx) {
		conn, err := dial(ctx, network, net.JoinHostPort(ip, port))
		if err == nil {
			return conn, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, err
		}
		r.markBad(ip, err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r.mu.Lock()
	if !r.warned {
		r.warned = true
		fs.Logf(nil, "drive direct: no reachable Google IP, using the normal network for %s", addr)
	}
	r.mu.Unlock()
	return dial(ctx, network, addr)
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

// scan probes every candidate and stores the results.
func (r *directRouter) scan(ctx context.Context) ([]directEndpoint, error) {
	ips, err := r.candidateList(ctx)
	if err != nil {
		return nil, err
	}
	fs.Infof(nil, "drive direct: probing %d candidate IPs", len(ips))
	jobs := make(chan netip.Addr)
	results := make([]directEndpoint, 0, len(ips))
	var resultsMu sync.Mutex
	var wg sync.WaitGroup
	for range min(directProbeWorkers, len(ips)) {
		wg.Go(func() {
			for ip := range jobs {
				e := directEndpoint{IP: ip.String(), CheckedAt: time.Now()}
				latency, err := probeDirectIP(ctx, ip.String(), r.sni)
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
	sortDirectEndpoints(results)

	r.mu.Lock()
	r.cache = directCache{ScannedAt: time.Now(), Endpoints: results}
	clear(r.bad)
	r.warned = false
	r.save()
	r.mu.Unlock()

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

// probeDirectIP makes an unauthenticated Drive API call through ip and
// checks Google answers it. Only the TLS handshake to the real hostname
// plus Google's JSON 401 counts as success.
func probeDirectIP(ctx context.Context, ip, sni string) (time.Duration, error) {
	t := fshttp.NewTransportCustom(ctx, func(t *http.Transport) {
		t.Proxy = nil
		t.DisableKeepAlives = true
		dial := t.DialContext
		t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			return dial(ctx, network, net.JoinHostPort(ip, port))
		}
		applyDirectSNI(t, sni)
	})
	client := &http.Client{
		Transport: t,
		Timeout:   directProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, directProbeURL, nil)
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

// applyDirect makes t dial Drive hosts on scanned Google IPs, bypassing
// any HTTP proxy for them.
func applyDirect(t *http.Transport, opt *Options) {
	r := getDirectRouter(opt)
	proxy := t.Proxy
	t.Proxy = func(req *http.Request) (*url.URL, error) {
		if directRoutedHost(req.URL.Hostname()) || proxy == nil {
			return nil, nil
		}
		return proxy(req)
	}
	dial := t.DialContext
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil || !directRoutedHost(host) {
			return dial(ctx, network, addr)
		}
		return r.dial(ctx, dial, network, addr)
	}
	applyDirectSNI(t, opt.DirectSNI)
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

// directStatus is the output of the "direct" backend command.
type directStatus struct {
	Enabled   bool             `json:"enabled"`
	SNI       string           `json:"sni,omitempty"`
	ScannedAt time.Time        `json:"scanned_at"`
	CacheFile string           `json:"cache_file"`
	Endpoints []directEndpoint `json:"endpoints"`
}

func (f *Fs) directCommand(ctx context.Context, opt map[string]string) (any, error) {
	r := getDirectRouter(&f.opt)
	if _, ok := opt["scan"]; ok {
		if _, err := r.scan(ctx); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return directStatus{
		Enabled:   f.opt.Direct,
		SNI:       f.opt.DirectSNI,
		ScannedAt: r.cache.ScannedAt,
		CacheFile: r.cachePath,
		Endpoints: slices.Clone(r.cache.Endpoints),
	}, nil
}
