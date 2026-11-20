// Package pan123 provides an interface to 123 Pan (123云盘) through
// its open platform API
package pan123

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/backend/pan123/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/list"
	"github.com/rclone/rclone/lib/dircache"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/rclone/rclone/lib/oauthutil"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
)

const (
	defaultAPIURL = "https://open-api.123pan.com"
	rootID        = "0"
	listLimit     = 100
	minSleep      = 10 * time.Millisecond
	maxSleep      = 4 * time.Second
	decayConstant = 2
)

// completeWait is how long to wait between polls for an upload to finish
var completeWait = time.Second

// apiQPS is the published request limit of each endpoint
var apiQPS = map[string]float64{
	"/api/v1/access_token":            1,
	"/api/v1/user/info":               1,
	"/api/v2/file/list":               3,
	"/api/v1/file/download_info":      5,
	"/upload/v1/file/mkdir":           2,
	"/api/v1/file/move":               1,
	"/api/v1/file/name":               1,
	"/api/v1/file/trash":              2,
	"/upload/v2/file/create":          2,
	"/upload/v2/file/upload_complete": 0, // unlimited
}

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "123pan",
		Description: "123 Pan (123云盘)",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "client_id",
			Help: `Client ID of your 123 Pan open platform developer app.

Apply for one at https://www.123pan.com/developer.`,
			Required:  true,
			Sensitive: true,
		}, {
			Name:       "client_secret",
			Help:       "Client secret of your 123 Pan open platform developer app.",
			Required:   true,
			IsPassword: true,
		}, {
			Name:     "api_url",
			Help:     "URL of the open platform API, or a reverse proxy of it.",
			Default:  defaultAPIURL,
			Advanced: true,
		}, {
			Name:     "complete_timeout",
			Help:     "How long to wait for 123 Pan to assemble an uploaded file.",
			Default:  fs.Duration(5 * time.Minute),
			Advanced: true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			// 123 Pan rejects \ / : * ? " < > |
			Default: encoder.Base |
				encoder.EncodeWin |
				encoder.EncodeBackSlash |
				encoder.EncodeRightSpace |
				encoder.EncodeInvalidUtf8,
		}},
	})
}

// Options defines the configuration for this backend
type Options struct {
	ClientID        string               `config:"client_id"`
	ClientSecret    string               `config:"client_secret"`
	APIURL          string               `config:"api_url"`
	CompleteTimeout fs.Duration          `config:"complete_timeout"`
	Enc             encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote 123 Pan
type Fs struct {
	name     string
	root     string
	opt      Options
	m        configmap.Mapper
	features *fs.Features
	srv      *rest.Client // API, root at the API URL
	plain    *rest.Client // downloads and upload servers
	dirCache *dircache.DirCache
	pacer    *fs.Pacer

	st *state // shared by copies of the Fs
}

// state is the rate limiters and token of an Fs
type state struct {
	limitersMu sync.Mutex
	limiters   map[string]*rate.Limiter

	tokenMu sync.Mutex
	token   *oauth2.Token
}

// Object describes a 123 Pan file
type Object struct {
	fs       *Fs
	remote   string
	id       int64
	parentID int64
	size     int64
	md5      string
	modTime  time.Time
}

// ------------------------------------------------------------

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string { return f.name }

// Root of the remote (as passed into NewFs)
func (f *Fs) Root() string { return f.root }

// String converts this Fs to a string
func (f *Fs) String() string { return fmt.Sprintf("123 Pan root '%s'", f.root) }

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features { return f.features }

// Precision of the modification times: 123 Pan sets its own
func (f *Fs) Precision() time.Duration { return fs.ModTimeNotSupported }

// Hashes returns the supported hash sets.
func (f *Fs) Hashes() hash.Set { return hash.Set(hash.MD5) }

// retryErrorCodes is a slice of HTTP status codes that we will retry
var retryErrorCodes = []int{429, 500, 502, 503, 504, 509}

func (f *Fs) shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.Code == api.CodeTooFast {
		return true, err
	}
	return fserrors.ShouldRetry(err) || fserrors.ShouldRetryHTTP(resp, retryErrorCodes), err
}

func errorHandler(resp *http.Response) error {
	body, _ := rest.ReadBody(resp)
	var r api.Response
	if json.Unmarshal(body, &r) == nil && r.Code != 0 {
		return &api.Error{Code: r.Code, Message: r.Message}
	}
	return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

// limiter returns the rate limiter for the endpoint p
func (f *Fs) limiter(p string) *rate.Limiter {
	qps, ok := apiQPS[p]
	if !ok || qps <= 0 {
		return nil
	}
	f.st.limitersMu.Lock()
	defer f.st.limitersMu.Unlock()
	l, ok := f.st.limiters[p]
	if !ok {
		l = rate.NewLimiter(rate.Limit(qps), 1)
		f.st.limiters[p] = l
	}
	return l
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	if err := configstruct.Set(m, opt); err != nil {
		return nil, err
	}
	if opt.ClientID == "" || opt.ClientSecret == "" {
		return nil, errors.New("123pan: client_id and client_secret are required")
	}
	secret, err := obscure.Reveal(opt.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("123pan: couldn't decrypt client_secret: %w", err)
	}
	opt.ClientSecret = secret
	apiURL := rest.CanonicalRoot(opt.APIURL)
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	client := fshttp.NewClient(ctx)
	f := &Fs{
		name:  name,
		root:  strings.Trim(root, "/"),
		opt:   *opt,
		m:     m,
		srv:   rest.NewClient(client).SetRoot(apiURL).SetErrorHandler(errorHandler),
		plain: rest.NewClient(client).SetErrorHandler(errorHandler),
		pacer: fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		st:    &state{limiters: map[string]*rate.Limiter{}},
	}
	f.st.token, _ = oauthutil.GetToken(name, m)
	f.features = (&fs.Features{
		CanHaveEmptyDirectories: true,
	}).Fill(ctx, f)

	f.dirCache = dircache.New(f.root, rootID, f)
	if err := f.dirCache.FindRoot(ctx, false); err != nil {
		// Assume it is a file
		newRoot, remote := dircache.SplitPath(f.root)
		tempF := *f
		tempF.dirCache = dircache.New(newRoot, rootID, &tempF)
		tempF.root = newRoot
		if err := tempF.dirCache.FindRoot(ctx, false); err != nil {
			return f, nil
		}
		if _, err := tempF.NewObject(ctx, remote); err != nil {
			if errors.Is(err, fs.ErrorObjectNotFound) {
				return f, nil
			}
			return nil, err
		}
		f.dirCache = tempF.dirCache
		f.root = tempF.root
		return f, fs.ErrorIsFile
	}
	return f, nil
}

// ------------------------------------------------------------
// API calls

// accessToken returns a valid access token, getting a new one with
// the developer credentials if needed
func (f *Fs) accessToken(ctx context.Context, force bool) (string, error) {
	f.st.tokenMu.Lock()
	defer f.st.tokenMu.Unlock()
	if !force && f.st.token != nil && f.st.token.AccessToken != "" && time.Until(f.st.token.Expiry) > 5*time.Minute {
		return f.st.token.AccessToken, nil
	}
	if l := f.limiter("/api/v1/access_token"); l != nil {
		if err := l.Wait(ctx); err != nil {
			return "", err
		}
	}
	opts := rest.Opts{
		Method:       "POST",
		Path:         "/api/v1/access_token",
		ExtraHeaders: map[string]string{"Platform": "open_platform"},
	}
	var r api.Response
	var tok api.AccessToken
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, &api.AccessTokenRequest{ClientID: f.opt.ClientID, ClientSecret: f.opt.ClientSecret}, &r)
		if err == nil && r.Code != api.CodeOK {
			err = &api.Error{Code: r.Code, Message: r.Message}
		}
		return f.shouldRetry(ctx, resp, err)
	})
	if err == nil {
		err = json.Unmarshal(r.Data, &tok)
	}
	if err != nil {
		return "", fmt.Errorf("123pan: couldn't get access token: %w", err)
	}
	f.st.token = &oauth2.Token{AccessToken: tok.AccessToken, TokenType: "Bearer", Expiry: tok.ExpiredAt}
	if err := oauthutil.PutToken(f.name, f.m, f.st.token, false); err != nil {
		fs.Debugf(f, "Couldn't save access token: %v", err)
	}
	return f.st.token.AccessToken, nil
}

// call makes an API call, decoding the data into out (which may be
// nil), observing the endpoint's rate limit and renewing the token
func (f *Fs) call(ctx context.Context, opts *rest.Opts, in, out any) error {
	var r api.Response
	renewed := false
	l := f.limiter(opts.Path)
	err := f.pacer.Call(func() (bool, error) {
		token, err := f.accessToken(ctx, false)
		if err != nil {
			return false, err
		}
		if l != nil {
			if err := l.Wait(ctx); err != nil {
				return false, err
			}
		}
		if opts.ExtraHeaders == nil {
			opts.ExtraHeaders = map[string]string{}
		}
		opts.ExtraHeaders["Authorization"] = "Bearer " + token
		opts.ExtraHeaders["Platform"] = "open_platform"
		r = api.Response{}
		resp, err := f.srv.CallJSON(ctx, opts, in, &r)
		if err == nil && r.Code != api.CodeOK {
			err = &api.Error{Code: r.Code, Message: r.Message}
		}
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.Code == api.CodeUnauthorized && !renewed {
			renewed = true
			if _, tErr := f.accessToken(ctx, true); tErr != nil {
				return false, tErr
			}
			return true, err
		}
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	if out != nil && len(r.Data) > 0 && string(r.Data) != "null" {
		if err := json.Unmarshal(r.Data, out); err != nil {
			return fmt.Errorf("failed to decode %s response: %w", opts.Path, err)
		}
	}
	return nil
}

func parseID(id string) int64 {
	n, _ := strconv.ParseInt(id, 10, 64)
	return n
}

// listAll calls fn for every live entry of the folder dirID, stopping
// early if fn returns true
func (f *Fs) listAll(ctx context.Context, dirID string, fn func(*api.File) bool) error {
	lastID := int64(0)
	for lastID != -1 {
		opts := rest.Opts{
			Method: "GET",
			Path:   "/api/v2/file/list",
			Parameters: url.Values{
				"parentFileId": {dirID},
				"limit":        {strconv.Itoa(listLimit)},
				"lastFileId":   {strconv.FormatInt(lastID, 10)},
			},
		}
		var r api.FileList
		if err := f.call(ctx, &opts, nil, &r); err != nil {
			return err
		}
		for i := range r.FileList {
			// The trashed filter of the API doesn't work
			if r.FileList[i].Trashed != 0 {
				continue
			}
			if fn(&r.FileList[i]) {
				return nil
			}
		}
		if len(r.FileList) == 0 && r.LastFileID == lastID {
			return nil // no progress
		}
		lastID = r.LastFileID
	}
	return nil
}

// FindLeaf finds a directory of name leaf in the folder with ID pathID
func (f *Fs) FindLeaf(ctx context.Context, pathID, leaf string) (pathIDOut string, found bool, err error) {
	name := f.opt.Enc.FromStandardName(leaf)
	err = f.listAll(ctx, pathID, func(file *api.File) bool {
		if file.IsDir() && file.FileName == name {
			pathIDOut, found = strconv.FormatInt(file.FileID, 10), true
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

// CreateDir makes a directory with pathID as parent and name leaf
func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (newID string, err error) {
	opts := rest.Opts{Method: "POST", Path: "/upload/v1/file/mkdir"}
	var r api.MkdirResponse
	if err := f.call(ctx, &opts, &api.MkdirRequest{Name: f.opt.Enc.FromStandardName(leaf), ParentID: parseID(pathID)}, &r); err != nil {
		return "", err
	}
	return strconv.FormatInt(r.DirID, 10), nil
}

// List the objects and directories in dir into entries.
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	return list.WithListP(ctx, dir, f)
}

// ListP lists the objects and directories of the Fs starting
// from dir non recursively into out.
func (f *Fs) ListP(ctx context.Context, dir string, callback fs.ListRCallback) error {
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	lh := list.NewHelper(callback)
	var iErr error
	err = f.listAll(ctx, dirID, func(file *api.File) bool {
		remote := path.Join(dir, f.opt.Enc.ToStandardName(file.FileName))
		if file.IsDir() {
			id := strconv.FormatInt(file.FileID, 10)
			f.dirCache.Put(remote, id)
			iErr = lh.Add(fs.NewDir(remote, time.Time(file.UpdateAt)).SetID(id))
		} else {
			iErr = lh.Add(f.newObject(remote, file))
		}
		return iErr != nil
	})
	if err != nil {
		return err
	}
	if iErr != nil {
		return iErr
	}
	return lh.Flush()
}

func (f *Fs) newObject(remote string, file *api.File) *Object {
	return &Object{
		fs:       f,
		remote:   remote,
		id:       file.FileID,
		parentID: file.ParentFileID,
		size:     file.Size,
		md5:      strings.ToLower(file.Etag),
		modTime:  time.Time(file.UpdateAt),
	}
}

// findFile finds the file leaf in the folder dirID
func (f *Fs) findFile(ctx context.Context, dirID, leaf string) (*api.File, error) {
	name := f.opt.Enc.FromStandardName(leaf)
	var found *api.File
	err := f.listAll(ctx, dirID, func(file *api.File) bool {
		if file.FileName == name {
			c := *file
			found = &c
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fs.ErrorObjectNotFound
	}
	if found.IsDir() {
		return nil, fs.ErrorIsDir
	}
	return found, nil
}

// NewObject finds the Object at remote.
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	leaf, dirID, err := f.dirCache.FindPath(ctx, remote, false)
	if err != nil {
		if errors.Is(err, fs.ErrorDirNotFound) {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	file, err := f.findFile(ctx, dirID, leaf)
	if err != nil {
		return nil, err
	}
	return f.newObject(remote, file), nil
}

// Put the object into the container
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	o := &Object{fs: f, remote: src.Remote()}
	return o, o.Update(ctx, in, src, options...)
}

// Mkdir creates the container if it doesn't exist
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	_, err := f.dirCache.FindDir(ctx, dir, true)
	return err
}

// trash moves the files or folders ids to the trash
func (f *Fs) trash(ctx context.Context, ids ...int64) error {
	opts := rest.Opts{Method: "POST", Path: "/api/v1/file/trash"}
	return f.call(ctx, &opts, &api.TrashRequest{FileIDs: ids}, nil)
}

// Rmdir deletes the root folder
//
// Returns an error if it isn't empty
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	empty := true
	if err := f.listAll(ctx, dirID, func(*api.File) bool { empty = false; return true }); err != nil {
		return err
	}
	if !empty {
		return fs.ErrorDirectoryNotEmpty
	}
	if dirID == rootID {
		return nil
	}
	if err := f.trash(ctx, parseID(dirID)); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

// Purge deletes all the files and the container
func (f *Fs) Purge(ctx context.Context, dir string) error {
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	if dirID == rootID {
		return errors.New("123pan: can't purge the root")
	}
	if err := f.trash(ctx, parseID(dirID)); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

// About gets quota information
func (f *Fs) About(ctx context.Context) (*fs.Usage, error) {
	opts := rest.Opts{Method: "GET", Path: "/api/v1/user/info"}
	var u api.UserInfo
	if err := f.call(ctx, &opts, nil, &u); err != nil {
		return nil, err
	}
	total := u.SpacePermanent + u.SpaceTemp
	return &fs.Usage{
		Total: fs.NewUsageValue(total),
		Used:  fs.NewUsageValue(u.SpaceUsed),
		Free:  fs.NewUsageValue(total - u.SpaceUsed),
	}, nil
}

func (f *Fs) rename(ctx context.Context, id int64, leaf string) error {
	opts := rest.Opts{Method: "PUT", Path: "/api/v1/file/name"}
	return f.call(ctx, &opts, &api.RenameRequest{FileID: id, FileName: f.opt.Enc.FromStandardName(leaf)}, nil)
}

func (f *Fs) move(ctx context.Context, id, toDirID int64) error {
	opts := rest.Opts{Method: "POST", Path: "/api/v1/file/move"}
	return f.call(ctx, &opts, &api.MoveRequest{FileIDs: []int64{id}, ToParentFileID: toDirID}, nil)
}

// relocate moves the file or folder id from srcDirID/srcLeaf to
// dstDirID/dstLeaf.
//
// When both the folder and the name change it goes through a
// temporary name, so it never depends on how 123 Pan treats a clash
// with an existing name in either folder.
func (f *Fs) relocate(ctx context.Context, id, srcDirID int64, srcLeaf string, dstDirID int64, dstLeaf string) error {
	switch {
	case srcDirID == dstDirID:
		if srcLeaf == dstLeaf {
			return nil
		}
		return f.rename(ctx, id, dstLeaf)
	case srcLeaf == dstLeaf:
		return f.move(ctx, id, dstDirID)
	}
	tmpLeaf := ".rclone-move-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := f.rename(ctx, id, tmpLeaf); err != nil {
		return err
	}
	if err := f.move(ctx, id, dstDirID); err != nil {
		return err
	}
	return f.rename(ctx, id, dstLeaf)
}

// Move src to this remote using server-side move operations.
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok || srcObj.fs.opt.ClientID != f.opt.ClientID {
		return nil, fs.ErrorCantMove
	}
	dstLeaf, dstDirID, err := f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return nil, err
	}
	srcLeaf := path.Base(srcObj.remote)
	if err := f.relocate(ctx, srcObj.id, srcObj.parentID, srcLeaf, parseID(dstDirID), dstLeaf); err != nil {
		return nil, err
	}
	o := *srcObj
	o.fs, o.remote, o.parentID = f, remote, parseID(dstDirID)
	return &o, nil
}

// DirMove moves src, srcRemote to this remote at dstRemote
// using server-side move operations.
func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	srcFs, ok := src.(*Fs)
	if !ok || srcFs.opt.ClientID != f.opt.ClientID {
		return fs.ErrorCantDirMove
	}
	srcID, srcDirectoryID, srcLeaf, dstDirectoryID, dstLeaf, err := f.dirCache.DirMove(ctx, srcFs.dirCache, srcFs.root, srcRemote, f.root, dstRemote)
	if err != nil {
		return err
	}
	if err := f.relocate(ctx, parseID(srcID), parseID(srcDirectoryID), srcLeaf, parseID(dstDirectoryID), dstLeaf); err != nil {
		return err
	}
	srcFs.dirCache.FlushDir(srcRemote)
	return nil
}

// Copy src to this remote using server-side copy operations.
//
// 123 Pan can't copy, but an upload of content it already holds
// completes at once, so a copy is an upload which sends no data.
func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok || srcObj.fs.opt.ClientID != f.opt.ClientID || srcObj.md5 == "" {
		return nil, fs.ErrorCantCopy
	}
	dstLeaf, dstDirID, err := f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return nil, err
	}
	r, err := f.create(ctx, parseID(dstDirID), dstLeaf, srcObj.md5, srcObj.size)
	if err != nil {
		return nil, err
	}
	if !r.Reuse || r.FileID == 0 {
		return nil, fs.ErrorCantCopy
	}
	o := *srcObj
	o.fs, o.remote, o.id, o.parentID, o.modTime = f, remote, r.FileID, parseID(dstDirID), time.Now()
	return &o, nil
}

// ------------------------------------------------------------
// Object

// Fs returns the parent Fs
func (o *Object) Fs() fs.Info { return o.fs }

// String returns a description of the Object
func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

// Remote returns the remote path
func (o *Object) Remote() string { return o.remote }

// Hash returns the MD5 of an object
func (o *Object) Hash(ctx context.Context, t hash.Type) (string, error) {
	if t != hash.MD5 {
		return "", hash.ErrUnsupported
	}
	return o.md5, nil
}

// Size returns the size of an object in bytes
func (o *Object) Size() int64 { return o.size }

// ModTime returns the modification time of the object
func (o *Object) ModTime(ctx context.Context) time.Time { return o.modTime }

// SetModTime sets the modification time of the object
func (o *Object) SetModTime(ctx context.Context, modTime time.Time) error {
	return fs.ErrorCantSetModTime
}

// Storable returns a boolean showing whether this object storable
func (o *Object) Storable() bool { return true }

// ID returns the ID of the Object
func (o *Object) ID() string { return strconv.FormatInt(o.id, 10) }

// Open an object for read
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	opts := rest.Opts{
		Method:     "GET",
		Path:       "/api/v1/file/download_info",
		Parameters: url.Values{"fileId": {strconv.FormatInt(o.id, 10)}},
	}
	var info api.DownloadInfo
	if err := o.fs.call(ctx, &opts, nil, &info); err != nil {
		return nil, err
	}
	if info.DownloadURL == "" {
		return nil, errors.New("123pan: no download URL")
	}
	fs.FixRangeOption(options, o.size)
	dl := rest.Opts{Method: "GET", RootURL: info.DownloadURL, Options: options}
	var resp *http.Response
	err := o.fs.pacer.Call(func() (bool, error) {
		var err error
		resp, err = o.fs.plain.Call(ctx, &dl)
		return o.fs.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Remove an object
func (o *Object) Remove(ctx context.Context) error {
	return o.fs.trash(ctx, o.id)
}

// ------------------------------------------------------------
// Upload

// create starts an upload, overwriting any file of the same name
func (f *Fs) create(ctx context.Context, dirID int64, leaf, md5sum string, size int64) (*api.CreateResponse, error) {
	opts := rest.Opts{Method: "POST", Path: "/upload/v2/file/create"}
	req := &api.CreateRequest{
		ParentFileID: dirID,
		Filename:     f.opt.Enc.FromStandardName(leaf),
		Etag:         strings.ToLower(md5sum),
		Size:         size,
		Duplicate:    api.DuplicateOverwrite,
	}
	var r api.CreateResponse
	if err := f.call(ctx, &opts, req, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// spool copies in to a temporary file, returning it rewound with the
// MD5 of the content. The caller must call cleanup.
func spool(in io.Reader) (file *os.File, md5sum string, cleanup func(), err error) {
	file, err = os.CreateTemp("", "rclone-123pan-")
	if err != nil {
		return nil, "", nil, err
	}
	cleanup = func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	h := md5.New()
	if _, err = io.Copy(io.MultiWriter(file, h), in); err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	return file, hex.EncodeToString(h.Sum(nil)), cleanup, nil
}

// uploadSlice sends slice number n (from 1) of an upload
func (f *Fs) uploadSlice(ctx context.Context, server, preuploadID string, n int, data []byte) error {
	sum := md5.Sum(data)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("preuploadID", preuploadID)
	_ = w.WriteField("sliceNo", strconv.Itoa(n))
	_ = w.WriteField("sliceMD5", hex.EncodeToString(sum[:]))
	part, err := w.CreateFormFile("slice", fmt.Sprintf("slice%d", n))
	if err != nil {
		return err
	}
	_, _ = part.Write(data)
	if err := w.Close(); err != nil {
		return err
	}
	return f.pacer.Call(func() (bool, error) {
		token, err := f.accessToken(ctx, false)
		if err != nil {
			return false, err
		}
		length := int64(body.Len())
		opts := rest.Opts{
			Method:        "POST",
			RootURL:       strings.TrimRight(server, "/") + "/upload/v2/file/slice",
			Body:          bytes.NewReader(body.Bytes()),
			ContentLength: &length,
			ContentType:   w.FormDataContentType(),
			ExtraHeaders:  map[string]string{"Authorization": "Bearer " + token, "Platform": "open_platform"},
		}
		var r api.Response
		resp, err := f.plain.CallJSON(ctx, &opts, nil, &r)
		if err == nil && r.Code != api.CodeOK {
			err = &api.Error{Code: r.Code, Message: r.Message}
		}
		return f.shouldRetry(ctx, resp, err)
	})
}

// upload stores in as leaf in the folder dirID
func (f *Fs) upload(ctx context.Context, in io.Reader, dirID int64, leaf string, size int64, md5sum string) (*api.File, error) {
	if md5sum == "" {
		file, sum, cleanup, err := spool(in)
		if err != nil {
			return nil, fmt.Errorf("123pan: couldn't buffer upload to hash it: %w", err)
		}
		defer cleanup()
		in, md5sum = file, sum
	}
	r, err := f.create(ctx, dirID, leaf, md5sum, size)
	if err != nil {
		return nil, err
	}
	done := func(id int64) *api.File {
		now := api.Time(time.Now())
		return &api.File{FileID: id, FileName: leaf, Size: size, Etag: md5sum, ParentFileID: dirID, UpdateAt: now}
	}
	if r.Reuse && r.FileID != 0 {
		return done(r.FileID), nil // 123 Pan has the content already
	}
	if len(r.Servers) == 0 || r.SliceSize <= 0 {
		return nil, errors.New("123pan: upload has no server or slice size")
	}
	buf := make([]byte, r.SliceSize)
	for n, sent := 1, int64(0); sent < size; n++ {
		chunk := buf[:min(r.SliceSize, size-sent)]
		if _, err := io.ReadFull(in, chunk); err != nil {
			return nil, fmt.Errorf("123pan: reading slice %d: %w", n, err)
		}
		if err := f.uploadSlice(ctx, r.Servers[0], r.PreuploadID, n, chunk); err != nil {
			return nil, fmt.Errorf("123pan: uploading slice %d: %w", n, err)
		}
		sent += int64(len(chunk))
	}
	timeout := time.Duration(f.opt.CompleteTimeout)
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		opts := rest.Opts{Method: "POST", Path: "/upload/v2/file/upload_complete"}
		var c api.CompleteResponse
		err := f.call(ctx, &opts, &api.CompleteRequest{PreuploadID: r.PreuploadID}, &c)
		if err == nil && c.Completed && c.FileID != 0 {
			return done(c.FileID), nil
		}
		// 123 Pan answers not completed, or an undocumented error
		// code, while it assembles the file
		if time.Now().After(deadline) {
			if err == nil {
				err = errors.New("not completed")
			}
			return nil, fmt.Errorf("123pan: upload didn't complete: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(completeWait):
		}
	}
}

// Update the object with the contents of the io.Reader, modTime and size
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	size := src.Size()
	if size < 0 {
		return errors.New("123pan: can't upload files of unknown size")
	}
	leaf, dirID, err := o.fs.dirCache.FindPath(ctx, o.remote, true)
	if err != nil {
		return err
	}
	md5sum, _ := src.Hash(ctx, hash.MD5)
	file, err := o.fs.upload(ctx, in, parseID(dirID), leaf, size, md5sum)
	if err != nil {
		return err
	}
	*o = *o.fs.newObject(o.remote, file)
	return nil
}

// Check the interfaces are satisfied
var (
	_ fs.Fs              = (*Fs)(nil)
	_ fs.Copier          = (*Fs)(nil)
	_ fs.Mover           = (*Fs)(nil)
	_ fs.DirMover        = (*Fs)(nil)
	_ fs.Purger          = (*Fs)(nil)
	_ fs.Abouter         = (*Fs)(nil)
	_ fs.ListPer         = (*Fs)(nil)
	_ dircache.DirCacher = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
)
