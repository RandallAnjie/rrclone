// Package baidu provides an interface to Baidu Netdisk (百度网盘)
// through its open platform (xpan) API
package baidu

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
	"time"

	"github.com/rclone/rclone/backend/baidu/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/list"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/rclone/rclone/lib/oauthutil"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
	"golang.org/x/oauth2"
)

const (
	defaultAPIURL    = "https://pan.baidu.com"
	defaultUploadURL = "https://d.pcs.baidu.com"
	defaultOAuthURL  = "https://openapi.baidu.com"
	listLimit        = 1000
	minSleep         = 100 * time.Millisecond
	maxSleep         = 8 * time.Second
	decayConstant    = 2
	// userAgent is required by Baidu for downloads
	userAgent = "pan.baidu.com"
	// sliceMD5Size is the prefix hashed for slice-md5
	sliceMD5Size = 256 * 1024
	// maxBlocks is the most blocks one upload can have
	maxBlocks = 2048
)

// Block sizes by membership: 0 normal, 1 VIP, 2 SVIP
var blockSizes = []int64{4 << 20, 16 << 20, 32 << 20}

func oauthConfig(m configmap.Mapper) *oauthutil.Config {
	base := defaultOAuthURL
	if v, ok := m.Get("oauth_url"); ok && v != "" {
		base = rest.CanonicalRoot(v)
	}
	return &oauthutil.Config{
		AuthURL:     base + "/oauth/2.0/authorize",
		TokenURL:    base + "/oauth/2.0/token",
		Scopes:      []string{"basic,netdisk"}, // Baidu separates scopes with commas
		RedirectURL: oauthutil.RedirectLocalhostURL,
		AuthStyle:   oauth2.AuthStyleInParams,
	}
}

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "baidu",
		Description: "Baidu Netdisk (百度网盘)",
		NewFs:       NewFs,
		Config: func(ctx context.Context, name string, m configmap.Mapper, config fs.ConfigIn) (*fs.ConfigOut, error) {
			return oauthutil.ConfigOut("", &oauthutil.Options{
				OAuth2Config: oauthConfig(m),
				NoOffline:    true,
			})
		},
		Options: append(oauthutil.SharedOptions, []fs.Option{{
			Name: "root_folder",
			Help: `Folder on Baidu Netdisk the remote starts at.

Leave blank for the whole drive. Apps which may only use their own
folder need "/apps/<app name>".`,
			Advanced: true,
		}, {
			Name: "block_size",
			Help: `Upload block size, or 0 to pick it from the membership.

Baidu allows 4 MiB blocks, 16 MiB for VIP and 32 MiB for SVIP, and at
most 2048 blocks per file.`,
			Default:  fs.SizeSuffix(0),
			Advanced: true,
		}, {
			Name:     "api_url",
			Help:     "URL of the Baidu API (https://pan.baidu.com), or a reverse proxy of it.",
			Default:  defaultAPIURL,
			Advanced: true,
		}, {
			Name:     "upload_url",
			Help:     "URL of the Baidu upload server (https://d.pcs.baidu.com), or a reverse proxy of it.",
			Default:  defaultUploadURL,
			Advanced: true,
		}, {
			Name:     "oauth_url",
			Help:     "URL of Baidu OAuth (https://openapi.baidu.com), or a reverse proxy of it.",
			Default:  defaultOAuthURL,
			Advanced: true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			// Baidu rejects \ ? | " > < : * and surrounding white space
			Default: encoder.Base |
				encoder.EncodeWin |
				encoder.EncodeBackSlash |
				encoder.EncodeLeftSpace |
				encoder.EncodeLeftCrLfHtVt |
				encoder.EncodeRightSpace |
				encoder.EncodeRightCrLfHtVt |
				encoder.EncodeInvalidUtf8,
		}}...),
	})
}

// Options defines the configuration for this backend
type Options struct {
	RootFolder string               `config:"root_folder"`
	BlockSize  fs.SizeSuffix        `config:"block_size"`
	APIURL     string               `config:"api_url"`
	UploadURL  string               `config:"upload_url"`
	Enc        encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote Baidu Netdisk
type Fs struct {
	name      string
	root      string
	opt       Options
	features  *fs.Features
	srv       *rest.Client // API
	plain     *rest.Client // uploads
	dl        *rest.Client // downloads, with Baidu's User-Agent
	ts        *oauthutil.TokenSource
	pacer     *fs.Pacer
	apiURL    string
	uploadURL string
	blockSize int64
}

// Object describes a Baidu file
type Object struct {
	fs      *Fs
	remote  string
	id      int64
	size    int64
	modTime time.Time
}

// ------------------------------------------------------------

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string { return f.name }

// Root of the remote (as passed into NewFs)
func (f *Fs) Root() string { return f.root }

// String converts this Fs to a string
func (f *Fs) String() string { return fmt.Sprintf("Baidu Netdisk root '%s'", f.root) }

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features { return f.features }

// Precision of the modification times
func (f *Fs) Precision() time.Duration { return time.Second }

// Hashes returns the supported hash sets.
//
// Baidu's md5 field isn't reliably the MD5 of the content, so none.
func (f *Fs) Hashes() hash.Set { return hash.Set(hash.None) }

func (f *Fs) shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Errno {
		case api.ErrnoFrequency:
			return true, err
		case api.ErrnoAuth, api.ErrnoTokenInvalid, api.ErrnoTokenExpired, api.ErrnoTokenOther:
			// The access token was rejected. Expire it, here and in the
			// config, so the retry refreshes it rather than reusing it.
			if eErr := f.ts.Expire(); eErr != nil {
				fs.Debugf(f, "Couldn't expire token: %v", eErr)
			}
			return true, err
		}
	}
	return fserrors.ShouldRetry(err) || fserrors.ShouldRetryHTTP(resp, []int{429, 500, 502, 503, 504}), err
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	if err := configstruct.Set(m, opt); err != nil {
		return nil, err
	}
	baseClient := fshttp.NewClient(ctx)
	// fshttp sets the User-Agent of every request from the config, so
	// downloads, which Baidu only serves to its own agent, need their
	// own client.
	dlCtx, dlConfig := fs.AddConfig(ctx)
	dlConfig.UserAgent = userAgent
	_, ts, err := oauthutil.NewClientWithBaseClient(ctx, name, m, oauthConfig(m), baseClient)
	if err != nil {
		return nil, fmt.Errorf("baidu: failed to configure token: %w", err)
	}
	f := &Fs{
		name:      name,
		root:      strings.Trim(root, "/"),
		opt:       *opt,
		ts:        ts,
		srv:       rest.NewClient(baseClient),
		plain:     rest.NewClient(baseClient),
		dl:        rest.NewClient(fshttp.NewClient(dlCtx)),
		pacer:     fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		apiURL:    orDefault(opt.APIURL, defaultAPIURL),
		uploadURL: orDefault(opt.UploadURL, defaultUploadURL),
	}
	f.srv.SetRoot(f.apiURL)
	f.features = (&fs.Features{
		CanHaveEmptyDirectories: true,
	}).Fill(ctx, f)

	f.blockSize = int64(opt.BlockSize)
	if f.blockSize <= 0 {
		var u api.UserInfo
		opts := rest.Opts{Method: "GET", Path: "/rest/2.0/xpan/nas", Parameters: url.Values{"method": {"uinfo"}}}
		if err := f.call(ctx, f.srv, &opts, nil, &u); err != nil {
			return nil, fmt.Errorf("baidu: couldn't read account info: %w", err)
		}
		f.blockSize = blockSizes[min(max(u.VipType, 0), len(blockSizes)-1)]
	}

	if f.root != "" {
		if file, err := f.stat(ctx, ""); err == nil && file.IsDir == 0 {
			f.root = path.Dir(f.root)
			if f.root == "." {
				f.root = ""
			}
			return f, fs.ErrorIsFile
		}
	}
	return f, nil
}

func orDefault(v, def string) string {
	if v = rest.CanonicalRoot(v); v != "" {
		return v
	}
	return def
}

// call makes an API call with the access token, decoding the JSON
// response into out, which must embed api.Status
func (f *Fs) call(ctx context.Context, srv *rest.Client, opts *rest.Opts, form url.Values, out interface{ Err() error }) error {
	return f.pacer.Call(func() (bool, error) {
		tok, err := f.ts.Token()
		if err != nil {
			return false, err
		}
		o := *opts
		o.Parameters = url.Values{}
		for k, v := range opts.Parameters {
			o.Parameters[k] = v
		}
		o.Parameters.Set("access_token", tok.AccessToken)
		if form != nil {
			o.Body = strings.NewReader(form.Encode())
			o.ContentType = "application/x-www-form-urlencoded"
		}
		resp, err := srv.CallJSON(ctx, &o, nil, out)
		if err == nil {
			err = out.Err()
		}
		return f.shouldRetry(ctx, resp, err)
	})
}

func errno(err error) int {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return apiErr.Errno
	}
	return api.ErrnoOK
}

// ------------------------------------------------------------
// Paths

// absPath returns the Baidu path of p, relative to the remote's root
func (f *Fs) absPath(p string) string {
	full := path.Join("/", f.opt.RootFolder, f.root, p)
	segments := strings.Split(strings.Trim(full, "/"), "/")
	if len(segments) == 1 && segments[0] == "" {
		return "/"
	}
	// The root folder is a real Baidu path, so isn't encoded
	rootSegments := len(strings.Split(strings.Trim(path.Join("/", f.opt.RootFolder), "/"), "/"))
	if strings.Trim(f.opt.RootFolder, "/") == "" {
		rootSegments = 0
	}
	for i := rootSegments; i < len(segments); i++ {
		segments[i] = f.opt.Enc.FromStandardName(segments[i])
	}
	return "/" + strings.Join(segments, "/")
}

// ------------------------------------------------------------
// Files and directories

// listAbs calls fn for each entry of the Baidu folder dir, stopping early if it returns true
func (f *Fs) listAbs(ctx context.Context, dir string, fn func(*api.File) bool) error {
	for start := 0; ; start += listLimit {
		opts := rest.Opts{
			Method: "GET",
			Path:   "/rest/2.0/xpan/file",
			Parameters: url.Values{
				"method": {"list"},
				"dir":    {dir},
				"start":  {strconv.Itoa(start)},
				"limit":  {strconv.Itoa(listLimit)},
				"web":    {"0"},
			},
		}
		var r api.ListResponse
		if err := f.call(ctx, f.srv, &opts, nil, &r); err != nil {
			if errno(err) == api.ErrnoNotExist {
				return fs.ErrorDirNotFound
			}
			return err
		}
		for i := range r.List {
			if fn(&r.List[i]) {
				return nil
			}
		}
		if len(r.List) < listLimit {
			return nil
		}
	}
}

// stat finds the file or folder at remote
func (f *Fs) stat(ctx context.Context, remote string) (*api.File, error) {
	p := f.absPath(remote)
	if p == "/" {
		return &api.File{Path: "/", IsDir: 1}, nil
	}
	dir, leaf := path.Split(p)
	var found *api.File
	err := f.listAbs(ctx, path.Clean(dir), func(file *api.File) bool {
		if file.ServerFilename == leaf {
			c := *file
			found = &c
			return true
		}
		return false
	})
	if errors.Is(err, fs.ErrorDirNotFound) || (err == nil && found == nil) {
		return nil, fs.ErrorObjectNotFound
	}
	return found, err
}

// List the objects and directories in dir into entries.
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	return list.WithListP(ctx, dir, f)
}

// ListP lists the objects and directories of the Fs starting
// from dir non recursively into out.
func (f *Fs) ListP(ctx context.Context, dir string, callback fs.ListRCallback) error {
	lh := list.NewHelper(callback)
	var iErr error
	err := f.listAbs(ctx, f.absPath(dir), func(file *api.File) bool {
		remote := path.Join(dir, f.opt.Enc.ToStandardName(file.ServerFilename))
		if file.IsDir != 0 {
			iErr = lh.Add(fs.NewDir(remote, time.Unix(file.ServerMtime, 0)).SetID(strconv.FormatInt(file.FsID, 10)))
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
	mtime := file.LocalMtime
	if mtime == 0 {
		mtime = file.ServerMtime
	}
	return &Object{fs: f, remote: remote, id: file.FsID, size: file.Size, modTime: time.Unix(mtime, 0)}
}

// NewObject finds the Object at remote.
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	file, err := f.stat(ctx, remote)
	if err != nil {
		return nil, err
	}
	if file.IsDir != 0 {
		return nil, fs.ErrorIsDir
	}
	return f.newObject(remote, file), nil
}

// Put the object into the container
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	o := &Object{fs: f, remote: src.Remote()}
	return o, o.Update(ctx, in, src, options...)
}

// mkdirAbs makes the Baidu folder p and its parents
func (f *Fs) mkdirAbs(ctx context.Context, p string) error {
	if p == "/" || p == "." {
		return nil
	}
	form := url.Values{"path": {p}, "isdir": {"1"}, "rtype": {"0"}}
	opts := rest.Opts{Method: "POST", Path: "/rest/2.0/xpan/file", Parameters: url.Values{"method": {"create"}}}
	var r api.CreateResponse
	err := f.call(ctx, f.srv, &opts, form, &r)
	switch errno(err) {
	case api.ErrnoOK, api.ErrnoExist:
		return nil
	case api.ErrnoNotExist:
		if err := f.mkdirAbs(ctx, path.Dir(p)); err != nil {
			return err
		}
		r = api.CreateResponse{}
		err = f.call(ctx, f.srv, &opts, form, &r)
		if errno(err) == api.ErrnoExist {
			return nil
		}
	}
	return err
}

// Mkdir creates the container if it doesn't exist
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	return f.mkdirAbs(ctx, f.absPath(dir))
}

// manage runs a filemanager operation (delete, move, copy, rename)
func (f *Fs) manage(ctx context.Context, opera string, items any) error {
	filelist, err := json.Marshal(items)
	if err != nil {
		return err
	}
	form := url.Values{"async": {"0"}, "filelist": {string(filelist)}, "ondup": {"fail"}}
	opts := rest.Opts{Method: "POST", Path: "/rest/2.0/xpan/file", Parameters: url.Values{"method": {"filemanager"}, "opera": {opera}}}
	var r api.ManageResponse
	err = f.call(ctx, f.srv, &opts, form, &r)
	// Partial failures carry the reason per item
	if err != nil && len(r.Info) > 0 && r.Info[0].Errno != 0 {
		return &api.Error{Errno: r.Info[0].Errno, Errmsg: "filemanager " + opera + " " + r.Info[0].Path}
	}
	return err
}

// deleteAbs moves the Baidu path p to the recycle bin
func (f *Fs) deleteAbs(ctx context.Context, p string) error {
	err := f.manage(ctx, "delete", []string{p})
	if errno(err) == api.ErrnoNotExist {
		return fs.ErrorObjectNotFound
	}
	return err
}

// Rmdir deletes the root folder
//
// Returns an error if it isn't empty
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	p := f.absPath(dir)
	empty := true
	if err := f.listAbs(ctx, p, func(*api.File) bool { empty = false; return true }); err != nil {
		return err
	}
	if !empty {
		return fs.ErrorDirectoryNotEmpty
	}
	if p == "/" {
		return nil
	}
	return f.deleteAbs(ctx, p)
}

// Purge deletes all the files and the container
func (f *Fs) Purge(ctx context.Context, dir string) error {
	p := f.absPath(dir)
	if p == "/" {
		return errors.New("baidu: can't purge the root of the drive")
	}
	err := f.deleteAbs(ctx, p)
	if errors.Is(err, fs.ErrorObjectNotFound) {
		return fs.ErrorDirNotFound
	}
	return err
}

// relocate moves or copies srcAbs to dstAbs in one request, as Baidu
// renames while moving or copying
func (f *Fs) relocate(ctx context.Context, srcAbs, dstAbs string, copy bool) error {
	dstDir, dstLeaf := path.Split(dstAbs)
	dstDir = path.Clean(dstDir)
	if !copy && path.Dir(srcAbs) == dstDir {
		return f.manage(ctx, "rename", []api.ManageItem{{Path: srcAbs, Newname: dstLeaf}})
	}
	if err := f.mkdirAbs(ctx, dstDir); err != nil {
		return err
	}
	opera := "move"
	if copy {
		opera = "copy"
	}
	return f.manage(ctx, opera, []api.ManageItem{{Path: srcAbs, Dest: dstDir, Newname: dstLeaf, Ondup: "fail"}})
}

// sameAccount reports whether src is the same Baidu account as f
func (f *Fs) sameAccount(src *Fs) bool {
	return src.name == f.name || src.apiURL == f.apiURL && f.tokenIs(src)
}

func (f *Fs) tokenIs(src *Fs) bool {
	a, errA := f.ts.Token()
	b, errB := src.ts.Token()
	return errA == nil && errB == nil && a.RefreshToken != "" && a.RefreshToken == b.RefreshToken
}

// Copy src to this remote using server-side copy operations.
func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok || !f.sameAccount(srcObj.fs) {
		return nil, fs.ErrorCantCopy
	}
	if err := f.relocate(ctx, srcObj.fs.absPath(srcObj.remote), f.absPath(remote), true); err != nil {
		return nil, err
	}
	return f.NewObject(ctx, remote)
}

// Move src to this remote using server-side move operations.
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok || !f.sameAccount(srcObj.fs) {
		return nil, fs.ErrorCantMove
	}
	if err := f.relocate(ctx, srcObj.fs.absPath(srcObj.remote), f.absPath(remote), false); err != nil {
		return nil, err
	}
	return f.NewObject(ctx, remote)
}

// DirMove moves src, srcRemote to this remote at dstRemote
// using server-side move operations.
func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	srcFs, ok := src.(*Fs)
	if !ok || !f.sameAccount(srcFs) {
		return fs.ErrorCantDirMove
	}
	if _, err := f.stat(ctx, dstRemote); err == nil {
		return fs.ErrorDirExists
	} else if !errors.Is(err, fs.ErrorObjectNotFound) {
		return err
	}
	return f.relocate(ctx, srcFs.absPath(srcRemote), f.absPath(dstRemote), false)
}

// About gets quota information
func (f *Fs) About(ctx context.Context) (*fs.Usage, error) {
	opts := rest.Opts{Method: "GET", Path: "/api/quota", Parameters: url.Values{"checkfree": {"1"}}}
	var q api.QuotaResponse
	if err := f.call(ctx, f.srv, &opts, nil, &q); err != nil {
		return nil, err
	}
	return &fs.Usage{
		Total: fs.NewUsageValue(q.Total),
		Used:  fs.NewUsageValue(q.Used),
		Free:  fs.NewUsageValue(q.Total - q.Used),
	}, nil
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

// Hash returns the hash of an object
func (o *Object) Hash(ctx context.Context, t hash.Type) (string, error) {
	return "", hash.ErrUnsupported
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
		Method: "GET",
		Path:   "/rest/2.0/xpan/multimedia",
		Parameters: url.Values{
			"method": {"filemetas"},
			"fsids":  {"[" + strconv.FormatInt(o.id, 10) + "]"},
			"dlink":  {"1"},
		},
	}
	var metas api.FileMetas
	if err := o.fs.call(ctx, o.fs.srv, &opts, nil, &metas); err != nil {
		if errno(err) == api.ErrnoNotFoundMeta {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	if len(metas.List) == 0 || metas.List[0].Dlink == "" {
		return nil, errors.New("baidu: no download link")
	}
	dlink := metas.List[0].Dlink
	fs.FixRangeOption(options, o.size)
	var resp *http.Response
	err := o.fs.pacer.Call(func() (bool, error) {
		tok, err := o.fs.ts.Token()
		if err != nil {
			return false, err
		}
		u, err := url.Parse(dlink)
		if err != nil {
			return false, err
		}
		q := u.Query()
		q.Set("access_token", tok.AccessToken)
		u.RawQuery = q.Encode()
		dl := rest.Opts{
			Method:  "GET",
			RootURL: u.String(),
			Options: options,
		}
		resp, err = o.fs.dl.Call(ctx, &dl)
		return o.fs.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Remove an object
func (o *Object) Remove(ctx context.Context) error {
	return o.fs.deleteAbs(ctx, o.fs.absPath(o.remote))
}

// ------------------------------------------------------------
// Upload

// spooled is an upload buffered to a temporary file with the hashes
// Baidu needs before it starts
type spooled struct {
	file       *os.File
	size       int64
	contentMD5 string
	sliceMD5   string   // MD5 of the first 256 KiB
	blocks     []string // MD5 of each block
}

func (s *spooled) close() {
	_ = s.file.Close()
	_ = os.Remove(s.file.Name())
}

// spool buffers in to a temporary file, hashing it in blocks
func spool(in io.Reader, blockSize int64) (*spooled, error) {
	file, err := os.CreateTemp("", "rclone-baidu-")
	if err != nil {
		return nil, err
	}
	s := &spooled{file: file}
	content, prefix, block := md5.New(), md5.New(), md5.New()
	var prefixLeft int64 = sliceMD5Size
	buf := make([]byte, 1<<20)
	var inBlock int64
	for {
		n, rErr := in.Read(buf)
		data := buf[:n]
		if _, err := file.Write(data); err != nil {
			s.close()
			return nil, err
		}
		content.Write(data)
		if prefixLeft > 0 {
			k := min(prefixLeft, int64(len(data)))
			prefix.Write(data[:k])
			prefixLeft -= k
		}
		for len(data) > 0 {
			k := min(blockSize-inBlock, int64(len(data)))
			block.Write(data[:k])
			inBlock += k
			data = data[k:]
			if inBlock == blockSize {
				s.blocks = append(s.blocks, hex.EncodeToString(block.Sum(nil)))
				block.Reset()
				inBlock = 0
			}
		}
		s.size += int64(n)
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			s.close()
			return nil, rErr
		}
	}
	if inBlock > 0 || len(s.blocks) == 0 {
		s.blocks = append(s.blocks, hex.EncodeToString(block.Sum(nil)))
	}
	s.contentMD5 = hex.EncodeToString(content.Sum(nil))
	s.sliceMD5 = hex.EncodeToString(prefix.Sum(nil))
	return s, nil
}

// blockSizeFor returns a block size giving at most maxBlocks blocks
func (f *Fs) blockSizeFor(size int64) int64 {
	bs := f.blockSize
	for size > bs*maxBlocks && bs < blockSizes[len(blockSizes)-1] {
		bs *= 2
	}
	return bs
}

// uploadServer finds the server to send blocks to
func (f *Fs) uploadServer(ctx context.Context, p, uploadID string) string {
	if f.uploadURL != defaultUploadURL {
		return f.uploadURL // a proxy
	}
	opts := rest.Opts{
		Method:  "GET",
		RootURL: f.uploadURL + "/rest/2.0/pcs/file",
		Parameters: url.Values{
			"method":         {"locateupload"},
			"appid":          {"250528"},
			"path":           {p},
			"uploadid":       {uploadID},
			"upload_version": {"2.0"},
		},
	}
	var r api.UploadServers
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.plain.CallJSON(ctx, &opts, nil, &r)
		return f.shouldRetry(ctx, resp, err)
	})
	if err == nil {
		for _, s := range append(r.Servers, r.BakServers...) {
			if strings.HasPrefix(s.Server, "https://") {
				return strings.TrimRight(s.Server, "/")
			}
		}
	}
	return f.uploadURL
}

// uploadBlock sends block n of an upload
func (f *Fs) uploadBlock(ctx context.Context, server, p, uploadID string, n int, data []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "blob")
	if err != nil {
		return err
	}
	_, _ = part.Write(data)
	if err := w.Close(); err != nil {
		return err
	}
	opts := rest.Opts{
		Method:  "POST",
		RootURL: server + "/rest/2.0/pcs/superfile2",
		Parameters: url.Values{
			"method":   {"upload"},
			"type":     {"tmpfile"},
			"path":     {p},
			"uploadid": {uploadID},
			"partseq":  {strconv.Itoa(n)},
		},
		ContentType: w.FormDataContentType(),
	}
	return f.pacer.Call(func() (bool, error) {
		tok, err := f.ts.Token()
		if err != nil {
			return false, err
		}
		o := opts
		o.Parameters = url.Values{}
		for k, v := range opts.Parameters {
			o.Parameters[k] = v
		}
		o.Parameters.Set("access_token", tok.AccessToken)
		length := int64(body.Len())
		o.ContentLength = &length
		o.Body = bytes.NewReader(body.Bytes())
		var r api.SliceResponse
		resp, err := f.plain.CallJSON(ctx, &o, nil, &r)
		if err == nil {
			err = r.Err()
		}
		if err == nil && r.Md5 == "" {
			err = errors.New("baidu: block upload returned no md5")
		}
		return f.shouldRetry(ctx, resp, err)
	})
}

// upload stores in at remote with modTime, replacing any existing file
func (f *Fs) upload(ctx context.Context, in io.Reader, remote string, size int64, modTime time.Time) (*api.File, error) {
	p := f.absPath(remote)
	if err := f.mkdirAbs(ctx, path.Dir(p)); err != nil {
		return nil, err
	}
	blockSize := f.blockSizeFor(size)
	s, err := spool(in, blockSize)
	if err != nil {
		return nil, fmt.Errorf("baidu: couldn't buffer upload: %w", err)
	}
	defer s.close()
	if size >= 0 && s.size != size {
		return nil, fmt.Errorf("baidu: read %d bytes, expected %d", s.size, size)
	}
	blockList, _ := json.Marshal(s.blocks)
	mtime := strconv.FormatInt(modTime.Unix(), 10)
	form := url.Values{
		"path":        {p},
		"size":        {strconv.FormatInt(s.size, 10)},
		"isdir":       {"0"},
		"autoinit":    {"1"},
		"rtype":       {"3"}, // overwrite
		"block_list":  {string(blockList)},
		"content-md5": {s.contentMD5},
		"slice-md5":   {s.sliceMD5},
		"local_mtime": {mtime},
		"local_ctime": {mtime},
	}
	opts := rest.Opts{Method: "POST", Path: "/rest/2.0/xpan/file", Parameters: url.Values{"method": {"precreate"}}}
	var pre api.PrecreateResponse
	if err := f.call(ctx, f.srv, &opts, form, &pre); err != nil {
		return nil, fmt.Errorf("baidu: precreate failed: %w", err)
	}
	if pre.ReturnType == api.ReturnTypeDone {
		// Baidu already had the content
		return f.stat(ctx, remote)
	}
	server := f.uploadServer(ctx, p, pre.UploadID)
	todo := pre.BlockList
	if len(todo) == 0 {
		for i := range s.blocks {
			todo = append(todo, i)
		}
	}
	buf := make([]byte, blockSize)
	for _, n := range todo {
		if n < 0 || n >= len(s.blocks) {
			return nil, fmt.Errorf("baidu: asked for block %d of %d", n, len(s.blocks))
		}
		chunk := buf[:min(blockSize, s.size-int64(n)*blockSize)]
		if _, err := s.file.ReadAt(chunk, int64(n)*blockSize); err != nil && err != io.EOF {
			return nil, err
		}
		if err := f.uploadBlock(ctx, server, p, pre.UploadID, n, chunk); err != nil {
			return nil, fmt.Errorf("baidu: uploading block %d: %w", n, err)
		}
	}
	form = url.Values{
		"path":        {p},
		"size":        {strconv.FormatInt(s.size, 10)},
		"isdir":       {"0"},
		"rtype":       {"3"},
		"uploadid":    {pre.UploadID},
		"block_list":  {string(blockList)},
		"local_mtime": {mtime},
		"local_ctime": {mtime},
	}
	opts = rest.Opts{Method: "POST", Path: "/rest/2.0/xpan/file", Parameters: url.Values{"method": {"create"}}}
	var created api.CreateResponse
	if err := f.call(ctx, f.srv, &opts, form, &created); err != nil {
		return nil, fmt.Errorf("baidu: create failed: %w", err)
	}
	return f.stat(ctx, remote)
}

// Update the object with the contents of the io.Reader, modTime and size
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	file, err := o.fs.upload(ctx, in, o.remote, src.Size(), src.ModTime(ctx))
	if err != nil {
		return err
	}
	*o = *o.fs.newObject(o.remote, file)
	return nil
}

// Check the interfaces are satisfied
var (
	_ fs.Fs       = (*Fs)(nil)
	_ fs.Copier   = (*Fs)(nil)
	_ fs.Mover    = (*Fs)(nil)
	_ fs.DirMover = (*Fs)(nil)
	_ fs.Purger   = (*Fs)(nil)
	_ fs.Abouter  = (*Fs)(nil)
	_ fs.ListPer  = (*Fs)(nil)
	_ fs.Object   = (*Object)(nil)
	_ fs.IDer     = (*Object)(nil)
)
