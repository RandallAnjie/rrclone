// Package baidu provides an interface to Baidu Netdisk using the Open API.
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
	"github.com/rclone/rclone/lib/multipart"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
)

const (
	defaultMinSleep = 200 * time.Millisecond
	maxSleep        = 4 * time.Second
	decayConstant   = 2
)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "baidu",
		Description: "Baidu Netdisk",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "refresh_token",
			Help: `Baidu Netdisk refresh token.

Create an app at https://pan.baidu.com/union/console/applist and
complete OAuth. rclone refreshes the access token with client_id and
client_secret. Do not use a third-party token broker.
`,
			Sensitive: true,
		}, {
			Name:      "client_id",
			Help:      "OAuth client ID from the Baidu open platform.",
			Sensitive: true,
		}, {
			Name:      "client_secret",
			Help:      "OAuth client secret from the Baidu open platform.",
			Sensitive: true,
		}, {
			Name:      "access_token",
			Help:      "Access token. Optional if refresh_token and client credentials are set.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:     "root_folder_path",
			Help:     `Root folder path. Leave blank to use "/".`,
			Advanced: true,
		}, {
			Name:     "endpoint",
			Help:     "Endpoint for the Baidu Netdisk REST API.",
			Default:  api.DefaultRoot,
			Advanced: true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default: encoder.Display |
				encoder.EncodeWin |
				encoder.EncodeBackSlash |
				encoder.EncodeInvalidUtf8,
		}},
	})
}

// Options defines the configuration of this backend.
type Options struct {
	RefreshToken   string               `config:"refresh_token"`
	ClientID       string               `config:"client_id"`
	ClientSecret   string               `config:"client_secret"`
	AccessToken    string               `config:"access_token"`
	RootFolderPath string               `config:"root_folder_path"`
	Endpoint       string               `config:"endpoint"`
	Enc            encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote Baidu Netdisk.
type Fs struct {
	name     string
	root     string
	opt      Options
	features *fs.Features
	srv      *rest.Client
	dl       *rest.Client
	pacer    *fs.Pacer
	dirCache *dircache.DirCache
	tokenMu  sync.Mutex
	token    string
	tokenExp time.Time
}

// Object describes a Baidu object.
type Object struct {
	fs      *Fs
	remote  string
	id      string
	fsPath  string
	size    int64
	md5sum  string
	modTime time.Time
}

var retryErrorCodes = []int{429, 500, 502, 503, 504}

func parsePath(p string) string { return strings.Trim(p, "/") }

func joinBaidu(dir, leaf string) string {
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

func errnoErr(errno int, what string) error {
	if errno == 0 {
		return nil
	}
	return fmt.Errorf("baidu: %s errno %d", what, errno)
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}
	if opt.Endpoint == "" {
		opt.Endpoint = api.DefaultRoot
	}
	if opt.AccessToken == "" && (opt.RefreshToken == "" || opt.ClientID == "" || opt.ClientSecret == "") {
		return nil, errors.New("baidu: refresh_token, client_id and client_secret are required (https://pan.baidu.com/union/console/applist)")
	}
	root = parsePath(root)
	client := fshttp.NewClient(ctx)
	rootFolder := opt.RootFolderPath
	if rootFolder == "" {
		rootFolder = "/"
	}
	if !strings.HasPrefix(rootFolder, "/") {
		rootFolder = "/" + rootFolder
	}
	f := &Fs{
		name:     name,
		root:     root,
		opt:      *opt,
		srv:      rest.NewClient(client).SetRoot(strings.TrimRight(opt.Endpoint, "/")),
		dl:       rest.NewClient(client),
		pacer:    fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(defaultMinSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		token:    strings.TrimSpace(opt.AccessToken),
		tokenExp: time.Now().Add(time.Hour),
	}
	f.srv.SetErrorHandler(errorHandler)
	f.dl.SetHeader("User-Agent", "pan.baidu.com")
	f.features = (&fs.Features{CanHaveEmptyDirectories: true}).Fill(ctx, f)
	f.dirCache = dircache.New(root, rootFolder, f)
	if err := f.ensureToken(ctx, false); err != nil {
		return nil, err
	}
	err = f.dirCache.FindRoot(ctx, false)
	if err != nil {
		newRoot, remote := dircache.SplitPath(root)
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

func (f *Fs) ensureToken(ctx context.Context, force bool) error {
	f.tokenMu.Lock()
	defer f.tokenMu.Unlock()
	if !force && f.token != "" && time.Now().Before(f.tokenExp.Add(-2*time.Minute)) {
		return nil
	}
	if f.opt.ClientID == "" || f.opt.ClientSecret == "" || f.opt.RefreshToken == "" {
		if f.token != "" && !force {
			return nil
		}
		return errors.New("baidu: token expired; set refresh_token, client_id and client_secret")
	}
	opts := rest.Opts{
		Method:     http.MethodPost,
		RootURL:    api.TokenURL,
		Parameters: url.Values{},
	}
	opts.Parameters.Set("grant_type", "refresh_token")
	opts.Parameters.Set("refresh_token", f.opt.RefreshToken)
	opts.Parameters.Set("client_id", f.opt.ClientID)
	opts.Parameters.Set("client_secret", f.opt.ClientSecret)
	var info api.TokenResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if info.Error != "" {
		return fmt.Errorf("baidu: %s: %s", info.Error, info.ErrorDescription)
	}
	if info.AccessToken == "" {
		return errors.New("baidu: empty access token")
	}
	f.token = info.AccessToken
	if info.RefreshToken != "" {
		f.opt.RefreshToken = info.RefreshToken
	}
	exp := info.ExpiresIn
	if exp <= 0 {
		exp = 2592000
	}
	f.tokenExp = time.Now().Add(time.Duration(exp) * time.Second)
	return nil
}

func (f *Fs) tokenValue() string {
	f.tokenMu.Lock()
	defer f.tokenMu.Unlock()
	return f.token
}

func (f *Fs) withToken(opts *rest.Opts) *rest.Opts {
	call := opts.Copy()
	if call.Parameters == nil {
		call.Parameters = url.Values{}
	}
	call.Parameters.Set("access_token", f.tokenValue())
	return call
}

func (f *Fs) Name() string                 { return f.name }
func (f *Fs) Root() string                 { return f.root }
func (f *Fs) String() string                { return fmt.Sprintf("Baidu Netdisk root '%s'", f.root) }
func (f *Fs) Features() *fs.Features        { return f.features }
func (f *Fs) Precision() time.Duration       { return time.Second }
func (f *Fs) Hashes() hash.Set               { return hash.Set(hash.MD5) }
func (f *Fs) DirCacheFlush()                { f.dirCache.ResetRoot() }

func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, info *api.File) (fs.Object, error) {
	o := &Object{fs: f, remote: remote}
	if info != nil {
		return o, o.setMetaData(info)
	}
	return o, o.readMetaData(ctx)
}

func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return f.newObjectWithInfo(ctx, remote, nil)
}

type listAllFn func(*api.File) bool

func (f *Fs) listAll(ctx context.Context, dirPath string, directoriesOnly, filesOnly bool, fn listAllFn) (found bool, err error) {
	if err := f.ensureToken(ctx, false); err != nil {
		return false, err
	}
	start := 0
	const limit = 1000
	for {
		opts := f.withToken(&rest.Opts{Method: http.MethodGet, Path: "/xpan/file", Parameters: url.Values{}})
		opts.Parameters.Set("method", "list")
		opts.Parameters.Set("dir", dirPath)
		opts.Parameters.Set("start", strconv.Itoa(start))
		opts.Parameters.Set("limit", strconv.Itoa(limit))
		opts.Parameters.Set("web", "web")
		var info api.ListResp
		err := f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, opts, nil, &info)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return false, err
		}
		if err := errnoErr(info.Errno, "list"); err != nil {
			return false, err
		}
		if len(info.List) == 0 {
			break
		}
		for i := range info.List {
			item := &info.List[i]
			item.ServerFilename = f.opt.Enc.ToStandardName(item.ServerFilename)
			if item.IsDir() {
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
		if len(info.List) < limit {
			break
		}
		start += limit
	}
	return false, nil
}

func (f *Fs) readMetaDataForPath(ctx context.Context, remote string) (*api.File, error) {
	leaf, directoryID, err := f.dirCache.FindPath(ctx, remote, false)
	if err != nil {
		if err == fs.ErrorDirNotFound {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	var info *api.File
	found, err := f.listAll(ctx, directoryID, false, true, func(item *api.File) bool {
		if item.ServerFilename == leaf {
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
	found, err = f.listAll(ctx, pathID, true, false, func(item *api.File) bool {
		if item.ServerFilename == leaf {
			pathIDOut = item.Path
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (string, error) {
	if err := f.ensureToken(ctx, false); err != nil {
		return "", err
	}
	newPath := joinBaidu(pathID, f.opt.Enc.FromStandardName(leaf))
	opts := f.withToken(&rest.Opts{Method: http.MethodPost, Path: "/xpan/file", Parameters: url.Values{}, MultipartParams: url.Values{}})
	opts.Parameters.Set("method", "create")
	opts.MultipartParams.Set("path", newPath)
	opts.MultipartParams.Set("isdir", "1")
	opts.MultipartParams.Set("rtype", "1")
	var info api.CreateResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	if info.Errno != 0 && info.Errno != -8 {
		return "", errnoErr(info.Errno, "mkdir")
	}
	if info.Path != "" {
		return info.Path, nil
	}
	return newPath, nil
}

func (f *Fs) itemToDirEntry(ctx context.Context, remote string, info *api.File) (fs.DirEntry, error) {
	if info.IsDir() {
		f.dirCache.Put(remote, info.Path)
		d := fs.NewDir(remote, info.ModTime())
		d.SetID(info.Path)
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
	_, err = f.listAll(ctx, directoryID, false, false, func(info *api.File) bool {
		remote := path.Join(dir, info.ServerFilename)
		entry, err := f.itemToDirEntry(ctx, remote, info)
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
	o = &Object{fs: f, remote: remote, fsPath: joinBaidu(directoryID, leaf)}
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

func (f *Fs) filemanager(ctx context.Context, opera string, filelist string) error {
	if err := f.ensureToken(ctx, false); err != nil {
		return err
	}
	opts := f.withToken(&rest.Opts{Method: http.MethodPost, Path: "/xpan/file", Parameters: url.Values{}, MultipartParams: url.Values{}})
	opts.Parameters.Set("method", "filemanager")
	opts.Parameters.Set("opera", opera)
	opts.MultipartParams.Set("async", "0")
	opts.MultipartParams.Set("filelist", filelist)
	opts.MultipartParams.Set("ondup", "overwrite")
	var info api.ManagerResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	return errnoErr(info.Errno, opera)
}

func (f *Fs) purgeCheck(ctx context.Context, dir string, check bool) error {
	root := path.Join(f.root, dir)
	if root == "" && (f.opt.RootFolderPath == "" || f.opt.RootFolderPath == "/") {
		return errors.New("can't purge root directory")
	}
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	if check {
		found, err := f.listAll(ctx, dirID, false, false, func(*api.File) bool { return true })
		if err != nil {
			return err
		}
		if found {
			return fs.ErrorDirectoryNotEmpty
		}
	}
	payload, _ := json.Marshal([]string{dirID})
	if err := f.filemanager(ctx, "delete", string(payload)); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

func (f *Fs) Rmdir(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, true) }
func (f *Fs) Purge(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, false) }

func (f *Fs) About(ctx context.Context) (*fs.Usage, error) {
	if err := f.ensureToken(ctx, false); err != nil {
		return nil, err
	}
	opts := rest.Opts{Method: http.MethodGet, RootURL: api.QuotaURL, Parameters: url.Values{}}
	opts.Parameters.Set("access_token", f.tokenValue())
	opts.Parameters.Set("checkfree", "1")
	var info api.QuotaResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	if err := errnoErr(info.Errno, "quota"); err != nil {
		return nil, err
	}
	usage := &fs.Usage{
		Used:  fs.NewUsageValue(info.Used),
		Total: fs.NewUsageValue(info.Total),
	}
	free := info.Total - info.Used
	if free < 0 {
		free = 0
	}
	usage.Free = fs.NewUsageValue(free)
	return usage, nil
}

func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fs.ErrorCantMove
	}
	_, dstLeaf, dstDirectoryID, err := f.createObject(ctx, remote, srcObj.modTime, srcObj.size)
	if err != nil {
		return nil, err
	}
	dest := joinBaidu(dstDirectoryID, f.opt.Enc.FromStandardName(dstLeaf))
	payload, _ := json.Marshal([]map[string]string{{
		"path":    srcObj.fsPath,
		"dest":    dstDirectoryID,
		"newname": f.opt.Enc.FromStandardName(dstLeaf),
	}})
	if err := f.filemanager(ctx, "move", string(payload)); err != nil {
		return nil, err
	}
	srcObj.fs.dirCache.FlushDir(path.Dir(srcObj.remote))
	f.dirCache.FlushDir(path.Dir(remote))
	_ = dest
	return f.NewObject(ctx, remote)
}

func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	srcFs, ok := src.(*Fs)
	if !ok {
		return fs.ErrorCantDirMove
	}
	srcID, _, srcLeaf, dstDirectoryID, dstLeaf, err := f.dirCache.DirMove(ctx, srcFs.dirCache, srcFs.root, srcRemote, f.root, dstRemote)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal([]map[string]string{{
		"path":    srcID,
		"dest":    dstDirectoryID,
		"newname": f.opt.Enc.FromStandardName(dstLeaf),
	}})
	_ = srcLeaf
	if err := f.filemanager(ctx, "move", string(payload)); err != nil {
		return err
	}
	srcFs.dirCache.FlushDir(srcRemote)
	return nil
}

func (f *Fs) downloadURL(ctx context.Context, fsid string) (string, error) {
	if err := f.ensureToken(ctx, false); err != nil {
		return "", err
	}
	opts := f.withToken(&rest.Opts{Method: http.MethodGet, Path: "/xpan/multimedia", Parameters: url.Values{}})
	opts.Parameters.Set("method", "filemetas")
	opts.Parameters.Set("fsids", "["+fsid+"]")
	opts.Parameters.Set("dlink", "1")
	var info api.DownloadResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	if err := errnoErr(info.Errno, "filemetas"); err != nil {
		return "", err
	}
	if len(info.List) == 0 || info.List[0].Dlink == "" {
		return "", errors.New("baidu: empty download url")
	}
	u := info.List[0].Dlink
	if !strings.Contains(u, "access_token=") {
		if strings.Contains(u, "?") {
			u += "&access_token=" + f.tokenValue()
		} else {
			u += "?access_token=" + f.tokenValue()
		}
	}
	return u, nil
}

func (o *Object) setMetaData(info *api.File) error {
	if info.IsDir() {
		return fs.ErrorIsDir
	}
	o.id = info.ID()
	o.fsPath = info.Path
	o.size = info.Size
	o.md5sum = strings.ToLower(info.MD5)
	o.modTime = info.ModTime()
	return nil
}

func (o *Object) readMetaData(ctx context.Context) error {
	info, err := o.fs.readMetaDataForPath(ctx, o.remote)
	if err != nil {
		return err
	}
	return o.setMetaData(info)
}

func (o *Object) Fs() fs.Info                              { return o.fs }
func (o *Object) String() string                               { return o.remote }
func (o *Object) Remote() string                                { return o.remote }
func (o *Object) Size() int64                                 { return o.size }
func (o *Object) ModTime(_ context.Context) time.Time          { return o.modTime }
func (o *Object) SetModTime(context.Context, time.Time) error { return fs.ErrorCantSetModTime }
func (o *Object) Storable() bool                                { return true }
func (o *Object) ID() string                                    { return o.id }

func (o *Object) Hash(_ context.Context, t hash.Type) (string, error) {
	if t != hash.MD5 {
		return "", hash.ErrUnsupported
	}
	return o.md5sum, nil
}

func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	if o.id == "" {
		if err := o.readMetaData(ctx); err != nil {
			return nil, err
		}
	}
	fs.FixRangeOption(options, o.size)
	var resp *http.Response
	err := o.fs.pacer.Call(func() (bool, error) {
		targetURL, err := o.fs.downloadURL(ctx, o.id)
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
	payload, _ := json.Marshal([]string{o.fsPath})
	if err := o.fs.filemanager(ctx, "delete", string(payload)); err != nil {
		return err
	}
	o.fs.dirCache.FlushDir(path.Dir(o.remote))
	return nil
}

func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, _ ...fs.OpenOption) error {
	leaf, directoryID, err := o.fs.dirCache.FindPath(ctx, o.remote, true)
	if err != nil {
		return err
	}
	dest := joinBaidu(directoryID, o.fs.opt.Enc.FromStandardName(leaf))
	rw := multipart.NewRW()
	n, err := io.Copy(rw, in)
	if err != nil {
		_ = rw.Close()
		return err
	}
	defer func() { _ = rw.Close() }()
	if _, err := rw.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := o.fs.ensureToken(ctx, false); err != nil {
		return err
	}
	opts := o.fs.withToken(&rest.Opts{
		Method:               http.MethodPost,
		Path:                 "/xpan/file",
		Parameters:           url.Values{},
		MultipartParams:     url.Values{},
		MultipartContentName: "file",
		MultipartFileName:   leaf,
		Body:                 rw,
	})
	opts.Parameters.Set("method", "upload")
	opts.Parameters.Set("path", dest)
	opts.Parameters.Set("ondup", "overwrite")
	var info api.CreateResp
	err = o.fs.pacer.Call(func() (bool, error) {
		if _, err := rw.Seek(0, io.SeekStart); err != nil {
			return false, err
		}
		opts.Body = rw
		resp, err := o.fs.srv.CallJSON(ctx, opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if err := errnoErr(info.Errno, "upload"); err != nil {
		return err
	}
	o.fsPath = dest
	if info.FsID != 0 {
		o.id = strconv.FormatInt(info.FsID, 10)
	}
	o.size = n
	o.modTime = src.ModTime(ctx)
	return nil
}

var (
	_ fs.Fs              = (*Fs)(nil)
	_ fs.Purger          = (*Fs)(nil)
	_ fs.Mover           = (*Fs)(nil)
	_ fs.DirMover        = (*Fs)(nil)
	_ fs.Abouter         = (*Fs)(nil)
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
)
