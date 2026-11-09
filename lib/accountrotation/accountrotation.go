// Package accountrotation rotates between several OAuth accounts.
//
// Each account lives in its own JSON file, either a bare OAuth token
// blob (as rclone stores under the "token" config key) or a structured
// file which also carries the OAuth application for that account:
//
//	{"client_id":"…","client_secret":"…","token":{…}}
//
// A Rotator is an http.RoundTripper which sends every request through
// the currently active account. When a response says the account is
// rate limited (as decided by the backend) the Rotator puts it on
// cooldown, switches to the next healthy account and, where the
// request body can be replayed, retries the request transparently.
package accountrotation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/lib/env"
)

// ParseFiles expands a comma or newline separated list of files,
// directories (all *.json files inside, sorted) and glob patterns
// (sorted) into a de-duplicated list of files.
func ParseFiles(spec string) ([]string, error) {
	parts := strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	seen := map[string]struct{}{}
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		expanded, err := expandSpec(env.ShellExpand(part))
		if err != nil {
			return nil, err
		}
		for _, file := range expanded {
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			files = append(files, file)
		}
	}
	return files, nil
}

func expandSpec(spec string) ([]string, error) {
	if strings.ContainsAny(spec, "*?[") {
		matches, err := filepath.Glob(spec)
		if err != nil {
			return nil, fmt.Errorf("invalid oauth account glob %q: %w", spec, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("oauth account glob %q matched no files", spec)
		}
		sort.Strings(matches)
		return matches, nil
	}
	info, err := os.Stat(spec)
	if err != nil {
		return nil, fmt.Errorf("couldn't access oauth account file %q: %w", spec, err)
	}
	if !info.IsDir() {
		return []string{spec}, nil
	}
	entries, err := os.ReadDir(spec)
	if err != nil {
		return nil, fmt.Errorf("couldn't read oauth account directory %q: %w", spec, err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		files = append(files, filepath.Join(spec, entry.Name()))
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("oauth account directory %q contained no JSON files", spec)
	}
	sort.Strings(files)
	return files, nil
}

// fileData is the structured account file format
type fileData struct {
	ClientID     string          `json:"client_id,omitempty"`
	ClientSecret string          `json:"client_secret,omitempty"`
	Token        json.RawMessage `json:"token,omitempty"`
}

// ReadFile reads an account file.
//
// If the file has a top level "token" object it is the structured
// format and structured is true, otherwise the whole file is the token.
func ReadFile(path string) (clientID, clientSecret, tokenJSON string, structured bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", false, err
	}
	var d fileData
	if jsonErr := json.Unmarshal(data, &d); jsonErr == nil && len(d.Token) > 0 {
		compacted := &bytes.Buffer{}
		if json.Compact(compacted, d.Token) == nil {
			return d.ClientID, d.ClientSecret, compacted.String(), true, nil
		}
		return d.ClientID, d.ClientSecret, string(d.Token), true, nil
	}
	return "", "", strings.TrimSpace(string(data)), false, nil
}

// normalizeTokenExpiry replaces a missing or zero "expiry" with a time
// in the past so the oauth2 library refreshes the token straight away
// rather than trusting a possibly stale access_token forever.
func normalizeTokenExpiry(tokenJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(tokenJSON), &m); err != nil {
		return tokenJSON
	}
	expiry, _ := m["expiry"].(string)
	if expiry == "" || expiry == "0001-01-01T00:00:00Z" {
		m["expiry"] = "1970-01-01T00:00:00Z"
		if b, err := json.Marshal(m); err == nil {
			return string(b)
		}
	}
	return tokenJSON
}

// TokenFileMapper is a configmap.Mapper which serves the OAuth token
// (and, for structured files, client_id and client_secret) from an
// account file. Refreshed tokens are written back to that file. Every
// other key is delegated to Base.
type TokenFileMapper struct {
	Base configmap.Mapper
	File string
	mu   sync.Mutex
}

// Get implements configmap.Getter
func (m *TokenFileMapper) Get(key string) (string, bool) {
	switch key {
	case config.ConfigToken:
		m.mu.Lock()
		defer m.mu.Unlock()
		_, _, tokenJSON, _, err := ReadFile(m.File)
		if err != nil {
			return "", false
		}
		return normalizeTokenExpiry(tokenJSON), true
	case config.ConfigClientID, config.ConfigClientSecret:
		m.mu.Lock()
		clientID, clientSecret, _, structured, err := ReadFile(m.File)
		m.mu.Unlock()
		if err == nil && structured {
			if key == config.ConfigClientID && clientID != "" {
				return clientID, true
			}
			if key == config.ConfigClientSecret && clientSecret != "" {
				return clientSecret, true
			}
		}
	}
	return m.Base.Get(key)
}

// Set implements configmap.Setter
func (m *TokenFileMapper) Set(key, value string) {
	if key != config.ConfigToken {
		m.Base.Set(key, value)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []byte(value)
	clientID, clientSecret, _, structured, err := ReadFile(m.File)
	if err == nil && structured {
		compacted := &bytes.Buffer{}
		if json.Compact(compacted, []byte(value)) != nil {
			compacted.Reset()
			compacted.WriteString(value)
		}
		out, err = json.MarshalIndent(fileData{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Token:        json.RawMessage(compacted.Bytes()),
		}, "", "  ")
		if err != nil {
			fs.Errorf(nil, "failed to marshal updated account file %q: %v", m.File, err)
			return
		}
	}
	if err := os.WriteFile(m.File, out, 0o600); err != nil {
		fs.Errorf(nil, "failed to save refreshed token to %q: %v", m.File, err)
	}
}

// Pool is a set of account files with per-file cooldowns
type Pool struct {
	mu        sync.Mutex
	files     []string
	cooldowns map[string]time.Time
}

// NewPool makes a pool from files, dropping blanks and duplicates
func NewPool(files []string) *Pool {
	p := &Pool{cooldowns: map[string]time.Time{}}
	for _, file := range files {
		if file != "" && !slices.Contains(p.files, file) {
			p.files = append(p.files, file)
		}
	}
	return p
}

// Len returns the number of accounts in the pool
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.files)
}

// coolDown puts file on cooldown until until (zero clears it)
func (p *Pool) coolDown(file string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if until.IsZero() {
		delete(p.cooldowns, file)
	} else {
		p.cooldowns[file] = until
	}
}

// coolingDown reports whether file is on cooldown at now
func (p *Pool) coolingDown(file string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cooldowns[file].After(now)
}

// candidates returns the accounts after current, in order, which are
// not cooling down, and how long until the first other one is free.
func (p *Pool) candidates(current string, now time.Time) (out []string, wait time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	start := slices.Index(p.files, current) + 1 // 0 if not found
	var earliest time.Time
	for i := range len(p.files) {
		file := p.files[(start+i)%len(p.files)]
		if file == current {
			continue
		}
		if until := p.cooldowns[file]; until.After(now) {
			if earliest.IsZero() || until.Before(earliest) {
				earliest = until
			}
			continue
		}
		out = append(out, file)
	}
	if !earliest.IsZero() {
		wait = max(earliest.Sub(now), 0)
	}
	return out, wait
}

// Verdict is what a Classifier decides about a round trip
type Verdict struct {
	Rotate   bool          // the account is unusable for now
	Reason   string        // short reason for logs
	Cooldown time.Duration // minimum cooldown requested by the server, if any
}

// Classifier inspects the result of a round trip. If it reads
// resp.Body it must restore it.
type Classifier func(resp *http.Response, err error) Verdict

// NewClientFunc makes an authorised client for an account file. It
// should check the account is usable (e.g. the refresh token works).
type NewClientFunc func(ctx context.Context, file string) (*http.Client, error)

// state is the active account
type state struct {
	client *http.Client
	file   string
	gen    uint64
}

// Rotator is an http.RoundTripper which uses the active account and
// rotates to the next one when the Classifier says so.
type Rotator struct {
	name      string // for logs, e.g. "dropbox"
	pool      *Pool
	cooldown  time.Duration
	classify  Classifier
	newClient NewClientFunc
	mu        sync.Mutex // serialises rotations
	cur       atomic.Pointer[state]
}

// NewRotator makes a Rotator whose first account is file, already
// authorised as client.
func NewRotator(name string, pool *Pool, cooldown time.Duration, file string, client *http.Client, classify Classifier, newClient NewClientFunc) *Rotator {
	r := &Rotator{name: name, pool: pool, cooldown: cooldown, classify: classify, newClient: newClient}
	r.cur.Store(&state{client: client, file: file, gen: 1})
	return r
}

// Current returns the file of the active account
func (r *Rotator) Current() string {
	return r.cur.Load().file
}

// Healthy reports whether the active account is not cooling down, so
// a failed request may be retried straight away.
func (r *Rotator) Healthy() bool {
	return !r.pool.coolingDown(r.Current(), time.Now())
}

// RoundTrip implements http.RoundTripper
func (r *Rotator) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		st := r.cur.Load()
		transport := st.client.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		resp, err := transport.RoundTrip(req)
		v := r.classify(resp, err)
		if !v.Rotate {
			return resp, err
		}
		if !r.rotate(req.Context(), st, v) {
			return resp, err
		}
		// Replay the request on the new account if we can
		if attempt+1 >= r.pool.Len() || (req.Body != nil && req.Body != http.NoBody && req.GetBody == nil) {
			return resp, err
		}
		next := req.Clone(req.Context())
		if req.GetBody != nil {
			body, bodyErr := req.GetBody()
			if bodyErr != nil {
				return resp, err
			}
			next.Body = body
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
			_ = resp.Body.Close()
		}
		req = next
	}
}

// rotate switches away from failed, which produced v.
//
// It returns true if a different account is now active, either
// because it switched or because another request already did.
func (r *Rotator) rotate(ctx context.Context, failed *state, v Verdict) bool {
	if r.cooldown <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur.Load().gen != failed.gen {
		return true // someone else already moved on
	}
	now := time.Now()
	r.pool.coolDown(failed.file, now.Add(max(r.cooldown, v.Cooldown)))
	candidates, wait := r.pool.candidates(failed.file, now)
	if len(candidates) == 0 {
		if wait > 0 {
			fs.Infof(r.name, "%s on OAuth account %q; all %d other accounts cooling down for up to %v", v.Reason, short(failed.file), r.pool.Len()-1, wait.Round(time.Second))
		}
		return false
	}
	for _, candidate := range candidates {
		client, err := r.newClient(ctx, candidate)
		if err != nil {
			r.pool.coolDown(candidate, time.Now().Add(r.cooldown))
			fs.Errorf(r.name, "%s on OAuth account %q; can't switch to %q: %v", v.Reason, short(failed.file), short(candidate), err)
			continue
		}
		r.cur.Store(&state{client: client, file: candidate, gen: failed.gen + 1})
		fs.Infof(r.name, "%s on OAuth account %q; switched to %q", v.Reason, short(failed.file), short(candidate))
		return true
	}
	return false
}

func short(file string) string {
	return filepath.Base(file)
}
