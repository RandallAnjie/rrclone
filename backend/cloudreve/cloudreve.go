// Package cloudreve provides an interface to Cloudreve v4 servers
package cloudreve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/backend/cloudreve/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/list"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/rclone/rclone/lib/oauthutil"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
	"golang.org/x/oauth2"
)

const (
	minSleep      = 10 * time.Millisecond
	maxSleep      = 2 * time.Second
	decayConstant = 2
	listPageSize  = 500
	apiPrefix     = "/api/v4"
	uriScheme     = "cloudreve://"
)

// oauthScopes are requested by the OAuth flow
var oauthScopes = []string{
	"openid",
	"offline_access",
	"UserInfo.Read",
	"Files.Read",
	"Files.Write",
	"Shares.Write",
}

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "cloudreve",
		Description: "Cloudreve v4",
		NewFs:       NewFs,
		Config:      configure,
		CommandHelp: commandHelp,
		Options: append(oauthutil.SharedOptions, []fs.Option{{
			Name:     "url",
			Help:     "URL of the Cloudreve site, e.g. https://cloud.example.com",
			Required: true,
		}, {
			Name: "email",
			Help: `Email to log in with.

Leave blank to log in with an OAuth app (client_id and client_secret)
instead, which also works for accounts which sign in with OIDC.`,
		}, {
			Name:       "password",
			Help:       "Password to log in with.",
			IsPassword: true,
		}, {
			Name: "share_id",
			Help: `ID of a share link to access instead of your own files.

For a link like https://cloud.example.com/s/AbCd it is "AbCd". The
remote is read only in this mode and needs no login unless the share
requires one.`,
			Advanced: true,
		}, {
			Name:       "share_password",
			Help:       "Password of the share link, if it has one.",
			IsPassword: true,
			Advanced:   true,
		}, {
			Name: "policy_id",
			Help: `ID of the storage policy to upload to.

Leave blank to use the policy of the target folder. Choosing a policy
needs Cloudreve Pro, where a user group may have several. "rclone
backend policy remote:" shows the policy a folder uses.`,
			Advanced: true,
		}, {
			Name: "client_encryption",
			Help: `Encrypt uploads client side when the storage policy asks for it.

If the policy has encryption enabled and uploads go straight to the
storage provider, Cloudreve expects the client to encrypt with
AES-256-CTR. Turn this off to upload such files unencrypted.`,
			Default:  true,
			Advanced: true,
		}, {
			Name:     "hard_delete",
			Help:     "Delete files permanently rather than moving them to the trash.",
			Default:  false,
			Advanced: true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			// Cloudreve rejects \ / : * ? " < > | and names of . or ..
			// Local storage paths are built from the names, so a server
			// on Windows can't store control characters or folders
			// ending in a space.
			Default: encoder.Base |
				encoder.EncodeWin |
				encoder.EncodeBackSlash |
				encoder.EncodeCtl |
				encoder.EncodeRightSpace |
				encoder.EncodeInvalidUtf8,
		}}...),
	})
}

// Options defines the configuration for this backend
type Options struct {
	URL              string               `config:"url"`
	Email            string               `config:"email"`
	Password         string               `config:"password"`
	ShareID          string               `config:"share_id"`
	SharePassword    string               `config:"share_password"`
	PolicyID         string               `config:"policy_id"`
	ClientEncryption bool                 `config:"client_encryption"`
	HardDelete       bool                 `config:"hard_delete"`
	Enc              encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote Cloudreve server
type Fs struct {
	name     string
	root     string
	opt      Options
	m        configmap.Mapper
	features *fs.Features
	siteURL  string       // site root without trailing slash
	srv      *rest.Client // authorised client for the Cloudreve API
	unauth   *rest.Client // unauthorised client for the Cloudreve API
	plain    *rest.Client // client for presigned storage URLs
	ts       *tokenSource // nil in anonymous share mode
	pacer    *fs.Pacer
}

// Object describes a Cloudreve file
type Object struct {
	fs      *Fs
	remote  string
	id      string
	entity  string // the content version
	size    int64
	modTime time.Time
}

// ------------------------------------------------------------

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string {
	return f.name
}

// Root of the remote (as passed into NewFs)
func (f *Fs) Root() string {
	return f.root
}

// String converts this Fs to a string
func (f *Fs) String() string {
	if f.opt.ShareID != "" {
		return fmt.Sprintf("Cloudreve share %s root '%s'", f.opt.ShareID, f.root)
	}
	return fmt.Sprintf("Cloudreve root '%s'", f.root)
}

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features {
	return f.features
}

// Precision of the modification times
//
// Cloudreve keeps the last_modified time given at upload, but the
// database may only store whole seconds.
func (f *Fs) Precision() time.Duration {
	return time.Second
}

// Hashes returns the supported hash sets.
func (f *Fs) Hashes() hash.Set {
	return hash.Set(hash.None)
}

// siteRoot normalises the configured site URL
func siteRoot(raw string) (string, error) {
	raw = rest.CanonicalRoot(raw)
	if raw == "" {
		return "", errors.New("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid url %q", raw)
	}
	return raw, nil
}

// errorHandler decodes the envelope of a non 2xx response
func errorHandler(resp *http.Response) error {
	body, _ := rest.ReadBody(resp)
	var r api.Response
	if json.Unmarshal(body, &r) == nil && r.Code != 0 {
		return r.Err()
	}
	return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

// retryErrorCodes is a slice of error codes that we will retry
var retryErrorCodes = []int{
	429, // Too Many Requests
	500, // Internal Server Error
	502, // Bad Gateway
	503, // Service Unavailable
	504, // Gateway Timeout
	509, // Bandwidth Limit Exceeded
}

// shouldRetry returns a boolean as to whether this resp and err
// deserve to be retried. It returns the err as a convenience
func (f *Fs) shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	return fserrors.ShouldRetry(err) || fserrors.ShouldRetryHTTP(resp, retryErrorCodes), err
}

// call makes an API call, decoding the envelope into out (which may
// be nil) and returning an *api.Error for a non zero code.
func (f *Fs) call(ctx context.Context, srv *rest.Client, opts *rest.Opts, in any, out any) error {
	var r api.Response
	authed := srv == f.srv && f.ts != nil
	err := f.pacer.Call(func() (bool, error) {
		r = api.Response{}
		var used string
		if authed {
			used = f.ts.accessToken()
		}
		resp, err := srv.CallJSON(ctx, opts, in, &r)
		if err == nil {
			err = r.Err()
		}
		switch apiCode(err) {
		case api.CodeCheckLogin:
			// A revoked or early expired access token: refresh and
			// retry. Only for the authorised client, since the token
			// source itself calls the unauthorised one with its lock held.
			if authed && f.ts.invalidate(used) {
				return true, err
			}
		case api.CodeLockConflict, api.CodeDBError:
			// Another operation holds the file, or a busy database
			return true, err
		}
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return classifyError(err)
	}
	if out != nil && len(r.Data) > 0 && string(r.Data) != "null" {
		if err := json.Unmarshal(r.Data, out); err != nil {
			return fmt.Errorf("failed to decode %s response: %w", opts.Path, err)
		}
	}
	return nil
}

// classifyError marks errors which retrying can't fix
func classifyError(err error) error {
	switch apiCode(err) {
	case api.CodeInsufficientCapacity:
		return fserrors.FatalError(err)
	case api.CodeFileTooLarge, api.CodeIllegalObjectName, api.CodePolicyNotAllowed:
		return fserrors.NoRetryError(err)
	}
	return err
}

// apiCode returns the Cloudreve error code in err or 0
func apiCode(err error) int {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return 0
}

// isNotFound reports whether err means the path doesn't exist
func isNotFound(err error) bool {
	switch apiCode(err) {
	case api.CodeNotFound, api.CodeFileNotFound, api.CodeParentNotExist:
		return true
	}
	return false
}

// newRestClients makes the API clients for f
func (f *Fs) newRestClients(ctx context.Context, authClient *http.Client) {
	plainClient := fshttp.NewClient(ctx)
	f.unauth = rest.NewClient(plainClient).SetRoot(f.siteURL + apiPrefix).SetErrorHandler(errorHandler)
	f.plain = rest.NewClient(plainClient).SetErrorHandler(errorHandler)
	if authClient == nil {
		authClient = plainClient
	}
	f.srv = rest.NewClient(authClient).SetRoot(f.siteURL + apiPrefix).SetErrorHandler(errorHandler)
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	if err := configstruct.Set(m, opt); err != nil {
		return nil, err
	}
	site, err := siteRoot(opt.URL)
	if err != nil {
		return nil, err
	}
	if opt.Password != "" {
		if opt.Password, err = obscure.Reveal(opt.Password); err != nil {
			return nil, fmt.Errorf("couldn't decrypt password: %w", err)
		}
	}
	if opt.SharePassword != "" {
		if opt.SharePassword, err = obscure.Reveal(opt.SharePassword); err != nil {
			return nil, fmt.Errorf("couldn't decrypt share password: %w", err)
		}
	}

	f := &Fs{
		name:    name,
		root:    strings.Trim(root, "/"),
		opt:     *opt,
		m:       m,
		siteURL: site,
		pacer:   fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
	}
	f.newRestClients(ctx, nil)

	// Log in unless we are browsing a share anonymously
	tok, _ := oauthutil.GetToken(name, m)
	if opt.ShareID == "" || tok != nil || opt.Email != "" {
		// Tokens outlive the context which made the Fs
		f.ts = &tokenSource{f: f, ctx: context.WithoutCancel(ctx), token: tok}
		if tok == nil {
			if opt.Email == "" || opt.Password == "" {
				return nil, fmt.Errorf("not logged in: run \"rclone config reconnect %s:\" or set email and password", name)
			}
			if _, err := f.ts.login(ctx); err != nil {
				return nil, err
			}
		}
		authClient := &http.Client{Transport: &oauth2.Transport{
			Source: f.ts,
			Base:   fshttp.NewTransport(ctx),
		}}
		f.newRestClients(ctx, authClient)
	}

	f.features = (&fs.Features{
		CanHaveEmptyDirectories: true,
		ReadMimeType:            false,
	}).Fill(ctx, f)
	if opt.ShareID != "" {
		f.features.Copy = nil
		f.features.Move = nil
		f.features.DirMove = nil
		f.features.Purge = nil
		f.features.PublicLink = nil
		f.features.About = nil
	}

	// See if the root is a file
	if f.root != "" {
		file, err := f.getInfo(ctx, "")
		if err == nil && !file.IsDir() {
			f.root = path.Dir(f.root)
			if f.root == "." {
				f.root = ""
			}
			return f, fs.ErrorIsFile
		}
	}
	return f, nil
}

// ------------------------------------------------------------
// Authentication

// tokenSource refreshes Cloudreve tokens and saves them to the config.
//
// Cloudreve refreshes both password and OAuth tokens through its own
// JSON endpoint rather than the OAuth token endpoint.
type tokenSource struct {
	f     *Fs
	ctx   context.Context
	mu    sync.Mutex
	token *oauth2.Token
}

func toOAuth2(t *api.Token) *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		TokenType:    "Bearer",
		Expiry:       t.AccessExpires,
	}
}

// Token returns a valid token, refreshing it if required
func (ts *tokenSource) Token() (*oauth2.Token, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.token.Valid() {
		return ts.token, nil
	}
	// Another rclone process may have refreshed it already
	if tok, err := oauthutil.GetToken(ts.f.name, ts.f.m); err == nil && tok.Valid() &&
		(ts.token == nil || tok.AccessToken != ts.token.AccessToken) {
		ts.token = tok
		return tok, nil
	}
	var err error
	if ts.token != nil && ts.token.RefreshToken != "" {
		var tok *oauth2.Token
		if tok, err = ts.refresh(ts.ctx); err == nil {
			return tok, nil
		}
		fs.Debugf(ts.f, "Token refresh failed: %v", err)
	}
	if ts.f.opt.Email != "" && ts.f.opt.Password != "" {
		return ts.loginLocked(ts.ctx)
	}
	if err == nil {
		err = errors.New("no refresh token")
	}
	return nil, fserrors.FatalError(fmt.Errorf("couldn't refresh token (%w) - try \"rclone config reconnect %s:\"", err, ts.f.name))
}

// accessToken returns the current access token without refreshing it
func (ts *tokenSource) accessToken() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.token == nil {
		return ""
	}
	return ts.token.AccessToken
}

// invalidate forgets the access token used, which the server refused,
// so the next request refreshes it. A token another request already
// replaced is left alone. It returns false if there is no way to get
// a new one.
func (ts *tokenSource) invalidate(used string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.token == nil || (ts.token.RefreshToken == "" && ts.f.opt.Password == "") {
		return false
	}
	if ts.token.AccessToken == used {
		expired := *ts.token
		expired.Expiry = time.Unix(1, 0)
		ts.token = &expired
	}
	return true
}

// save stores a new token in the config
func (ts *tokenSource) save(tok *oauth2.Token) (*oauth2.Token, error) {
	ts.token = tok
	if err := oauthutil.PutToken(ts.f.name, ts.f.m, tok, false); err != nil {
		return nil, fmt.Errorf("couldn't save token: %w", err)
	}
	return tok, nil
}

// refresh gets a new token pair with the refresh token
func (ts *tokenSource) refresh(ctx context.Context) (*oauth2.Token, error) {
	opts := rest.Opts{Method: "POST", Path: "/session/token/refresh"}
	var tok api.Token
	err := ts.f.call(ctx, ts.f.unauth, &opts, &api.RefreshRequest{RefreshToken: ts.token.RefreshToken}, &tok)
	if err != nil {
		return nil, err
	}
	return ts.save(toOAuth2(&tok))
}

// login logs in with the configured email and password
func (ts *tokenSource) login(ctx context.Context) (*oauth2.Token, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.loginLocked(ctx)
}

func (ts *tokenSource) loginLocked(ctx context.Context) (*oauth2.Token, error) {
	tok, twoFASession, err := passwordLogin(ctx, ts.f, ts.f.opt.Email, ts.f.opt.Password)
	if err != nil {
		return nil, err
	}
	if twoFASession != "" {
		return nil, fserrors.FatalError(fmt.Errorf("account needs a 2FA code: run \"rclone config reconnect %s:\"", ts.f.name))
	}
	return ts.save(tok)
}

// passwordLogin logs in with email and password. If the account uses
// 2FA it returns the 2FA session ID instead of a token.
func passwordLogin(ctx context.Context, f *Fs, email, password string) (tok *oauth2.Token, twoFASession string, err error) {
	opts := rest.Opts{Method: "POST", Path: "/session/token"}
	var r api.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err := f.unauth.CallJSON(ctx, &opts, &api.LoginRequest{Email: email, Password: password}, &r)
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, "", err
	}
	switch r.Code {
	case api.CodeOK:
		var lr api.LoginResponse
		if err := json.Unmarshal(r.Data, &lr); err != nil {
			return nil, "", fmt.Errorf("failed to decode login response: %w", err)
		}
		return toOAuth2(&lr.Token), "", nil
	case api.CodeNotFullySuccess:
		if err := json.Unmarshal(r.Data, &twoFASession); err != nil || twoFASession == "" {
			return nil, "", fmt.Errorf("failed to decode 2FA session: %s", r.Data)
		}
		return nil, twoFASession, nil
	case api.CodeCaptchaError:
		return nil, "", fserrors.FatalError(errors.New("the site requires a captcha to log in: use an OAuth app instead"))
	}
	return nil, "", fserrors.FatalError(fmt.Errorf("login failed: %w", &api.Error{Code: r.Code, Msg: r.Msg}))
}

// verify2FA completes a password login with an OTP code
func verify2FA(ctx context.Context, f *Fs, session, otp string) (*oauth2.Token, error) {
	opts := rest.Opts{Method: "POST", Path: "/session/token/2fa"}
	var lr api.LoginResponse
	if err := f.call(ctx, f.unauth, &opts, &api.TwoFARequest{OTP: strings.TrimSpace(otp), SessionID: session}, &lr); err != nil {
		return nil, err
	}
	return toOAuth2(&lr.Token), nil
}

// oauthConfig is the OAuth app configuration for the site
func oauthConfig(site string) *oauthutil.Config {
	return &oauthutil.Config{
		AuthURL:     site + "/session/authorize",
		TokenURL:    site + apiPrefix + "/session/oauth/token",
		Scopes:      oauthScopes,
		RedirectURL: oauthutil.RedirectLocalhostURL,
		AuthStyle:   oauth2.AuthStyleInParams,
		UsePKCE:     true,
	}
}

// state2FA prefixes the config state asking for a 2FA code, which
// carries the session of the login in progress
const state2FA = "2fa:"

// configure logs in during rclone config
func configure(ctx context.Context, name string, m configmap.Mapper, in fs.ConfigIn) (*fs.ConfigOut, error) {
	rawURL, _ := m.Get("url")
	site, err := siteRoot(rawURL)
	if err != nil {
		return nil, err
	}
	f := &Fs{
		name:    name,
		m:       m,
		siteURL: site,
		pacer:   fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
	}
	f.newRestClients(ctx, nil)

	if session, ok := strings.CutPrefix(in.State, state2FA); ok {
		tok, err := verify2FA(ctx, f, session, in.Result)
		if err != nil {
			return nil, err
		}
		return nil, oauthutil.PutToken(name, m, tok, true)
	}
	switch in.State {
	case "":
		if shareID, _ := m.Get("share_id"); shareID != "" {
			if email, _ := m.Get("email"); email == "" {
				return nil, nil // anonymous share access
			}
		}
		email, _ := m.Get("email")
		password, _ := m.Get("password")
		if email != "" && password != "" {
			password, err = obscure.Reveal(password)
			if err != nil {
				return nil, fmt.Errorf("couldn't decrypt password: %w", err)
			}
			tok, session, err := passwordLogin(ctx, f, email, password)
			if err != nil {
				return nil, err
			}
			if session != "" {
				return fs.ConfigInput(state2FA+session, "config_2fa", "Two-factor authentication code")
			}
			return nil, oauthutil.PutToken(name, m, tok, true)
		}
		clientID, _ := m.Get(config.ConfigClientID)
		if clientID == "" {
			return nil, errors.New("set email and password, or the client_id and client_secret of a Cloudreve OAuth app")
		}
		return oauthutil.ConfigOut("", &oauthutil.Options{
			OAuth2Config: oauthConfig(site),
			NoOffline:    true,
		})
	}
	return nil, fmt.Errorf("unknown state %q", in.State)
}

// ------------------------------------------------------------
// Paths

// baseURI is the file system URI of the remote
func (f *Fs) baseURI() string {
	if f.opt.ShareID == "" {
		return uriScheme + "my"
	}
	user := url.PathEscape(f.opt.ShareID)
	if f.opt.SharePassword != "" {
		user += ":" + url.PathEscape(f.opt.SharePassword)
	}
	return uriScheme + user + "@share"
}

// uri returns the Cloudreve URI of remote under the root
func (f *Fs) uri(remote string) string {
	full := path.Join(f.root, remote)
	if full == "" || full == "." {
		return f.baseURI()
	}
	segments := strings.Split(full, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(f.opt.Enc.FromStandardName(s))
	}
	return f.baseURI() + "/" + strings.Join(segments, "/")
}

// checkWritable returns an error in read only share mode
func (f *Fs) checkWritable() error {
	if f.opt.ShareID != "" {
		return errors.New("cloudreve: share links are read only")
	}
	return nil
}

// ------------------------------------------------------------
// Files and directories

// getInfo reads the metadata of remote
func (f *Fs) getInfo(ctx context.Context, remote string) (*api.File, error) {
	opts := rest.Opts{
		Method:     "GET",
		Path:       "/file/info",
		Parameters: url.Values{"uri": {f.uri(remote)}},
	}
	var file api.File
	if err := f.call(ctx, f.srv, &opts, nil, &file); err != nil {
		return nil, err
	}
	return &file, nil
}

// isPlaceholder reports whether file is a new file still being
// uploaded, which has no content yet. A file being replaced keeps its
// old content until the upload finishes so isn't one.
func isPlaceholder(file *api.File) bool {
	return !file.IsDir() && file.Size == 0 && file.Metadata[api.MetaUploadSessionID] != ""
}

// listAll calls fn for every entry in dir, stopping early if fn returns true
func (f *Fs) listAll(ctx context.Context, dir string, fn func(*api.File) bool) error {
	params := url.Values{
		"uri":       {f.uri(dir)},
		"page_size": {strconv.Itoa(listPageSize)},
	}
	seen := 0
	for page := 1; ; page++ {
		opts := rest.Opts{Method: "GET", Path: "/file", Parameters: params}
		var r api.ListResponse
		if err := f.call(ctx, f.srv, &opts, nil, &r); err != nil {
			return err
		}
		if r.SingleFileView {
			return fs.ErrorDirNotFound // dir is a file
		}
		for i := range r.Files {
			if isPlaceholder(&r.Files[i]) {
				continue
			}
			if fn(&r.Files[i]) {
				return nil
			}
		}
		seen += len(r.Files)
		// Sites page by cursor by default, or optionally by number. The
		// server may also use a smaller page than the one asked for.
		p := r.Pagination
		switch {
		case p == nil:
			return nil
		case p.NextPageToken != "":
			if p.NextPageToken == params.Get("next_page_token") {
				return nil // no progress
			}
			params.Set("next_page_token", p.NextPageToken)
		case !p.IsCursor && len(r.Files) > 0 && p.TotalItems > seen:
			params.Set("page", strconv.Itoa(page))
		default:
			return nil
		}
	}
}

// List the objects and directories in dir into entries.
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	return list.WithListP(ctx, dir, f)
}

// ListP lists the objects and directories of the Fs starting
// from dir non recursively into out.
func (f *Fs) ListP(ctx context.Context, dir string, callback fs.ListRCallback) error {
	lh := list.NewHelper(callback)
	var iErr error
	err := f.listAll(ctx, dir, func(file *api.File) bool {
		remote := path.Join(dir, f.opt.Enc.ToStandardName(file.Name))
		if file.IsDir() {
			iErr = lh.Add(fs.NewDir(remote, file.UpdatedAt).SetID(file.ID))
		} else {
			iErr = lh.Add(f.newObject(remote, file))
		}
		return iErr != nil
	})
	if err != nil {
		if isNotFound(err) {
			return fs.ErrorDirNotFound
		}
		return err
	}
	if iErr != nil {
		return iErr
	}
	return lh.Flush()
}

func (f *Fs) newObject(remote string, file *api.File) *Object {
	return &Object{
		fs:      f,
		remote:  remote,
		id:      file.ID,
		entity:  file.PrimaryEntity,
		size:    file.Size,
		modTime: fileModTime(file),
	}
}

// NewObject finds the Object at remote.
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	file, err := f.getInfo(ctx, remote)
	if err != nil {
		if isNotFound(err) {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	if file.IsDir() {
		return nil, fs.ErrorIsDir
	}
	if isPlaceholder(file) {
		return nil, fs.ErrorObjectNotFound
	}
	return f.newObject(remote, file), nil
}

// Put the object into the container
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	if err := f.checkWritable(); err != nil {
		return nil, err
	}
	o := &Object{fs: f, remote: src.Remote()}
	return o, o.Update(ctx, in, src, options...)
}

// mkdir makes the directory dir
func (f *Fs) mkdir(ctx context.Context, dir string) error {
	opts := rest.Opts{Method: "POST", Path: "/file/create"}
	return f.call(ctx, f.srv, &opts, &api.CreateFileRequest{URI: f.uri(dir), Type: "folder"}, nil)
}

// Mkdir creates the container if it doesn't exist
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if f.root == "" && dir == "" {
		return nil
	}
	return f.mkdir(ctx, dir)
}

// delete removes the files or folders at uris
func (f *Fs) delete(ctx context.Context, uris ...string) error {
	opts := rest.Opts{Method: "DELETE", Path: "/file"}
	return f.call(ctx, f.srv, &opts, &api.DeleteRequest{URIs: uris, SkipSoftDelete: f.opt.HardDelete}, nil)
}

// Rmdir deletes the root folder
//
// Returns an error if it isn't empty
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	file, err := f.getInfo(ctx, dir)
	if err != nil {
		if isNotFound(err) {
			return fs.ErrorDirNotFound
		}
		return err
	}
	if !file.IsDir() {
		return fs.ErrorIsFile
	}
	empty := true
	err = f.listAll(ctx, dir, func(*api.File) bool {
		empty = false
		return true
	})
	if err != nil {
		return err
	}
	if !empty {
		return fs.ErrorDirectoryNotEmpty
	}
	if f.root == "" && dir == "" {
		return nil // can't remove the root of the file system
	}
	return f.delete(ctx, f.uri(dir))
}

// Purge deletes all the files and the container
func (f *Fs) Purge(ctx context.Context, dir string) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if f.root == "" && dir == "" {
		return errors.New("can't purge the root of the file system")
	}
	err := f.delete(ctx, f.uri(dir))
	if isNotFound(err) {
		return fs.ErrorDirNotFound
	}
	return err
}

// rename renames the file or folder at uri to the leaf newName
func (f *Fs) rename(ctx context.Context, uri, newName string) error {
	opts := rest.Opts{Method: "POST", Path: "/file/rename"}
	return f.call(ctx, f.srv, &opts, &api.RenameRequest{URI: uri, NewName: f.opt.Enc.FromStandardName(newName)}, nil)
}

// relocate moves or copies srcRemote of srcFs to dstRemote of f,
// renaming as needed.
func (f *Fs) relocate(ctx context.Context, srcFs *Fs, srcRemote, dstRemote string, copy bool) error {
	srcPath := path.Join(srcFs.root, srcRemote)
	dstPath := path.Join(f.root, dstRemote)
	srcDir, srcLeaf := path.Split(srcPath)
	dstDir, dstLeaf := path.Split(dstPath)
	srcURI := srcFs.uri(srcRemote)
	if srcDir == dstDir {
		if copy {
			return fs.ErrorCantCopy // copying into the same folder would clash
		}
		return f.rename(ctx, srcURI, dstLeaf)
	}
	// Make sure the destination folder exists
	dstParent := strings.TrimSuffix(dstDir, "/")
	if dstParent != "" {
		if err := f.mkdirAbs(ctx, dstParent); err != nil {
			return err
		}
	}
	move := func(uri string) error {
		opts := rest.Opts{Method: "POST", Path: "/file/move"}
		return f.call(ctx, f.srv, &opts, &api.MoveRequest{URIs: []string{uri}, Dst: f.absURI(dstParent), Copy: copy}, nil)
	}
	// Move keeps the name, so move then rename, unless the
	// destination already has something with the source name.
	err := move(srcURI)
	if err == nil {
		if srcLeaf == dstLeaf {
			return nil
		}
		arrived := f.absURI(path.Join(dstParent, srcLeaf))
		err = f.rename(ctx, arrived, dstLeaf)
		if err != nil && copy {
			if delErr := f.delete(ctx, arrived); delErr != nil {
				fs.Debugf(f, "Failed to remove the partial copy %q: %v", arrived, delErr)
			}
			if apiCode(err) == api.CodeObjectExist {
				return fs.ErrorCantCopy
			}
		}
		return err
	}
	if apiCode(err) != api.CodeObjectExist {
		return err
	}
	// The server can't copy over a file, so rclone uploads a new version
	if copy {
		return fs.ErrorCantCopy
	}
	if srcLeaf == dstLeaf {
		return err
	}
	// Rename then move, through a temporary name if the source
	// folder already has something with the destination name.
	srcParent := strings.TrimSuffix(srcDir, "/")
	err = f.rename(ctx, srcURI, dstLeaf)
	if err == nil {
		return move(f.absURI(path.Join(srcParent, dstLeaf)))
	}
	if apiCode(err) != api.CodeObjectExist {
		return err
	}
	tmpLeaf := fmt.Sprintf(".rclone-move-%d-%s", time.Now().UnixNano(), dstLeaf)
	if err = f.rename(ctx, srcURI, tmpLeaf); err != nil {
		return err
	}
	if err = move(f.absURI(path.Join(srcParent, tmpLeaf))); err != nil {
		return err
	}
	return f.rename(ctx, f.absURI(path.Join(dstParent, tmpLeaf)), dstLeaf)
}

// absURI returns the URI of p, a path from the file system root
func (f *Fs) absURI(p string) string {
	g := *f
	g.root = ""
	return g.uri(p)
}

// mkdirAbs makes p, a path from the file system root
func (f *Fs) mkdirAbs(ctx context.Context, p string) error {
	opts := rest.Opts{Method: "POST", Path: "/file/create"}
	return f.call(ctx, f.srv, &opts, &api.CreateFileRequest{URI: f.absURI(p), Type: "folder"}, nil)
}

// Copy src to this remote using server-side copy operations.
func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok || srcObj.fs.siteURL != f.siteURL || srcObj.fs.opt.ShareID != "" {
		return nil, fs.ErrorCantCopy
	}
	if err := f.relocate(ctx, srcObj.fs, srcObj.remote, remote, true); err != nil {
		return nil, err
	}
	return f.relocated(ctx, remote, srcObj.modTime)
}

// Move src to this remote using server-side move operations.
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok || srcObj.fs.siteURL != f.siteURL || srcObj.fs.opt.ShareID != "" {
		return nil, fs.ErrorCantMove
	}
	if err := f.relocate(ctx, srcObj.fs, srcObj.remote, remote, false); err != nil {
		return nil, err
	}
	return f.relocated(ctx, remote, srcObj.modTime)
}

// relocated returns the file moved or copied to remote, giving it back
// modTime as the server resets it. The file has arrived by now, so
// failing to do that isn't an error.
func (f *Fs) relocated(ctx context.Context, remote string, modTime time.Time) (fs.Object, error) {
	obj, err := f.NewObject(ctx, remote)
	if err != nil {
		return nil, err
	}
	o := obj.(*Object)
	if !o.modTime.Equal(modTime) {
		if err := f.patchModTime(ctx, f.uri(remote), o.entity, modTime); err != nil {
			fs.Logf(o, "Failed to keep the modification time: %v", err)
			return o, nil
		}
		o.modTime = modTime
	}
	return o, nil
}

// DirMove moves src, srcRemote to this remote at dstRemote
// using server-side move operations.
func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	srcFs, ok := src.(*Fs)
	if !ok || srcFs.siteURL != f.siteURL || srcFs.opt.ShareID != "" {
		return fs.ErrorCantDirMove
	}
	if _, err := f.getInfo(ctx, dstRemote); err == nil {
		return fs.ErrorDirExists
	} else if !isNotFound(err) {
		return err
	}
	return f.relocate(ctx, srcFs, srcRemote, dstRemote, false)
}

// About gets quota information
func (f *Fs) About(ctx context.Context) (*fs.Usage, error) {
	opts := rest.Opts{Method: "GET", Path: "/user/capacity"}
	var c api.Capacity
	if err := f.call(ctx, f.srv, &opts, nil, &c); err != nil {
		return nil, err
	}
	return &fs.Usage{
		Total: fs.NewUsageValue(c.Total),
		Used:  fs.NewUsageValue(c.Used),
		Free:  fs.NewUsageValue(max(c.Total-c.Used, 0)),
	}, nil
}

// PublicLink generates a share link for remote
func (f *Fs) PublicLink(ctx context.Context, remote string, expire fs.Duration, unlink bool) (string, error) {
	if unlink {
		return "", errors.New("cloudreve: removing share links isn't supported, delete them on the web")
	}
	req := &api.ShareCreateRequest{URI: f.uri(remote)}
	if expire < fs.DurationOff {
		req.Expire = int(time.Duration(expire).Seconds())
	}
	opts := rest.Opts{Method: "PUT", Path: "/share"}
	var link string
	if err := f.call(ctx, f.srv, &opts, req, &link); err != nil {
		if isNotFound(err) {
			return "", fs.ErrorObjectNotFound
		}
		return "", err
	}
	return link, nil
}

var commandHelp = []fs.CommandHelp{{
	Name:  "policy",
	Short: "Show the storage policy of a folder.",
	Long: `This shows the storage policy uploads to a folder go to, whose ID
is what the policy_id option takes.

    rclone backend policy remote:
    rclone backend policy remote: path/to/folder`,
}}

// Command runs a backend specific command
func (f *Fs) Command(ctx context.Context, name string, arg []string, opt map[string]string) (any, error) {
	switch name {
	case "policy":
		dir := ""
		if len(arg) > 0 {
			dir = arg[0]
		}
		opts := rest.Opts{
			Method:     "GET",
			Path:       "/file",
			Parameters: url.Values{"uri": {f.uri(dir)}, "page_size": {"1"}},
		}
		var r api.ListResponse
		if err := f.call(ctx, f.srv, &opts, nil, &r); err != nil {
			return nil, err
		}
		if r.StoragePolicy == nil {
			return nil, errors.New("cloudreve: the server didn't report a storage policy")
		}
		return r.StoragePolicy, nil
	}
	return nil, fs.ErrorCommandNotFound
}

// ------------------------------------------------------------
// Object

// Fs returns the parent Fs
func (o *Object) Fs() fs.Info {
	return o.fs
}

// String returns a description of the Object
func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

// Remote returns the remote path
func (o *Object) Remote() string {
	return o.remote
}

// Hash returns the hash of an object
func (o *Object) Hash(ctx context.Context, t hash.Type) (string, error) {
	return "", hash.ErrUnsupported
}

// Size returns the size of an object in bytes
func (o *Object) Size() int64 {
	return o.size
}

// ModTime returns the modification time of the object
func (o *Object) ModTime(ctx context.Context) time.Time {
	return o.modTime
}

// SetModTime sets the modification time of the object
func (o *Object) SetModTime(ctx context.Context, modTime time.Time) error {
	if err := o.fs.checkWritable(); err != nil {
		return err
	}
	if err := o.fs.patchModTime(ctx, o.fs.uri(o.remote), o.entity, modTime); err != nil {
		if apiCode(err) == api.CodeParamErr {
			return fs.ErrorCantSetModTime
		}
		return err
	}
	o.modTime = modTime
	return nil
}

// Storable returns a boolean showing whether this object storable
func (o *Object) Storable() bool {
	return true
}

// ID returns the ID of the Object if known, or "" if not
func (o *Object) ID() string {
	return o.id
}

// Open an object for read
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	opts := rest.Opts{Method: "POST", Path: "/file/url"}
	var r api.FileURLResponse
	if err := o.fs.call(ctx, o.fs.srv, &opts, &api.FileURLRequest{URIs: []string{o.fs.uri(o.remote)}, Download: true}, &r); err != nil {
		if isNotFound(err) {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	if len(r.URLs) == 0 || r.URLs[0].URL == "" {
		return nil, errors.New("cloudreve: no download URL returned")
	}
	dl, err := o.fs.resolve(r.URLs[0].URL)
	if err != nil {
		return nil, err
	}
	fs.FixRangeOption(options, o.size)
	var resp *http.Response
	dlOpts := rest.Opts{Method: "GET", RootURL: dl, Options: options}
	err = o.fs.pacer.Call(func() (bool, error) {
		resp, err = o.fs.plain.Call(ctx, &dlOpts)
		return o.fs.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// resolve makes a possibly site relative URL absolute
func (f *Fs) resolve(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("cloudreve: bad URL %q: %w", raw, err)
	}
	if u.IsAbs() {
		return raw, nil
	}
	base, err := url.Parse(f.siteURL + "/")
	if err != nil {
		return "", err
	}
	return base.ResolveReference(u).String(), nil
}

// Update the object with the contents of the io.Reader, modTime and size
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	if err := o.fs.checkWritable(); err != nil {
		return err
	}
	file, err := o.fs.upload(ctx, in, o.remote, src.Size(), src.ModTime(ctx), o.id != "", options...)
	if err != nil {
		return err
	}
	*o = *o.fs.newObject(o.remote, file)
	return nil
}

// Remove an object
func (o *Object) Remove(ctx context.Context) error {
	if err := o.fs.checkWritable(); err != nil {
		return err
	}
	err := o.fs.delete(ctx, o.fs.uri(o.remote))
	if isNotFound(err) {
		return fs.ErrorObjectNotFound
	}
	return err
}

// Check the interfaces are satisfied
var (
	_ fs.Fs           = (*Fs)(nil)
	_ fs.Copier       = (*Fs)(nil)
	_ fs.Mover        = (*Fs)(nil)
	_ fs.DirMover     = (*Fs)(nil)
	_ fs.Purger       = (*Fs)(nil)
	_ fs.Abouter      = (*Fs)(nil)
	_ fs.PublicLinker = (*Fs)(nil)
	_ fs.ListPer      = (*Fs)(nil)
	_ fs.Commander    = (*Fs)(nil)
	_ fs.Object       = (*Object)(nil)
	_ fs.IDer         = (*Object)(nil)
)
