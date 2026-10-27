// Package baidu provides an interface to Baidu Netdisk (百度网盘).
package baidu

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

	"github.com/rclone/rclone/backend/baidu/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/dircache"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
)

const (
	defaultMinSleep = 400 * time.Millisecond
	maxSleep        = 5 * time.Second
	decayConstant   = 2
	panRootURL      = "https://pan.baidu.com"
	pcsRootURL      = "https://d.pcs.baidu.com"
	oauthTokenURL   = "https://openapi.baidu.com/oauth/2.0/token"
	webAppID        = "250528"
	webChannel      = "chunlei"
	defaultListPage = 1000
	rootID          = "/"
	openAPIUA       = "pan.baidu.com"
	webUA           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "baidu",
		Description: "Baidu Netdisk (百度网盘)",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "cookie",
			Help: `Baidu Netdisk web cookie string.

The easier login: sign in at https://pan.baidu.com in a browser, then
copy BDUSS and STOKEN from DevTools → Application → Cookies. Paste
them as one string, for example:

    BDUSS=...; STOKEN=...

BDUSS is required for cookie login. STOKEN is recommended for upload
and delete. Cookies expire; copy a fresh pair if rclone starts
returning login errors.

Alternatively set access_token (and optionally refresh_token with
client_id / client_secret) from the Baidu Netdisk open platform:
https://pan.baidu.com/union/doc
`,
			Sensitive: true,
		}, {
			Name:      "bduss",
			Help:      "BDUSS cookie. Optional if cookie is set.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:      "stoken",
			Help:      "STOKEN cookie. Optional if cookie is set.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:      "access_token",
			Help:      "Open platform access_token. Used instead of cookie when set.",
			Sensitive: true,
		}, {
			Name:      "refresh_token",
			Help:      "Open platform refresh_token. Used with client_id and client_secret to renew access_token.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:      "client_id",
			Help:      "Open platform AppKey (client_id). Required to refresh access_token.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:      "client_secret",
			Help:      "Open platform SecretKey (client_secret). Required to refresh access_token.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:     "root_folder_path",
			Help:     "Restrict rclone to this absolute netdisk path, for example /backup. Leave blank for the account root.",
			Advanced: true,
		}, {
			Name:     "list_chunk",
			Help:     "Number of items to list in each call.",
			Default:  defaultListPage,
			Advanced: true,
		}, {
			Name:     "pacer_min_sleep",
			Help:     "Minimum time to sleep between API calls.",
			Default:  fs.Duration(defaultMinSleep),
			Advanced: true,
		}, {
			Name:     "download_user_agent",
			Help:     "User-Agent sent when downloading. Open API dlinks often require pan.baidu.com.",
			Advanced: true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default: encoder.Display |
				encoder.EncodeLeftSpace |
				encoder.EncodeRightSpace |
				encoder.EncodeBackSlash |
				encoder.EncodeColon |
				encoder.EncodeAsterisk |
				encoder.EncodeQuestion |
				encoder.EncodeDoubleQuote |
				encoder.EncodeLtGt |
				encoder.EncodePipe |
				encoder.EncodePercent |
				encoder.EncodeInvalidUtf8,
		}},
	})
}

// Options defines the configuration of this backend.
type Options struct {
	Cookie            string               `config:"cookie"`
	BDUSS             string               `config:"bduss"`
	STOKEN            string               `config:"stoken"`
	AccessToken       string               `config:"access_token"`
	RefreshToken      string               `config:"refresh_token"`
	ClientID          string               `config:"client_id"`
	ClientSecret      string               `config:"client_secret"`
	RootFolderPath    string               `config:"root_folder_path"`
	ListChunk         int                  `config:"list_chunk"`
	PacerMinSleep     fs.Duration          `config:"pacer_min_sleep"`
	DownloadUserAgent string               `config:"download_user_agent"`
	Enc               encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a Baidu Netdisk remote.
type Fs struct {
	name     string
	root     string
	opt      Options
	features *fs.Features
	srv      *rest.Client
	pcs      *rest.Client
	pacer    *fs.Pacer
	dirCache *dircache.DirCache
	m        configmap.Mapper
	mu       *sync.Mutex
	bduss    string
	stoken   string
	trueRoot string
	bdstoken string
}

// Object describes a Baidu Netdisk file.
type Object struct {
	fs      *Fs
	remote  string
	id      int64
	path    string
	md5sum  string
	size    int64
	modTime time.Time
}

var retryErrorCodes = []int{429, 500, 502, 503, 504}

func compactValue(v string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		default:
			return r
		}
	}, v)
}

// parseCookies reads BDUSS and STOKEN from a cookie header and/or split fields.
func parseCookies(cookie, bduss, stoken string) (bdussOut, stokenOut string) {
	bdussOut, stokenOut = compactValue(bduss), compactValue(stoken)
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		val = compactValue(val)
		switch strings.ToUpper(name) {
		case "BDUSS", "BDUSS_BFESS":
			if val != "" {
				bdussOut = val
			}
		case "STOKEN", "STOKEN_BFESS":
			if val != "" {
				stokenOut = val
			}
		}
	}
	return bdussOut, stokenOut
}

func parsePath(p string) string {
	return strings.Trim(p, "/")
}

func panJoin(parent, leaf string) string {
	parent = strings.TrimRight(parent, "/")
	if parent == "" {
		parent = "/"
	}
	if parent == "/" {
		return "/" + leaf
	}
	return parent + "/" + leaf
}

func shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		if api.IsLogin(apiErr.Errno) {
			return false, fserrors.FatalError(err)
		}
		if api.IsRetry(apiErr.Errno) {
			return true, err
		}
	}
	return fserrors.ShouldRetry(err) || fserrors.ShouldRetryHTTP(resp, retryErrorCodes), err
}

func errorHandler(resp *http.Response) error {
	body, err := rest.ReadBody(resp)
	if err != nil {
		return fmt.Errorf("error reading error out of body: %w", err)
	}
	var parsed api.Base
	if len(body) > 0 {
		if json.Unmarshal(body, &parsed) == nil && parsed.Errno != 0 {
			if e := parsed.Err(); e != nil {
				return e
			}
		}
	}
	return fmt.Errorf("HTTP error %v (%v) returned body: %q", resp.StatusCode, resp.Status, body)
}

func isHexMD5(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// NewFs constructs an Fs from the path.
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}
	bduss, stoken := parseCookies(opt.Cookie, opt.BDUSS, opt.STOKEN)
	if opt.AccessToken == "" && opt.RefreshToken == "" && bduss == "" {
		return nil, errors.New("baidu: set cookie (BDUSS; STOKEN) or access_token")
	}
	if opt.ListChunk <= 0 {
		opt.ListChunk = defaultListPage
	}
	if opt.PacerMinSleep <= 0 {
		opt.PacerMinSleep = fs.Duration(defaultMinSleep)
	}
	root = parsePath(root)
	trueRootID := rootID
	if strings.Trim(opt.RootFolderPath, "/") != "" {
		trueRootID = "/" + strings.Trim(opt.RootFolderPath, "/")
	}
	client := fshttp.NewClient(ctx)
	f := &Fs{
		name:     name,
		root:     root,
		opt:      *opt,
		srv:      rest.NewClient(client).SetRoot(panRootURL),
		pcs:      rest.NewClient(client).SetRoot(pcsRootURL),
		pacer:    fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(time.Duration(opt.PacerMinSleep)), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		m:        m,
		mu:       new(sync.Mutex),
		bduss:    bduss,
		stoken:   stoken,
		trueRoot: trueRootID,
	}
	f.srv.SetErrorHandler(errorHandler)
	f.pcs.SetErrorHandler(errorHandler)
	ua := webUA
	if f.useOpenAPI() {
		ua = openAPIUA
	}
	if opt.DownloadUserAgent != "" {
		ua = opt.DownloadUserAgent
	} else {
		f.opt.DownloadUserAgent = ua
	}
	f.srv.SetHeader("User-Agent", ua)
	f.srv.SetHeader("Referer", "https://pan.baidu.com/disk/home")
	f.pcs.SetHeader("User-Agent", ua)
	if bduss != "" {
		cookie := "BDUSS=" + bduss
		if stoken != "" {
			cookie += "; STOKEN=" + stoken
		}
		f.srv.SetHeader("Cookie", cookie)
		f.pcs.SetHeader("Cookie", cookie)
	}
	f.features = (&fs.Features{
		CaseInsensitive:         false,
		CanHaveEmptyDirectories: true,
		DuplicateFiles:          false,
		ReadMimeType:            false,
		WriteMimeType:           false,
	}).Fill(ctx, f)
	f.dirCache = dircache.New(root, trueRootID, f)

	if f.useOpenAPI() && f.opt.AccessToken == "" {
		if err := f.refreshToken(ctx); err != nil {
			return nil, err
		}
	}
	if err := f.ping(ctx); err != nil {
		return nil, err
	}

	err = f.dirCache.FindRoot(ctx, false)
	if err != nil {
		newRoot, remote := dircache.SplitPath(root)
		tempF := *f
		tempF.dirCache = dircache.New(newRoot, trueRootID, &tempF)
		tempF.root = newRoot
		err = tempF.dirCache.FindRoot(ctx, false)
		if err != nil {
			return f, nil
		}
		_, err := tempF.newObjectWithInfo(ctx, remote, nil)
		if err != nil {
			if err == fs.ErrorObjectNotFound {
				return f, nil
			}
			return nil, err
		}
		f.features.Fill(ctx, &tempF)
		f.dirCache = tempF.dirCache
		f.root = tempF.root
		return f, fs.ErrorIsFile
	}
	return f, nil
}

func (f *Fs) useOpenAPI() bool {
	return f.opt.AccessToken != "" || f.opt.RefreshToken != ""
}

// writePath is the pan.baidu.com path for a mutating call.
// Cookie login uses the web /api/* endpoints; an open-platform token uses xpan.
func (f *Fs) writePath(method string) string {
	if f.useOpenAPI() {
		return "/rest/2.0/xpan/file"
	}
	switch method {
	case "precreate":
		return "/api/precreate"
	case "create":
		return "/api/create"
	case "filemanager":
		return "/api/filemanager"
	default:
		return "/api/" + method
	}
}

func (f *Fs) ensureBdstoken(ctx context.Context) string {
	if f.useOpenAPI() {
		return ""
	}
	f.mu.Lock()
	if f.bdstoken != "" {
		tok := f.bdstoken
		f.mu.Unlock()
		return tok
	}
	f.mu.Unlock()

	params := f.withAuth(url.Values{
		"fields": {`["bdstoken"]`},
	})
	opts := rest.Opts{
		Method:     http.MethodGet,
		Path:       "/api/gettemplatevariable",
		Parameters: params,
	}
	var out api.TemplateVariableResponse
	if err := f.callJSON(ctx, &opts, nil, &out); err != nil {
		fs.Debugf(f, "bdstoken: %v", err)
		return ""
	}
	if err := out.Err(); err != nil {
		fs.Debugf(f, "bdstoken: %v", err)
		return ""
	}
	tok := out.Bdstoken()
	if tok == "" {
		return ""
	}
	f.mu.Lock()
	f.bdstoken = tok
	f.mu.Unlock()
	return tok
}

func (f *Fs) postMethod(ctx context.Context, method string, extra, form url.Values, resp any) error {
	query := url.Values{}
	for k, vs := range extra {
		query[k] = append([]string{}, vs...)
	}
	if f.useOpenAPI() {
		query.Set("method", method)
	} else if tok := f.ensureBdstoken(ctx); tok != "" {
		query.Set("bdstoken", tok)
	}
	return f.postForm(ctx, f.writePath(method), query, form, resp)
}

func (f *Fs) absRoot() string {
	base := f.trueRoot
	if base == "" {
		base = "/"
	}
	r := strings.Trim(f.root, "/")
	if r == "" {
		return base
	}
	if base == "/" {
		return "/" + r
	}
	return strings.TrimRight(base, "/") + "/" + r
}

func (f *Fs) panPath(remote string) string {
	remote = strings.Trim(remote, "/")
	base := f.absRoot()
	if remote == "" {
		return base
	}
	if base == "/" {
		return "/" + remote
	}
	return strings.TrimRight(base, "/") + "/" + remote
}

func (f *Fs) ping(ctx context.Context) error {
	_, err := f.listDir(ctx, f.trueRoot, 0, 1)
	if err != nil {
		return fmt.Errorf("baidu login check failed: %w (re-copy BDUSS/STOKEN, or refresh access_token)", err)
	}
	return nil
}

func (f *Fs) accessToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opt.AccessToken
}

func (f *Fs) refreshToken(ctx context.Context) error {
	if f.opt.RefreshToken == "" || f.opt.ClientID == "" || f.opt.ClientSecret == "" {
		return errors.New("baidu: access_token expired; set refresh_token, client_id and client_secret")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {f.opt.RefreshToken},
		"client_id":     {f.opt.ClientID},
		"client_secret": {f.opt.ClientSecret},
	}
	opts := rest.Opts{
		Method:      http.MethodPost,
		Path:        "/",
		RootURL:     oauthTokenURL,
		Body:        strings.NewReader(form.Encode()),
		ContentType: "application/x-www-form-urlencoded",
	}
	var out api.TokenResponse
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &out)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if out.Error != "" {
		return fmt.Errorf("baidu token refresh: %s (%s)", out.Error, out.ErrorDesc)
	}
	if out.AccessToken == "" {
		return errors.New("baidu token refresh: empty access_token")
	}
	f.mu.Lock()
	f.opt.AccessToken = out.AccessToken
	if out.RefreshToken != "" {
		f.opt.RefreshToken = out.RefreshToken
	}
	f.mu.Unlock()
	if f.m != nil {
		f.m.Set("access_token", out.AccessToken)
		if out.RefreshToken != "" {
			f.m.Set("refresh_token", out.RefreshToken)
		}
	}
	return nil
}

func (f *Fs) withAuth(params url.Values) url.Values {
	if params == nil {
		params = url.Values{}
	}
	if token := f.accessToken(); token != "" {
		params.Set("access_token", token)
	}
	params.Set("app_id", webAppID)
	params.Set("channel", webChannel)
	params.Set("clienttype", "0")
	params.Set("web", "1")
	return params
}

func (f *Fs) callJSON(ctx context.Context, opts *rest.Opts, req, resp any) error {
	return f.pacer.Call(func() (bool, error) {
		httpResp, err := f.srv.CallJSON(ctx, opts, req, resp)
		if err != nil {
			var apiErr *api.Error
			if errors.As(err, &apiErr) && api.IsLogin(apiErr.Errno) && f.opt.RefreshToken != "" {
				if rerr := f.refreshToken(ctx); rerr == nil {
					if opts.Parameters != nil {
						opts.Parameters.Set("access_token", f.accessToken())
					}
					return true, err
				}
			}
			return shouldRetry(ctx, httpResp, err)
		}
		return false, nil
	})
}

func (f *Fs) listDir(ctx context.Context, dir string, start, limit int) ([]api.Item, error) {
	if dir == "" {
		dir = "/"
	}
	if limit <= 0 {
		limit = f.opt.ListChunk
	}
	params := f.withAuth(url.Values{
		"dir":       {dir},
		"order":     {"name"},
		"desc":      {"0"},
		"showempty": {"1"},
		"folder":    {"0"},
	})
	var opts rest.Opts
	if f.useOpenAPI() {
		params.Set("method", "list")
		params.Set("start", strconv.Itoa(start))
		params.Set("limit", strconv.Itoa(limit))
		opts = rest.Opts{Method: http.MethodGet, Path: "/rest/2.0/xpan/file", Parameters: params}
	} else {
		page := start/limit + 1
		params.Set("page", strconv.Itoa(page))
		params.Set("num", strconv.Itoa(limit))
		opts = rest.Opts{Method: http.MethodGet, Path: "/api/list", Parameters: params}
	}
	var out api.ListResponse
	if err := f.callJSON(ctx, &opts, nil, &out); err != nil {
		return nil, err
	}
	if err := out.Err(); err != nil {
		if api.IsNotFound(out.Errno) {
			return nil, fs.ErrorDirNotFound
		}
		if api.IsLogin(out.Errno) {
			return nil, fserrors.FatalError(err)
		}
		return nil, err
	}
	return out.List, nil
}

func (f *Fs) fileMetas(ctx context.Context, fsid int64, panPath string) (*api.Item, error) {
	params := f.withAuth(url.Values{
		"dlink": {"1"},
	})
	var opts rest.Opts
	if f.useOpenAPI() {
		fsids, err := json.Marshal([]int64{fsid})
		if err != nil {
			return nil, err
		}
		params.Set("method", "filemetas")
		params.Set("fsids", string(fsids))
		opts = rest.Opts{
			Method:     http.MethodGet,
			Path:       "/rest/2.0/xpan/multimedia",
			Parameters: params,
		}
	} else {
		if panPath != "" {
			target, err := json.Marshal([]string{panPath})
			if err != nil {
				return nil, err
			}
			params.Set("target", string(target))
		} else {
			fsids, err := json.Marshal([]int64{fsid})
			if err != nil {
				return nil, err
			}
			params.Set("fsids", string(fsids))
		}
		opts = rest.Opts{
			Method:     http.MethodGet,
			Path:       "/api/filemetas",
			Parameters: params,
		}
	}
	var out api.FileMetasResponse
	if err := f.callJSON(ctx, &opts, nil, &out); err != nil {
		return nil, err
	}
	if err := out.Err(); err != nil {
		if api.IsNotFound(out.Errno) {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	items := out.Items()
	if len(items) == 0 {
		return nil, fs.ErrorObjectNotFound
	}
	return &items[0], nil
}

func (f *Fs) postForm(ctx context.Context, pathStr string, query, form url.Values, resp any) error {
	query = f.withAuth(query)
	opts := rest.Opts{
		Method:      http.MethodPost,
		Path:        pathStr,
		Parameters:  query,
		Body:        strings.NewReader(form.Encode()),
		ContentType: "application/x-www-form-urlencoded",
	}
	return f.callJSON(ctx, &opts, nil, resp)
}

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string {
	return f.name
}

// Root of the remote
func (f *Fs) Root() string {
	return f.root
}

// String converts this Fs to a string
func (f *Fs) String() string {
	return fmt.Sprintf("Baidu Netdisk root '%s'", f.root)
}

// Precision of the ModTimes in this Fs
func (f *Fs) Precision() time.Duration {
	return time.Second
}

// Hashes returns the supported hash types of the filesystem
func (f *Fs) Hashes() hash.Set {
	return hash.NewHashSet(hash.MD5)
}

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features {
	return f.features
}

// FindLeaf finds a directory of name leaf in the folder with ID pathID
func (f *Fs) FindLeaf(ctx context.Context, pathID, leaf string) (string, bool, error) {
	want := f.opt.Enc.FromStandardName(leaf)
	for start := 0; ; start += f.opt.ListChunk {
		items, err := f.listDir(ctx, pathID, start, f.opt.ListChunk)
		if err != nil {
			if err == fs.ErrorDirNotFound {
				return "", false, nil
			}
			return "", false, err
		}
		if len(items) == 0 {
			return "", false, nil
		}
		for _, it := range items {
			if it.IsDir == 0 {
				continue
			}
			if it.Name() == want {
				id := it.Path
				if id == "" {
					id = panJoin(pathID, it.Name())
				}
				return id, true, nil
			}
		}
		if len(items) < f.opt.ListChunk {
			return "", false, nil
		}
	}
}

// CreateDir makes a directory with pathID as parent and name leaf
func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (string, error) {
	name := f.opt.Enc.FromStandardName(leaf)
	dir := panJoin(pathID, name)
	form := url.Values{
		"path":  {dir},
		"isdir": {"1"},
		"rtype": {"0"},
	}
	var out api.CreateResponse
	if err := f.postMethod(ctx, "create", nil, form, &out); err != nil {
		return "", err
	}
	if err := out.Err(); err != nil {
		if out.Errno == -8 || out.Errno == 31061 {
			return dir, nil
		}
		return "", err
	}
	if out.Path != "" {
		return out.Path, nil
	}
	return dir, nil
}

// List the objects and directories in dir into dst
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return nil, err
	}
	for start := 0; ; start += f.opt.ListChunk {
		items, err := f.listDir(ctx, dirID, start, f.opt.ListChunk)
		if err != nil {
			return nil, err
		}
		for i := range items {
			it := &items[i]
			name := f.opt.Enc.ToStandardName(it.Name())
			if name == "" {
				fs.Debugf(dir, "Skipping empty name %q", it.Name())
				continue
			}
			remote := name
			if dir != "" {
				remote = path.Join(dir, name)
			}
			if it.IsDir != 0 {
				id := it.Path
				if id == "" {
					id = panJoin(dirID, it.Name())
				}
				mod := time.Unix(it.ServerMtime, 0)
				d := fs.NewDir(remote, mod).SetID(id)
				f.dirCache.Put(remote, id)
				entries = append(entries, d)
				continue
			}
			o := f.newObjectFromItem(remote, it)
			entries = append(entries, o)
		}
		if len(items) < f.opt.ListChunk {
			break
		}
	}
	return entries, nil
}

func (f *Fs) newObjectFromItem(remote string, it *api.Item) *Object {
	mod := time.Unix(it.ServerMtime, 0)
	if it.ServerMtime == 0 && it.LocalMtime != 0 {
		mod = time.Unix(it.LocalMtime, 0)
	}
	p := it.Path
	if p == "" {
		p = f.panPath(remote)
	}
	return &Object{
		fs:      f,
		remote:  remote,
		id:      it.FsID,
		path:    p,
		md5sum:  it.MD5,
		size:    it.Size,
		modTime: mod,
	}
}

func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, it *api.Item) (fs.Object, error) {
	if it != nil {
		return f.newObjectFromItem(remote, it), nil
	}
	dir, leaf := dircache.SplitPath(remote)
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		if err == fs.ErrorDirNotFound {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	want := f.opt.Enc.FromStandardName(leaf)
	for start := 0; ; start += f.opt.ListChunk {
		items, err := f.listDir(ctx, dirID, start, f.opt.ListChunk)
		if err != nil {
			return nil, err
		}
		for i := range items {
			if items[i].IsDir != 0 {
				continue
			}
			if items[i].Name() == want {
				return f.newObjectFromItem(remote, &items[i]), nil
			}
		}
		if len(items) < f.opt.ListChunk {
			break
		}
	}
	return nil, fs.ErrorObjectNotFound
}

// NewObject finds the Object at remote
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return f.newObjectWithInfo(ctx, remote, nil)
}

// Mkdir creates the directory if it doesn't exist
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	_, err := f.dirCache.FindDir(ctx, dir, true)
	return err
}

func (f *Fs) filemanager(ctx context.Context, opera string, filelist any) error {
	payload, err := json.Marshal(filelist)
	if err != nil {
		return err
	}
	form := url.Values{
		"filelist": {string(payload)},
		"async":    {"0"},
		"onnest":   {"fail"},
	}
	var out api.ManagerResponse
	if err := f.postMethod(ctx, "filemanager", url.Values{"opera": {opera}}, form, &out); err != nil {
		return err
	}
	return out.Err()
}

// DirCacheFlush resets the directory cache.
func (f *Fs) DirCacheFlush() {
	f.dirCache.ResetRoot()
}

// Purge deletes dir and all of its contents
func (f *Fs) Purge(ctx context.Context, dir string) error {
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	if dirID == "/" {
		items, err := f.listAll(ctx, dirID)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		filelist := make([]map[string]string, 0, len(items))
		for _, it := range items {
			p := it.Path
			if p == "" {
				p = panJoin(dirID, it.Name())
			}
			filelist = append(filelist, map[string]string{"path": p})
		}
		if err := f.filemanager(ctx, "delete", filelist); err != nil {
			return err
		}
		f.dirCache.ResetRoot()
		return nil
	}
	if err := f.filemanager(ctx, "delete", []map[string]string{{"path": dirID}}); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

func (f *Fs) listAll(ctx context.Context, dirID string) ([]api.Item, error) {
	var all []api.Item
	for start := 0; ; start += f.opt.ListChunk {
		items, err := f.listDir(ctx, dirID, start, f.opt.ListChunk)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < f.opt.ListChunk {
			return all, nil
		}
	}
}

// Rmdir deletes the directory if it is empty
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	items, err := f.listDir(ctx, dirID, 0, 1)
	if err != nil {
		return err
	}
	if len(items) > 0 {
		return fs.ErrorDirectoryNotEmpty
	}
	if err := f.filemanager(ctx, "delete", []map[string]string{{"path": dirID}}); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

// About gets quota information
func (f *Fs) About(ctx context.Context) (*fs.Usage, error) {
	params := f.withAuth(nil)
	opts := rest.Opts{Method: http.MethodGet, Path: "/api/quota", Parameters: params}
	var out api.QuotaResponse
	if err := f.callJSON(ctx, &opts, nil, &out); err != nil {
		return nil, err
	}
	if err := out.Err(); err != nil {
		return nil, err
	}
	usage := &fs.Usage{}
	if out.Total > 0 {
		usage.Total = fs.NewUsageValue(out.Total)
	}
	if out.Used >= 0 {
		usage.Used = fs.NewUsageValue(out.Used)
	}
	if out.Free >= 0 {
		usage.Free = fs.NewUsageValue(out.Free)
	} else if out.Total > 0 && out.Used >= 0 {
		usage.Free = fs.NewUsageValue(out.Total - out.Used)
	}
	return usage, nil
}

func (f *Fs) moveCopy(ctx context.Context, opera, srcPath, destDir, newName string) error {
	return f.filemanager(ctx, opera, []map[string]string{{
		"path":    srcPath,
		"dest":    destDir,
		"newname": newName,
	}})
}

// Copy src to this remote using server-side copy
func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fs.ErrorCantCopy
	}
	leaf, dirID, err := f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return nil, err
	}
	if err := f.moveCopy(ctx, "copy", srcObj.path, dirID, f.opt.Enc.FromStandardName(leaf)); err != nil {
		return nil, err
	}
	f.dirCache.FlushDir(path.Dir(remote))
	return f.NewObject(ctx, remote)
}

// Move src to this remote using server-side move
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fs.ErrorCantMove
	}
	leaf, dirID, err := f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return nil, err
	}
	if err := f.moveCopy(ctx, "move", srcObj.path, dirID, f.opt.Enc.FromStandardName(leaf)); err != nil {
		return nil, err
	}
	f.dirCache.FlushDir(path.Dir(src.Remote()))
	f.dirCache.FlushDir(path.Dir(remote))
	return f.NewObject(ctx, remote)
}

// DirMove moves src, srcRemote to this remote at dstRemote
func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	srcFs, ok := src.(*Fs)
	if !ok {
		return fs.ErrorCantDirMove
	}
	srcID, _, _, dstDirectoryID, dstLeaf, err := f.dirCache.DirMove(ctx, srcFs.dirCache, srcFs.root, srcRemote, f.root, dstRemote)
	if err != nil {
		return err
	}
	if err := f.moveCopy(ctx, "move", srcID, dstDirectoryID, f.opt.Enc.FromStandardName(dstLeaf)); err != nil {
		return err
	}
	srcFs.dirCache.FlushDir(srcRemote)
	f.dirCache.FlushDir(path.Dir(dstRemote))
	return nil
}

// Put uploads the object
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	existing, err := f.NewObject(ctx, src.Remote())
	if err == nil {
		return existing, existing.(*Object).Update(ctx, in, src, options...)
	}
	if err != fs.ErrorObjectNotFound {
		return nil, err
	}
	return f.putUnchecked(ctx, in, src, options...)
}

// PutStream uploads with unknown size by buffering to a temporary file via Put
func (f *Fs) PutStream(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	return f.Put(ctx, in, src, options...)
}

// Object methods

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

// ModTime returns the modification time
func (o *Object) ModTime(ctx context.Context) time.Time {
	return o.modTime
}

// Size of the object
func (o *Object) Size() int64 {
	return o.size
}

// Hash returns the selected checksum
func (o *Object) Hash(ctx context.Context, t hash.Type) (string, error) {
	if t != hash.MD5 {
		return "", hash.ErrUnsupported
	}
	if isHexMD5(o.md5sum) {
		return strings.ToLower(o.md5sum), nil
	}
	return "", nil
}

// Storable returns whether this object can be stored
func (o *Object) Storable() bool {
	return true
}

// SetModTime is not supported by Baidu Netdisk
func (o *Object) SetModTime(ctx context.Context, t time.Time) error {
	return fs.ErrorCantSetModTime
}

// ID returns the fs_id of the object
func (o *Object) ID() string {
	if o.id == 0 {
		return ""
	}
	return strconv.FormatInt(o.id, 10)
}

// Remove the object
func (o *Object) Remove(ctx context.Context) error {
	if o.path == "" {
		return errors.New("baidu: missing object path")
	}
	return o.fs.filemanager(ctx, "delete", []map[string]string{{"path": o.path}})
}

// PublicLink returns a time-limited direct download URL for a file.
func (f *Fs) PublicLink(ctx context.Context, remote string, expire fs.Duration, unlink bool) (string, error) {
	if unlink {
		return "", errors.New("baidu: download URLs expire on their own; unlink is not supported")
	}
	if _, err := f.dirCache.FindDir(ctx, remote, false); err == nil {
		return "", fs.ErrorCantShareDirectories
	}
	o, err := f.NewObject(ctx, remote)
	if err != nil {
		return "", err
	}
	obj, ok := o.(*Object)
	if !ok {
		return "", fs.ErrorObjectNotFound
	}
	return f.downloadURL(ctx, obj.id, obj.path)
}

// Check the interfaces are satisfied
var (
	_ fs.Fs              = (*Fs)(nil)
	_ fs.Purger          = (*Fs)(nil)
	_ fs.Copier          = (*Fs)(nil)
	_ fs.Mover           = (*Fs)(nil)
	_ fs.DirMover        = (*Fs)(nil)
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.PutStreamer     = (*Fs)(nil)
	_ fs.Abouter         = (*Fs)(nil)
	_ fs.PublicLinker    = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
)
