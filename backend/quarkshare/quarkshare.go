// Package quarkshare provides an interface to a Quark Drive share link.
package quarkshare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/backend/quarkshare/api"
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
	defaultMinSleep = 200 * time.Millisecond
	maxSleep        = 4 * time.Second
	decayConstant   = 2
	rootID          = "0"
)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "quarkshare",
		Description: "Quark Share",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "cookie",
			Help: `Quark web cookie string.

Copy the Cookie header from a logged-in session at https://pan.quark.cn.
Share listing and download use this cookie with the share stoken.
`,
			Sensitive: true,
		}, {
			Name:      "share_key",
			Help:      "Share pwd_id from pan.quark.cn/s/<pwd_id>.",
			Required:  true,
			Sensitive: true,
		}, {
			Name:      "share_pwd",
			Help:      "Share passcode, if any.",
			Sensitive: true,
		}, {
			Name:     "endpoint",
			Help:     "Endpoint for the Quark share API.",
			Default:  api.DefaultRoot,
			Advanced: true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default: encoder.Display |
				encoder.EncodeZero |
				encoder.EncodeSlash |
				encoder.EncodeBackSlash |
				encoder.EncodeInvalidUtf8,
		}},
	})
}

// Options defines the configuration of this backend.
type Options struct {
	Cookie   string               `config:"cookie"`
	ShareKey string               `config:"share_key"`
	SharePwd string               `config:"share_pwd"`
	Endpoint string               `config:"endpoint"`
	Enc      encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a Quark share.
type Fs struct {
	name     string
	root     string
	opt      Options
	features *fs.Features
	srv      *rest.Client
	dl       *rest.Client
	pacer    *fs.Pacer
	dirCache *dircache.DirCache
	mu       sync.Mutex
	stoken   string
}

// Object describes a file in a Quark share.
type Object struct {
	fs      *Fs
	remote  string
	id      string
	dirID   string
	size    int64
	modTime time.Time
}

var retryErrorCodes = []int{429, 500, 502, 503, 504}

func parsePath(p string) string { return strings.Trim(p, "/") }

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
	var parsed api.BaseResp
	if len(body) > 0 {
		if err := json.Unmarshal(body, &parsed); err == nil && !parsed.OK() && parsed.Message != "" {
			return fmt.Errorf("quarkshare: %s", parsed.Message)
		}
	}
	return fmt.Errorf("HTTP error %v (%v) returned body: %q", resp.StatusCode, resp.Status, body)
}

func commonQuery() url.Values {
	v := url.Values{}
	v.Set("pr", "ucpro")
	v.Set("fr", "pc")
	return v
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opt.ShareKey) == "" {
		return nil, errors.New("quarkshare: share_key is required")
	}
	if opt.Endpoint == "" {
		opt.Endpoint = api.DefaultRoot
	}
	root = parsePath(root)
	client := fshttp.NewClient(ctx)
	f := &Fs{
		name:  name,
		root:  root,
		opt:   *opt,
		srv:   rest.NewClient(client).SetRoot(strings.TrimRight(opt.Endpoint, "/")),
		dl:    rest.NewClient(client),
		pacer: fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(defaultMinSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
	}
	f.srv.SetErrorHandler(errorHandler)
	f.srv.SetHeader("User-Agent", api.DefaultUA)
	f.srv.SetHeader("Referer", api.DefaultReferer)
	if opt.Cookie != "" {
		f.srv.SetHeader("Cookie", opt.Cookie)
		f.dl.SetHeader("Cookie", opt.Cookie)
	}
	f.dl.SetHeader("User-Agent", api.DefaultUA)
	f.dl.SetHeader("Referer", api.DefaultReferer)
	f.features = (&fs.Features{CanHaveEmptyDirectories: true}).Fill(ctx, f)
	f.dirCache = dircache.New(root, rootID, f)
	if err := f.ensureStoken(ctx); err != nil {
		return nil, fmt.Errorf("quarkshare login check failed: %w", err)
	}
	err = f.dirCache.FindRoot(ctx, false)
	if err != nil {
		newRoot, remote := dircache.SplitPath(root)
		tempF := *f
		tempF.dirCache = dircache.New(newRoot, rootID, &tempF)
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

func (f *Fs) ensureStoken(ctx context.Context) error {
	f.mu.Lock()
	if f.stoken != "" {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()
	opts := rest.Opts{Method: http.MethodPost, Path: "/share/sharepage/token", Parameters: commonQuery()}
	req := map[string]string{"pwd_id": f.opt.ShareKey, "passcode": f.opt.SharePwd}
	var info api.TokenResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if !info.OK() && info.Message != "" {
		return fmt.Errorf("quarkshare: %s", info.Message)
	}
	if info.Data.Stoken == "" {
		return errors.New("quarkshare: empty stoken")
	}
	f.mu.Lock()
	f.stoken = info.Data.Stoken
	f.mu.Unlock()
	return nil
}

func (f *Fs) Name() string             { return f.name }
func (f *Fs) Root() string             { return f.root }
func (f *Fs) String() string           { return fmt.Sprintf("Quark Share root '%s'", f.root) }
func (f *Fs) Features() *fs.Features   { return f.features }
func (f *Fs) Precision() time.Duration { return time.Millisecond }
func (f *Fs) Hashes() hash.Set         { return hash.NewHashSet(hash.MD5, hash.SHA1) }
func (f *Fs) DirCacheFlush()           { f.dirCache.ResetRoot() }

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

func (f *Fs) listAll(ctx context.Context, dirID string, directoriesOnly, filesOnly bool, fn listAllFn) (found bool, err error) {
	if err := f.ensureStoken(ctx); err != nil {
		return false, err
	}
	f.mu.Lock()
	stoken := f.stoken
	f.mu.Unlock()
	page := 1
	const size = 50
	for {
		opts := rest.Opts{Method: http.MethodPost, Path: "/share/sharepage/detail", Parameters: commonQuery()}
		req := map[string]any{
			"pwd_id":        f.opt.ShareKey,
			"stoken":        stoken,
			"pdir_fid":      dirID,
			"force":         0,
			"_page":         page,
			"_size":         size,
			"_fetch_banner": 0,
			"_fetch_share":  0,
		}
		var info api.DetailResp
		err := f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return false, err
		}
		if !info.OK() && info.Message != "" {
			return false, fmt.Errorf("quarkshare: list: %s", info.Message)
		}
		for i := range info.Data.List {
			item := &info.Data.List[i]
			item.FileName = html.UnescapeString(f.opt.Enc.ToStandardName(item.FileName))
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
		if page*size >= info.Metadata.Total || len(info.Data.List) == 0 {
			break
		}
		page++
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
		if item.FileName == leaf {
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
		if item.FileName == leaf {
			pathIDOut = item.ID()
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

func (f *Fs) CreateDir(context.Context, string, string) (string, error) {
	return "", errors.New("quarkshare: mkdir is not supported on a share")
}

func (f *Fs) itemToDirEntry(ctx context.Context, remote string, info *api.File) (fs.DirEntry, error) {
	if info.IsDir() {
		f.dirCache.Put(remote, info.ID())
		d := fs.NewDir(remote, info.ModTime())
		d.SetID(info.ID())
		d.SetParentID(info.Parent())
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
		remote := path.Join(dir, info.FileName)
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

func (f *Fs) Mkdir(context.Context, string) error {
	return errors.New("quarkshare: mkdir is not supported on a share")
}

func (f *Fs) Rmdir(context.Context, string) error {
	return errors.New("quarkshare: rmdir is not supported on a share")
}

func (f *Fs) Put(context.Context, io.Reader, fs.ObjectInfo, ...fs.OpenOption) (fs.Object, error) {
	return nil, errors.New("quarkshare: upload is not supported on a share")
}

func (f *Fs) downloadURL(ctx context.Context, id string) (string, error) {
	if err := f.ensureStoken(ctx); err != nil {
		return "", err
	}
	f.mu.Lock()
	stoken := f.stoken
	f.mu.Unlock()
	opts := rest.Opts{Method: http.MethodPost, Path: "/share/sharepage/download", Parameters: commonQuery()}
	req := map[string]any{
		"pwd_id": f.opt.ShareKey,
		"stoken": stoken,
		"fids":   []string{id},
	}
	var info api.DownloadResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	if !info.OK() && info.Message != "" {
		return "", fmt.Errorf("quarkshare: download: %s", info.Message)
	}
	if len(info.Data) == 0 || info.Data[0].DownloadURL == "" {
		return "", errors.New("quarkshare: empty download url")
	}
	return info.Data[0].DownloadURL, nil
}

func (o *Object) setMetaData(info *api.File) error {
	if info.IsDir() {
		return fs.ErrorIsDir
	}
	o.id = info.ID()
	o.dirID = info.Parent()
	o.size = info.Size
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

func (o *Object) Fs() fs.Info                                 { return o.fs }
func (o *Object) String() string                              { return o.remote }
func (o *Object) Remote() string                              { return o.remote }
func (o *Object) Size() int64                                 { return o.size }
func (o *Object) ModTime(_ context.Context) time.Time         { return o.modTime }
func (o *Object) SetModTime(context.Context, time.Time) error { return fs.ErrorCantSetModTime }
func (o *Object) Storable() bool                              { return true }
func (o *Object) ID() string                                  { return o.id }
func (o *Object) ParentID() string                            { return o.dirID }
func (o *Object) Hash(context.Context, hash.Type) (string, error) {
	return "", hash.ErrUnsupported
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

func (o *Object) Remove(context.Context) error {
	return errors.New("quarkshare: delete is not supported on a share")
}

func (o *Object) Update(context.Context, io.Reader, fs.ObjectInfo, ...fs.OpenOption) error {
	return errors.New("quarkshare: upload is not supported on a share")
}

var (
	_ fs.Fs              = (*Fs)(nil)
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
	_ fs.ParentIDer      = (*Object)(nil)
)
