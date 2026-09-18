// Package alipan provides an interface to Aliyun Drive.
package alipan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rclone/rclone/backend/alipan/api"
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
	partSize        = 10 << 20
	memUploadLimit  = 64 << 20
)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "alipan",
		Description: "Aliyun Drive",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "refresh_token",
			Help: `Aliyun Drive refresh token.

Easy path: paste the refresh_token from a logged-in
https://www.alipan.com session (DevTools → Application →
Local Storage → token). rclone uses the web API and does
not need a developer app.

Advanced: create an app at https://www.alipan.com/developer
and set client_id and client_secret to use the Open API.
`,
			Sensitive: true,
		}, {
			Name: "client_id",
			Help: `OAuth client ID from the Aliyun Drive developer console.

Optional. Leave empty to use the web API with refresh_token
only. Required together with client_secret for the Open API.
`,
			Sensitive: true,
			Advanced:  true,
		}, {
			Name: "client_secret",
			Help: `OAuth client secret from the Aliyun Drive developer console.

Required with client_id for the Open API.
`,
			Sensitive: true,
			Advanced:  true,
		}, {
			Name: "access_token",
			Help: `Access token.

Optional if refresh_token, client_id and client_secret are set.
`,
			Sensitive: true,
			Advanced:  true,
		}, {
			Name: "drive_id",
			Help: `Drive ID.

Leave blank to use the account default drive. Set to a resource
or backup drive id to use that drive.
`,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name: "root_folder_id",
			Help: `ID of the root folder.

Leave blank to use "root".
`,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name:     "list_chunk",
			Help:     "Number of items to list in each call.",
			Default:  100,
			Advanced: true,
		}, {
			Name:     "endpoint",
			Help:     "Endpoint for the Aliyun Drive API. Open API default is openapi.alipan.com; web token login uses api.alipan.com.",
			Advanced: true,
		}, {
			Name:     "token_endpoint",
			Help:     "Token URL for web refresh_token login. Default https://auth.alipan.com/v2/account/token.",
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
	RefreshToken  string               `config:"refresh_token"`
	ClientID      string               `config:"client_id"`
	ClientSecret  string               `config:"client_secret"`
	AccessToken   string               `config:"access_token"`
	DriveID       string               `config:"drive_id"`
	RootFolderID  string               `config:"root_folder_id"`
	ListChunk     int                  `config:"list_chunk"`
	Endpoint      string               `config:"endpoint"`
	TokenEndpoint string               `config:"token_endpoint"`
	Enc           encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote Aliyun Drive.
type Fs struct {
	name      string
	root      string
	opt       Options
	features  *fs.Features
	srv       *rest.Client
	dl        *rest.Client
	pacer     *fs.Pacer
	dirCache  *dircache.DirCache
	tokenMu   sync.Mutex
	token     string
	tokenExp  time.Time
	driveID   string
	m         configmap.Mapper
	web       bool
	userID    string
	sigMu     sync.RWMutex
	deviceID  string
	signature string
}

// Object describes an Aliyun Drive object.
type Object struct {
	fs      *Fs
	remote  string
	id      string
	dirID   string
	sha1sum string
	size    int64
	modTime time.Time
}

var retryErrorCodes = []int{429, 500, 502, 503, 504}

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
	var parsed api.Error
	if len(body) > 0 {
		if err := json.Unmarshal(body, &parsed); err == nil && (parsed.Code != "" || parsed.Message != "") {
			return parsed
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
	if opt.ListChunk <= 0 {
		opt.ListChunk = 100
	}
	web := strings.TrimSpace(opt.ClientID) == "" || strings.TrimSpace(opt.ClientSecret) == ""
	if strings.TrimSpace(opt.AccessToken) == "" && strings.TrimSpace(opt.RefreshToken) == "" {
		return nil, errors.New("alipan: refresh_token is required (from www.alipan.com, or with client_id/client_secret for the Open API)")
	}
	if opt.Endpoint == "" {
		if web {
			opt.Endpoint = api.DefaultWebRoot
		} else {
			opt.Endpoint = api.DefaultRoot
		}
	}
	root = parsePath(root)
	client := fshttp.NewClient(ctx)
	rootFolderID := opt.RootFolderID
	if rootFolderID == "" {
		rootFolderID = api.RootFolder
	}
	f := &Fs{
		name:     name,
		root:     root,
		opt:      *opt,
		m:        m,
		web:      web,
		srv:      rest.NewClient(client).SetRoot(strings.TrimRight(opt.Endpoint, "/")),
		dl:       rest.NewClient(client),
		pacer:    fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(defaultMinSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		token:    strings.TrimSpace(opt.AccessToken),
		tokenExp: time.Now().Add(time.Hour),
		driveID:  opt.DriveID,
	}
	f.srv.SetErrorHandler(errorHandler)
	f.features = (&fs.Features{
		CaseInsensitive:         true,
		CanHaveEmptyDirectories: true,
	}).Fill(ctx, f)
	f.dirCache = dircache.New(root, rootFolderID, f)
	if err := f.ensureToken(ctx, false); err != nil {
		return nil, err
	}
	if f.driveID == "" || f.web {
		info, err := f.driveInfo(ctx)
		if err != nil {
			return nil, err
		}
		if f.driveID == "" {
			f.driveID = info.DefaultDriveID
			if f.driveID == "" {
				f.driveID = info.ResourceDriveID
			}
		}
		if f.web {
			f.userID = info.UserID
			_ = f.initWebSession(ctx)
		}
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
	if !force && f.token != "" && time.Now().Before(f.tokenExp.Add(-2*time.Minute)) {
		return nil
	}
	if f.web {
		if force && strings.TrimSpace(f.opt.RefreshToken) == "" {
			return fserrors.FatalError(errors.New("alipan: token expired; set refresh_token"))
		}
		return f.webRefreshLocked(ctx)
	}
	if f.opt.ClientID == "" || f.opt.ClientSecret == "" || f.opt.RefreshToken == "" {
		if f.token != "" && !force {
			return nil
		}
		return errors.New("alipan: token expired; set refresh_token, client_id and client_secret")
	}
	opts := rest.Opts{
		Method: http.MethodPost,
		Path:   "/oauth/access_token",
	}
	req := api.TokenReq{
		ClientID:     f.opt.ClientID,
		ClientSecret: f.opt.ClientSecret,
		GrantType:    "refresh_token",
		RefreshToken: f.opt.RefreshToken,
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
	f.token = info.AccessToken
	if info.RefreshToken != "" {
		f.opt.RefreshToken = info.RefreshToken
	}
	exp := info.ExpiresIn
	if exp <= 0 {
		exp = 7200
	}
	f.tokenExp = time.Now().Add(time.Duration(exp) * time.Second)
	return nil
}

func (f *Fs) authHeaders() map[string]string {
	f.tokenMu.Lock()
	token := f.token
	f.tokenMu.Unlock()
	h := map[string]string{"Authorization": "Bearer " + token}
	if f.web {
		f.sigMu.RLock()
		if f.signature != "" {
			h["X-Signature"] = f.signature
			h["X-Device-Id"] = f.deviceID
			h["X-Canary"] = "client=Android,app=adrive,version=v4.1.0"
			h["x-request-id"] = uuid.NewString()
		}
		f.sigMu.RUnlock()
		h["Origin"] = "https://www.alipan.com"
		h["Referer"] = "https://www.alipan.com/"
	}
	return h
}

func (f *Fs) doJSON(ctx context.Context, opts *rest.Opts, request, response any) error {
	return f.pacer.Call(func() (bool, error) {
		if err := f.ensureToken(ctx, false); err != nil {
			return false, err
		}
		callOpts := opts.Copy()
		callOpts.ExtraHeaders = f.authHeaders()
		resp, err := f.srv.CallJSON(ctx, callOpts, request, response)
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			if rerr := f.ensureToken(ctx, true); rerr != nil {
				return false, rerr
			}
			return true, err
		}
		if err != nil {
			var apiErr api.Error
			if errors.As(err, &apiErr) {
				switch apiErr.Code {
				case "AccessTokenInvalid":
					if rerr := f.ensureToken(ctx, true); rerr != nil {
						return false, rerr
					}
					return true, err
				case "DeviceSessionSignatureInvalid":
					if rerr := f.initWebSession(ctx); rerr != nil {
						return false, rerr
					}
					return true, err
				}
			}
		}
		return shouldRetry(ctx, resp, err)
	})
}

func (f *Fs) apiPath(openPath, webPath string) string {
	if f.web {
		return webPath
	}
	return openPath
}

func (f *Fs) driveInfo(ctx context.Context) (*api.DriveInfoResp, error) {
	opts := rest.Opts{Method: http.MethodPost, Path: f.apiPath("/adrive/v1.0/user/getDriveInfo", "/v2/user/get")}
	var info api.DriveInfoResp
	if err := f.doJSON(ctx, &opts, map[string]string{}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string { return f.name }

// Root of the remote
func (f *Fs) Root() string { return f.root }

// String converts this Fs to a string
func (f *Fs) String() string { return fmt.Sprintf("Aliyun Drive root '%s'", f.root) }

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features { return f.features }

// Precision of the ModTimes in this Fs
func (f *Fs) Precision() time.Duration { return time.Second }

// Hashes returns the supported hash types
func (f *Fs) Hashes() hash.Set { return hash.Set(hash.SHA1) }

// DirCacheFlush resets the directory cache
func (f *Fs) DirCacheFlush() { f.dirCache.ResetRoot() }

func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, info *api.File) (fs.Object, error) {
	o := &Object{fs: f, remote: remote}
	if info != nil {
		return o, o.setMetaData(info)
	}
	return o, o.readMetaData(ctx)
}

// NewObject finds the Object at remote
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return f.newObjectWithInfo(ctx, remote, nil)
}

type listAllFn func(*api.File) bool

func (f *Fs) listAll(ctx context.Context, dirID string, directoriesOnly, filesOnly bool, fn listAllFn) (found bool, err error) {
	marker := ""
	for {
		req := api.ListReq{
			DriveID:        f.driveID,
			ParentFileID:   dirID,
			Limit:          f.opt.ListChunk,
			Marker:         marker,
			Fields:         "*",
			OrderBy:        "name",
			OrderDirection: "ASC",
		}
		opts := rest.Opts{Method: http.MethodPost, Path: f.apiPath("/adrive/v1.0/openFile/list", "/v2/file/list")}
		var info api.ListResp
		if err := f.doJSON(ctx, &opts, &req, &info); err != nil {
			return false, err
		}
		for i := range info.Items {
			item := &info.Items[i]
			if item.IsDir() {
				if filesOnly {
					continue
				}
			} else if directoriesOnly {
				continue
			}
			item.Name = f.opt.Enc.ToStandardName(item.DisplayName())
			if fn(item) {
				return true, nil
			}
		}
		if info.NextMarker == "" {
			break
		}
		marker = info.NextMarker
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
		if item.DisplayName() == leaf || item.Name == leaf {
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

// FindLeaf finds a directory of name leaf in the folder with ID pathID
func (f *Fs) FindLeaf(ctx context.Context, pathID, leaf string) (pathIDOut string, found bool, err error) {
	found, err = f.listAll(ctx, pathID, true, false, func(item *api.File) bool {
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
	req := api.CreateFolderReq{
		DriveID:       f.driveID,
		ParentFileID:  pathID,
		Name:          f.opt.Enc.FromStandardName(leaf),
		CheckNameMode: "refuse",
		Type:          "folder",
	}
	opts := rest.Opts{Method: http.MethodPost, Path: f.apiPath("/adrive/v1.0/openFile/createFolder", "/adrive/v2/file/createWithFolders")}
	var info api.CreateFolderResp
	if err := f.doJSON(ctx, &opts, &req, &info); err != nil {
		id, found, findErr := f.FindLeaf(ctx, pathID, leaf)
		if findErr == nil && found {
			return id, nil
		}
		return "", err
	}
	if info.FileID == "" {
		id, found, err := f.FindLeaf(ctx, pathID, leaf)
		if err != nil {
			return "", err
		}
		if !found {
			return "", errors.New("alipan: mkdir succeeded but folder id missing")
		}
		return id, nil
	}
	return info.FileID, nil
}

func (f *Fs) itemToDirEntry(ctx context.Context, remote string, info *api.File) (fs.DirEntry, error) {
	if info.IsDir() {
		f.dirCache.Put(remote, info.ID())
		d := fs.NewDir(remote, info.UpdatedAt)
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

func (f *Fs) trash(ctx context.Context, id string) error {
	req := api.TrashReq{DriveID: f.driveID, FileID: id}
	opts := rest.Opts{Method: http.MethodPost, Path: f.apiPath("/adrive/v1.0/openFile/recyclebin/trash", "/v2/recyclebin/trash")}
	return f.doJSON(ctx, &opts, &req, nil)
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
		found, err := f.listAll(ctx, dirID, false, false, func(*api.File) bool { return true })
		if err != nil {
			return err
		}
		if found {
			return fs.ErrorDirectoryNotEmpty
		}
	}
	if err := f.trash(ctx, dirID); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

// Rmdir deletes the directory if empty
func (f *Fs) Rmdir(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, true) }

// Purge deletes the directory and all contents
func (f *Fs) Purge(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, false) }

// About gets quota information
func (f *Fs) About(ctx context.Context) (*fs.Usage, error) {
	opts := rest.Opts{Method: http.MethodPost, Path: f.apiPath("/adrive/v1.0/user/getSpaceInfo", "/adrive/v1/user/getSpaceInfo")}
	var info api.SpaceResp
	if err := f.doJSON(ctx, &opts, map[string]string{}, &info); err != nil {
		return nil, err
	}
	used := info.PersonalSpaceInfo.UsedSize
	total := info.PersonalSpaceInfo.TotalSize
	usage := &fs.Usage{Used: fs.NewUsageValue(used)}
	if total > 0 {
		usage.Total = fs.NewUsageValue(total)
		free := total - used
		if free < 0 {
			free = 0
		}
		usage.Free = fs.NewUsageValue(free)
	}
	return usage, nil
}

func (f *Fs) rename(ctx context.Context, id, newLeaf string) error {
	req := api.UpdateReq{DriveID: f.driveID, FileID: id, Name: f.opt.Enc.FromStandardName(newLeaf)}
	opts := rest.Opts{Method: http.MethodPost, Path: f.apiPath("/adrive/v1.0/openFile/update", "/v3/file/update")}
	return f.doJSON(ctx, &opts, &req, nil)
}

func (f *Fs) move(ctx context.Context, id, newDirID string) error {
	req := api.MoveReq{DriveID: f.driveID, FileID: id, ToParentFileID: newDirID, CheckNameMode: "auto_rename"}
	opts := rest.Opts{Method: http.MethodPost, Path: f.apiPath("/adrive/v1.0/openFile/move", "/v2/file/move")}
	return f.doJSON(ctx, &opts, &req, nil)
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

func (f *Fs) downloadURL(ctx context.Context, fileID string) (string, error) {
	req := api.DownloadReq{DriveID: f.driveID, FileID: fileID, ExpireSec: 14400}
	opts := rest.Opts{Method: http.MethodPost, Path: f.apiPath("/adrive/v1.0/openFile/getDownloadUrl", "/v2/file/get_download_url")}
	var info api.DownloadResp
	if err := f.doJSON(ctx, &opts, &req, &info); err != nil {
		return "", err
	}
	if info.URL == "" {
		return "", errors.New("alipan: empty download url")
	}
	return info.URL, nil
}

// Move src to this remote using server-side move
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

// DirMove moves a directory
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
	o.modTime = info.UpdatedAt
	o.sha1sum = strings.ToLower(info.ContentHash)
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

func (o *Object) Hash(_ context.Context, t hash.Type) (string, error) {
	if t != hash.SHA1 {
		return "", hash.ErrUnsupported
	}
	return o.sha1sum, nil
}

// Open an object for read
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

// Remove this object
func (o *Object) Remove(ctx context.Context) error {
	if o.id == "" {
		if err := o.readMetaData(ctx); err != nil {
			return err
		}
	}
	if err := o.fs.trash(ctx, o.id); err != nil {
		return err
	}
	o.fs.dirCache.FlushDir(path.Dir(o.remote))
	return nil
}

// PublicLink returns a time-limited direct download URL for a file.
func (f *Fs) PublicLink(ctx context.Context, remote string, expire fs.Duration, unlink bool) (string, error) {
	if unlink {
		return "", errors.New("alipan: download URLs expire on their own; unlink is not supported")
	}
	if _, err := f.dirCache.FindDir(ctx, remote, false); err == nil {
		return "", fs.ErrorCantShareDirectories
	}
	o, err := f.NewObject(ctx, remote)
	if err != nil {
		return "", err
	}
	obj, ok := o.(*Object)
	if !ok {
		return "", fs.ErrorObjectNotFound
	}
	if obj.id == "" {
		return "", fs.ErrorObjectNotFound
	}
	return f.downloadURL(ctx, obj.id)
}

func partsForSize(size int64) []api.PartInfo {
	if size <= 0 {
		return []api.PartInfo{{PartNumber: 1}}
	}
	n := int((size + partSize - 1) / partSize)
	if n < 1 {
		n = 1
	}
	parts := make([]api.PartInfo, n)
	for i := range parts {
		parts[i].PartNumber = i + 1
	}
	return parts
}

// Update uploads the object
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, _ ...fs.OpenOption) error {
	leaf, directoryID, err := o.fs.dirCache.FindPath(ctx, o.remote, true)
	if err != nil {
		return err
	}
	o.dirID = directoryID
	sha1sum, _ := src.Hash(ctx, hash.SHA1)
	size := src.Size()
	var rs io.ReadSeeker
	var cleanup func()
	if sha1sum == "" || size < 0 {
		rw := multipart.NewRW()
		hasher, err := hash.NewMultiHasherTypes(hash.NewHashSet(hash.SHA1))
		if err != nil {
			_ = rw.Close()
			return err
		}
		n, err := io.Copy(rw, io.TeeReader(in, hasher))
		if err != nil {
			_ = rw.Close()
			return err
		}
		if _, err := rw.Seek(0, io.SeekStart); err != nil {
			_ = rw.Close()
			return err
		}
		sha1sum = hasher.Sums()[hash.SHA1]
		size = n
		rs = rw
		cleanup = func() { _ = rw.Close() }
	} else if seeker, ok := in.(io.ReadSeeker); ok {
		rs = seeker
	} else {
		rw := multipart.NewRW()
		n, err := io.Copy(rw, in)
		if err != nil {
			_ = rw.Close()
			return err
		}
		if _, err := rw.Seek(0, io.SeekStart); err != nil {
			_ = rw.Close()
			return err
		}
		size = n
		rs = rw
		cleanup = func() { _ = rw.Close() }
	}
	if cleanup != nil {
		defer cleanup()
	}
	req := api.CreateFileReq{
		DriveID:         o.fs.driveID,
		ParentFileID:    directoryID,
		Name:            o.fs.opt.Enc.FromStandardName(leaf),
		Type:            "file",
		CheckNameMode:   "overwrite",
		Size:            size,
		ContentHash:     strings.ToLower(sha1sum),
		ContentHashName: "sha1",
		PartInfoList:    partsForSize(size),
	}
	opts := rest.Opts{Method: http.MethodPost, Path: o.fs.apiPath("/adrive/v1.0/openFile/create", "/adrive/v2/file/createWithFolders")}
	var created api.CreateFileResp
	if err := o.fs.doJSON(ctx, &opts, &req, &created); err != nil {
		return err
	}
	o.id = created.FileID
	if !created.RapidUpload {
		for i, part := range created.PartInfoList {
			offset := int64(i) * partSize
			left := size - offset
			if left > partSize {
				left = partSize
			}
			if _, err := rs.Seek(offset, io.SeekStart); err != nil {
				return err
			}
			putOpts := rest.Opts{
				Method:        http.MethodPut,
				RootURL:       part.UploadURL,
				Body:          io.LimitReader(rs, left),
				ContentLength: &left,
			}
			err := o.fs.pacer.Call(func() (bool, error) {
				if _, err := rs.Seek(offset, io.SeekStart); err != nil {
					return false, err
				}
				putOpts.Body = io.LimitReader(rs, left)
				resp, err := o.fs.dl.Call(ctx, &putOpts)
				if err == nil && resp != nil && resp.Body != nil {
					_ = resp.Body.Close()
				}
				return shouldRetry(ctx, resp, err)
			})
			if err != nil {
				return err
			}
		}
		comp := api.CompleteReq{DriveID: o.fs.driveID, FileID: created.FileID, UploadID: created.UploadID}
		compOpts := rest.Opts{Method: http.MethodPost, Path: o.fs.apiPath("/adrive/v1.0/openFile/complete", "/v2/file/complete")}
		var done api.CompleteResp
		if err := o.fs.doJSON(ctx, &compOpts, &comp, &done); err != nil {
			return err
		}
		if done.FileID != "" {
			o.id = done.FileID
		}
	}
	o.size = size
	o.sha1sum = strings.ToLower(sha1sum)
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
	_ fs.PublicLinker    = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
	_ fs.ParentIDer      = (*Object)(nil)
)
