// Package _115 provides an interface to 115 Drive using the web API.
package _115

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

	"github.com/rclone/rclone/backend/115/api"
	"github.com/rclone/rclone/backend/115/crypto"
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
	defaultMinSleep = 500 * time.Millisecond
	maxSleep        = 4 * time.Second
	decayConstant   = 2
	defaultEndpoint = "https://webapi.115.com"
	proAPIRoot      = "https://proapi.115.com"
	uplbRoot        = "https://uplb.115.com"
	ossEndpoint     = "oss-cn-shenzhen.aliyuncs.com"
	defaultUA       = "Mozilla/5.0 115Browser/27.0.3.7"
	defaultAppVer   = "35.4.0"
	ossUserAgent    = "aliyun-sdk-android/2.9.1"
	rootID          = "0"
	memUploadLimit  = 64 << 20
	dirExistsErrno  = 20004
	rateLimitErrno  = 990009
)

// Register with Fs
func init() {
	fs.Register(&fs.RegInfo{
		Name:        "115",
		Description: "115 Drive",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "cookie",
			Help: `115 web cookie string.

Copy UID, CID, SEID and KID from the browser after logging in to
https://115.com (DevTools → Application → Cookies). A single string
like "UID=...; CID=...; SEID=...; KID=..." is enough.

This backend talks to the 115 web API (webapi.115.com). Do not use
115 OpenAPI tokens; that API is deprecated.
`,
			Sensitive: true,
		}, {
			Name:      "uid",
			Help:      "UID cookie. Optional if cookie is set.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:      "cid",
			Help:      "CID cookie. Optional if cookie is set.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:      "seid",
			Help:      "SEID cookie. Optional if cookie is set.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:      "kid",
			Help:      "KID cookie. Optional if cookie is set.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name: "root_folder_id",
			Help: `ID of the root folder.

Leave blank to use the account root. Set this to a directory CID to
restrict rclone to that folder.
`,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name:     "user_agent",
			Help:     "HTTP user agent sent to 115. Leave blank to use a 115 Browser UA.",
			Advanced: true,
		}, {
			Name:     "app_ver",
			Help:     "115 app version string used for uploads.",
			Default:  defaultAppVer,
			Advanced: true,
		}, {
			Name:     "list_chunk",
			Help:     "Number of items to list in each call.",
			Default:  1000,
			Advanced: true,
		}, {
			Name:     "pacer_min_sleep",
			Help:     "Minimum time to sleep between API calls.",
			Default:  fs.Duration(defaultMinSleep),
			Advanced: true,
		}, {
			Name:     "endpoint",
			Help:     "Endpoint for the 115 web API.",
			Default:  defaultEndpoint,
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
	Cookie        string               `config:"cookie"`
	UID           string               `config:"uid"`
	CID           string               `config:"cid"`
	SEID          string               `config:"seid"`
	KID           string               `config:"kid"`
	RootFolderID  string               `config:"root_folder_id"`
	UserAgent     string               `config:"user_agent"`
	AppVer        string               `config:"app_ver"`
	ListChunk     int                  `config:"list_chunk"`
	PacerMinSleep fs.Duration          `config:"pacer_min_sleep"`
	Endpoint      string               `config:"endpoint"`
	Enc           encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote 115 drive.
type Fs struct {
	name     string
	root     string
	opt      Options
	features *fs.Features
	srv      *rest.Client
	dl       *rest.Client
	oss      *rest.Client
	pacer    *fs.Pacer
	dirCache *dircache.DirCache
	upload   *uploadState
}

type uploadState struct {
	mu        sync.Mutex
	userID    int64
	userKey   string
	sizeLimit int64
	once      sync.Once
	err       error
}

// Object describes a 115 object.
type Object struct {
	fs       *Fs
	remote   string
	id       string
	dirID    string
	pickCode string
	sha1sum  string
	size     int64
	modTime  time.Time
}

// retryErrorCodes is a slice of HTTP status codes we retry.
var retryErrorCodes = []int{
	429,
	500,
	502,
	503,
	504,
	509,
}

func compactCookieValue(v string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		default:
			return r
		}
	}, v)
}

// parseCookies reads UID/CID/SEID/KID from a cookie header and/or split fields.
func parseCookies(cookie, uid, cid, seid, kid string) (uidOut, cidOut, seidOut, kidOut string, err error) {
	uidOut, cidOut, seidOut, kidOut = compactCookieValue(uid), compactCookieValue(cid), compactCookieValue(seid), compactCookieValue(kid)
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
		val = compactCookieValue(val)
		switch strings.ToUpper(name) {
		case "UID":
			if val != "" {
				uidOut = val
			}
		case "CID":
			if val != "" {
				cidOut = val
			}
		case "SEID":
			if val != "" {
				seidOut = val
			}
		case "KID":
			if val != "" {
				kidOut = val
			}
		}
	}
	if uidOut == "" || cidOut == "" || seidOut == "" {
		return "", "", "", "", errors.New("115: uid, cid and seid cookies are required (copy them from 115.com after login)")
	}
	return uidOut, cidOut, seidOut, kidOut, nil
}

func cookieHeader(uid, cid, seid, kid string) string {
	parts := []string{
		"UID=" + uid,
		"CID=" + cid,
		"SEID=" + seid,
	}
	if kid != "" {
		parts = append(parts, "KID="+kid)
	}
	return strings.Join(parts, "; ")
}

func parsePath(p string) string {
	return strings.Trim(p, "/")
}

func isLoginErrno(errno int64) bool {
	switch errno {
	case 99, 990001, 40101023, 40101032, 40101037:
		return true
	default:
		return false
	}
}

func isRetryErrno(errno int64) bool {
	return errno == rateLimitErrno
}

func shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "errno 990009") || strings.Contains(msg, fmt.Sprintf("errno %d", rateLimitErrno)) {
			return true, err
		}
		if strings.Contains(msg, "errno 99") || strings.Contains(msg, "login") {
			return false, fserrors.FatalError(err)
		}
	}
	if resp != nil && resp.StatusCode == http.StatusForbidden {
		time.Sleep(time.Second)
		return true, err
	}
	return fserrors.ShouldRetry(err) || fserrors.ShouldRetryHTTP(resp, retryErrorCodes), err
}

func errorHandler(resp *http.Response) error {
	body, err := rest.ReadBody(resp)
	if err != nil {
		return fmt.Errorf("error reading error out of body: %w", err)
	}
	var parsed api.BaseResponse
	if len(body) > 0 {
		if err := json.Unmarshal(body, &parsed); err == nil && (!parsed.State || parsed.Message() != "") {
			if e := parsed.Err(); e != nil {
				return e
			}
		}
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
	uid, cid, seid, kid, err := parseCookies(opt.Cookie, opt.UID, opt.CID, opt.SEID, opt.KID)
	if err != nil {
		return nil, err
	}
	opt.UID, opt.CID, opt.SEID, opt.KID = uid, cid, seid, kid
	if opt.Endpoint == "" {
		opt.Endpoint = defaultEndpoint
	}
	if opt.UserAgent == "" {
		opt.UserAgent = defaultUA
	}
	if opt.AppVer == "" {
		opt.AppVer = defaultAppVer
	}
	if opt.ListChunk <= 0 {
		opt.ListChunk = 1000
	}
	if opt.PacerMinSleep <= 0 {
		opt.PacerMinSleep = fs.Duration(defaultMinSleep)
	}
	root = parsePath(root)
	client := fshttp.NewClient(ctx)
	rootFolderID := opt.RootFolderID
	if rootFolderID == "" {
		rootFolderID = rootID
	}
	f := &Fs{
		name:   name,
		root:   root,
		opt:    *opt,
		srv:    rest.NewClient(client).SetRoot(strings.TrimRight(opt.Endpoint, "/")),
		dl:     rest.NewClient(client),
		oss:    rest.NewClient(client),
		pacer:  fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(time.Duration(opt.PacerMinSleep)), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		upload: &uploadState{},
	}
	f.srv.SetErrorHandler(errorHandler)
	f.srv.SetHeader("User-Agent", opt.UserAgent)
	f.srv.SetHeader("Referer", "https://115.com/")
	f.srv.SetHeader("Cookie", cookieHeader(uid, cid, seid, kid))
	f.dl.SetHeader("User-Agent", opt.UserAgent)
	f.dl.SetHeader("Cookie", cookieHeader(uid, cid, seid, kid))
	f.oss.SetHeader("User-Agent", ossUserAgent)
	f.features = (&fs.Features{
		CaseInsensitive:         false,
		CanHaveEmptyDirectories: true,
		DuplicateFiles:          false,
		ReadMimeType:            false,
		WriteMimeType:           false,
	}).Fill(ctx, f)
	f.dirCache = dircache.New(root, rootFolderID, f)

	if err := f.ping(ctx); err != nil {
		return nil, err
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
	_, err := f.indexInfo(ctx)
	if err != nil {
		return fmt.Errorf("115 login check failed: %w (re-copy UID/CID/SEID/KID as one line; the same app slot can kick an older session; 115 may also reject non-China IPs)", err)
	}
	return nil
}

func (f *Fs) indexInfo(ctx context.Context) (*api.IndexInfoResponse, error) {
	opts := rest.Opts{
		Method: http.MethodGet,
		Path:   "/files/index_info",
	}
	var info api.IndexInfoResponse
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	if err := info.Err(); err != nil {
		if isLoginErrno(info.GetErrno()) {
			return nil, fserrors.FatalError(fmt.Errorf("115 cookies expired or invalid: %w", err))
		}
		return nil, err
	}
	return &info, nil
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
	return fmt.Sprintf("115 Drive root '%s'", f.root)
}

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features {
	return f.features
}

func (f *Fs) rootSlash() string {
	if f.root == "" {
		return f.root
	}
	return f.root + "/"
}

// Precision of the ModTimes in this Fs
func (f *Fs) Precision() time.Duration {
	return fs.ModTimeNotSupported
}

// Hashes returns the supported hash types
func (f *Fs) Hashes() hash.Set {
	return hash.Set(hash.SHA1)
}

// DirCacheFlush resets the directory cache
func (f *Fs) DirCacheFlush() {
	f.dirCache.ResetRoot()
}

func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, info *api.FileInfo) (fs.Object, error) {
	o := &Object{
		fs:     f,
		remote: remote,
	}
	var err error
	if info != nil {
		err = o.setMetaData(info)
	} else {
		err = o.readMetaData(ctx)
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}

// NewObject finds the Object at remote
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return f.newObjectWithInfo(ctx, remote, nil)
}

func (f *Fs) readMetaDataForPath(ctx context.Context, remote string) (*api.FileInfo, error) {
	leaf, directoryID, err := f.dirCache.FindPath(ctx, remote, false)
	if err != nil {
		if err == fs.ErrorDirNotFound {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	var info *api.FileInfo
	found, err := f.listAll(ctx, directoryID, false, true, func(item *api.FileInfo) bool {
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

type listAllFn func(*api.FileInfo) bool

func (f *Fs) listAll(ctx context.Context, dirID string, directoriesOnly, filesOnly bool, fn listAllFn) (found bool, err error) {
	offset := int64(0)
	limit := int64(f.opt.ListChunk)
OUTER:
	for {
		opts := rest.Opts{
			Method:     http.MethodGet,
			Path:       "/files",
			Parameters: url.Values{},
		}
		opts.Parameters.Set("aid", "1")
		opts.Parameters.Set("cid", dirID)
		opts.Parameters.Set("o", "user_ptime")
		opts.Parameters.Set("asc", "0")
		opts.Parameters.Set("offset", strconv.FormatInt(offset, 10))
		opts.Parameters.Set("show_dir", "1")
		opts.Parameters.Set("limit", strconv.FormatInt(limit, 10))
		opts.Parameters.Set("snap", "0")
		opts.Parameters.Set("record_open_time", "0")
		opts.Parameters.Set("format", "json")
		opts.Parameters.Set("fc_mix", "0")

		var info api.GetFilesResponse
		err = f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return false, err
		}
		if err := info.Err(); err != nil {
			if isLoginErrno(info.GetErrno()) {
				return false, fserrors.FatalError(err)
			}
			if info.GetErrno() != 0 {
				return false, fmt.Errorf("couldn't list files: %w", err)
			}
			return false, fs.ErrorDirNotFound
		}
		if !info.State && len(info.Data) == 0 {
			return false, fs.ErrorDirNotFound
		}
		for i := range info.Data {
			item := &info.Data[i]
			if item.IsDir() {
				if filesOnly {
					continue
				}
			} else if directoriesOnly {
				continue
			}
			item.Name = f.opt.Enc.ToStandardName(item.Name)
			if fn(item) {
				found = true
				break OUTER
			}
		}
		offset += limit
		if offset >= info.Count || len(info.Data) == 0 {
			break
		}
	}
	return found, nil
}

// FindLeaf finds a directory of name leaf in the folder with ID pathID
func (f *Fs) FindLeaf(ctx context.Context, pathID, leaf string) (pathIDOut string, found bool, err error) {
	found, err = f.listAll(ctx, pathID, true, false, func(item *api.FileInfo) bool {
		if item.Name == leaf {
			pathIDOut = item.ID()
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

// CreateDir makes a directory with pathID as parent and name leaf
func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (newID string, err error) {
	opts := rest.Opts{
		Method:          http.MethodPost,
		Path:            "/files/add",
		MultipartParams: url.Values{},
	}
	opts.MultipartParams.Set("pid", pathID)
	opts.MultipartParams.Set("cname", f.opt.Enc.FromStandardName(leaf))

	var info api.MkdirResponse
	err = f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	if !info.State {
		if info.GetErrno() == dirExistsErrno {
			id, found, err := f.FindLeaf(ctx, pathID, leaf)
			if err != nil {
				return "", err
			}
			if found {
				return id, nil
			}
			return "", fs.ErrorDirExists
		}
		if err := info.Err(); err != nil {
			return "", fmt.Errorf("failed to create folder: %w", err)
		}
	}
	newID = info.CategoryID.String()
	if newID == "" || newID == "0" {
		id, found, err := f.FindLeaf(ctx, pathID, leaf)
		if err != nil {
			return "", err
		}
		if !found {
			return "", errors.New("115: mkdir succeeded but folder id missing")
		}
		return id, nil
	}
	return newID, nil
}

func (f *Fs) itemToDirEntry(ctx context.Context, remote string, info *api.FileInfo) (fs.DirEntry, error) {
	if info.IsDir() {
		f.dirCache.Put(remote, info.ID())
		d := fs.NewDir(remote, info.ModTime())
		d.SetID(info.ID())
		d.SetParentID(info.Parent())
		return d, nil
	}
	return f.newObjectWithInfo(ctx, remote, info)
}

// List the objects and directories in dir
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	directoryID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return nil, err
	}
	var iErr error
	_, err = f.listAll(ctx, directoryID, false, false, func(info *api.FileInfo) bool {
		remote := path.Join(dir, info.Name)
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
	if iErr != nil {
		return nil, iErr
	}
	return entries, nil
}

func (f *Fs) createObject(ctx context.Context, remote string, _ time.Time, _ int64) (o *Object, leaf, directoryID string, err error) {
	leaf, directoryID, err = f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return
	}
	o = &Object{
		fs:     f,
		remote: remote,
		dirID:  directoryID,
	}
	return o, leaf, directoryID, nil
}

// Put the object
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

// Mkdir creates the directory if it doesn't exist
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	_, err := f.dirCache.FindDir(ctx, dir, true)
	return err
}

func (f *Fs) deleteIDs(ctx context.Context, id, parentID string) error {
	opts := rest.Opts{
		Method:          http.MethodPost,
		Path:            "/rb/delete",
		MultipartParams: url.Values{},
	}
	opts.MultipartParams.Set("fid[0]", id)
	if parentID != "" {
		opts.MultipartParams.Set("pid", parentID)
	}
	opts.MultipartParams.Set("ignore_warn", "1")

	var info api.BaseResponse
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if err := info.Err(); err != nil {
		if isRetryErrno(info.GetErrno()) {
			time.Sleep(time.Second)
			return err
		}
		return fmt.Errorf("failed to delete: %w", err)
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
		found, err := f.listAll(ctx, dirID, false, false, func(*api.FileInfo) bool {
			return true
		})
		if err != nil {
			return err
		}
		if found {
			return fs.ErrorDirectoryNotEmpty
		}
	}
	parentID := rootID
	if dir != "" {
		_, parentID, err = f.dirCache.FindPath(ctx, dir, false)
		if err != nil {
			parentID = ""
		}
	}
	err = f.deleteIDs(ctx, dirID, parentID)
	if err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

// Rmdir deletes the directory if empty
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	return f.purgeCheck(ctx, dir, true)
}

// Purge deletes the directory and all contents
func (f *Fs) Purge(ctx context.Context, dir string) error {
	return f.purgeCheck(ctx, dir, false)
}

// About gets quota information
func (f *Fs) About(ctx context.Context) (*fs.Usage, error) {
	info, err := f.indexInfo(ctx)
	if err != nil {
		return nil, err
	}
	usage := &fs.Usage{}
	if totalInfo, ok := info.Data.SpaceInfo["all_total"]; ok {
		usage.Total = fs.NewUsageValue(int64(totalInfo.Size))
	}
	if useInfo, ok := info.Data.SpaceInfo["all_use"]; ok {
		usage.Used = fs.NewUsageValue(int64(useInfo.Size))
	}
	if remainInfo, ok := info.Data.SpaceInfo["all_remain"]; ok {
		usage.Free = fs.NewUsageValue(int64(remainInfo.Size))
	}
	return usage, nil
}

func (f *Fs) rename(ctx context.Context, id, newLeaf string) error {
	opts := rest.Opts{
		Method:          http.MethodPost,
		Path:            "/files/batch_rename",
		MultipartParams: url.Values{},
	}
	opts.MultipartParams.Set(fmt.Sprintf("files_new_name[%s]", id), f.opt.Enc.FromStandardName(newLeaf))
	var info api.BaseResponse
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if err := info.Err(); err != nil {
		return fmt.Errorf("failed to rename: %w", err)
	}
	return nil
}

func (f *Fs) move(ctx context.Context, id, newDirID string) error {
	opts := rest.Opts{
		Method:          http.MethodPost,
		Path:            "/files/move",
		MultipartParams: url.Values{},
	}
	opts.MultipartParams.Set("fid[0]", id)
	opts.MultipartParams.Set("pid", newDirID)
	var info api.BaseResponse
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if err := info.Err(); err != nil {
		return fmt.Errorf("failed to move: %w", err)
	}
	return nil
}

func (f *Fs) copy(ctx context.Context, id, newDirID string) error {
	opts := rest.Opts{
		Method:          http.MethodPost,
		Path:            "/files/copy",
		MultipartParams: url.Values{},
	}
	opts.MultipartParams.Set("fid[0]", id)
	opts.MultipartParams.Set("pid", newDirID)
	var info api.BaseResponse
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if err := info.Err(); err != nil {
		return fmt.Errorf("failed to copy: %w", err)
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

func (f *Fs) findInDir(ctx context.Context, dirID, name, sha1sum string) (*api.FileInfo, error) {
	var info *api.FileInfo
	_, err := f.listAll(ctx, dirID, false, true, func(item *api.FileInfo) bool {
		if sha1sum != "" && strings.EqualFold(item.Sha1, sha1sum) && (name == "" || item.Name == name) {
			info = item
			return true
		}
		if sha1sum == "" && item.Name == name {
			info = item
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fs.ErrorObjectNotFound
	}
	return info, nil
}

// Move src to this remote using server-side move
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		fs.Debugf(src, "Can't move - not same remote type")
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
	if existing, err := f.NewObject(ctx, remote); err == nil {
		if rem, ok := existing.(*Object); ok && rem.id != srcObj.id {
			_ = rem.Remove(ctx)
		}
	}
	err = f.moveTo(ctx, srcObj.id, srcLeaf, dstLeaf, srcDirectoryID, dstDirectoryID)
	if err != nil {
		return nil, err
	}
	srcObj.fs.dirCache.FlushDir(path.Dir(srcObj.remote))
	f.dirCache.FlushDir(path.Dir(remote))
	return f.NewObject(ctx, remote)
}

// DirMove moves a directory
func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	srcFs, ok := src.(*Fs)
	if !ok {
		fs.Debugf(src, "Can't move directory - not same remote type")
		return fs.ErrorCantDirMove
	}
	srcID, srcDirectoryID, srcLeaf, dstDirectoryID, dstLeaf, err := f.dirCache.DirMove(ctx, srcFs.dirCache, srcFs.root, srcRemote, f.root, dstRemote)
	if err != nil {
		return err
	}
	err = f.moveTo(ctx, srcID, srcLeaf, dstLeaf, srcDirectoryID, dstDirectoryID)
	if err != nil {
		return err
	}
	srcFs.dirCache.FlushDir(srcRemote)
	return nil
}

// Copy src to this remote using server-side copy
func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		fs.Debugf(src, "Can't copy - not same remote type")
		return nil, fs.ErrorCantCopy
	}
	srcLeaf := path.Base(srcObj.remote)
	srcPath := srcObj.fs.rootSlash() + srcObj.remote
	dstPath := f.rootSlash() + remote
	if srcPath == dstPath {
		return nil, fmt.Errorf("can't copy %q -> %q as are same name", srcPath, dstPath)
	}
	if existing, err := f.NewObject(ctx, remote); err == nil {
		_ = existing.Remove(ctx)
	}
	_, dstLeaf, dstDirectoryID, err := f.createObject(ctx, remote, srcObj.modTime, srcObj.size)
	if err != nil {
		return nil, err
	}
	err = f.copy(ctx, srcObj.id, dstDirectoryID)
	if err != nil {
		return nil, err
	}
	f.dirCache.FlushDir(path.Dir(remote))
	var info *api.FileInfo
	for i := 0; i < 5; i++ {
		info, err = f.findInDir(ctx, dstDirectoryID, srcLeaf, srcObj.sha1sum)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("copied file not found in destination: %w", err)
	}
	if srcLeaf != dstLeaf {
		if err := f.rename(ctx, info.ID(), dstLeaf); err != nil {
			return nil, err
		}
		f.dirCache.FlushDir(path.Dir(remote))
	}
	return f.NewObject(ctx, remote)
}

func (f *Fs) getDownloadURL(ctx context.Context, pickCode string) (string, error) {
	if u, err := f.getWebDownloadURL(ctx, pickCode); err == nil && u != "" {
		return u, nil
	} else if err != nil {
		fs.Debugf(f, "web download url failed, falling back to proapi: %v", err)
	}
	return f.getProDownloadURL(ctx, pickCode)
}

func (f *Fs) getWebDownloadURL(ctx context.Context, pickCode string) (string, error) {
	opts := rest.Opts{
		Method:     http.MethodGet,
		Path:       "/files/download",
		Parameters: url.Values{},
	}
	opts.Parameters.Set("pickcode", pickCode)
	var info api.WebDownloadResponse
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	if err := info.Err(); err != nil {
		return "", err
	}
	if info.FileURL != "" {
		return info.FileURL, nil
	}
	if len(info.Data) > 0 {
		var nested struct {
			URL     string `json:"url"`
			FileURL string `json:"file_url"`
		}
		if err := json.Unmarshal(info.Data, &nested); err == nil {
			if nested.URL != "" {
				return nested.URL, nil
			}
			if nested.FileURL != "" {
				return nested.FileURL, nil
			}
		}
		var asString string
		if err := json.Unmarshal(info.Data, &asString); err == nil && strings.HasPrefix(asString, "http") {
			return asString, nil
		}
	}
	return "", errors.New("115: empty download url")
}

func (f *Fs) getProDownloadURL(ctx context.Context, pickCode string) (string, error) {
	key := crypto.GenerateKey()
	payload, err := json.Marshal(map[string]string{"pickcode": pickCode})
	if err != nil {
		return "", err
	}
	opts := rest.Opts{
		Method:          http.MethodPost,
		RootURL:         proAPIRoot,
		Path:            "/app/chrome/downurl",
		Parameters:      url.Values{},
		MultipartParams: url.Values{},
	}
	opts.Parameters.Set("t", strconv.FormatInt(time.Now().Unix(), 10))
	opts.MultipartParams.Set("data", crypto.Encode(payload, key))
	var info api.GetURLResponse
	err = f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	if err := info.Err(); err != nil {
		return "", err
	}
	var encoded string
	if err := json.Unmarshal(info.Data, &encoded); err != nil {
		return "", fmt.Errorf("115: decode downurl envelope: %w", err)
	}
	decoded, err := crypto.Decode(encoded, key)
	if err != nil {
		return "", fmt.Errorf("115: decode downurl: %w", err)
	}
	var result api.DownloadData
	if err := json.Unmarshal(decoded, &result); err != nil {
		return "", fmt.Errorf("115: parse downurl: %w", err)
	}
	for _, item := range result {
		if item != nil && item.URL.URL != "" {
			return item.URL.URL, nil
		}
	}
	return "", fs.ErrorObjectNotFound
}

// ------------------------------------------------------------

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

// Hash returns the SHA-1 of an object
func (o *Object) Hash(_ context.Context, t hash.Type) (string, error) {
	if t != hash.SHA1 {
		return "", hash.ErrUnsupported
	}
	return strings.ToLower(o.sha1sum), nil
}

// Size returns the size of the file
func (o *Object) Size() int64 {
	return o.size
}

func (o *Object) setMetaData(info *api.FileInfo) error {
	if info.IsDir() {
		return fs.ErrorIsDir
	}
	o.id = info.ID()
	o.dirID = info.Parent()
	o.pickCode = info.PickCode
	o.sha1sum = strings.ToLower(info.Sha1)
	o.size = info.Size.Int64()
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

// ModTime returns the modification time of the object
func (o *Object) ModTime(_ context.Context) time.Time {
	return o.modTime
}

// SetModTime is not supported by 115
func (o *Object) SetModTime(_ context.Context, _ time.Time) error {
	return fs.ErrorCantSetModTime
}

// Storable returns whether this object can be stored
func (o *Object) Storable() bool {
	return true
}

// ID of the Object
func (o *Object) ID() string {
	return o.id
}

// ParentID of the Object
func (o *Object) ParentID() string {
	return o.dirID
}

// Open an object for read
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (in io.ReadCloser, err error) {
	if o.pickCode == "" {
		if err := o.readMetaData(ctx); err != nil {
			return nil, err
		}
	}
	fs.FixRangeOption(options, o.size)
	var resp *http.Response
	err = o.fs.pacer.Call(func() (bool, error) {
		targetURL, err := o.fs.getDownloadURL(ctx, o.pickCode)
		if err != nil {
			return shouldRetry(ctx, resp, err)
		}
		opts := rest.Opts{
			Method:  http.MethodGet,
			RootURL: targetURL,
			Options: options,
		}
		resp, err = o.fs.dl.Call(ctx, &opts)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Remove this object
func (o *Object) Remove(ctx context.Context) error {
	if o.id == "" {
		if err := o.readMetaData(ctx); err != nil {
			return err
		}
	}
	err := o.fs.deleteIDs(ctx, o.id, o.dirID)
	if err != nil {
		return err
	}
	o.fs.dirCache.FlushDir(path.Dir(o.remote))
	return nil
}

// Check the interfaces are satisfied
var (
	_ fs.Fs              = (*Fs)(nil)
	_ fs.Purger          = (*Fs)(nil)
	_ fs.Copier          = (*Fs)(nil)
	_ fs.Mover           = (*Fs)(nil)
	_ fs.DirMover        = (*Fs)(nil)
	_ fs.Abouter         = (*Fs)(nil)
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
	_ fs.ParentIDer      = (*Object)(nil)
)
