// Package openlist provides an interface to OpenList and AList servers
package openlist

import (
	"context"
	"encoding/base64"
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

	"github.com/rclone/rclone/backend/openlist/api"
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
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
)

const (
	minSleep      = 10 * time.Millisecond
	maxSleep      = 2 * time.Second
	decayConstant = 2
	taskPollMax   = 2 * time.Second
)

// listPageSize is the number of entries asked for per listing page
var listPageSize = 1000

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "openlist",
		Description: "OpenList / AList",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name:     "url",
			Help:     "URL of the OpenList or AList site, e.g. https://alist.example.com",
			Required: true,
		}, {
			Name: "username",
			Help: "User name to log in with.\n\nLeave blank to use token, or guest access.",
		}, {
			Name:       "password",
			Help:       "Password to log in with.",
			IsPassword: true,
		}, {
			Name: "token",
			Help: `Admin token to use instead of a user name and password.

It's under Settings, Other, Token in the OpenList admin panel and
doesn't expire. It gives admin access.`,
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:       "meta_password",
			Help:       `Password for folders protected by a meta password.`,
			IsPassword: true,
			Advanced:   true,
		}, {
			Name: "preserve_mod_time",
			Help: `Trust the modification times OpenList reports.

Many storages behind OpenList (most cloud drives) ignore the time
rclone uploads with, so by default rclone only compares sizes (and
hashes where the storage has them). Set this if the storage keeps
modification times, such as local storage, for normal syncs.`,
			Default:  false,
			Advanced: true,
		}, {
			Name:     "task_timeout",
			Help:     "How long to wait for a server-side copy or move that OpenList runs as a background task.",
			Default:  fs.Duration(2 * time.Hour),
			Advanced: true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default: encoder.Base |
				encoder.EncodeBackSlash |
				encoder.EncodeInvalidUtf8,
		}},
	})
}

// Options defines the configuration for this backend
type Options struct {
	URL             string               `config:"url"`
	Username        string               `config:"username"`
	Password        string               `config:"password"`
	Token           string               `config:"token"`
	MetaPassword    string               `config:"meta_password"`
	PreserveModTime bool                 `config:"preserve_mod_time"`
	TaskTimeout     fs.Duration          `config:"task_timeout"`
	Enc             encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote OpenList server
type Fs struct {
	name     string
	root     string
	opt      Options
	features *fs.Features
	siteURL  string
	srv      *rest.Client
	pacer    *fs.Pacer
	mu       sync.Mutex // protects token
	token    string
}

// Object describes an OpenList file
type Object struct {
	fs      *Fs
	remote  string
	size    int64
	modTime time.Time
	hashes  map[string]string
}

// ------------------------------------------------------------

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string { return f.name }

// Root of the remote (as passed into NewFs)
func (f *Fs) Root() string { return f.root }

// String converts this Fs to a string
func (f *Fs) String() string { return fmt.Sprintf("OpenList root '%s'", f.root) }

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features { return f.features }

// Precision of the modification times
func (f *Fs) Precision() time.Duration {
	if f.opt.PreserveModTime {
		return time.Second
	}
	return fs.ModTimeNotSupported
}

// Hashes returns the supported hash sets.
//
// Which ones an object has depends on the storage behind OpenList.
func (f *Fs) Hashes() hash.Set {
	return hash.NewHashSet(hash.MD5, hash.SHA1, hash.SHA256)
}

// hashKeys maps rclone hashes to OpenList hash_info keys
var hashKeys = map[hash.Type]string{
	hash.MD5:    "md5",
	hash.SHA1:   "sha1",
	hash.SHA256: "sha256",
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

func errorHandler(resp *http.Response) error {
	body, _ := rest.ReadBody(resp)
	var r api.Response
	if json.Unmarshal(body, &r) == nil && r.Code != 0 {
		return &api.Error{Code: r.Code, Message: r.Message}
	}
	return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

// retryErrorCodes is a slice of error codes that we will retry
var retryErrorCodes = []int{429, 500, 502, 503, 504, 509}

func (f *Fs) shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	return fserrors.ShouldRetry(err) || fserrors.ShouldRetryHTTP(resp, retryErrorCodes), err
}

// apiError returns the *api.Error in err or nil
func apiError(err error) *api.Error {
	var e *api.Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// isNotFound reports whether err means the path doesn't exist
func isNotFound(err error) bool {
	e := apiError(err)
	if e == nil {
		return false
	}
	msg := strings.ToLower(e.Message)
	return e.Code == 404 || strings.Contains(msg, "not found") || strings.Contains(msg, "not exist")
}

// isExist reports whether err means the target already exists
func isExist(err error) bool {
	e := apiError(err)
	return e != nil && (strings.Contains(e.Message, "exists") || strings.Contains(e.Message, "already exist"))
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
	for _, p := range []*string{&opt.Password, &opt.MetaPassword} {
		if *p != "" {
			if *p, err = obscure.Reveal(*p); err != nil {
				return nil, fmt.Errorf("couldn't decrypt password: %w", err)
			}
		}
	}
	f := &Fs{
		name:    name,
		root:    strings.Trim(root, "/"),
		opt:     *opt,
		siteURL: site,
		srv:     rest.NewClient(fshttp.NewClient(ctx)).SetRoot(site).SetErrorHandler(errorHandler),
		pacer:   fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		token:   opt.Token,
	}
	if f.token == "" && opt.Username != "" {
		if err := f.login(ctx); err != nil {
			return nil, err
		}
	}
	f.features = (&fs.Features{
		CanHaveEmptyDirectories: true,
	}).Fill(ctx, f)

	if f.root != "" {
		obj, err := f.get(ctx, "")
		if err == nil && !obj.IsDir {
			f.root = path.Dir(f.root)
			if f.root == "." {
				f.root = ""
			}
			return f, fs.ErrorIsFile
		}
	}
	return f, nil
}

// login gets a new session token with the user name and password
func (f *Fs) login(ctx context.Context) error {
	opts := rest.Opts{Method: "POST", Path: "/api/auth/login"}
	var lr api.LoginResponse
	err := f.callWith(ctx, &opts, &api.LoginRequest{Username: f.opt.Username, Password: f.opt.Password}, &lr, false)
	if err != nil {
		if e := apiError(err); e != nil && strings.Contains(strings.ToLower(e.Message), "2fa") {
			return fserrors.FatalError(fmt.Errorf("openlist: the account uses 2FA, which rclone can't answer: set token instead: %w", err))
		}
		return fmt.Errorf("openlist: login failed: %w", err)
	}
	if lr.Token == "" {
		return errors.New("openlist: login returned no token")
	}
	f.mu.Lock()
	f.token = lr.Token
	f.mu.Unlock()
	return nil
}

// tokenExpiry returns the expiry of a JWT session token, or the zero
// time if it isn't one (such as the admin token, which doesn't expire)
func tokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// freshToken logs in again if the session token expires soon. Uploads
// call it since their body can't be replayed after a 401.
func (f *Fs) freshToken(ctx context.Context) error {
	if f.opt.Username == "" || f.opt.Token != "" {
		return nil
	}
	f.mu.Lock()
	exp := tokenExpiry(f.token)
	f.mu.Unlock()
	if exp.IsZero() || time.Until(exp) > 5*time.Minute {
		return nil
	}
	return f.login(ctx)
}

// call makes an authorised API call, logging in again if the session
// token expired
func (f *Fs) call(ctx context.Context, opts *rest.Opts, in, out any) error {
	return f.callWith(ctx, opts, in, out, true)
}

func (f *Fs) callWith(ctx context.Context, opts *rest.Opts, in, out any, auth bool) error {
	var r api.Response
	relogged := false
	err := f.pacer.Call(func() (bool, error) {
		r = api.Response{}
		if auth {
			f.mu.Lock()
			token := f.token
			f.mu.Unlock()
			if token != "" {
				if opts.ExtraHeaders == nil {
					opts.ExtraHeaders = map[string]string{}
				}
				opts.ExtraHeaders["Authorization"] = token
			}
		}
		resp, err := f.srv.CallJSON(ctx, opts, in, &r)
		if err == nil && r.Code != api.CodeOK {
			err = &api.Error{Code: r.Code, Message: r.Message}
		}
		if e := apiError(err); auth && e != nil && e.Code == 401 && !relogged && f.opt.Username != "" && f.opt.Token == "" {
			relogged = true
			if lErr := f.login(ctx); lErr != nil {
				return false, lErr
			}
			return true, err
		}
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if out != nil && len(r.Data) > 0 && string(r.Data) != "null" {
		if err := json.Unmarshal(r.Data, out); err != nil {
			return fmt.Errorf("failed to decode %s response: %w", opts.Path, err)
		}
	}
	return nil
}

// ------------------------------------------------------------
// Paths

// fullPath returns the OpenList path of remote under the root
func (f *Fs) fullPath(remote string) string {
	return f.absPath(path.Join(f.root, remote))
}

// absPath returns the OpenList path of p, relative to the user's base
func (f *Fs) absPath(p string) string {
	if p == "" || p == "." {
		return "/"
	}
	segments := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segments {
		segments[i] = f.opt.Enc.FromStandardName(s)
	}
	return "/" + strings.Join(segments, "/")
}

// escapePath URL escapes each segment of an OpenList path
func escapePath(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

// ------------------------------------------------------------
// Files and directories

func (f *Fs) get(ctx context.Context, remote string) (*api.GetResponse, error) {
	opts := rest.Opts{Method: "POST", Path: "/api/fs/get"}
	var r api.GetResponse
	if err := f.call(ctx, &opts, &api.GetRequest{Path: f.fullPath(remote), Password: f.opt.MetaPassword}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// listAll calls fn for every entry in dir, stopping early if fn returns true
func (f *Fs) listAll(ctx context.Context, dir string, fn func(*api.Object) bool) error {
	seen := int64(0)
	for page := 1; ; page++ {
		opts := rest.Opts{Method: "POST", Path: "/api/fs/list"}
		var r api.ListResponse
		req := &api.ListRequest{Path: f.fullPath(dir), Password: f.opt.MetaPassword, Page: page, PerPage: listPageSize}
		if err := f.call(ctx, &opts, req, &r); err != nil {
			return err
		}
		for i := range r.Content {
			if fn(&r.Content[i]) {
				return nil
			}
		}
		seen += int64(len(r.Content))
		if len(r.Content) == 0 || seen >= r.Total {
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
	err := f.listAll(ctx, dir, func(obj *api.Object) bool {
		remote := path.Join(dir, f.opt.Enc.ToStandardName(obj.Name))
		if obj.IsDir {
			iErr = lh.Add(fs.NewDir(remote, obj.Modified))
		} else {
			iErr = lh.Add(f.newObject(remote, obj))
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

func (f *Fs) newObject(remote string, obj *api.Object) *Object {
	return &Object{fs: f, remote: remote, size: obj.Size, modTime: obj.Modified, hashes: obj.HashInfo}
}

// NewObject finds the Object at remote.
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	obj, err := f.get(ctx, remote)
	if err != nil {
		if isNotFound(err) {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	if obj.IsDir {
		return nil, fs.ErrorIsDir
	}
	return f.newObject(remote, &obj.Object), nil
}

// Put the object into the container
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	o := &Object{fs: f, remote: src.Remote()}
	return o, o.Update(ctx, in, src, options...)
}

func (f *Fs) mkdirAbs(ctx context.Context, p string) error {
	opts := rest.Opts{Method: "POST", Path: "/api/fs/mkdir"}
	return f.call(ctx, &opts, &api.MkdirRequest{Path: p}, nil)
}

// Mkdir creates the container if it doesn't exist
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	if f.root == "" && dir == "" {
		return nil
	}
	return f.mkdirAbs(ctx, f.fullPath(dir))
}

// removeAbs removes the entry at p
func (f *Fs) removeAbs(ctx context.Context, p string) error {
	opts := rest.Opts{Method: "POST", Path: "/api/fs/remove"}
	return f.call(ctx, &opts, &api.RemoveRequest{Dir: path.Dir(p), Names: []string{path.Base(p)}}, nil)
}

// Rmdir deletes the root folder
//
// Returns an error if it isn't empty
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	obj, err := f.get(ctx, dir)
	if err != nil {
		if isNotFound(err) {
			return fs.ErrorDirNotFound
		}
		return err
	}
	if !obj.IsDir {
		return fs.ErrorIsFile
	}
	empty := true
	if err := f.listAll(ctx, dir, func(*api.Object) bool { empty = false; return true }); err != nil {
		return err
	}
	if !empty {
		return fs.ErrorDirectoryNotEmpty
	}
	if f.root == "" && dir == "" {
		return nil
	}
	return f.removeAbs(ctx, f.fullPath(dir))
}

// Purge deletes all the files and the container
func (f *Fs) Purge(ctx context.Context, dir string) error {
	if f.root == "" && dir == "" {
		return errors.New("can't purge the root of OpenList")
	}
	if _, err := f.get(ctx, dir); err != nil {
		if isNotFound(err) {
			return fs.ErrorDirNotFound
		}
		return err
	}
	return f.removeAbs(ctx, f.fullPath(dir))
}

// waitTasks waits for the background tasks OpenList started
func (f *Fs) waitTasks(ctx context.Context, kind string, tasks []api.TaskInfo) error {
	timeout := time.Duration(f.opt.TaskTimeout)
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	deadline := time.Now().Add(timeout)
	for _, t := range tasks {
		sleep := 100 * time.Millisecond
		for {
			opts := rest.Opts{Method: "POST", Path: "/api/task/" + kind + "/info", Parameters: url.Values{"tid": {t.ID}}}
			var info api.TaskInfo
			if err := f.call(ctx, &opts, nil, &info); err != nil {
				return fmt.Errorf("openlist: %s task %s: %w", kind, t.ID, err)
			}
			switch info.State {
			case api.TaskSucceeded:
			case api.TaskCanceled, api.TaskFailed, api.TaskErrored:
				return fmt.Errorf("openlist: %s task %q failed: %s", kind, info.Name, info.Error)
			default:
				if time.Now().After(deadline) {
					return fmt.Errorf("openlist: timed out waiting for %s task %q (%s)", kind, info.Name, info.Status)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(sleep):
				}
				sleep = min(sleep*2, taskPollMax)
				continue
			}
			break
		}
	}
	return nil
}

// moveOrCopy moves or copies the entry at srcAbs into dstDir keeping its name
func (f *Fs) moveOrCopy(ctx context.Context, kind, srcAbs, dstDir string) error {
	opts := rest.Opts{Method: "POST", Path: "/api/fs/" + kind}
	var r api.TasksResponse
	req := &api.MoveCopyRequest{SrcDir: path.Dir(srcAbs), DstDir: dstDir, Names: []string{path.Base(srcAbs)}}
	if err := f.call(ctx, &opts, req, &r); err != nil {
		return err
	}
	return f.waitTasks(ctx, kind, r.Tasks)
}

func (f *Fs) renameAbs(ctx context.Context, p, newLeaf string) error {
	opts := rest.Opts{Method: "POST", Path: "/api/fs/rename"}
	return f.call(ctx, &opts, &api.RenameRequest{Path: p, Name: newLeaf}, nil)
}

// relocate moves or copies srcAbs to dstAbs, renaming as needed
func (f *Fs) relocate(ctx context.Context, srcAbs, dstAbs string, copy bool) error {
	srcDir, srcLeaf := path.Split(srcAbs)
	dstDir, dstLeaf := path.Split(dstAbs)
	srcDir, dstDir = path.Clean(srcDir), path.Clean(dstDir)
	kind := "move"
	if copy {
		kind = "copy"
	}
	if srcDir == dstDir {
		if copy {
			return fs.ErrorCantCopy
		}
		return f.renameAbs(ctx, srcAbs, dstLeaf)
	}
	if err := f.mkdirAbs(ctx, dstDir); err != nil {
		return err
	}
	err := f.moveOrCopy(ctx, kind, srcAbs, dstDir)
	if err == nil {
		if srcLeaf == dstLeaf {
			return nil
		}
		return f.renameAbs(ctx, path.Join(dstDir, srcLeaf), dstLeaf)
	}
	if !isExist(err) || srcLeaf == dstLeaf {
		return err
	}
	if copy {
		return fs.ErrorCantCopy
	}
	// The destination has the source name: rename first, through a
	// temporary name if the source folder has the destination name.
	err = f.renameAbs(ctx, srcAbs, dstLeaf)
	if err == nil {
		return f.moveOrCopy(ctx, kind, path.Join(srcDir, dstLeaf), dstDir)
	}
	if !isExist(err) {
		return err
	}
	tmpLeaf := ".rclone-move-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + dstLeaf
	if err = f.renameAbs(ctx, srcAbs, tmpLeaf); err != nil {
		return err
	}
	if err = f.moveOrCopy(ctx, kind, path.Join(srcDir, tmpLeaf), dstDir); err != nil {
		return err
	}
	return f.renameAbs(ctx, path.Join(dstDir, tmpLeaf), dstLeaf)
}

// sameSite reports whether src lives on the same server as f
func (f *Fs) sameSite(src *Fs) bool {
	return src.siteURL == f.siteURL && src.opt.Username == f.opt.Username && src.opt.Token == f.opt.Token
}

// Copy src to this remote using server-side copy operations.
func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok || !f.sameSite(srcObj.fs) {
		return nil, fs.ErrorCantCopy
	}
	if err := f.relocate(ctx, srcObj.fs.fullPath(srcObj.remote), f.fullPath(remote), true); err != nil {
		return nil, err
	}
	return f.NewObject(ctx, remote)
}

// Move src to this remote using server-side move operations.
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok || !f.sameSite(srcObj.fs) {
		return nil, fs.ErrorCantMove
	}
	if err := f.relocate(ctx, srcObj.fs.fullPath(srcObj.remote), f.fullPath(remote), false); err != nil {
		return nil, err
	}
	return f.NewObject(ctx, remote)
}

// DirMove moves src, srcRemote to this remote at dstRemote
// using server-side move operations.
func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	srcFs, ok := src.(*Fs)
	if !ok || !f.sameSite(srcFs) {
		return fs.ErrorCantDirMove
	}
	if _, err := f.get(ctx, dstRemote); err == nil {
		return fs.ErrorDirExists
	} else if !isNotFound(err) {
		return err
	}
	return f.relocate(ctx, srcFs.fullPath(srcRemote), f.fullPath(dstRemote), false)
}

// PublicLink returns a signed download link for remote
func (f *Fs) PublicLink(ctx context.Context, remote string, expire fs.Duration, unlink bool) (string, error) {
	if unlink {
		return "", errors.New("openlist: signed links can't be revoked")
	}
	obj, err := f.get(ctx, remote)
	if err != nil {
		if isNotFound(err) {
			return "", fs.ErrorObjectNotFound
		}
		return "", err
	}
	return f.downloadURL(f.fullPath(remote), obj.Sign), nil
}

// downloadURL is the /d link of the file at p
func (f *Fs) downloadURL(p, sign string) string {
	u := f.siteURL + "/d" + escapePath(p)
	if sign != "" {
		u += "?sign=" + url.QueryEscape(sign)
	}
	return u
}

// ------------------------------------------------------------
// Object

// Fs returns the parent Fs
func (o *Object) Fs() fs.Info { return o.fs }

// String returns a description of the Object
func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

// Remote returns the remote path
func (o *Object) Remote() string { return o.remote }

// Hash returns the hash of an object, or "" if the storage has none
func (o *Object) Hash(ctx context.Context, t hash.Type) (string, error) {
	key, ok := hashKeys[t]
	if !ok {
		return "", hash.ErrUnsupported
	}
	return strings.ToLower(o.hashes[key]), nil
}

// Size returns the size of an object in bytes
func (o *Object) Size() int64 { return o.size }

// ModTime returns the modification time of the object
func (o *Object) ModTime(ctx context.Context) time.Time { return o.modTime }

// SetModTime sets the modification time of the object
func (o *Object) SetModTime(ctx context.Context, modTime time.Time) error {
	return fs.ErrorCantSetModTime
}

// Storable returns a boolean showing whether this object storable
func (o *Object) Storable() bool { return true }

// Open an object for read
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	obj, err := o.fs.get(ctx, o.remote)
	if err != nil {
		if isNotFound(err) {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	fs.FixRangeOption(options, o.size)
	opts := rest.Opts{
		Method:  "GET",
		RootURL: o.fs.downloadURL(o.fs.fullPath(o.remote), obj.Sign),
		Options: options,
	}
	o.fs.mu.Lock()
	if o.fs.token != "" {
		// Go drops this on redirects to other hosts
		opts.ExtraHeaders = map[string]string{"Authorization": o.fs.token}
	}
	o.fs.mu.Unlock()
	var resp *http.Response
	err = o.fs.pacer.Call(func() (bool, error) {
		resp, err = o.fs.srv.Call(ctx, &opts)
		return o.fs.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Update the object with the contents of the io.Reader, modTime and size
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	size := src.Size()
	if size < 0 {
		return errors.New("openlist: can't upload files of unknown size")
	}
	if err := o.fs.freshToken(ctx); err != nil {
		return err
	}
	p := o.fs.fullPath(o.remote)
	headers := map[string]string{
		"File-Path":     escapePath(p),
		"Last-Modified": strconv.FormatInt(src.ModTime(ctx).UnixMilli(), 10),
		"Overwrite":     "true",
		"As-Task":       "false",
	}
	if o.fs.opt.MetaPassword != "" {
		headers["Password"] = o.fs.opt.MetaPassword
	}
	// Hashes let some storages upload instantly by hash
	for t, h := range map[hash.Type]string{hash.MD5: "X-File-Md5", hash.SHA1: "X-File-Sha1", hash.SHA256: "X-File-Sha256"} {
		if sum, err := src.Hash(ctx, t); err == nil && sum != "" {
			headers[h] = sum
		}
	}
	opts := rest.Opts{
		Method:        "PUT",
		Path:          "/api/fs/put",
		Body:          in,
		ContentLength: &size,
		ContentType:   "application/octet-stream",
		ExtraHeaders:  headers,
	}
	// The body can't be replayed, so this isn't retried here
	var r api.Response
	err := o.fs.pacer.CallNoRetry(func() (bool, error) {
		o.fs.mu.Lock()
		if o.fs.token != "" {
			headers["Authorization"] = o.fs.token
		}
		o.fs.mu.Unlock()
		resp, err := o.fs.srv.CallJSON(ctx, &opts, nil, &r)
		if err == nil && r.Code != api.CodeOK {
			err = &api.Error{Code: r.Code, Message: r.Message}
		}
		if e := apiError(err); e != nil && e.Code == 401 && o.fs.opt.Username != "" && o.fs.opt.Token == "" {
			// Log in again so the retry rclone makes works
			if lErr := o.fs.login(ctx); lErr == nil {
				return true, err
			}
		}
		return o.fs.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	obj, err := o.fs.get(ctx, o.remote)
	if err != nil {
		return fmt.Errorf("openlist: couldn't read back uploaded file: %w", err)
	}
	o.size, o.modTime, o.hashes = obj.Size, obj.Modified, obj.HashInfo
	return nil
}

// Remove an object
func (o *Object) Remove(ctx context.Context) error {
	err := o.fs.removeAbs(ctx, o.fs.fullPath(o.remote))
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
	_ fs.PublicLinker = (*Fs)(nil)
	_ fs.ListPer      = (*Fs)(nil)
	_ fs.Object       = (*Object)(nil)
)
