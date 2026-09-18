// Package openlistshare provides an interface to OpenList Share.
package openlistshare

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
	"time"

	"github.com/rclone/rclone/backend/openlistshare/api"
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
	rootID          = "/"
)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "openlistshare",
		Description: "OpenList Share",
		NewFs:       NewFs,
		Options: []fs.Option{
			{
				Name:      "share_key",
				Help:      `Share key or share id.`,
				Required:  true,
				Sensitive: true,
			},
			{
				Name:      "share_pwd",
				Help:      `Share password, if any.`,
				Sensitive: true,
			},
			{
				Name:     "endpoint",
				Help:     `API origin.`,
				Advanced: true,
				Default:  "http://127.0.0.1:5244",
			},
			{
				Name:     config.ConfigEncoding,
				Help:     config.ConfigEncodingHelp,
				Advanced: true,
				Default: encoder.Display |
					encoder.EncodeZero |
					encoder.EncodeSlash |
					encoder.EncodeBackSlash |
					encoder.EncodeInvalidUtf8,
			},
		},
	})
}

// Options defines the configuration of this backend.
type Options struct {
	ShareKey string               `config:"share_key"`
	SharePwd string               `config:"share_pwd"`
	Endpoint string               `config:"endpoint"`
	Enc      encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote OpenList Share.
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

// Object describes a OpenList Share object.
type Object struct {
	fs      *Fs
	remote  string
	id      string
	dirID   string
	size    int64
	modTime time.Time
	md5sum  string
	dURL    string
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

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opt.ShareKey) == "" {
		return nil, errors.New("openlistshare: share_key is required")
	}

	if strings.TrimSpace(opt.Endpoint) == "" {
		opt.Endpoint = api.DefaultRoot
	}
	root = parsePath(root)
	client := fshttp.NewClient(ctx)
	rootFolderID := rootID

	f := &Fs{
		name:  name,
		root:  root,
		opt:   *opt,
		srv:   rest.NewClient(client).SetRoot(strings.TrimRight(opt.Endpoint, "/")),
		dl:    rest.NewClient(client),
		pacer: fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(defaultMinSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		token: "",
	}
	f.srv.SetErrorHandler(errorHandler)

	f.features = (&fs.Features{CanHaveEmptyDirectories: true}).Fill(ctx, f)
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

func (f *Fs) Name() string             { return f.name }
func (f *Fs) Root() string             { return f.root }
func (f *Fs) String() string           { return fmt.Sprintf("OpenList Share root '%s'", f.root) }
func (f *Fs) Features() *fs.Features   { return f.features }
func (f *Fs) Precision() time.Duration { return time.Second }
func (f *Fs) Hashes() hash.Set         { return hash.NewHashSet(hash.MD5) }
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
	opts := rest.Opts{Method: http.MethodGet, Path: "/api/fs/list", Parameters: url.Values{}}
	opts.Parameters.Set("path", dirID)

	var req any
	req = map[string]any{"path": "/@s/" + f.opt.ShareKey + dirID, "password": f.opt.SharePwd, "page": 1, "per_page": 0}

	var raw json.RawMessage
	err = f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, req, &raw)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return false, err
	}
	var info api.ListResp
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &info.Items); err != nil {
			return false, err
		}
	} else if len(raw) > 0 {
		if err := json.Unmarshal(raw, &info); err != nil {
			return false, err
		}
		info.Normalize()
	}
	for i := range info.Items {
		item := &info.Items[i]
		name := f.opt.Enc.ToStandardName(item.DisplayName())
		item.SetName(name)
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
		if item.DisplayName() == leaf {
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
		if item.DisplayName() == leaf {
			pathIDOut = item.ID()
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (string, error) {
	opts := rest.Opts{Method: http.MethodPost, Path: "/api/fs/list"}
	req := map[string]any{"parent_id": pathID, "name": f.opt.Enc.FromStandardName(leaf)}
	var info api.DirResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	if info.ID() != "" {
		return info.ID(), nil
	}
	return joinAPI(pathID, leaf), nil
}

func (f *Fs) itemToDirEntry(ctx context.Context, remote string, info *api.File) (fs.DirEntry, error) {
	if info.IsDir() {
		f.dirCache.Put(remote, info.ID())
		d := fs.NewDir(remote, info.ModTime())
		d.SetID(info.ID())
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
		remote := path.Join(dir, info.DisplayName())
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
	o = &Object{fs: f, remote: remote, dirID: directoryID}
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

func (f *Fs) deleteIDs(ctx context.Context, ids ...string) error {
	opts := rest.Opts{Method: http.MethodPost, Path: "/api/fs/list"}
	req := map[string]any{"ids": ids}
	var info api.BaseResp
	return f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
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
		found, err := f.listAll(ctx, dirID, false, false, func(*api.File) bool { return true })
		if err != nil {
			return err
		}
		if found {
			return fs.ErrorDirectoryNotEmpty
		}
	}
	if err := f.deleteIDs(ctx, dirID); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

func (f *Fs) Rmdir(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, true) }
func (f *Fs) Purge(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, false) }

func (f *Fs) moveRename(ctx context.Context, src *Object, dstLeaf, dstDirectoryID string) error {
	return fmt.Errorf("%s: move is not implemented yet", f.Name())
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
	if err := f.moveRename(ctx, srcObj, dstLeaf, dstDirectoryID); err != nil {
		return nil, err
	}
	srcObj.fs.dirCache.FlushDir(path.Dir(srcObj.remote))
	f.dirCache.FlushDir(path.Dir(remote))
	return f.NewObject(ctx, remote)
}

func (f *Fs) downloadURL(ctx context.Context, o *Object) (string, error) {
	if o.dURL != "" {
		return o.dURL, nil
	}
	return "", errors.New("empty download url")
}

func (o *Object) setMetaData(info *api.File) error {
	if info.IsDir() {
		return fs.ErrorIsDir
	}
	o.id = info.ID()
	o.dirID = info.Parent()
	o.size = info.Size()
	o.modTime = info.ModTime()
	o.md5sum = info.MD5()
	o.dURL = info.DownloadURL()
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
		targetURL, err := o.fs.downloadURL(ctx, o)
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
	if o.id == "" {
		if err := o.readMetaData(ctx); err != nil {
			return err
		}
	}
	if err := o.fs.deleteIDs(ctx, o.id); err != nil {
		return err
	}
	o.fs.dirCache.FlushDir(path.Dir(o.remote))
	return nil
}

func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, _ ...fs.OpenOption) error {
	return fmt.Errorf("openlistshare: upload is not implemented yet")
}

var (
	_ fs.Fs              = (*Fs)(nil)
	_ fs.Purger          = (*Fs)(nil)
	_ fs.Mover           = (*Fs)(nil)
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
	_                    = json.Marshal
	_                    = url.Values{}
	_                    = strconv.Itoa
)
