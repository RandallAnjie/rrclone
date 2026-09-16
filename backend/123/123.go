// Package _123 provides an interface to 123 Cloud using the Open Platform API.
package _123

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

	"github.com/rclone/rclone/backend/123/api"
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
	memUploadLimit  = 64 << 20
)

// Register with Fs
func init() {
	fs.Register(&fs.RegInfo{
		Name:        "123",
		Description: "123 Cloud",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "client_id",
			Help: `123 Open Platform client ID.

Apply at https://www.123pan.com/developer. Used with client_secret
to obtain an access token. Do not use a third-party token broker.
`,
			Sensitive: true,
		}, {
			Name: "client_secret",
			Help: `123 Open Platform client secret.

Apply at https://www.123pan.com/developer.
`,
			Sensitive: true,
		}, {
			Name: "access_token",
			Help: `Access token.

Optional if client_id and client_secret are set. Tokens expire;
rclone refreshes them from the client credentials when possible.
`,
			Sensitive: true,
			Advanced:  true,
		}, {
			Name: "root_folder_id",
			Help: `ID of the root folder.

Leave blank to use the account root. Set this to a directory file
id to restrict rclone to that folder.
`,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name:     "list_chunk",
			Help:     "Number of items to list in each call.",
			Default:  100,
			Advanced: true,
		}, {
			Name:     "pacer_min_sleep",
			Help:     "Minimum time to sleep between API calls.",
			Default:  fs.Duration(defaultMinSleep),
			Advanced: true,
		}, {
			Name:     "endpoint",
			Help:     "Endpoint for the 123 Open Platform API.",
			Default:  api.DefaultRoot,
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
	ClientID      string               `config:"client_id"`
	ClientSecret  string               `config:"client_secret"`
	AccessToken   string               `config:"access_token"`
	RootFolderID  string               `config:"root_folder_id"`
	ListChunk     int                  `config:"list_chunk"`
	PacerMinSleep fs.Duration          `config:"pacer_min_sleep"`
	Endpoint      string               `config:"endpoint"`
	Enc           encoder.MultiEncoder `config:"encoding"`
}

type codedJSON interface {
	GetCode() int
	Err() error
}

// Fs represents a remote 123 Cloud.
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

// Object describes a 123 Cloud object.
type Object struct {
	fs      *Fs
	remote  string
	id      string
	dirID   string
	md5sum  string
	sha1sum string
	size    int64
	modTime time.Time
}

var retryErrorCodes = []int{
	429,
	500,
	502,
	503,
	504,
	509,
}

func parsePath(p string) string {
	return strings.Trim(p, "/")
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
	var parsed api.BaseResp
	if len(body) > 0 {
		if err := json.Unmarshal(body, &parsed); err == nil && parsed.Code != 0 {
			return parsed.Err()
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
	if opt.Endpoint == "" {
		opt.Endpoint = api.DefaultRoot
	}
	if opt.ListChunk <= 0 {
		opt.ListChunk = 100
	}
	if opt.PacerMinSleep <= 0 {
		opt.PacerMinSleep = fs.Duration(defaultMinSleep)
	}
	if strings.TrimSpace(opt.ClientID) == "" && strings.TrimSpace(opt.AccessToken) == "" {
		return nil, errors.New("123: client_id and client_secret are required (https://www.123pan.com/developer)")
	}
	root = parsePath(root)
	client := fshttp.NewClient(ctx)
	rootFolderID := opt.RootFolderID
	if rootFolderID == "" {
		rootFolderID = rootID
	}
	f := &Fs{
		name:     name,
		root:     root,
		opt:      *opt,
		srv:      rest.NewClient(client).SetRoot(strings.TrimRight(opt.Endpoint, "/")),
		dl:       rest.NewClient(client),
		pacer:    fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(time.Duration(opt.PacerMinSleep)), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		token:    strings.TrimSpace(opt.AccessToken),
		tokenExp: time.Now().Add(90 * 24 * time.Hour),
	}
	f.srv.SetErrorHandler(errorHandler)
	f.srv.SetHeader("Platform", api.PlatformHeader)
	f.features = (&fs.Features{
		CaseInsensitive:         false,
		CanHaveEmptyDirectories: true,
		DuplicateFiles:          false,
		ReadMimeType:            false,
		WriteMimeType:           false,
	}).Fill(ctx, f)
	f.dirCache = dircache.New(root, rootFolderID, f)

	if err := f.ensureToken(ctx, false); err != nil {
		return nil, err
	}
	if _, err := f.userInfo(ctx); err != nil {
		return nil, fmt.Errorf("123 login check failed: %w", err)
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

func (f *Fs) ensureToken(ctx context.Context, force bool) error {
	f.tokenMu.Lock()
	defer f.tokenMu.Unlock()
	if !force && f.token != "" && time.Now().Before(f.tokenExp.Add(-5*time.Minute)) {
		return nil
	}
	if f.opt.ClientID == "" || f.opt.ClientSecret == "" {
		if f.token != "" && !force {
			return nil
		}
		if f.token != "" && force {
			return fserrors.FatalError(errors.New("123: access token expired; set client_id and client_secret to refresh"))
		}
		return errors.New("123: client_id and client_secret are required (https://www.123pan.com/developer)")
	}
	opts := rest.Opts{
		Method: http.MethodPost,
		Path:   "/api/v1/access_token",
	}
	req := api.TokenReq{
		ClientID:     f.opt.ClientID,
		ClientSecret: f.opt.ClientSecret,
	}
	var info api.TokenResp
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if err := info.Err(); err != nil {
		return err
	}
	if info.Data.AccessToken == "" {
		return errors.New("123: empty access token")
	}
	f.token = info.Data.AccessToken
	if info.Data.ExpiredAt != "" {
		if t, err := time.Parse(time.RFC3339, info.Data.ExpiredAt); err == nil {
			f.tokenExp = t
		} else {
			f.tokenExp = time.Now().Add(24 * time.Hour)
		}
	} else {
		f.tokenExp = time.Now().Add(24 * time.Hour)
	}
	return nil
}

func (f *Fs) authHeaders() map[string]string {
	f.tokenMu.Lock()
	token := f.token
	f.tokenMu.Unlock()
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Platform":      api.PlatformHeader,
	}
}

func (f *Fs) doJSON(ctx context.Context, opts *rest.Opts, request any, response codedJSON) error {
	err := f.pacer.Call(func() (bool, error) {
		if err := f.ensureToken(ctx, false); err != nil {
			return false, err
		}
		callOpts := opts.Copy()
		callOpts.ExtraHeaders = f.authHeaders()
		resp, err := f.srv.CallJSON(ctx, callOpts, request, response)
		if err != nil {
			return shouldRetry(ctx, resp, err)
		}
		if response == nil {
			return false, nil
		}
		switch response.GetCode() {
		case api.TokenExpired:
			if rerr := f.ensureToken(ctx, true); rerr != nil {
				return false, rerr
			}
			return true, response.Err()
		case api.TooManyRequests:
			return true, response.Err()
		default:
			return false, nil
		}
	})
	if err != nil {
		return err
	}
	if response != nil {
		return response.Err()
	}
	return nil
}

func (f *Fs) userInfo(ctx context.Context) (*api.UserInfoResp, error) {
	opts := rest.Opts{
		Method: http.MethodGet,
		Path:   "/api/v1/user/info",
	}
	var info api.UserInfoResp
	if err := f.doJSON(ctx, &opts, nil, &info); err != nil {
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
	return fmt.Sprintf("123 Cloud root '%s'", f.root)
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
	return time.Second
}

// Hashes returns the supported hash types
func (f *Fs) Hashes() hash.Set {
	return hash.NewHashSet(hash.MD5, hash.SHA1)
}

// DirCacheFlush resets the directory cache
func (f *Fs) DirCacheFlush() {
	f.dirCache.ResetRoot()
}

func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, info *api.File) (fs.Object, error) {
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

type listAllFn func(*api.File) bool

func (f *Fs) listAll(ctx context.Context, dirID string, directoriesOnly, filesOnly bool, fn listAllFn) (found bool, err error) {
	parentID, err := api.ParseID(dirID)
	if err != nil {
		return false, err
	}
	lastFileID := int64(0)
	limit := f.opt.ListChunk
OUTER:
	for lastFileID != api.ListDone {
		opts := rest.Opts{
			Method:     http.MethodGet,
			Path:       "/api/v2/file/list",
			Parameters: url.Values{},
		}
		opts.Parameters.Set("parentFileId", strconv.FormatInt(parentID, 10))
		opts.Parameters.Set("limit", strconv.Itoa(limit))
		opts.Parameters.Set("lastFileId", strconv.FormatInt(lastFileID, 10))
		opts.Parameters.Set("trashed", "false")

		var info api.FileListResp
		if err := f.doJSON(ctx, &opts, nil, &info); err != nil {
			return false, err
		}
		if len(info.Data.FileList) == 0 {
			break
		}
		for i := range info.Data.FileList {
			item := &info.Data.FileList[i]
			if item.Trashed != 0 {
				continue
			}
			if item.IsDir() {
				if filesOnly {
					continue
				}
			} else if directoriesOnly {
				continue
			}
			item.FileName = f.opt.Enc.ToStandardName(item.FileName)
			if fn(item) {
				found = true
				break OUTER
			}
		}
		if info.Data.LastFileID == lastFileID {
			break
		}
		lastFileID = info.Data.LastFileID
	}
	return found, nil
}

// FindLeaf finds a directory of name leaf in the folder with ID pathID
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

// CreateDir makes a directory with pathID as parent and name leaf
func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (newID string, err error) {
	opts := rest.Opts{
		Method: http.MethodPost,
		Path:   "/upload/v1/file/mkdir",
	}
	req := api.MkdirReq{
		ParentID: pathID,
		Name:     f.opt.Enc.FromStandardName(leaf),
	}
	var info api.MkdirResp
	if err := f.doJSON(ctx, &opts, &req, &info); err != nil {
		id, found, findErr := f.FindLeaf(ctx, pathID, leaf)
		if findErr == nil && found {
			return id, nil
		}
		return "", err
	}
	if info.Data.DirID != 0 {
		return strconv.FormatInt(info.Data.DirID, 10), nil
	}
	id, found, err := f.FindLeaf(ctx, pathID, leaf)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("123: mkdir succeeded but folder id missing")
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

// List the objects and directories in dir
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

func (f *Fs) trashIDs(ctx context.Context, ids ...int64) error {
	if len(ids) == 0 {
		return nil
	}
	opts := rest.Opts{
		Method: http.MethodPost,
		Path:   "/api/v1/file/trash",
	}
	req := api.TrashReq{FileIDs: ids}
	var info api.BaseResp
	return f.doJSON(ctx, &opts, &req, &info)
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
		found, err := f.listAll(ctx, dirID, false, false, func(*api.File) bool {
			return true
		})
		if err != nil {
			return err
		}
		if found {
			return fs.ErrorDirectoryNotEmpty
		}
	}
	id, err := api.ParseID(dirID)
	if err != nil {
		return err
	}
	if err := f.trashIDs(ctx, id); err != nil {
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
	info, err := f.userInfo(ctx)
	if err != nil {
		return nil, err
	}
	total := info.Data.SpacePermanent + info.Data.SpaceTemp
	usage := &fs.Usage{
		Used: fs.NewUsageValue(info.Data.SpaceUsed),
	}
	if total > 0 {
		usage.Total = fs.NewUsageValue(total)
		free := total - info.Data.SpaceUsed
		if free < 0 {
			free = 0
		}
		usage.Free = fs.NewUsageValue(free)
	}
	return usage, nil
}

func (f *Fs) rename(ctx context.Context, id int64, newLeaf string) error {
	opts := rest.Opts{
		Method: http.MethodPut,
		Path:   "/api/v1/file/name",
	}
	req := api.RenameReq{
		FileID:   id,
		FileName: f.opt.Enc.FromStandardName(newLeaf),
	}
	var info api.BaseResp
	return f.doJSON(ctx, &opts, &req, &info)
}

func (f *Fs) move(ctx context.Context, id, newDirID int64) error {
	opts := rest.Opts{
		Method: http.MethodPost,
		Path:   "/api/v1/file/move",
	}
	req := api.MoveReq{
		FileIDs:        []int64{id},
		ToParentFileID: newDirID,
	}
	var info api.BaseResp
	return f.doJSON(ctx, &opts, &req, &info)
}

func (f *Fs) moveTo(ctx context.Context, id, srcLeaf, dstLeaf, srcDirectoryID, dstDirectoryID string) error {
	fileID, err := api.ParseID(id)
	if err != nil {
		return err
	}
	if srcLeaf != dstLeaf {
		if err := f.rename(ctx, fileID, dstLeaf); err != nil {
			return err
		}
	}
	if srcDirectoryID != dstDirectoryID {
		dstID, err := api.ParseID(dstDirectoryID)
		if err != nil {
			return err
		}
		if err := f.move(ctx, fileID, dstID); err != nil {
			return err
		}
	}
	return nil
}

func (f *Fs) findInDir(ctx context.Context, dirID, name, md5sum string) (*api.File, error) {
	var info *api.File
	_, err := f.listAll(ctx, dirID, false, true, func(item *api.File) bool {
		if md5sum != "" && strings.EqualFold(item.Etag, md5sum) && (name == "" || item.FileName == name) {
			info = item
			return true
		}
		if md5sum == "" && item.FileName == name {
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

func (f *Fs) createUpload(ctx context.Context, parentID int64, filename, etag string, size int64) (*api.UploadCreateResp, error) {
	opts := rest.Opts{
		Method: http.MethodPost,
		Path:   "/upload/v2/file/create",
	}
	req := api.UploadCreateReq{
		ParentFileID: parentID,
		Filename:     filename,
		Etag:         strings.ToLower(etag),
		Size:         size,
		Duplicate:    api.DuplicateOverwrite,
	}
	var info api.UploadCreateResp
	if err := f.doJSON(ctx, &opts, &req, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (f *Fs) sha1Reuse(ctx context.Context, parentID int64, filename, sha1sum string, size int64) (*api.SHA1ReuseResp, error) {
	opts := rest.Opts{
		Method: http.MethodPost,
		Path:   "/upload/v2/file/sha1_reuse",
	}
	req := api.SHA1ReuseReq{
		ParentFileID: parentID,
		Filename:     filename,
		SHA1:         strings.ToLower(sha1sum),
		Size:         size,
		Duplicate:    api.DuplicateOverwrite,
	}
	var info api.SHA1ReuseResp
	if err := f.doJSON(ctx, &opts, &req, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (f *Fs) completeUpload(ctx context.Context, preuploadID string) (*api.UploadCompleteResp, error) {
	opts := rest.Opts{
		Method: http.MethodPost,
		Path:   "/upload/v2/file/upload_complete",
	}
	req := api.UploadCompleteReq{PreuploadID: preuploadID}
	var info api.UploadCompleteResp
	if err := f.doJSON(ctx, &opts, &req, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (f *Fs) downloadURL(ctx context.Context, fileID string) (string, error) {
	opts := rest.Opts{
		Method:     http.MethodGet,
		Path:       "/api/v1/file/download_info",
		Parameters: url.Values{},
	}
	opts.Parameters.Set("fileId", fileID)
	var info api.DownloadInfoResp
	if err := f.doJSON(ctx, &opts, nil, &info); err != nil {
		return "", err
	}
	if info.Data.DownloadURL == "" {
		return "", errors.New("123: empty download url")
	}
	return info.Data.DownloadURL, nil
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

// Copy src to this remote using MD5 reuse when the etag is known.
func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		fs.Debugf(src, "Can't copy - not same remote type")
		return nil, fs.ErrorCantCopy
	}
	if srcObj.md5sum == "" {
		return nil, fs.ErrorCantCopy
	}
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
	parentID, err := api.ParseID(dstDirectoryID)
	if err != nil {
		return nil, err
	}
	created, err := f.createUpload(ctx, parentID, f.opt.Enc.FromStandardName(dstLeaf), srcObj.md5sum, srcObj.size)
	if err != nil {
		return nil, err
	}
	if !created.Data.Reuse {
		return nil, fs.ErrorCantCopy
	}
	f.dirCache.FlushDir(path.Dir(remote))
	return f.NewObject(ctx, remote)
}

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

// Hash returns the MD5 or SHA-1 of an object
func (o *Object) Hash(_ context.Context, t hash.Type) (string, error) {
	switch t {
	case hash.MD5:
		return strings.ToLower(o.md5sum), nil
	case hash.SHA1:
		return strings.ToLower(o.sha1sum), nil
	default:
		return "", hash.ErrUnsupported
	}
}

// Size returns the size of the file
func (o *Object) Size() int64 {
	return o.size
}

func (o *Object) setMetaData(info *api.File) error {
	if info.IsDir() {
		return fs.ErrorIsDir
	}
	o.id = info.ID()
	o.dirID = info.Parent()
	o.size = info.Size
	o.modTime = info.ModTime()
	etag := strings.ToLower(info.Etag)
	if len(etag) == 32 {
		o.md5sum = etag
	} else if len(etag) == 40 {
		o.sha1sum = etag
	} else {
		o.md5sum = etag
	}
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

// SetModTime is not supported by 123 Cloud
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
	if o.id == "" {
		if err := o.readMetaData(ctx); err != nil {
			return nil, err
		}
	}
	fs.FixRangeOption(options, o.size)
	var resp *http.Response
	err = o.fs.pacer.Call(func() (bool, error) {
		targetURL, err := o.fs.downloadURL(ctx, o.id)
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
	id, err := api.ParseID(o.id)
	if err != nil {
		return err
	}
	if err := o.fs.trashIDs(ctx, id); err != nil {
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
