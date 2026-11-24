// Package aliyundrive provides an interface to Aliyun Drive (阿里云盘)
// through its open platform API
package aliyundrive

import (
	"bytes"
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

	"github.com/rclone/rclone/backend/aliyundrive/api"
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
	defaultAPIURL = "https://openapi.alipan.com"
	rootID        = "root"
	listLimit     = 100
	minSleep      = 10 * time.Millisecond
	maxSleep      = 4 * time.Second
	decayConstant = 2
	oauthScopes   = "user:base,file:all:read,file:all:write"
	timeFormat    = "2006-01-02T15:04:05.000Z"
	// uploadURLLife is how long an upload URL may be used for
	uploadURLLife = 50 * time.Minute
)

// Request limits per second (documented as 4, 1 and 15, kept just under)
var (
	listRate  rate.Limit = 3.9
	linkRate  rate.Limit = 0.9
	otherRate rate.Limit = 14.9
)

// partSizeOverride replaces the part size if set, for tests
var partSizeOverride int64

// partSize returns the upload part size for a file of size bytes,
// keeping under the limit of 10,000 parts
func partSize(size int64) int64 {
	if partSizeOverride > 0 {
		return partSizeOverride
	}
	ps := int64(20 << 20)
	for size > ps*10000 {
		ps *= 2
	}
	return ps
}

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "aliyundrive",
		Description: "Aliyun Drive (阿里云盘)",
		NewFs:       NewFs,
		Config:      configure,
		Options: []fs.Option{{
			Name:      "client_id",
			Help:      "App ID of your Aliyun Drive open platform app.",
			Required:  true,
			Sensitive: true,
		}, {
			Name:       "client_secret",
			Help:       "App secret of your Aliyun Drive open platform app.",
			Required:   true,
			IsPassword: true,
		}, {
			Name: "refresh_token",
			Help: `A refresh token to start with, instead of authorising in the browser.

Leave blank normally.`,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name:      config.ConfigToken,
			Help:      "OAuth Access Token as a JSON blob, saved by rclone.",
			Advanced:  true,
			Sensitive: true,
		}, {
			Name:    "drive_type",
			Help:    "Which drive of the account to use.",
			Default: "default",
			Examples: []fs.OptionExample{{
				Value: "default",
				Help:  "The default drive (备份盘)",
			}, {
				Value: "resource",
				Help:  "The resource drive (资源库)",
			}, {
				Value: "backup",
				Help:  "The backup drive",
			}},
		}, {
			Name:     "hard_delete",
			Help:     "Delete files permanently rather than moving them to the recycle bin.",
			Default:  false,
			Advanced: true,
		}, {
			Name:     "api_url",
			Help:     "URL of the open platform API, or a reverse proxy of it.",
			Default:  defaultAPIURL,
			Advanced: true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			// Aliyun Drive rejects / and names of . and ..; \ and
			// surrounding spaces are unreliable
			Default: encoder.Base |
				encoder.EncodeBackSlash |
				encoder.EncodeRightSpace |
				encoder.EncodeInvalidUtf8,
		}},
	})
}

// Options defines the configuration for this backend
type Options struct {
	ClientID     string               `config:"client_id"`
	ClientSecret string               `config:"client_secret"`
	RefreshToken string               `config:"refresh_token"`
	DriveType    string               `config:"drive_type"`
	HardDelete   bool                 `config:"hard_delete"`
	APIURL       string               `config:"api_url"`
	Enc          encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote Aliyun Drive
type Fs struct {
	name     string
	root     string
	opt      Options
	m        configmap.Mapper
	features *fs.Features
	srv      *rest.Client // API
	plain    *rest.Client // upload and download URLs
	dirCache *dircache.DirCache
	pacer    *fs.Pacer
	driveID  string
	apiURL   string
	st       *state // shared by copies of the Fs
}

// state is the token and rate limiters of an Fs
type state struct {
	mu    sync.Mutex
	token *oauth2.Token
	list  *rate.Limiter
	link  *rate.Limiter
	other *rate.Limiter
}

// Object describes an Aliyun Drive file
type Object struct {
	fs       *Fs
	remote   string
	id       string
	parentID string
	size     int64
	sha1     string
	modTime  time.Time
}

// ------------------------------------------------------------

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string { return f.name }

// Root of the remote (as passed into NewFs)
func (f *Fs) Root() string { return f.root }

// String converts this Fs to a string
func (f *Fs) String() string { return fmt.Sprintf("Aliyun Drive root '%s'", f.root) }

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features { return f.features }

// Precision of the modification times: Aliyun Drive sets its own
func (f *Fs) Precision() time.Duration { return fs.ModTimeNotSupported }

// Hashes returns the supported hash sets.
func (f *Fs) Hashes() hash.Set { return hash.Set(hash.SHA1) }

func errorHandler(resp *http.Response) error {
	body, _ := rest.ReadBody(resp)
	e := &api.Error{Status: resp.StatusCode}
	if json.Unmarshal(body, e) != nil || e.Code == "" {
		e.Message = strings.TrimSpace(string(body))
	}
	return e
}

func apiCode(err error) string {
	var e *api.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func isNotFound(err error) bool {
	var e *api.Error
	return errors.As(err, &e) && (e.Code == api.CodeNotFoundFile || e.Status == http.StatusNotFound)
}

func (f *Fs) shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	if apiCode(err) == api.CodeTooManyRequests {
		return true, err
	}
	return fserrors.ShouldRetry(err) || fserrors.ShouldRetryHTTP(resp, []int{429, 500, 502, 503, 504}), err
}

// newFs makes an Fs without contacting the server
func newFs(ctx context.Context, name string, m configmap.Mapper) (*Fs, error) {
	opt := new(Options)
	if err := configstruct.Set(m, opt); err != nil {
		return nil, err
	}
	if opt.ClientSecret != "" {
		secret, err := obscure.Reveal(opt.ClientSecret)
		if err != nil {
			return nil, fmt.Errorf("aliyundrive: couldn't decrypt client_secret: %w", err)
		}
		opt.ClientSecret = secret
	}
	apiURL := rest.CanonicalRoot(opt.APIURL)
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	client := fshttp.NewClient(ctx)
	f := &Fs{
		name:   name,
		opt:    *opt,
		m:      m,
		apiURL: apiURL,
		srv:    rest.NewClient(client).SetRoot(apiURL).SetErrorHandler(errorHandler),
		plain:  rest.NewClient(client).SetErrorHandler(errorHandler),
		pacer:  fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		st: &state{
			list:  rate.NewLimiter(listRate, 1),
			link:  rate.NewLimiter(linkRate, 1),
			other: rate.NewLimiter(otherRate, 1),
		},
	}
	f.st.token, _ = oauthutil.GetToken(name, m)
	return f, nil
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	f, err := newFs(ctx, name, m)
	if err != nil {
		return nil, err
	}
	f.root = strings.Trim(root, "/")
	if f.opt.ClientID == "" || f.opt.ClientSecret == "" {
		return nil, errors.New("aliyundrive: client_id and client_secret are required")
	}
	if f.st.token == nil {
		if f.opt.RefreshToken == "" {
			return nil, fmt.Errorf("aliyundrive: not authorised: run \"rclone config reconnect %s:\"", name)
		}
		f.st.token = &oauth2.Token{RefreshToken: f.opt.RefreshToken}
	}
	f.features = (&fs.Features{
		CanHaveEmptyDirectories: true,
	}).Fill(ctx, f)

	var info api.DriveInfo
	if err := f.call(ctx, f.st.other, "/adrive/v1.0/user/getDriveInfo", nil, &info); err != nil {
		return nil, fmt.Errorf("aliyundrive: couldn't read drive info: %w", err)
	}
	switch f.opt.DriveType {
	case "resource":
		f.driveID = info.ResourceDriveID
	case "backup":
		f.driveID = info.BackupDriveID
	default:
		f.driveID = info.DefaultDriveID
	}
	if f.driveID == "" {
		return nil, fmt.Errorf("aliyundrive: the account has no %q drive", f.opt.DriveType)
	}

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
// Authentication

// exchange gets tokens with an authorisation code or refresh token
func (f *Fs) exchange(ctx context.Context, req *api.TokenRequest) (*oauth2.Token, error) {
	req.ClientID, req.ClientSecret = f.opt.ClientID, f.opt.ClientSecret
	opts := rest.Opts{Method: "POST", Path: "/oauth/access_token"}
	var r api.TokenResponse
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, req, &r)
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	if r.AccessToken == "" || r.RefreshToken == "" {
		return nil, errors.New("aliyundrive: token response without tokens")
	}
	tok := &oauth2.Token{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, TokenType: "Bearer", Expiry: time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)}
	return tok, oauthutil.PutToken(f.name, f.m, tok, false)
}

// accessToken returns a valid access token, refreshing it if needed
func (f *Fs) accessToken(ctx context.Context, force bool) (string, error) {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	if !force && f.st.token.AccessToken != "" && time.Until(f.st.token.Expiry) > 5*time.Minute {
		return f.st.token.AccessToken, nil
	}
	// Another rclone process may have refreshed it already
	if tok, err := oauthutil.GetToken(f.name, f.m); err == nil && tok.AccessToken != f.st.token.AccessToken && time.Until(tok.Expiry) > 5*time.Minute {
		f.st.token = tok
		return tok.AccessToken, nil
	}
	if err := f.st.other.Wait(ctx); err != nil {
		return "", err
	}
	tok, err := f.exchange(ctx, &api.TokenRequest{GrantType: "refresh_token", RefreshToken: f.st.token.RefreshToken})
	if err != nil {
		return "", fserrors.FatalError(fmt.Errorf("aliyundrive: couldn't refresh token (%w): try \"rclone config reconnect %s:\"", err, f.name))
	}
	f.st.token = tok
	return tok.AccessToken, nil
}

// authURL is the page where the user authorises rclone
func authURL(apiURL, clientID string) string {
	return apiURL + "/oauth/authorize?" + url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {"oob"},
		"scope":         {oauthScopes},
		"response_type": {"code"},
	}.Encode()
}

// configure authorises rclone during rclone config
func configure(ctx context.Context, name string, m configmap.Mapper, in fs.ConfigIn) (*fs.ConfigOut, error) {
	f, err := newFs(ctx, name, m)
	if err != nil {
		return nil, err
	}
	switch in.State {
	case "":
		if f.opt.RefreshToken != "" {
			_, err := f.exchange(ctx, &api.TokenRequest{GrantType: "refresh_token", RefreshToken: f.opt.RefreshToken})
			return nil, err
		}
		return fs.ConfigInput("code", "config_code", fmt.Sprintf(`Open this page, log in and allow rclone, then paste the code it shows:

%s`, authURL(f.apiURL, f.opt.ClientID)))
	case "code":
		_, err := f.exchange(ctx, &api.TokenRequest{GrantType: "authorization_code", Code: strings.TrimSpace(in.Result)})
		return nil, err
	}
	return nil, fmt.Errorf("unknown state %q", in.State)
}

// call POSTs in to an API path, decoding the response into out (which
// may be nil), observing the rate limit l and refreshing the token
func (f *Fs) call(ctx context.Context, l *rate.Limiter, p string, in, out any) error {
	refreshed := false
	return f.pacer.Call(func() (bool, error) {
		token, err := f.accessToken(ctx, false)
		if err != nil {
			return false, err
		}
		if err := l.Wait(ctx); err != nil {
			return false, err
		}
		opts := rest.Opts{
			Method:       "POST",
			Path:         p,
			ExtraHeaders: map[string]string{"Authorization": "Bearer " + token},
		}
		if in == nil {
			in = struct{}{}
		}
		resp, err := f.srv.CallJSON(ctx, &opts, in, out)
		switch apiCode(err) {
		case api.CodeAccessTokenInvalid, api.CodeAccessTokenExpired:
			if !refreshed {
				refreshed = true
				if _, tErr := f.accessToken(ctx, true); tErr != nil {
					return false, tErr
				}
				return true, err
			}
		}
		return f.shouldRetry(ctx, resp, err)
	})
}

// ------------------------------------------------------------
// Files and directories

// listAll calls fn for each entry of the folder dirID, stopping early if it returns true
func (f *Fs) listAll(ctx context.Context, dirID string, fn func(*api.File) bool) error {
	marker := ""
	for {
		var r api.ListResponse
		req := &api.ListRequest{DriveID: f.driveID, ParentFileID: dirID, Limit: listLimit, Marker: marker}
		if err := f.call(ctx, f.st.list, "/adrive/v1.0/openFile/list", req, &r); err != nil {
			if isNotFound(err) {
				return fs.ErrorDirNotFound
			}
			return err
		}
		for i := range r.Items {
			if fn(&r.Items[i]) {
				return nil
			}
		}
		if r.NextMarker == "" {
			return nil
		}
		marker = r.NextMarker
	}
}

// FindLeaf finds a directory of name leaf in the folder with ID pathID
func (f *Fs) FindLeaf(ctx context.Context, pathID, leaf string) (pathIDOut string, found bool, err error) {
	name := f.opt.Enc.FromStandardName(leaf)
	err = f.listAll(ctx, pathID, func(file *api.File) bool {
		if file.IsDir() && file.Name == name {
			pathIDOut, found = file.FileID, true
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

// CreateDir makes a directory with pathID as parent and name leaf
func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (newID string, err error) {
	req := &api.CreateRequest{DriveID: f.driveID, ParentFileID: pathID, Name: f.opt.Enc.FromStandardName(leaf), Type: api.TypeFolder, CheckNameMode: api.CheckRefuse}
	var r api.CreateResponse
	if err := f.call(ctx, f.st.other, "/adrive/v1.0/openFile/create", req, &r); err != nil {
		return "", err
	}
	return r.FileID, nil
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
		remote := path.Join(dir, f.opt.Enc.ToStandardName(file.Name))
		if file.IsDir() {
			f.dirCache.Put(remote, file.FileID)
			iErr = lh.Add(fs.NewDir(remote, file.UpdatedAt).SetID(file.FileID))
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
		sha1:     strings.ToLower(file.ContentHash),
		modTime:  file.UpdatedAt,
	}
}

// findFile finds the file leaf in the folder dirID
func (f *Fs) findFile(ctx context.Context, dirID, leaf string) (*api.File, error) {
	name := f.opt.Enc.FromStandardName(leaf)
	var found *api.File
	err := f.listAll(ctx, dirID, func(file *api.File) bool {
		if file.Name == name {
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

// remove trashes or deletes the file or folder id
func (f *Fs) remove(ctx context.Context, id string) error {
	p := "/adrive/v1.0/openFile/recyclebin/trash"
	if f.opt.HardDelete {
		p = "/adrive/v1.0/openFile/delete"
	}
	return f.call(ctx, f.st.other, p, &api.FileRequest{DriveID: f.driveID, FileID: id}, nil)
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
	if err := f.remove(ctx, dirID); err != nil {
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
		return errors.New("aliyundrive: can't purge the root of the drive")
	}
	if err := f.remove(ctx, dirID); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

// About gets quota information
func (f *Fs) About(ctx context.Context) (*fs.Usage, error) {
	var s api.SpaceInfo
	if err := f.call(ctx, f.st.other, "/adrive/v1.0/user/getSpaceInfo", nil, &s); err != nil {
		return nil, err
	}
	total, used := s.PersonalSpaceInfo.TotalSize, s.PersonalSpaceInfo.UsedSize
	return &fs.Usage{
		Total: fs.NewUsageValue(total),
		Used:  fs.NewUsageValue(used),
		Free:  fs.NewUsageValue(total - used),
	}, nil
}

func (f *Fs) rename(ctx context.Context, id, leaf string) error {
	req := &api.UpdateRequest{DriveID: f.driveID, FileID: id, Name: f.opt.Enc.FromStandardName(leaf), CheckNameMode: api.CheckRefuse}
	return f.call(ctx, f.st.other, "/adrive/v1.0/openFile/update", req, nil)
}

// move moves id into the folder dstDirID as dstLeaf in one request
func (f *Fs) move(ctx context.Context, id, srcDirID, dstDirID, dstLeaf string) error {
	if srcDirID == dstDirID {
		return f.rename(ctx, id, dstLeaf)
	}
	req := &api.MoveRequest{DriveID: f.driveID, FileID: id, ToParentFileID: dstDirID, NewName: f.opt.Enc.FromStandardName(dstLeaf), CheckNameMode: api.CheckRefuse}
	var r api.MoveCopyResponse
	if err := f.call(ctx, f.st.other, "/adrive/v1.0/openFile/move", req, &r); err != nil {
		return err
	}
	if r.Exist {
		return fmt.Errorf("aliyundrive: can't move, %q already exists", dstLeaf)
	}
	return nil
}

// sameDrive reports whether src is on the same drive as f
func (f *Fs) sameDrive(src *Fs) bool {
	return src.driveID == f.driveID && src.opt.ClientID == f.opt.ClientID
}

// Move src to this remote using server-side move operations.
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok || !f.sameDrive(srcObj.fs) {
		return nil, fs.ErrorCantMove
	}
	dstLeaf, dstDirID, err := f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return nil, err
	}
	if err := f.move(ctx, srcObj.id, srcObj.parentID, dstDirID, dstLeaf); err != nil {
		return nil, err
	}
	o := *srcObj
	o.fs, o.remote, o.parentID = f, remote, dstDirID
	return &o, nil
}

// DirMove moves src, srcRemote to this remote at dstRemote
// using server-side move operations.
func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	srcFs, ok := src.(*Fs)
	if !ok || !f.sameDrive(srcFs) {
		return fs.ErrorCantDirMove
	}
	srcID, srcDirectoryID, _, dstDirectoryID, dstLeaf, err := f.dirCache.DirMove(ctx, srcFs.dirCache, srcFs.root, srcRemote, f.root, dstRemote)
	if err != nil {
		return err
	}
	if err := f.move(ctx, srcID, srcDirectoryID, dstDirectoryID, dstLeaf); err != nil {
		return err
	}
	srcFs.dirCache.FlushDir(srcRemote)
	return nil
}

// Copy src to this remote using server-side copy operations.
func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok || !f.sameDrive(srcObj.fs) {
		return nil, fs.ErrorCantCopy
	}
	dstLeaf, dstDirID, err := f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return nil, err
	}
	if dstDirID == srcObj.parentID {
		return nil, fs.ErrorCantCopy // the copy would have the same name
	}
	req := &api.CopyRequest{DriveID: f.driveID, FileID: srcObj.id, ToParentFileID: dstDirID}
	var r api.MoveCopyResponse
	if err := f.call(ctx, f.st.other, "/adrive/v1.0/openFile/copy", req, &r); err != nil {
		return nil, err
	}
	if r.FileID == "" {
		return nil, errors.New("aliyundrive: copy returned no file")
	}
	// The copy keeps the source name even if a file of that name exists,
	// so rename it by its ID, which refuses an existing name. A copy
	// keeping its name goes through a temporary name to be checked too.
	if path.Base(srcObj.remote) == dstLeaf {
		err = f.rename(ctx, r.FileID, ".rclone-copy-"+strconv.FormatInt(time.Now().UnixNano(), 36)+"-"+dstLeaf)
	}
	if err == nil {
		err = f.rename(ctx, r.FileID, dstLeaf)
	}
	if err != nil {
		// Deleted rather than trashed, as nobody asked for this copy
		delReq := &api.FileRequest{DriveID: f.driveID, FileID: r.FileID}
		if delErr := f.call(ctx, f.st.other, "/adrive/v1.0/openFile/delete", delReq, nil); delErr != nil {
			fs.Debugf(f, "Failed to remove the partial copy %q: %v", r.FileID, delErr)
		}
		// Uploading over a file replaces it, which the copy API can't do
		if apiCode(err) == api.CodeAlreadyExist {
			return nil, fs.ErrorCantCopy
		}
		return nil, err
	}
	o := *srcObj
	o.fs, o.remote, o.id, o.parentID = f, remote, r.FileID, dstDirID
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

// Hash returns the SHA1 of an object
func (o *Object) Hash(ctx context.Context, t hash.Type) (string, error) {
	if t != hash.SHA1 {
		return "", hash.ErrUnsupported
	}
	return o.sha1, nil
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
func (o *Object) ID() string { return o.id }

// Open an object for read
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	var r api.DownloadURLResponse
	req := &api.DownloadURLRequest{DriveID: o.fs.driveID, FileID: o.id, ExpireSec: 3600}
	if err := o.fs.call(ctx, o.fs.st.link, "/adrive/v1.0/openFile/getDownloadUrl", req, &r); err != nil {
		if isNotFound(err) {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	if r.URL == "" {
		return nil, errors.New("aliyundrive: no download URL")
	}
	fs.FixRangeOption(options, o.size)
	dl := rest.Opts{Method: "GET", RootURL: r.URL, Options: options}
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
	return o.fs.remove(ctx, o.id)
}

// ------------------------------------------------------------
// Upload

// uploadURLs gets fresh upload URLs for the parts of an upload
func (f *Fs) uploadURLs(ctx context.Context, fileID, uploadID string, parts int) ([]api.PartInfo, error) {
	req := &api.UploadURLRequest{DriveID: f.driveID, FileID: fileID, UploadID: uploadID, PartInfoList: partList(parts)}
	var r api.CreateResponse
	if err := f.call(ctx, f.st.other, "/adrive/v1.0/openFile/getUploadUrl", req, &r); err != nil {
		return nil, err
	}
	return r.PartInfoList, nil
}

func partList(n int) []api.PartInfo {
	parts := make([]api.PartInfo, n)
	for i := range parts {
		parts[i].PartNumber = i + 1
	}
	return parts
}

// upload stores in as leaf in the folder dirID, as a new file beside
// any existing one, and returns it
func (f *Fs) upload(ctx context.Context, in io.Reader, dirID, leaf string, size int64, modTime time.Time) (*api.File, error) {
	ps := partSize(size)
	n := int((size + ps - 1) / ps)
	parts := partList(n)
	req := &api.CreateRequest{
		DriveID:         f.driveID,
		ParentFileID:    dirID,
		Name:            f.opt.Enc.FromStandardName(leaf),
		Type:            api.TypeFile,
		CheckNameMode:   api.CheckIgnore,
		PartInfoList:    &parts,
		Size:            size,
		LocalModifiedAt: modTime.UTC().Format(timeFormat),
	}
	var c api.CreateResponse
	if err := f.call(ctx, f.st.other, "/adrive/v1.0/openFile/create", req, &c); err != nil {
		return nil, err
	}
	if !c.RapidUpload {
		parts := c.PartInfoList
		fetched := time.Now()
		buf := make([]byte, min(ps, max(size, 1)))
		for i, sent := 0, int64(0); i < n; i++ {
			chunk := buf[:min(ps, size-sent)]
			if _, err := io.ReadFull(in, chunk); err != nil {
				return nil, fmt.Errorf("aliyundrive: reading part %d: %w", i+1, err)
			}
			if time.Since(fetched) > uploadURLLife || i >= len(parts) {
				var err error
				if parts, err = f.uploadURLs(ctx, c.FileID, c.UploadID, n); err != nil {
					return nil, err
				}
				fetched = time.Now()
			}
			if i >= len(parts) || parts[i].UploadURL == "" {
				return nil, fmt.Errorf("aliyundrive: no upload URL for part %d", i+1)
			}
			length := int64(len(chunk))
			err := f.pacer.Call(func() (bool, error) {
				opts := rest.Opts{Method: "PUT", RootURL: parts[i].UploadURL, Body: bytes.NewReader(chunk), ContentLength: &length, NoResponse: true}
				resp, err := f.plain.Call(ctx, &opts)
				var apiErr *api.Error
				if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
					// The URL expired: get new ones and retry
					if fresh, uErr := f.uploadURLs(ctx, c.FileID, c.UploadID, n); uErr == nil {
						parts, fetched = fresh, time.Now()
						return true, err
					}
				}
				return f.shouldRetry(ctx, resp, err)
			})
			if err != nil {
				return nil, fmt.Errorf("aliyundrive: uploading part %d: %w", i+1, err)
			}
			sent += length
		}
	}
	var file api.File
	if err := f.call(ctx, f.st.other, "/adrive/v1.0/openFile/complete", &api.CompleteRequest{DriveID: f.driveID, FileID: c.FileID, UploadID: c.UploadID}, &file); err != nil {
		return nil, err
	}
	if file.FileID == "" {
		file.FileID = c.FileID
	}
	if file.ParentFileID == "" {
		file.ParentFileID = dirID
	}
	return &file, nil
}

// Update the object with the contents of the io.Reader, modTime and size
//
// The new data is uploaded beside the old file, which is removed
// afterwards, so a failed upload leaves the old one in place.
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	size := src.Size()
	if size < 0 {
		return errors.New("aliyundrive: can't upload files of unknown size")
	}
	leaf, dirID, err := o.fs.dirCache.FindPath(ctx, o.remote, true)
	if err != nil {
		return err
	}
	old := o.id
	if old == "" {
		// Put over an existing file replaces it too
		if existing, err := o.fs.findFile(ctx, dirID, leaf); err == nil {
			old = existing.FileID
		}
	}
	file, err := o.fs.upload(ctx, in, dirID, leaf, size, src.ModTime(ctx))
	if err != nil {
		return err
	}
	if old != "" && old != file.FileID {
		if err := o.fs.remove(ctx, old); err != nil && !isNotFound(err) {
			return fmt.Errorf("aliyundrive: uploaded but couldn't remove the old version: %w", err)
		}
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
