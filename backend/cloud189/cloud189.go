// Package cloud189 provides an interface to China Telecom Cloud 189 using the web API.
package cloud189

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/rclone/rclone/backend/cloud189/api"
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
		Name:        "cloud189",
		Description: "China Telecom Cloud 189",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "cookie",
			Help: `Cloud 189 web cookie string.

Copy the Cookie header from a logged-in session at https://cloud.189.cn
(DevTools → Network).
`,
			Sensitive: true,
		}, {
			Name: "root_folder_id",
			Help: `ID of the root folder.

Leave blank to use the account root (-11).
`,
			Advanced:  true,
			Sensitive: true,
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
	Cookie       string               `config:"cookie"`
	RootFolderID string               `config:"root_folder_id"`
	Endpoint     string               `config:"endpoint"`
	Enc          encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote Cloud 189.
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

// Object describes a Cloud 189 object.
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

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opt.Cookie) == "" {
		return nil, errors.New("cloud189: cookie is required (copy it from cloud.189.cn after login)")
	}
	if opt.Endpoint == "" {
		opt.Endpoint = api.DefaultRoot
	}
	root = parsePath(root)
	client := fshttp.NewClient(ctx)
	rootFolderID := opt.RootFolderID
	if rootFolderID == "" {
		rootFolderID = api.RootFolder
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
	f.srv.SetHeader("User-Agent", defaultUA)
	f.srv.SetHeader("Referer", "https://cloud.189.cn/")
	f.srv.SetHeader("Accept", "application/json;charset=UTF-8")
	f.srv.SetHeader("Cookie", opt.Cookie)
	f.dl.SetHeader("User-Agent", defaultUA)
	f.dl.SetHeader("Cookie", opt.Cookie)
	f.features = (&fs.Features{CanHaveEmptyDirectories: true}).Fill(ctx, f)
	f.dirCache = dircache.New(root, rootFolderID, f)
	if _, err := f.capacity(ctx); err != nil {
		return nil, fmt.Errorf("cloud189 login check failed: %w", err)
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

func (f *Fs) capacity(ctx context.Context) (*api.CapacityResp, error) {
	opts := rest.Opts{Method: http.MethodGet, Path: "/api/portal/getUserSizeInfo.action"}
	var info api.CapacityResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return &info, nil
}

func (f *Fs) Name() string                 { return f.name }
func (f *Fs) Root() string                 { return f.root }
func (f *Fs) String() string                { return fmt.Sprintf("Cloud 189 root '%s'", f.root) }
func (f *Fs) Features() *fs.Features        { return f.features }
func (f *Fs) Precision() time.Duration       { return time.Second }
func (f *Fs) Hashes() hash.Set               { return hash.Set(hash.None) }
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

type listAllFn func(name, id string, isDir bool, size int64, mod time.Time) bool

func (f *Fs) listAll(ctx context.Context, dirID string, directoriesOnly, filesOnly bool, fn listAllFn) (found bool, err error) {
	page := 1
	for {
		opts := rest.Opts{Method: http.MethodGet, Path: "/api/open/file/listFiles.action", Parameters: url.Values{}}
		opts.Parameters.Set("pageSize", "60")
		opts.Parameters.Set("pageNum", fmt.Sprintf("%d", page))
		opts.Parameters.Set("mediaType", "0")
		opts.Parameters.Set("folderId", dirID)
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
		if info.FileListAO.Count == 0 && len(info.FileListAO.FolderList) == 0 && len(info.FileListAO.FileList) == 0 {
			break
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

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscan(s, &n)
	return n, err
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

func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (string, error) {
	opts := rest.Opts{Method: http.MethodPost, Path: "/api/open/file/createFolder.action", MultipartParams: url.Values{}}
	opts.MultipartParams.Set("parentFolderId", pathID)
	opts.MultipartParams.Set("folderName", f.opt.Enc.FromStandardName(leaf))
	var info api.CreateFolderResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	if info.ID != 0 {
		return api.IDString(info.ID), nil
	}
	id, found, err := f.FindLeaf(ctx, pathID, leaf)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("cloud189: mkdir succeeded but folder id missing")
	}
	return id, nil
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

func mustInt(s string) int64 {
	n, _ := parseInt64(s)
	return n
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

func (f *Fs) batchTask(ctx context.Context, taskType, targetFolderID string, fileID, fileName string, isFolder int) error {
	taskInfos, _ := json.Marshal([]map[string]any{{
		"fileId":   fileID,
		"fileName": fileName,
		"isFolder": isFolder,
	}})
	opts := rest.Opts{Method: http.MethodPost, Path: "/api/open/batch/createBatchTask.action", MultipartParams: url.Values{}}
	opts.MultipartParams.Set("type", taskType)
	opts.MultipartParams.Set("targetFolderId", targetFolderID)
	opts.MultipartParams.Set("taskInfos", string(taskInfos))
	var info struct {
		ResCode    int    `json:"res_code"`
		ResMessage string `json:"res_message"`
	}
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if info.ResCode != 0 {
		return fmt.Errorf("cloud189: %s: %s", taskType, info.ResMessage)
	}
	return nil
}

func (f *Fs) purgeCheck(ctx context.Context, dir string, check bool) error {
	root := path.Join(f.root, dir)
	if root == "" && (f.opt.RootFolderID == "" || f.opt.RootFolderID == api.RootFolder) {
		return errors.New("can't purge root directory")
	}
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	if check {
		found, err := f.listAll(ctx, dirID, false, false, func(string, string, bool, int64, time.Time) bool { return true })
		if err != nil {
			return err
		}
		if found {
			return fs.ErrorDirectoryNotEmpty
		}
	}
	leaf := path.Base(dir)
	if err := f.batchTask(ctx, "DELETE", "", dirID, leaf, 1); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

func (f *Fs) Rmdir(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, true) }
func (f *Fs) Purge(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, false) }

func (f *Fs) About(ctx context.Context) (*fs.Usage, error) {
	info, err := f.capacity(ctx)
	if err != nil {
		return nil, err
	}
	c := info.CloudCapacityInfo
	usage := &fs.Usage{
		Used:  fs.NewUsageValue(c.UsedSize),
		Total: fs.NewUsageValue(c.TotalSize),
		Free:  fs.NewUsageValue(c.FreeSize),
	}
	return usage, nil
}

func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fs.ErrorCantMove
	}
	srcLeaf, srcDirectoryID, err := srcObj.fs.dirCache.FindPath(ctx, srcObj.remote, false)
	if err != nil {
		return nil, err
	}
	_, dstLeaf, dstDirectoryID, err := f.createObject(ctx, remote, srcObj.modTime, srcObj.size)
	if err != nil {
		return nil, err
	}
	if srcLeaf != dstLeaf {
		opts := rest.Opts{Method: http.MethodPost, Path: "/api/open/file/renameFile.action", MultipartParams: url.Values{}}
		opts.MultipartParams.Set("fileId", srcObj.id)
		opts.MultipartParams.Set("destFileName", f.opt.Enc.FromStandardName(dstLeaf))
		err := f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, &opts, nil, nil)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return nil, err
		}
	}
	if srcDirectoryID != dstDirectoryID {
		if err := f.batchTask(ctx, "MOVE", dstDirectoryID, srcObj.id, dstLeaf, 0); err != nil {
			return nil, err
		}
	}
	srcObj.fs.dirCache.FlushDir(path.Dir(srcObj.remote))
	f.dirCache.FlushDir(path.Dir(remote))
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
	if srcLeaf != dstLeaf {
		opts := rest.Opts{Method: http.MethodPost, Path: "/api/open/file/renameFolder.action", MultipartParams: url.Values{}}
		opts.MultipartParams.Set("folderId", srcID)
		opts.MultipartParams.Set("destFolderName", f.opt.Enc.FromStandardName(dstLeaf))
		err := f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, &opts, nil, nil)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return err
		}
	}
	if err := f.batchTask(ctx, "MOVE", dstDirectoryID, srcID, dstLeaf, 1); err != nil {
		return err
	}
	srcFs.dirCache.FlushDir(srcRemote)
	return nil
}

func (f *Fs) downloadURL(ctx context.Context, id string) (string, error) {
	opts := rest.Opts{Method: http.MethodGet, Path: "/api/portal/getFileInfo.action", Parameters: url.Values{}}
	opts.Parameters.Set("fileId", id)
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
		return "", errors.New("cloud189: empty download url")
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

func (o *Object) Fs() fs.Info                              { return o.fs }
func (o *Object) String() string                               { return o.remote }
func (o *Object) Remote() string                                { return o.remote }
func (o *Object) Size() int64                                 { return o.size }
func (o *Object) ModTime(_ context.Context) time.Time          { return o.modTime }
func (o *Object) SetModTime(context.Context, time.Time) error { return fs.ErrorCantSetModTime }
func (o *Object) Storable() bool                                { return true }
func (o *Object) ID() string                                    { return o.id }
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

func (o *Object) Remove(ctx context.Context) error {
	if o.id == "" {
		if err := o.readMetaData(ctx); err != nil {
			return err
		}
	}
	if err := o.fs.batchTask(ctx, "DELETE", "", o.id, path.Base(o.remote), 0); err != nil {
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
	o.dirID = directoryID
	opts := rest.Opts{
		Method:               http.MethodPost,
		RootURL:              "https://hb02.upload.cloud.189.cn/v1/DCIWebUploadAction",
		MultipartParams:     url.Values{},
		MultipartContentName: "Filedata",
		MultipartFileName:   o.fs.opt.Enc.FromStandardName(leaf),
		Body:                 in,
		ExtraHeaders:         map[string]string{"Cookie": o.fs.opt.Cookie, "User-Agent": defaultUA},
	}
	opts.MultipartParams.Set("parentId", directoryID)
	opts.MultipartParams.Set("opertype", "1")
	opts.MultipartParams.Set("fname", o.fs.opt.Enc.FromStandardName(leaf))
	err = o.fs.pacer.Call(func() (bool, error) {
		resp, err := o.fs.srv.CallJSON(ctx, &opts, nil, nil)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	o.size = src.Size()
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
	_ fs.ParentIDer      = (*Object)(nil)
)
