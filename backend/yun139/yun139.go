// Package yun139 provides an interface to China Mobile Yun 139 using the web API.
package yun139

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/rclone/rclone/backend/yun139/api"
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
	rootID          = "root"
)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "yun139",
		Description: "China Mobile Yun 139",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "authorization",
			Help: `Authorization header from a logged-in yun.139.com session.

Copy the Authorization request header from DevTools → Network after
opening https://yun.139.com.
`,
			Sensitive: true,
		}, {
			Name: "account",
			Help: `Account name, usually the phone number of the 139 Yun account.
`,
			Sensitive: true,
		}, {
			Name: "root_folder_id",
			Help: `ID of the root catalog.

Leave blank to use the account root.
`,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name:     "endpoint",
			Help:     "Endpoint for the Yun 139 API.",
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
	Authorization string               `config:"authorization"`
	Account       string               `config:"account"`
	RootFolderID  string               `config:"root_folder_id"`
	Endpoint      string               `config:"endpoint"`
	Enc           encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote Yun 139.
type Fs struct {
	name     string
	root     string
	opt      Options
	features *fs.Features
	srv      *rest.Client
	dl       *rest.Client
	pacer    *fs.Pacer
	dirCache *dircache.DirCache
}

// Object describes a Yun 139 object.
type Object struct {
	fs      *Fs
	remote  string
	id      string
	size    int64
	md5sum  string
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

func (f *Fs) accountInfo() map[string]any {
	return map[string]any{"account": f.opt.Account, "accountType": 1}
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opt.Authorization) == "" || strings.TrimSpace(opt.Account) == "" {
		return nil, errors.New("yun139: authorization and account are required (copy them from yun.139.com after login)")
	}
	if opt.Endpoint == "" {
		opt.Endpoint = api.DefaultRoot
	}
	root = parsePath(root)
	client := fshttp.NewClient(ctx)
	rootFolderID := opt.RootFolderID
	if rootFolderID == "" {
		rootFolderID = rootID
	}
	f := &Fs{
		name:  name,
		root:  root,
		opt:   *opt,
		srv:   rest.NewClient(client).SetRoot(strings.TrimRight(opt.Endpoint, "/")),
		dl:    rest.NewClient(client),
		pacer: fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(defaultMinSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
	}
	f.srv.SetErrorHandler(errorHandler)
	f.srv.SetHeader("Authorization", opt.Authorization)
	f.srv.SetHeader("User-Agent", defaultUA)
	f.srv.SetHeader("Referer", "https://yun.139.com/w/")
	f.srv.SetHeader("Origin", "https://yun.139.com")
	f.features = (&fs.Features{CanHaveEmptyDirectories: true}).Fill(ctx, f)
	f.dirCache = dircache.New(root, rootFolderID, f)
	if _, err := f.listAll(ctx, rootFolderID, false, false, func(*api.Content, *api.Catalog) bool { return true }); err != nil {
		return nil, fmt.Errorf("yun139 login check failed: %w", err)
	}
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

func (f *Fs) Name() string                 { return f.name }
func (f *Fs) Root() string                 { return f.root }
func (f *Fs) String() string                { return fmt.Sprintf("Yun 139 root '%s'", f.root) }
func (f *Fs) Features() *fs.Features        { return f.features }
func (f *Fs) Precision() time.Duration       { return time.Second }
func (f *Fs) Hashes() hash.Set               { return hash.Set(hash.MD5) }
func (f *Fs) DirCacheFlush()                { f.dirCache.ResetRoot() }

func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, info *api.Content) (fs.Object, error) {
	o := &Object{fs: f, remote: remote}
	if info != nil {
		return o, o.setMetaData(info)
	}
	return o, o.readMetaData(ctx)
}

func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return f.newObjectWithInfo(ctx, remote, nil)
}

type listAllFn func(*api.Content, *api.Catalog) bool

func (f *Fs) listAll(ctx context.Context, dirID string, directoriesOnly, filesOnly bool, fn listAllFn) (found bool, err error) {
	catalogID := dirID
	if catalogID == rootID {
		catalogID = ""
	}
	start := 0
	limit := 100
	for {
		req := map[string]any{
			"catalogID":          catalogID,
			"sortDirection":      1,
			"startNumber":        start + 1,
			"endNumber":          start + limit,
			"filterType":         0,
			"catalogSortType":    0,
			"contentSortType":    0,
			"commonAccountInfo":  f.accountInfo(),
		}
		opts := rest.Opts{Method: http.MethodPost, Path: "/orchestration/personalCloud/catalog/v1.0/getDisk"}
		var info api.DiskResp
		err := f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return false, err
		}
		if !info.Success && info.Code != "" && info.Code != "0" && info.Code != "S_OK" {
			return false, fmt.Errorf("yun139: %s", info.Message)
		}
		res := info.Data.GetDiskResult
		if !filesOnly {
			for i := range res.CatalogList {
				item := &res.CatalogList[i]
				item.CatalogName = f.opt.Enc.ToStandardName(item.CatalogName)
				if fn(nil, item) {
					return true, nil
				}
			}
		}
		if !directoriesOnly {
			for i := range res.ContentList {
				item := &res.ContentList[i]
				item.ContentName = f.opt.Enc.ToStandardName(item.ContentName)
				if fn(item, nil) {
					return true, nil
				}
			}
		}
		if start+limit >= res.NodeCount || (len(res.CatalogList)+len(res.ContentList) == 0) {
			break
		}
		start += limit
	}
	return false, nil
}

func (f *Fs) readMetaDataForPath(ctx context.Context, remote string) (*api.Content, error) {
	leaf, directoryID, err := f.dirCache.FindPath(ctx, remote, false)
	if err != nil {
		if err == fs.ErrorDirNotFound {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	var info *api.Content
	found, err := f.listAll(ctx, directoryID, false, true, func(item *api.Content, _ *api.Catalog) bool {
		if item != nil && item.ContentName == leaf {
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
	found, err = f.listAll(ctx, pathID, true, false, func(_ *api.Content, folder *api.Catalog) bool {
		if folder != nil && folder.CatalogName == leaf {
			pathIDOut = folder.CatalogID
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (string, error) {
	parent := pathID
	if parent == rootID {
		parent = ""
	}
	req := map[string]any{
		"createCatalogExtReq": map[string]any{
			"parentCatalogID": parent,
			"newCatalogName":  f.opt.Enc.FromStandardName(leaf),
			"commonAccountInfo": f.accountInfo(),
		},
	}
	opts := rest.Opts{Method: http.MethodPost, Path: "/orchestration/personalCloud/catalog/v1.0/createCatalogExt"}
	var info api.BaseResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	id, found, err := f.FindLeaf(ctx, pathID, leaf)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("yun139: mkdir succeeded but folder id missing")
	}
	return id, nil
}

func (f *Fs) itemToDirEntry(ctx context.Context, remote string, file *api.Content, folder *api.Catalog) (fs.DirEntry, error) {
	if folder != nil {
		f.dirCache.Put(remote, folder.CatalogID)
		d := fs.NewDir(remote, api.ParseTime(folder.UpdateTime))
		d.SetID(folder.CatalogID)
		return d, nil
	}
	return f.newObjectWithInfo(ctx, remote, file)
}

func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	directoryID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return nil, err
	}
	var iErr error
	_, err = f.listAll(ctx, directoryID, false, false, func(file *api.Content, folder *api.Catalog) bool {
		name := ""
		if folder != nil {
			name = folder.CatalogName
		} else if file != nil {
			name = file.ContentName
		}
		remote := path.Join(dir, name)
		entry, err := f.itemToDirEntry(ctx, remote, file, folder)
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

func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	_, err := f.dirCache.FindDir(ctx, dir, true)
	return err
}

func (f *Fs) Put(context.Context, io.Reader, fs.ObjectInfo, ...fs.OpenOption) (fs.Object, error) {
	return nil, errors.New("yun139: upload is not implemented yet")
}

func (f *Fs) batchDelete(ctx context.Context, id string, isFolder bool) error {
	taskType := 2
	if isFolder {
		taskType = 2
	}
	contentIDs := []string{}
	catalogIDs := []string{}
	if isFolder {
		catalogIDs = []string{id}
	} else {
		contentIDs = []string{id}
	}
	req := map[string]any{
		"createBatchOprTaskReq": map[string]any{
			"taskType": taskType,
			"actionType": 201,
			"taskInfo": map[string]any{
				"contentInfoList": contentIDs,
				"catalogInfoList": catalogIDs,
				"newCatalogID":    "",
			},
			"commonAccountInfo": f.accountInfo(),
		},
	}
	_ = taskType
	opts := rest.Opts{Method: http.MethodPost, Path: "/orchestration/personalCloud/batchOprTask/v1.0/createBatchOprTask"}
	var info api.BaseResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if !info.Success && info.Message != "" && info.Code != "S_OK" && info.Code != "0" && info.Code != "" {
		return fmt.Errorf("yun139: delete: %s", info.Message)
	}
	return nil
}

func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	found, err := f.listAll(ctx, dirID, false, false, func(*api.Content, *api.Catalog) bool { return true })
	if err != nil {
		return err
	}
	if found {
		return fs.ErrorDirectoryNotEmpty
	}
	if err := f.batchDelete(ctx, dirID, true); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

func (f *Fs) downloadURL(ctx context.Context, id string) (string, error) {
	req := map[string]any{
		"contentID": id,
		"commonAccountInfo": f.accountInfo(),
	}
	opts := rest.Opts{Method: http.MethodPost, Path: "/orchestration/personalCloud/uploadAndDownload/v1.0/downloadRequest"}
	var info api.DownloadResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	if info.Data.DownloadURL == "" {
		return "", errors.New("yun139: empty download url")
	}
	return info.Data.DownloadURL, nil
}

func (o *Object) setMetaData(info *api.Content) error {
	o.id = info.ContentID
	o.size = info.ContentSize
	o.md5sum = strings.ToLower(info.Digest)
	o.modTime = api.ParseTime(info.UpdateTime)
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
	if o.id == "" {
		if err := o.readMetaData(ctx); err != nil {
			return err
		}
	}
	if err := o.fs.batchDelete(ctx, o.id, false); err != nil {
		return err
	}
	o.fs.dirCache.FlushDir(path.Dir(o.remote))
	return nil
}

func (o *Object) Update(context.Context, io.Reader, fs.ObjectInfo, ...fs.OpenOption) error {
	return errors.New("yun139: upload is not implemented yet")
}

var (
	_ fs.Fs              = (*Fs)(nil)
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
)
