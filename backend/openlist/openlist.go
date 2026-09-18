// Package openlist provides an interface to an AList or OpenList v3 server.
package openlist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/rclone/rclone/backend/openlist/api"
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
	defaultMinSleep = 10 * time.Millisecond
	maxSleep        = 2 * time.Second
	decayConstant   = 2
)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "openlist",
		Description: "AList / OpenList v3",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "url",
			Help: `URL of the AList or OpenList server.

This talks to your own server over the public AList v3 HTTP API.
Do not point this at a third-party token broker.
`,
			Required: true,
		}, {
			Name:      "username",
			Help:      "Username. Optional if token is set.",
			Sensitive: true,
		}, {
			Name:      "password",
			Help:      "Password. Optional if token is set.",
			Sensitive: true,
		}, {
			Name:      "token",
			Help:      "API token. Optional if username and password are set.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default:  encoder.Display,
		}},
	})
}

// Options defines the configuration of this backend.
type Options struct {
	URL      string               `config:"url"`
	Username string               `config:"username"`
	Password string               `config:"password"`
	Token    string               `config:"token"`
	Enc      encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote AList / OpenList server.
type Fs struct {
	name     string
	root     string
	opt      Options
	features *fs.Features
	srv      *rest.Client
	dl       *rest.Client
	pacer    *fs.Pacer
	dirCache *dircache.DirCache
	token    string
}

// Object describes an AList object.
type Object struct {
	fs      *Fs
	remote  string
	fsPath  string
	size    int64
	md5sum  string
	modTime time.Time
}

var retryErrorCodes = []int{429, 500, 502, 503, 504}

func parsePath(p string) string { return strings.Trim(p, "/") }

func joinAPI(dir, leaf string) string {
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "/"
	}
	if leaf == "" {
		return dir
	}
	if dir == "/" {
		return "/" + leaf
	}
	return dir + "/" + leaf
}

func shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	return fserrors.ShouldRetry(err) || fserrors.ShouldRetryHTTP(resp, retryErrorCodes), err
}

func errorHandler(resp *http.Response) error {
	body, err := rest.ReadBody(resp)
	if err != nil {
		return fmt.Errorf("error reading error out of body: %w", err)
	}
	return fmt.Errorf("HTTP error %v (%v) returned body: %q", resp.StatusCode, resp.Status, body)
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}
	opt.URL = strings.TrimRight(opt.URL, "/")
	if opt.URL == "" {
		return nil, errors.New("openlist: url is required")
	}
	if opt.Token == "" && (opt.Username == "" || opt.Password == "") {
		return nil, errors.New("openlist: username and password, or token, are required")
	}
	root = parsePath(root)
	client := fshttp.NewClient(ctx)
	f := &Fs{
		name:  name,
		root:  root,
		opt:   *opt,
		srv:   rest.NewClient(client).SetRoot(opt.URL),
		dl:    rest.NewClient(client),
		pacer: fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(defaultMinSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		token: opt.Token,
	}
	f.srv.SetErrorHandler(errorHandler)
	f.features = (&fs.Features{CanHaveEmptyDirectories: true}).Fill(ctx, f)
	rootFolder := "/"
	if f.root != "" {
		rootFolder = "/" + f.root
		f.root = ""
		// keep dircache relative to configured root via NewFs path; use trimmed root below
	}
	// Restore rclone root handling: dircache root is the remote path, ID is the API path.
	f.root = parsePath(root)
	if f.root != "" {
		rootFolder = "/" + f.root
		f.root = f.root
	} else {
		rootFolder = "/"
	}
	f.dirCache = dircache.New(f.root, rootFolder, f)
	if err := f.ensureToken(ctx); err != nil {
		return nil, err
	}
	err = f.dirCache.FindRoot(ctx, false)
	if err != nil {
		newRoot, remote := dircache.SplitPath(f.root)
		tempF := *f
		tempF.dirCache = dircache.New(newRoot, rootFolder, &tempF)
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

func (f *Fs) ensureToken(ctx context.Context) error {
	if f.token != "" {
		f.srv.SetHeader("Authorization", f.token)
		return nil
	}
	opts := rest.Opts{Method: http.MethodPost, Path: "/api/auth/login"}
	req := map[string]string{"username": f.opt.Username, "password": f.opt.Password}
	var info api.Envelope[api.LoginData]
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if info.Code != 200 {
		return fmt.Errorf("openlist: login: %s", info.Message)
	}
	if info.Data.Token == "" {
		return errors.New("openlist: empty token")
	}
	f.token = info.Data.Token
	f.srv.SetHeader("Authorization", f.token)
	return nil
}

func (f *Fs) Name() string                 { return f.name }
func (f *Fs) Root() string                 { return f.root }
func (f *Fs) String() string                { return fmt.Sprintf("OpenList root '%s'", f.root) }
func (f *Fs) Features() *fs.Features        { return f.features }
func (f *Fs) Precision() time.Duration       { return time.Second }
func (f *Fs) Hashes() hash.Set               { return hash.NewHashSet(hash.MD5, hash.SHA1) }
func (f *Fs) DirCacheFlush()                { f.dirCache.ResetRoot() }

func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, info *api.Obj) (fs.Object, error) {
	o := &Object{fs: f, remote: remote}
	if info != nil {
		return o, o.setMetaData(info, joinAPI(mustDir(f, remote), info.Name))
	}
	return o, o.readMetaData(ctx)
}

func mustDir(f *Fs, remote string) string {
	dir := path.Dir(remote)
	if dir == "." {
		if f.root == "" {
			return "/"
		}
		return "/" + f.root
	}
	if f.root == "" {
		return "/" + dir
	}
	return "/" + f.root + "/" + dir
}

func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return f.newObjectWithInfo(ctx, remote, nil)
}

type listAllFn func(*api.Obj) bool

func (f *Fs) listAll(ctx context.Context, dirPath string, directoriesOnly, filesOnly bool, fn listAllFn) (found bool, err error) {
	opts := rest.Opts{Method: http.MethodPost, Path: "/api/fs/list"}
	req := map[string]any{"path": dirPath, "page": 1, "per_page": 0, "refresh": false}
	var info api.Envelope[api.ListData]
	err = f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return false, err
	}
	if info.Code != 200 {
		if strings.Contains(strings.ToLower(info.Message), "not found") {
			return false, fs.ErrorDirNotFound
		}
		return false, fmt.Errorf("openlist: list: %s", info.Message)
	}
	for i := range info.Data.Content {
		item := &info.Data.Content[i]
		item.Name = f.opt.Enc.ToStandardName(item.Name)
		if item.IsDir {
			if filesOnly {
				continue
			}
		} else if directoriesOnly {
			continue
		}
		if fn(item) {
			return true, nil
		}
	}
	return false, nil
}

func (f *Fs) readMetaDataForPath(ctx context.Context, remote string) (*api.Obj, error) {
	leaf, directoryID, err := f.dirCache.FindPath(ctx, remote, false)
	if err != nil {
		if err == fs.ErrorDirNotFound {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	var info *api.Obj
	found, err := f.listAll(ctx, directoryID, false, true, func(item *api.Obj) bool {
		if item.Name == leaf {
			info = item
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fs.ErrorObjectNotFound
	}
	return info, nil
}

func (f *Fs) FindLeaf(ctx context.Context, pathID, leaf string) (pathIDOut string, found bool, err error) {
	found, err = f.listAll(ctx, pathID, true, false, func(item *api.Obj) bool {
		if item.Name == leaf {
			pathIDOut = joinAPI(pathID, leaf)
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (string, error) {
	newPath := joinAPI(pathID, f.opt.Enc.FromStandardName(leaf))
	opts := rest.Opts{Method: http.MethodPost, Path: "/api/fs/mkdir"}
	req := map[string]string{"path": newPath}
	var info api.Envelope[any]
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	if info.Code != 200 {
		return "", fmt.Errorf("openlist: mkdir: %s", info.Message)
	}
	return newPath, nil
}

func (f *Fs) itemToDirEntry(ctx context.Context, remote string, dirPath string, info *api.Obj) (fs.DirEntry, error) {
	if info.IsDir {
		id := joinAPI(dirPath, info.Name)
		f.dirCache.Put(remote, id)
		d := fs.NewDir(remote, info.Modified)
		d.SetID(id)
		return d, nil
	}
	return f.newObjectWithInfo(ctx, remote, info)
}

func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	directoryID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return nil, err
	}
	var iErr error
	_, err = f.listAll(ctx, directoryID, false, false, func(info *api.Obj) bool {
		remote := path.Join(dir, info.Name)
		entry, err := f.itemToDirEntry(ctx, remote, directoryID, info)
		if err != nil {
			iErr = err
			return true
		}
		if entry != nil {
			entries = append(entries, entry)
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	return entries, iErr
}

func (f *Fs) createObject(ctx context.Context, remote string, _ time.Time, _ int64) (o *Object, leaf, directoryID string, err error) {
	leaf, directoryID, err = f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return
	}
	o = &Object{fs: f, remote: remote, fsPath: joinAPI(directoryID, leaf)}
	return o, leaf, directoryID, nil
}

func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	existingObj, err := f.NewObject(ctx, src.Remote())
	switch {
	case err == nil:
		return existingObj, existingObj.Update(ctx, in, src, options...)
	case errors.Is(err, fs.ErrorObjectNotFound):
		o, _, _, err := f.createObject(ctx, src.Remote(), src.ModTime(ctx), src.Size())
		if err != nil {
			return nil, err
		}
		return o, o.Update(ctx, in, src, options...)
	default:
		return nil, err
	}
}

func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	_, err := f.dirCache.FindDir(ctx, dir, true)
	return err
}

func (f *Fs) purgeCheck(ctx context.Context, dir string, check bool) error {
	if dir == "" && f.root == "" {
		return errors.New("can't purge root directory")
	}
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	if check {
		found, err := f.listAll(ctx, dirID, false, false, func(*api.Obj) bool { return true })
		if err != nil {
			return err
		}
		if found {
			return fs.ErrorDirectoryNotEmpty
		}
	}
	parent, name := path.Split(strings.TrimSuffix(dirID, "/"))
	if parent == "" {
		parent = "/"
	}
	opts := rest.Opts{Method: http.MethodPost, Path: "/api/fs/remove"}
	req := map[string]any{"dir": strings.TrimSuffix(parent, "/"), "names": []string{name}}
	if req["dir"] == "" {
		req["dir"] = "/"
	}
	var info api.Envelope[any]
	err = f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if info.Code != 200 {
		return fmt.Errorf("openlist: remove: %s", info.Message)
	}
	f.dirCache.FlushDir(dir)
	return nil
}

func (f *Fs) Rmdir(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, true) }
func (f *Fs) Purge(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, false) }

func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fs.ErrorCantMove
	}
	_, dstLeaf, dstDirectoryID, err := f.createObject(ctx, remote, srcObj.modTime, srcObj.size)
	if err != nil {
		return nil, err
	}
	srcDir, srcName := path.Split(strings.TrimSuffix(srcObj.fsPath, "/"))
	if srcDir == "" {
		srcDir = "/"
	}
	opts := rest.Opts{Method: http.MethodPost, Path: "/api/fs/move"}
	req := map[string]any{"src_dir": strings.TrimSuffix(srcDir, "/"), "dst_dir": dstDirectoryID, "names": []string{srcName}}
	var info api.Envelope[any]
	err = f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	if info.Code != 200 {
		return nil, fmt.Errorf("openlist: move: %s", info.Message)
	}
	if srcName != dstLeaf {
		renameOpts := rest.Opts{Method: http.MethodPost, Path: "/api/fs/rename"}
		renameReq := map[string]string{"path": joinAPI(dstDirectoryID, srcName), "name": f.opt.Enc.FromStandardName(dstLeaf)}
		err = f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, &renameOpts, &renameReq, &info)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return nil, err
		}
	}
	srcObj.fs.dirCache.FlushDir(path.Dir(srcObj.remote))
	f.dirCache.FlushDir(path.Dir(remote))
	return f.NewObject(ctx, remote)
}

func (f *Fs) downloadURL(ctx context.Context, fsPath string) (string, error) {
	opts := rest.Opts{Method: http.MethodPost, Path: "/api/fs/get"}
	req := map[string]any{"path": fsPath}
	var info api.Envelope[api.GetData]
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	if info.Code != 200 {
		return "", fmt.Errorf("openlist: get: %s", info.Message)
	}
	if info.Data.RawURL == "" {
		return "", errors.New("openlist: empty download url")
	}
	return info.Data.RawURL, nil
}

func (o *Object) setMetaData(info *api.Obj, fsPath string) error {
	if info.IsDir {
		return fs.ErrorIsDir
	}
	o.fsPath = fsPath
	o.size = info.Size
	o.modTime = info.Modified
	o.md5sum = strings.ToLower(info.HashInfo.MD5)
	return nil
}

func (o *Object) readMetaData(ctx context.Context) error {
	info, err := o.fs.readMetaDataForPath(ctx, o.remote)
	if err != nil {
		return err
	}
	_, directoryID, err := o.fs.dirCache.FindPath(ctx, o.remote, false)
	if err != nil {
		return err
	}
	return o.setMetaData(info, joinAPI(directoryID, info.Name))
}

func (o *Object) Fs() fs.Info                              { return o.fs }
func (o *Object) String() string                               { return o.remote }
func (o *Object) Remote() string                                { return o.remote }
func (o *Object) Size() int64                                 { return o.size }
func (o *Object) ModTime(_ context.Context) time.Time          { return o.modTime }
func (o *Object) SetModTime(context.Context, time.Time) error { return fs.ErrorCantSetModTime }
func (o *Object) Storable() bool                                { return true }

func (o *Object) Hash(_ context.Context, t hash.Type) (string, error) {
	if t != hash.MD5 {
		return "", hash.ErrUnsupported
	}
	return o.md5sum, nil
}

func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	if o.fsPath == "" {
		if err := o.readMetaData(ctx); err != nil {
			return nil, err
		}
	}
	fs.FixRangeOption(options, o.size)
	var resp *http.Response
	err := o.fs.pacer.Call(func() (bool, error) {
		targetURL, err := o.fs.downloadURL(ctx, o.fsPath)
		if err != nil {
			return shouldRetry(ctx, resp, err)
		}
		opts := rest.Opts{Method: http.MethodGet, RootURL: targetURL, Options: options}
		resp, err = o.fs.dl.Call(ctx, &opts)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (o *Object) Remove(ctx context.Context) error {
	if o.fsPath == "" {
		if err := o.readMetaData(ctx); err != nil {
			return err
		}
	}
	parent, name := path.Split(strings.TrimSuffix(o.fsPath, "/"))
	if parent == "" {
		parent = "/"
	}
	opts := rest.Opts{Method: http.MethodPost, Path: "/api/fs/remove"}
	req := map[string]any{"dir": strings.TrimSuffix(parent, "/"), "names": []string{name}}
	var info api.Envelope[any]
	err := o.fs.pacer.Call(func() (bool, error) {
		resp, err := o.fs.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if info.Code != 200 {
		return fmt.Errorf("openlist: remove: %s", info.Message)
	}
	o.fs.dirCache.FlushDir(path.Dir(o.remote))
	return nil
}

func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, _ ...fs.OpenOption) error {
	leaf, directoryID, err := o.fs.dirCache.FindPath(ctx, o.remote, true)
	if err != nil {
		return err
	}
	dest := joinAPI(directoryID, o.fs.opt.Enc.FromStandardName(leaf))
	size := src.Size()
	opts := rest.Opts{
		Method:        http.MethodPut,
		Path:          "/api/fs/put",
		Body:          in,
		ContentLength: &size,
		ExtraHeaders:  map[string]string{"File-Path": url.PathEscape(dest)},
	}
	var info api.Envelope[any]
	err = o.fs.pacer.Call(func() (bool, error) {
		resp, err := o.fs.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if info.Code != 200 {
		return fmt.Errorf("openlist: put: %s", info.Message)
	}
	o.fsPath = dest
	o.size = src.Size()
	o.modTime = src.ModTime(ctx)
	return nil
}

var (
	_ fs.Fs              = (*Fs)(nil)
	_ fs.Purger          = (*Fs)(nil)
	_ fs.Mover           = (*Fs)(nil)
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
)
