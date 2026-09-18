// Package cloud189share provides an interface to a Cloud 189 share link.
package cloud189share

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

	"github.com/rclone/rclone/backend/cloud189share/api"
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
	defaultUA       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "cloud189share",
		Description: "China Telecom Cloud 189 Share",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name:      "share_code",
			Help:      "Share code from cloud.189.cn/t/<code> or web/share?code=.",
			Required:  true,
			Sensitive: true,
		}, {
			Name:      "share_pwd",
			Help:      "Share access code, if any.",
			Sensitive: true,
		}, {
			Name: "cookie",
			Help: `Optional Cloud 189 cookie.

Some shares need a logged-in cookie from https://cloud.189.cn to download.
`,
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:     "endpoint",
			Help:     "Endpoint for the Cloud 189 web API.",
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
	ShareCode string               `config:"share_code"`
	SharePwd  string               `config:"share_pwd"`
	Cookie    string               `config:"cookie"`
	Endpoint  string               `config:"endpoint"`
	Enc       encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a Cloud 189 share.
type Fs struct {
	name      string
	root      string
	opt       Options
	features  *fs.Features
	srv       *rest.Client
	dl        *rest.Client
	pacer     *fs.Pacer
	dirCache  *dircache.DirCache
	shareID   string
	shareMode string
}

// Object describes a file in a Cloud 189 share.
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
	return fmt.Errorf("HTTP error %v (%v) returned body: %q", resp.StatusCode, resp.Status, body)
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscan(s, &n)
	return n, err
}

func mustInt(s string) int64 {
	n, _ := parseInt64(s)
	return n
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opt.ShareCode) == "" {
		return nil, errors.New("cloud189share: share_code is required")
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
	f.srv.SetHeader("User-Agent", defaultUA)
	f.srv.SetHeader("Referer", "https://cloud.189.cn/")
	f.srv.SetHeader("Accept", "application/json;charset=UTF-8")
	f.dl.SetHeader("User-Agent", defaultUA)
	if opt.Cookie != "" {
		f.srv.SetHeader("Cookie", opt.Cookie)
		f.dl.SetHeader("Cookie", opt.Cookie)
	}
	f.features = (&fs.Features{CanHaveEmptyDirectories: true}).Fill(ctx, f)
	rootFolderID, err := f.initShare(ctx)
	if err != nil {
		return nil, err
	}
	f.dirCache = dircache.New(root, rootFolderID, f)
	err = f.dirCache.FindRoot(ctx, false)
	if err != nil {
		newRoot, remote := dircache.SplitPath(root)
		tempF := *f
		tempF.dirCache = dircache.New(newRoot, rootFolderID, &tempF)
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

func (f *Fs) initShare(ctx context.Context) (string, error) {
	opts := rest.Opts{Method: http.MethodGet, Path: "/api/open/share/getShareInfoByCodeV2.action", Parameters: url.Values{}}
	opts.Parameters.Set("shareCode", f.opt.ShareCode)
	var info api.ShareInfo
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	shareID := info.ShareID
	if f.opt.SharePwd != "" {
		opts = rest.Opts{Method: http.MethodGet, Path: "/api/open/share/checkAccessCode.action", Parameters: url.Values{}}
		opts.Parameters.Set("shareCode", f.opt.ShareCode)
		opts.Parameters.Set("accessCode", f.opt.SharePwd)
		var acc api.AccessResp
		err := f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, &opts, nil, &acc)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return "", err
		}
		if acc.ShareID != 0 {
			shareID = acc.ShareID
		}
	}
	if shareID == 0 {
		return "", errors.New("cloud189share: empty share id")
	}
	f.shareID = api.IDString(shareID)
	f.shareMode = fmt.Sprintf("%d", info.ShareMode)
	if info.FileID == 0 {
		return "", errors.New("cloud189share: empty share file id")
	}
	return api.IDString(info.FileID), nil
}

func (f *Fs) Name() string             { return f.name }
func (f *Fs) Root() string             { return f.root }
func (f *Fs) String() string           { return fmt.Sprintf("Cloud 189 Share root '%s'", f.root) }
func (f *Fs) Features() *fs.Features   { return f.features }
func (f *Fs) Precision() time.Duration { return time.Second }
func (f *Fs) Hashes() hash.Set         { return hash.Set(hash.None) }
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

type listAllFn func(name, id string, isDir bool, size int64, mod time.Time) bool

func (f *Fs) listAll(ctx context.Context, dirID string, directoriesOnly, filesOnly bool, fn listAllFn) (found bool, err error) {
	page := 1
	for {
		opts := rest.Opts{Method: http.MethodGet, Path: "/api/open/share/listShareDir.action", Parameters: url.Values{}}
		opts.Parameters.Set("pageSize", "60")
		opts.Parameters.Set("pageNum", fmt.Sprintf("%d", page))
		opts.Parameters.Set("fileId", dirID)
		opts.Parameters.Set("shareId", f.shareID)
		opts.Parameters.Set("shareMode", f.shareMode)
		opts.Parameters.Set("isFolder", "true")
		opts.Parameters.Set("iconOption", "5")
		opts.Parameters.Set("orderBy", "lastOpTime")
		opts.Parameters.Set("descending", "true")
		var info api.Files
		err := f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return false, err
		}
		if err := info.Err(); err != nil {
			return false, err
		}
		if !directoriesOnly {
			for i := range info.FileListAO.FileList {
				item := &info.FileListAO.FileList[i]
				name := f.opt.Enc.ToStandardName(item.Name)
				if fn(name, api.IDString(item.ID), false, item.Size, api.ParseCNTime(item.LastOpTime)) {
					return true, nil
				}
			}
		}
		if !filesOnly {
			for i := range info.FileListAO.FolderList {
				item := &info.FileListAO.FolderList[i]
				name := f.opt.Enc.ToStandardName(item.Name)
				if fn(name, api.IDString(item.ID), true, 0, api.ParseCNTime(item.LastOpTime)) {
					return true, nil
				}
			}
		}
		if len(info.FileListAO.FileList)+len(info.FileListAO.FolderList) < 60 {
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
	found, err := f.listAll(ctx, directoryID, false, true, func(name, id string, isDir bool, size int64, mod time.Time) bool {
		if !isDir && name == leaf {
			fid, _ := parseInt64(id)
			info = &api.File{ID: fid, Name: name, Size: size, LastOpTime: mod.Format("2006-01-02 15:04:05")}
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
	found, err = f.listAll(ctx, pathID, true, false, func(name, id string, isDir bool, _ int64, _ time.Time) bool {
		if isDir && name == leaf {
			pathIDOut = id
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

func (f *Fs) CreateDir(context.Context, string, string) (string, error) {
	return "", errors.New("cloud189share: mkdir is not supported on a share")
}

func (f *Fs) itemToDirEntry(ctx context.Context, remote, name, id string, isDir bool, size int64, mod time.Time) (fs.DirEntry, error) {
	if isDir {
		f.dirCache.Put(remote, id)
		d := fs.NewDir(remote, mod)
		d.SetID(id)
		return d, nil
	}
	return f.newObjectWithInfo(ctx, remote, &api.File{ID: mustInt(id), Name: name, Size: size, LastOpTime: mod.Format("2006-01-02 15:04:05")})
}

func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	directoryID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return nil, err
	}
	var iErr error
	_, err = f.listAll(ctx, directoryID, false, false, func(name, id string, isDir bool, size int64, mod time.Time) bool {
		remote := path.Join(dir, name)
		entry, err := f.itemToDirEntry(ctx, remote, name, id, isDir, size, mod)
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
	return errors.New("cloud189share: mkdir is not supported on a share")
}

func (f *Fs) Rmdir(context.Context, string) error {
	return errors.New("cloud189share: rmdir is not supported on a share")
}

func (f *Fs) Put(context.Context, io.Reader, fs.ObjectInfo, ...fs.OpenOption) (fs.Object, error) {
	return nil, errors.New("cloud189share: upload is not supported on a share")
}

func (f *Fs) downloadURL(ctx context.Context, id string) (string, error) {
	opts := rest.Opts{Method: http.MethodGet, Path: "/api/open/file/getFileDownloadUrl.action", Parameters: url.Values{}}
	opts.Parameters.Set("fileId", id)
	opts.Parameters.Set("shareId", f.shareID)
	var info api.DownResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	u := info.URL()
	if u == "" {
		return "", errors.New("cloud189share: empty download url")
	}
	if strings.HasPrefix(u, "//") {
		u = "https:" + u
	}
	if strings.Contains(u, "cloud.189.cn") || strings.Contains(u, "189.cn") {
		u = strings.Replace(u, "http://", "https://", 1)
	}
	return u, nil
}

func (o *Object) setMetaData(info *api.File) error {
	o.id = api.IDString(info.ID)
	o.size = info.Size
	o.modTime = api.ParseCNTime(info.LastOpTime)
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
	return errors.New("cloud189share: delete is not supported on a share")
}

func (o *Object) Update(context.Context, io.Reader, fs.ObjectInfo, ...fs.OpenOption) error {
	return errors.New("cloud189share: upload is not supported on a share")
}

var (
	_ fs.Fs              = (*Fs)(nil)
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
	_ fs.ParentIDer      = (*Object)(nil)
)
