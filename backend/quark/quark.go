// Package quark provides an interface to Quark Drive using the web API.
package quark

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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/backend/quark/api"
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
	rootID          = "0"
	ossUserAgent    = "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit"
	defaultPartSize = 4 << 20
)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "quark",
		Description: "Quark Drive",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "cookie",
			Help: `Quark web cookie string.

Copy the Cookie header from a logged-in session at https://pan.quark.cn
(DevTools → Network). A string containing __uid and __puus is enough.
`,
			Sensitive: true,
		}, {
			Name: "root_folder_id",
			Help: `ID of the root folder.

Leave blank to use the account root.
`,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name:     "user_agent",
			Help:     "HTTP user agent sent to Quark.",
			Advanced: true,
		}, {
			Name:     "endpoint",
			Help:     "Endpoint for the Quark Drive API.",
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
	UserAgent    string               `config:"user_agent"`
	Endpoint     string               `config:"endpoint"`
	Enc          encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote Quark Drive.
type Fs struct {
	name     string
	root     string
	opt      Options
	features *fs.Features
	srv      *rest.Client
	dl       *rest.Client
	pacer    *fs.Pacer
	dirCache *dircache.DirCache
	m        configmap.Mapper
	cookieMu sync.Mutex
}

// Object describes a Quark object.
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
			return fmt.Errorf("quark: %s", parsed.Message)
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
	if strings.TrimSpace(opt.Cookie) == "" {
		return nil, errors.New("quark: cookie is required (copy it from pan.quark.cn after login)")
	}
	if opt.Endpoint == "" {
		opt.Endpoint = api.DefaultRoot
	}
	if opt.UserAgent == "" {
		opt.UserAgent = api.DefaultUA
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
		m:     m,
		srv:   rest.NewClient(client).SetRoot(strings.TrimRight(opt.Endpoint, "/")),
		dl:    rest.NewClient(client),
		pacer: fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(defaultMinSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
	}
	f.srv.SetErrorHandler(errorHandler)
	f.srv.SetHeader("User-Agent", opt.UserAgent)
	f.srv.SetHeader("Referer", api.DefaultReferer)
	f.srv.SetHeader("Cookie", opt.Cookie)
	f.dl.SetHeader("User-Agent", opt.UserAgent)
	f.dl.SetHeader("Referer", api.DefaultReferer)
	f.dl.SetHeader("Cookie", opt.Cookie)
	f.features = (&fs.Features{CanHaveEmptyDirectories: true}).Fill(ctx, f)
	f.dirCache = dircache.New(root, rootFolderID, f)
	if err := f.ping(ctx); err != nil {
		return nil, fmt.Errorf("quark login check failed: %w", err)
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

func (f *Fs) ping(ctx context.Context) error {
	opts := rest.Opts{Method: http.MethodGet, Path: "/config", Parameters: commonQuery()}
	var info api.BaseResp
	err := f.callJSON(ctx, &opts, nil, &info)
	if err != nil {
		return err
	}
	if !info.OK() && info.Message != "" {
		return fmt.Errorf("quark: %s", info.Message)
	}
	return nil
}

func (f *Fs) callJSON(ctx context.Context, opts *rest.Opts, request, response any) error {
	return f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, opts, request, response)
		f.harvestCookies(resp)
		return shouldRetry(ctx, resp, err)
	})
}

func (f *Fs) harvestCookies(resp *http.Response) {
	if resp == nil {
		return
	}
	f.cookieMu.Lock()
	defer f.cookieMu.Unlock()
	cookie := f.opt.Cookie
	changed := false
	for _, c := range resp.Cookies() {
		if c.Name == "__puus" || c.Name == "__pus" {
			cookie = setCookieValue(cookie, c.Name, c.Value)
			changed = true
		}
	}
	if !changed {
		return
	}
	f.opt.Cookie = cookie
	f.srv.SetHeader("Cookie", cookie)
	f.dl.SetHeader("Cookie", cookie)
	if f.m != nil {
		f.m.Set("cookie", cookie)
	}
}

func setCookieValue(header, name, value string) string {
	parts := strings.Split(header, ";")
	found := false
	for i, part := range parts {
		part = strings.TrimSpace(part)
		n, _, ok := strings.Cut(part, "=")
		if ok && strings.TrimSpace(n) == name {
			parts[i] = name + "=" + value
			found = true
		} else {
			parts[i] = part
		}
	}
	if !found {
		return strings.TrimSpace(header) + "; " + name + "=" + value
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(parts, "; ")
}

func (f *Fs) Name() string             { return f.name }
func (f *Fs) Root() string             { return f.root }
func (f *Fs) String() string           { return fmt.Sprintf("Quark Drive root '%s'", f.root) }
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
	page := 1
	const size = 50
	for {
		opts := rest.Opts{Method: http.MethodGet, Path: "/file/sort", Parameters: commonQuery()}
		opts.Parameters.Set("pdir_fid", dirID)
		opts.Parameters.Set("_page", strconv.Itoa(page))
		opts.Parameters.Set("_size", strconv.Itoa(size))
		var info api.SortResp
		err := f.callJSON(ctx, &opts, nil, &info)
		if err != nil {
			return false, err
		}
		if !info.OK() && info.Message != "" {
			return false, fmt.Errorf("quark: list: %s", info.Message)
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

func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (string, error) {
	opts := rest.Opts{Method: http.MethodPost, Path: "/file", Parameters: commonQuery()}
	req := map[string]any{
		"dir_init_lock": false,
		"dir_path":      "",
		"file_name":     f.opt.Enc.FromStandardName(leaf),
		"pdir_fid":      pathID,
	}
	var info api.DirResp
	err := f.callJSON(ctx, &opts, &req, &info)
	if err != nil {
		return "", err
	}
	if info.Data.Fid != "" {
		return info.Data.Fid, nil
	}
	id, found, err := f.FindLeaf(ctx, pathID, leaf)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("quark: mkdir succeeded but folder id missing")
	}
	return id, nil
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
	opts := rest.Opts{Method: http.MethodPost, Path: "/file/delete", Parameters: commonQuery()}
	req := map[string]any{"action_type": 1, "filelist": ids}
	var info api.BaseResp
	err := f.callJSON(ctx, &opts, &req, &info)
	if err != nil {
		return err
	}
	if !info.OK() && info.Message != "" {
		return fmt.Errorf("quark: delete: %s", info.Message)
	}
	return nil
}

func (f *Fs) purgeCheck(ctx context.Context, dir string, check bool) error {
	root := path.Join(f.root, dir)
	if root == "" && (f.opt.RootFolderID == "" || f.opt.RootFolderID == rootID) {
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

func (f *Fs) rename(ctx context.Context, id, newLeaf string) error {
	opts := rest.Opts{Method: http.MethodPost, Path: "/file/rename", Parameters: commonQuery()}
	req := map[string]any{"fid": id, "file_name": f.opt.Enc.FromStandardName(newLeaf)}
	var info api.BaseResp
	err := f.callJSON(ctx, &opts, &req, &info)
	if err != nil {
		return err
	}
	if !info.OK() && info.Message != "" {
		return fmt.Errorf("quark: rename: %s", info.Message)
	}
	return nil
}

func (f *Fs) move(ctx context.Context, id, newDirID string) error {
	opts := rest.Opts{Method: http.MethodPost, Path: "/file/move", Parameters: commonQuery()}
	req := map[string]any{"action_type": 1, "to_pdir_fid": newDirID, "filelist": []string{id}}
	var info api.BaseResp
	err := f.callJSON(ctx, &opts, &req, &info)
	if err != nil {
		return err
	}
	if !info.OK() && info.Message != "" {
		return fmt.Errorf("quark: move: %s", info.Message)
	}
	return nil
}

func (f *Fs) moveTo(ctx context.Context, id, srcLeaf, dstLeaf, srcDirectoryID, dstDirectoryID string) error {
	if srcLeaf != dstLeaf {
		if err := f.rename(ctx, id, dstLeaf); err != nil {
			return err
		}
	}
	if srcDirectoryID != dstDirectoryID {
		if err := f.move(ctx, id, dstDirectoryID); err != nil {
			return err
		}
	}
	return nil
}

func (f *Fs) downloadURL(ctx context.Context, id string) (string, error) {
	opts := rest.Opts{Method: http.MethodPost, Path: "/file/download", Parameters: commonQuery()}
	req := map[string]any{"fids": []string{id}}
	var info api.DownloadResp
	err := f.callJSON(ctx, &opts, &req, &info)
	if err != nil {
		return "", err
	}
	if len(info.Data) == 0 || info.Data[0].DownloadURL == "" {
		return "", errors.New("quark: empty download url")
	}
	return info.Data[0].DownloadURL, nil
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
	if err := f.moveTo(ctx, srcObj.id, srcLeaf, dstLeaf, srcDirectoryID, dstDirectoryID); err != nil {
		return nil, err
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
	srcID, srcDirectoryID, srcLeaf, dstDirectoryID, dstLeaf, err := f.dirCache.DirMove(ctx, srcFs.dirCache, srcFs.root, srcRemote, f.root, dstRemote)
	if err != nil {
		return err
	}
	if err := f.moveTo(ctx, srcID, srcLeaf, dstLeaf, srcDirectoryID, dstDirectoryID); err != nil {
		return err
	}
	srcFs.dirCache.FlushDir(srcRemote)
	return nil
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
	leaf, directoryID, err := o.fs.dirCache.FindPath(ctx, o.remote, true)
	if err != nil {
		return err
	}
	o.dirID = directoryID
	rw := multipart.NewRW()
	hasher, err := hash.NewMultiHasherTypes(hash.NewHashSet(hash.MD5, hash.SHA1))
	if err != nil {
		return err
	}
	n, err := io.Copy(rw, io.TeeReader(in, hasher))
	if err != nil {
		_ = rw.Close()
		return err
	}
	defer func() { _ = rw.Close() }()
	if _, err := rw.Seek(0, io.SeekStart); err != nil {
		return err
	}
	sums := hasher.Sums()
	now := time.Now().UnixMilli()
	preOpts := rest.Opts{Method: http.MethodPost, Path: "/file/upload/pre", Parameters: commonQuery()}
	preReq := map[string]any{
		"ccp_hash_update": true,
		"dir_name":        "",
		"file_name":       o.fs.opt.Enc.FromStandardName(leaf),
		"format_type":     "application/octet-stream",
		"l_created_at":    now,
		"l_updated_at":    now,
		"pdir_fid":        directoryID,
		"size":            n,
	}
	var pre api.UpPreResp
	err = o.fs.callJSON(ctx, &preOpts, &preReq, &pre)
	if err != nil {
		return err
	}
	if pre.Data.Finish {
		o.id = pre.Data.Fid
		o.size = n
		o.modTime = src.ModTime(ctx)
		return nil
	}
	hashOpts := rest.Opts{Method: http.MethodPost, Path: "/file/update/hash", Parameters: commonQuery()}
	hashReq := map[string]any{"md5": sums[hash.MD5], "sha1": sums[hash.SHA1], "task_id": pre.Data.TaskID}
	var hashInfo api.HashResp
	err = o.fs.callJSON(ctx, &hashOpts, &hashReq, &hashInfo)
	if err != nil {
		return err
	}
	if hashInfo.Data.Finish {
		if hashInfo.Data.Fid != "" {
			o.id = hashInfo.Data.Fid
		}
		o.size = n
		o.modTime = src.ModTime(ctx)
		return nil
	}
	if err := o.ossUpload(ctx, rw, n, pre); err != nil {
		return err
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
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
	_ fs.ParentIDer      = (*Object)(nil)
)
