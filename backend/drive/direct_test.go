package drive

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectRoutedHost(t *testing.T) {
	for host, want := range map[string]bool{
		"www.googleapis.com":                   true,
		"WWW.GOOGLEAPIS.COM.":                  true,
		"drive.google.com":                     true,
		"drive.usercontent.google.com":         true,
		"doc-0k-4c-docs.googleusercontent.com": true,
		"googleusercontent.com":                false,
		"oauth2.googleapis.com":                false,
		"accounts.google.com":                  false,
		"googleapis.example.com":               false,
	} {
		assert.Equal(t, want, directRoutedHost(host), host)
	}
}

func TestIsPublicIP(t *testing.T) {
	for s, want := range map[string]bool{
		"142.250.1.1":     true,
		"183.56.143.147":  true,
		"2404:6800::1":    true,
		"127.0.0.1":       false,
		"10.1.2.3":        false,
		"192.168.1.1":     false,
		"198.18.0.1":      false,
		"198.19.255.1":    false,
		"100.64.0.1":      false,
		"192.0.2.1":       false,
		"::1":             false,
		"fc00::1":         false,
		"fe80::1":         false,
		"::ffff:10.0.0.1": false,
	} {
		assert.Equal(t, want, isPublicIP(netip.MustParseAddr(s)), s)
	}
}

func TestSortDirectEndpoints(t *testing.T) {
	es := []directEndpoint{
		{IP: "4", Error: "x"},
		{IP: "3", LatencyMs: 30},
		{IP: "1", LatencyMs: 10},
		{IP: "2", LatencyMs: 20},
	}
	sortDirectEndpoints(es)
	var got []string
	for _, e := range es {
		got = append(got, e.IP)
	}
	assert.Equal(t, []string{"1", "2", "3", "4"}, got)
}

// closeTestServer stops s without httptest.Server.Close, which sets up
// http.DefaultTransport. fshttp copies its settings, so later tests would
// get an h2 TLSNextProto without h2 in ALPN and fall back to HTTP/1.1.
func closeTestServer(s *httptest.Server) {
	_ = s.Config.Close()
}

// directTestServer is a TLS server with a certificate for the Google API
// names, trusted through --ca-cert.
type directTestServer struct {
	*httptest.Server
	port string

	mu   sync.Mutex
	snis []string
}

func (s *directTestServer) lastSNI() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snis[len(s.snis)-1]
}

func newDirectTestServer(t *testing.T) (context.Context, *directTestServer) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "direct test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		DNSNames:              []string{"www.googleapis.com", "*.googleusercontent.com"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))

	s := &directTestServer{}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/drive/v3/about" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":401,"message":"Login Required"}}`)
			return
		}
		_, _ = io.WriteString(w, "hello "+r.Host+" "+r.Proto)
	}))
	s.EnableHTTP2 = true
	s.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			s.mu.Lock()
			s.snis = append(s.snis, hello.ServerName)
			s.mu.Unlock()
			return nil, nil
		},
	}
	s.StartTLS()
	t.Cleanup(func() { closeTestServer(s.Server) })
	_, s.port, _ = net.SplitHostPort(s.Listener.Addr().String())

	oldAllow, oldProbe, oldCache := directAllowIP, directProbeURL, config.GetCacheDir()
	directAllowIP = func(netip.Addr) bool { return true }
	directProbeURL = "https://www.googleapis.com:" + s.port + "/drive/v3/about?fields=user"
	require.NoError(t, config.SetCacheDir(t.TempDir()))
	t.Cleanup(func() {
		directAllowIP, directProbeURL = oldAllow, oldProbe
		_ = config.SetCacheDir(oldCache)
		directRoutersMu.Lock()
		clear(directRouters)
		directRoutersMu.Unlock()
	})

	ctx, ci := fs.AddConfig(context.Background())
	ci.CaCert = []string{caFile}
	// Routed hosts must bypass the proxy; this one doesn't exist.
	ci.HTTPProxy = "http://127.0.0.1:1"
	return ctx, s
}

func directGet(t *testing.T, ctx context.Context, opt *Options, url string) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := getClient(ctx, opt).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

func TestDirectEndToEnd(t *testing.T) {
	ctx, s := newDirectTestServer(t)
	// 127.0.0.2 refuses: the server only listens on 127.0.0.1.
	opt := &Options{Direct: true, DirectIPs: fs.CommaSepList{"127.0.0.2", "127.0.0.1"}}

	body, err := directGet(t, ctx, opt, "https://www.googleapis.com:"+s.port+"/drive/v3/files")
	require.NoError(t, err)
	assert.Equal(t, "hello www.googleapis.com:"+s.port+" HTTP/2.0", body)
	assert.Equal(t, "www.googleapis.com", s.lastSNI())

	r := getDirectRouter(opt)
	r.mu.Lock()
	es := r.cache.Endpoints
	r.mu.Unlock()
	require.Len(t, es, 2)
	assert.Equal(t, "127.0.0.1", es[0].IP)
	assert.True(t, es[0].ok())
	assert.Equal(t, "127.0.0.2", es[1].IP)
	assert.False(t, es[1].ok())
	_, err = os.Stat(r.cachePath)
	assert.NoError(t, err, "scan results are cached")

	// Hosts not routed still go through the (broken) proxy.
	_, err = directGet(t, ctx, opt, "https://oauth2.googleapis.com:"+s.port+"/token")
	assert.Error(t, err)
}

func TestDirectSNI(t *testing.T) {
	ctx, s := newDirectTestServer(t)
	for _, test := range []struct {
		sni, want string
	}{
		{"off", ""},
		{"example.org", "example.org"},
	} {
		opt := &Options{Direct: true, DirectIPs: fs.CommaSepList{"127.0.0.1"}, DirectSNI: test.sni}
		body, err := directGet(t, ctx, opt, "https://www.googleapis.com:"+s.port+"/x")
		require.NoError(t, err, test.sni)
		assert.Equal(t, "hello www.googleapis.com:"+s.port+" HTTP/2.0", body)
		assert.Equal(t, test.want, s.lastSNI())

		// The certificate is still checked against the real hostname.
		_, err = directGet(t, ctx, opt, "https://drive.google.com:"+s.port+"/x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "drive.google.com")
	}
}

func TestDirectRejectsPrivateCandidate(t *testing.T) {
	r := &directRouter{candidates: []string{"10.0.0.1"}, bad: map[string]*directBackoff{}}
	_, err := r.candidateList(context.Background())
	assert.ErrorContains(t, err, "not a public IP")
}
