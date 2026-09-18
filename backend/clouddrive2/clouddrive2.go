// Package clouddrive2 provides an interface to a CloudDrive2 gRPC server.
package clouddrive2

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/backend/clouddrive2/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/dircache"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	defaultMinSleep = 10 * time.Millisecond
	maxSleep        = 2 * time.Second
	decayConstant   = 2
	defaultAddr     = "localhost:19798"
	defaultChunk    = 1 << 20
	hashTypeMD5     = 1
	hashTypeSHA1    = 2
	hashTypePikPak  = 3
)

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "clouddrive2",
		Description: "CloudDrive2",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "address",
			Help: `CloudDrive2 gRPC address.

host:port, or an http(s) URL. Default localhost:19798, the usual
local CloudDrive2 HTTP/gRPC port.
`,
			Default: defaultAddr,
		}, {
			Name:      "username",
			Help:      "CloudDrive2 username. Optional if token is set.",
			Sensitive: true,
		}, {
			Name:       "password",
			Help:       "CloudDrive2 password. Optional if token is set.",
			IsPassword: true,
		}, {
			Name:      "token",
			Help:      "JWT or API token. Optional if username and password are set.",
			Sensitive: true,
			Advanced:  true,
		}, {
			Name:     "totp",
			Help:     "TOTP code for GetToken when 2FA is enabled.",
			Advanced: true,
		}, {
			Name: "root_folder",
			Help: `Path inside CloudDrive2 to use as the remote root.

Leave as / for the CloudDrive2 tree (CloudNAS mounts and local folders).
Set this to a mount such as /CloudNAS/115 to restrict rclone to that folder.
`,
			Default: "/",
		}, {
			Name:    "tls",
			Help:    "Use TLS for gRPC. Implied when address starts with https://.",
			Default: false,
		}, {
			Name:     "insecure_skip_verify",
			Help:     "Skip TLS certificate verification.",
			Default:  false,
			Advanced: true,
		}, {
			Name:     "upload_chunk_size",
			Help:     "Size of each WriteToFile chunk.",
			Default:  fs.SizeSuffix(defaultChunk),
			Advanced: true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default:  encoder.Display,
		}},
	})
}

// Options defines the configuration of this backend.
type Options struct {
	Address            string               `config:"address"`
	Username           string               `config:"username"`
	Password           string               `config:"password"`
	Token              string               `config:"token"`
	TOTP               string               `config:"totp"`
	RootFolder         string               `config:"root_folder"`
	TLS                bool                 `config:"tls"`
	InsecureSkipVerify bool                 `config:"insecure_skip_verify"`
	UploadChunkSize    fs.SizeSuffix        `config:"upload_chunk_size"`
	Enc                encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a CloudDrive2 server.
type Fs struct {
	name     string
	root     string
	opt      Options
	features *fs.Features
	pacer    *fs.Pacer
	dirCache *dircache.DirCache
	dl       *rest.Client
	conn     *grpc.ClientConn
	client   api.CloudDriveFileSrvClient
	origin   string
	scheme   string
	host     string
	mu       sync.Mutex
	token    string
}

// Object describes a CloudDrive2 file.
type Object struct {
	fs      *Fs
	remote  string
	fsPath  string
	size    int64
	modTime time.Time
	md5sum  string
	sha1sum string
	dURL    string
	headers map[string]string
	ua      string
}

func parsePath(p string) string { return strings.Trim(p, "/") }

func joinAPI(parts ...string) string {
	var bits []string
	for _, p := range parts {
		p = strings.Trim(p, "/")
		if p != "" {
			bits = append(bits, p)
		}
	}
	if len(bits) == 0 {
		return "/"
	}
	return "/" + strings.Join(bits, "/")
}

func parseAddr(addr string, tlsFlag bool) (target, origin, scheme, host string, useTLS bool, err error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = defaultAddr
	}
	if !strings.Contains(addr, "://") {
		useTLS = tlsFlag
		scheme = "http"
		if useTLS {
			scheme = "https"
		}
		return addr, scheme + "://" + addr, scheme, addr, useTLS, nil
	}
	u, err := url.Parse(addr)
	if err != nil {
		return "", "", "", "", false, err
	}
	if u.Host == "" {
		return "", "", "", "", false, fmt.Errorf("clouddrive2: invalid address %q", addr)
	}
	useTLS = u.Scheme == "https" || tlsFlag
	scheme = u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	host = u.Host
	target = u.Host
	origin = scheme + "://" + u.Host
	return target, origin, scheme, host, useTLS, nil
}

func revealPassword(s string) string {
	if s == "" {
		return ""
	}
	out, err := obscure.Reveal(s)
	if err != nil {
		return s
	}
	return out
}

func shouldRetry(ctx context.Context, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	if err == nil {
		return false, nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return fserrors.ShouldRetry(err), err
	}
	switch st.Code() {
	case codes.Unavailable, codes.ResourceExhausted, codes.Aborted, codes.DeadlineExceeded:
		return true, err
	default:
		return false, err
	}
}

func checkOp(res *api.FileOperationResult, err error) error {
	if err != nil {
		return err
	}
	if res == nil {
		return nil
	}
	if res.GetSuccess() {
		return nil
	}
	if res.GetErrorMessage() != "" {
		return fmt.Errorf("clouddrive2: %s", res.GetErrorMessage())
	}
	return errors.New("clouddrive2: operation failed")
}

func fileTime(info *api.CloudDriveFile) time.Time {
	if info.GetWriteTime() != nil {
		return info.GetWriteTime().AsTime()
	}
	if info.GetCreateTime() != nil {
		return info.GetCreateTime().AsTime()
	}
	return time.Time{}
}

func fileIsDir(info *api.CloudDriveFile) bool {
	return info.GetIsDirectory() || info.GetFileType() == api.CloudDriveFile_Directory
}

func filePath(info *api.CloudDriveFile, parent string) string {
	if p := info.GetFullPathName(); p != "" {
		return joinAPI(p)
	}
	return joinAPI(parent, info.GetName())
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}
	opt.Password = revealPassword(opt.Password)
	if strings.TrimSpace(opt.Token) == "" && (strings.TrimSpace(opt.Username) == "" || opt.Password == "") {
		return nil, errors.New("clouddrive2: token or username and password are required")
	}
	if opt.Address == "" {
		opt.Address = defaultAddr
	}
	if opt.RootFolder == "" {
		opt.RootFolder = "/"
	}
	if opt.UploadChunkSize <= 0 {
		opt.UploadChunkSize = fs.SizeSuffix(defaultChunk)
	}
	target, origin, scheme, host, useTLS, err := parseAddr(opt.Address, opt.TLS)
	if err != nil {
		return nil, err
	}
	var creds credentials.TransportCredentials
	if useTLS {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if opt.InsecureSkipVerify {
			tlsCfg.InsecureSkipVerify = true
		}
		creds = credentials.NewTLS(tlsCfg)
	} else {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("clouddrive2: dial: %w", err)
	}
	root = parsePath(root)
	client := fshttp.NewClient(ctx)
	rootFolderID := joinAPI(opt.RootFolder, root)
	f := &Fs{
		name:   name,
		root:   root,
		opt:    *opt,
		pacer:  fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(defaultMinSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		dl:     rest.NewClient(client),
		conn:   conn,
		client: api.NewCloudDriveFileSrvClient(conn),
		origin: origin,
		scheme: scheme,
		host:   host,
		token:  strings.TrimSpace(opt.Token),
	}
	f.features = (&fs.Features{CanHaveEmptyDirectories: true}).Fill(ctx, f)
	f.dirCache = dircache.New(root, rootFolderID, f)
	if err := f.ensureToken(ctx); err != nil {
		return nil, err
	}
	if _, err := f.listAll(ctx, rootFolderID, false, false, func(*api.CloudDriveFile) bool { return false }); err != nil {
		return nil, fmt.Errorf("clouddrive2 login check failed: %w", err)
	}
	err = f.dirCache.FindRoot(ctx, false)
	if err != nil {
		newRoot, remote := dircache.SplitPath(root)
		tempF := *f
		tempF.dirCache = dircache.New(newRoot, joinAPI(opt.RootFolder, newRoot), &tempF)
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

func (f *Fs) authCtx(ctx context.Context) context.Context {
	f.mu.Lock()
	token := f.token
	f.mu.Unlock()
	if token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func (f *Fs) ensureToken(ctx context.Context) error {
	f.mu.Lock()
	if f.token != "" {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()
	return f.refreshToken(ctx)
}

func (f *Fs) refreshToken(ctx context.Context) error {
	if strings.TrimSpace(f.opt.Username) == "" || f.opt.Password == "" {
		if f.token != "" {
			return nil
		}
		return errors.New("clouddrive2: token expired and no username/password to refresh")
	}
	req := &api.GetTokenRequest{UserName: f.opt.Username, Password: f.opt.Password}
	if f.opt.TOTP != "" {
		totp := f.opt.TOTP
		req.TotpCode = &totp
	}
	var info *api.JWTToken
	err := f.pacer.Call(func() (bool, error) {
		var err error
		info, err = f.client.GetToken(ctx, req)
		return shouldRetry(ctx, err)
	})
	if err != nil {
		return fmt.Errorf("clouddrive2: GetToken: %w", err)
	}
	if !info.GetSuccess() || info.GetToken() == "" {
		msg := info.GetErrorMessage()
		if msg == "" {
			msg = "empty token"
		}
		return fmt.Errorf("clouddrive2: GetToken: %s", msg)
	}
	f.mu.Lock()
	f.token = info.GetToken()
	f.mu.Unlock()
	return nil
}

func (f *Fs) call(ctx context.Context, fn func(context.Context) error) error {
	if err := f.ensureToken(ctx); err != nil {
		return err
	}
	return f.pacer.Call(func() (bool, error) {
		err := fn(f.authCtx(ctx))
		if status.Code(err) == codes.Unauthenticated && f.opt.Username != "" {
			if rerr := f.refreshToken(ctx); rerr != nil {
				return false, rerr
			}
			err = fn(f.authCtx(ctx))
		}
		return shouldRetry(ctx, err)
	})
}

func (f *Fs) Name() string             { return f.name }
func (f *Fs) Root() string             { return f.root }
func (f *Fs) String() string           { return fmt.Sprintf("CloudDrive2 root '%s'", f.root) }
func (f *Fs) Features() *fs.Features   { return f.features }
func (f *Fs) Precision() time.Duration { return time.Second }
func (f *Fs) Hashes() hash.Set         { return hash.NewHashSet(hash.MD5, hash.SHA1) }
func (f *Fs) DirCacheFlush()           { f.dirCache.ResetRoot() }

func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, info *api.CloudDriveFile) (fs.Object, error) {
	o := &Object{fs: f, remote: remote}
	if info != nil {
		parent := joinAPI(f.opt.RootFolder, f.root, path.Dir(remote))
		if path.Dir(remote) == "." {
			parent = joinAPI(f.opt.RootFolder, f.root)
		}
		return o, o.setMetaData(info, filePath(info, parent))
	}
	return o, o.readMetaData(ctx)
}

func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return f.newObjectWithInfo(ctx, remote, nil)
}

type listAllFn func(*api.CloudDriveFile) bool

func (f *Fs) listAll(ctx context.Context, dirPath string, directoriesOnly, filesOnly bool, fn listAllFn) (found bool, err error) {
	req := &api.ListSubFileRequest{Path: dirPath}
	var stream grpc.ServerStreamingClient[api.SubFilesReply]
	err = f.call(ctx, func(ctx context.Context) error {
		var err error
		stream, err = f.client.GetSubFiles(ctx, req)
		return err
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return false, fs.ErrorDirNotFound
		}
		return false, err
	}
	for {
		reply, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return false, fs.ErrorDirNotFound
			}
			return false, err
		}
		for i := range reply.SubFiles {
			item := reply.SubFiles[i]
			item.Name = f.opt.Enc.ToStandardName(item.GetName())
			if fileIsDir(item) {
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
	}
	return false, nil
}

func (f *Fs) readMetaDataForPath(ctx context.Context, remote string) (*api.CloudDriveFile, error) {
	leaf, directoryID, err := f.dirCache.FindPath(ctx, remote, false)
	if err != nil {
		if err == fs.ErrorDirNotFound {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	info, err := f.findFile(ctx, directoryID, leaf)
	if err != nil {
		return nil, err
	}
	if fileIsDir(info) {
		return nil, fs.ErrorIsDir
	}
	return info, nil
}

func (f *Fs) findFile(ctx context.Context, parent, leaf string) (*api.CloudDriveFile, error) {
	var info *api.CloudDriveFile
	err := f.call(ctx, func(ctx context.Context) error {
		var err error
		info, err = f.client.FindFileByPath(ctx, &api.FindFileByPathRequest{ParentPath: parent, Path: leaf})
		return err
	})
	if err == nil && info != nil && info.GetName() != "" {
		if fileIsDir(info) {
			return info, fs.ErrorIsDir
		}
		return info, nil
	}
	if err != nil && status.Code(err) != codes.NotFound {
		return nil, err
	}
	found, err := f.listAll(ctx, parent, false, true, func(item *api.CloudDriveFile) bool {
		if item.GetName() == leaf {
			info = item
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if !found || info == nil {
		return nil, fs.ErrorObjectNotFound
	}
	return info, nil
}

func (f *Fs) FindLeaf(ctx context.Context, pathID, leaf string) (pathIDOut string, found bool, err error) {
	found, err = f.listAll(ctx, pathID, true, false, func(item *api.CloudDriveFile) bool {
		if item.GetName() == leaf {
			pathIDOut = filePath(item, pathID)
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (string, error) {
	name := f.opt.Enc.FromStandardName(leaf)
	var info *api.CreateFolderResult
	err := f.call(ctx, func(ctx context.Context) error {
		var err error
		info, err = f.client.CreateFolder(ctx, &api.CreateFolderRequest{ParentPath: pathID, FolderName: name})
		return err
	})
	if err != nil {
		id, found, ferr := f.FindLeaf(ctx, pathID, leaf)
		if ferr == nil && found {
			return id, nil
		}
		return "", err
	}
	if err := checkOp(info.GetResult(), nil); err != nil {
		id, found, ferr := f.FindLeaf(ctx, pathID, leaf)
		if ferr == nil && found {
			return id, nil
		}
		return "", err
	}
	if info.GetFolderCreated() != nil && info.GetFolderCreated().GetFullPathName() != "" {
		return filePath(info.GetFolderCreated(), pathID), nil
	}
	return joinAPI(pathID, leaf), nil
}

func (f *Fs) itemToDirEntry(ctx context.Context, remote, dirPath string, info *api.CloudDriveFile) (fs.DirEntry, error) {
	if fileIsDir(info) {
		id := filePath(info, dirPath)
		f.dirCache.Put(remote, id)
		d := fs.NewDir(remote, fileTime(info))
		d.SetID(id)
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
	_, err = f.listAll(ctx, directoryID, false, false, func(info *api.CloudDriveFile) bool {
		remote := path.Join(dir, info.GetName())
		entry, err := f.itemToDirEntry(ctx, remote, directoryID, info)
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
	o = &Object{fs: f, remote: remote, fsPath: joinAPI(directoryID, leaf)}
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

func (f *Fs) purgeCheck(ctx context.Context, dir string, check bool) error {
	if dir == "" && f.root == "" && (f.opt.RootFolder == "" || f.opt.RootFolder == "/") {
		return errors.New("can't purge root directory")
	}
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	if check {
		found, err := f.listAll(ctx, dirID, false, false, func(*api.CloudDriveFile) bool { return true })
		if err != nil {
			return err
		}
		if found {
			return fs.ErrorDirectoryNotEmpty
		}
	}
	if err := f.deletePath(ctx, dirID); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

func (f *Fs) deletePath(ctx context.Context, p string) error {
	var res *api.FileOperationResult
	err := f.call(ctx, func(ctx context.Context) error {
		var err error
		res, err = f.client.DeleteFile(ctx, &api.FileRequest{Path: p})
		return err
	})
	return checkOp(res, err)
}

func (f *Fs) Rmdir(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, true) }
func (f *Fs) Purge(ctx context.Context, dir string) error { return f.purgeCheck(ctx, dir, false) }

func (f *Fs) About(ctx context.Context) (*fs.Usage, error) {
	rootID, err := f.dirCache.FindDir(ctx, "", false)
	if err != nil {
		return nil, err
	}
	var info *api.SpaceInfo
	err = f.call(ctx, func(ctx context.Context) error {
		var err error
		info, err = f.client.GetSpaceInfo(ctx, &api.FileRequest{Path: rootID})
		return err
	})
	if err != nil {
		return nil, err
	}
	return &fs.Usage{
		Used:  fs.NewUsageValue(info.GetUsedSpace()),
		Total: fs.NewUsageValue(info.GetTotalSpace()),
		Free:  fs.NewUsageValue(info.GetFreeSpace()),
	}, nil
}

func (f *Fs) moveCopy(ctx context.Context, srcPath, dstDir, dstLeaf string, copy bool) error {
	srcParent, srcLeaf := path.Split(strings.TrimSuffix(srcPath, "/"))
	srcParent = joinAPI(srcParent)
	srcLeaf = strings.TrimSuffix(srcLeaf, "/")
	if copy {
		var res *api.FileOperationResult
		err := f.call(ctx, func(ctx context.Context) error {
			var err error
			res, err = f.client.CopyFile(ctx, &api.CopyFileRequest{TheFilePaths: []string{srcPath}, DestPath: dstDir})
			return err
		})
		if err := checkOp(res, err); err != nil {
			return err
		}
		if srcLeaf != dstLeaf {
			copied := joinAPI(dstDir, srcLeaf)
			return f.rename(ctx, copied, dstLeaf)
		}
		return nil
	}
	if srcParent == dstDir {
		if srcLeaf == dstLeaf {
			return nil
		}
		return f.rename(ctx, srcPath, dstLeaf)
	}
	var res *api.FileOperationResult
	err := f.call(ctx, func(ctx context.Context) error {
		var err error
		policy := api.MoveFileRequest_Overwrite
		res, err = f.client.MoveFile(ctx, &api.MoveFileRequest{
			TheFilePaths:   []string{srcPath},
			DestPath:       dstDir,
			ConflictPolicy: &policy,
		})
		return err
	})
	if err := checkOp(res, err); err != nil {
		return err
	}
	if srcLeaf != dstLeaf {
		return f.rename(ctx, joinAPI(dstDir, srcLeaf), dstLeaf)
	}
	return nil
}

func (f *Fs) rename(ctx context.Context, fsPath, newLeaf string) error {
	var res *api.FileOperationResult
	err := f.call(ctx, func(ctx context.Context) error {
		var err error
		res, err = f.client.RenameFile(ctx, &api.RenameFileRequest{
			TheFilePath: fsPath,
			NewName:     f.opt.Enc.FromStandardName(newLeaf),
		})
		return err
	})
	return checkOp(res, err)
}

func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fs.ErrorCantCopy
	}
	if srcObj.fsPath == "" {
		if err := srcObj.readMetaData(ctx); err != nil {
			return nil, err
		}
	}
	_, dstLeaf, dstDirectoryID, err := f.createObject(ctx, remote, srcObj.modTime, srcObj.size)
	if err != nil {
		return nil, err
	}
	if err := f.moveCopy(ctx, srcObj.fsPath, dstDirectoryID, dstLeaf, true); err != nil {
		return nil, err
	}
	f.dirCache.FlushDir(path.Dir(remote))
	return f.NewObject(ctx, remote)
}

func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fs.ErrorCantMove
	}
	if srcObj.fsPath == "" {
		if err := srcObj.readMetaData(ctx); err != nil {
			return nil, err
		}
	}
	_, dstLeaf, dstDirectoryID, err := f.createObject(ctx, remote, srcObj.modTime, srcObj.size)
	if err != nil {
		return nil, err
	}
	if err := f.moveCopy(ctx, srcObj.fsPath, dstDirectoryID, dstLeaf, false); err != nil {
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
	srcID, _, _, dstDirectoryID, dstLeafName, err := f.dirCache.DirMove(ctx, srcFs.dirCache, srcFs.root, srcRemote, f.root, dstRemote)
	if err != nil {
		return err
	}
	if err := f.moveCopy(ctx, srcID, dstDirectoryID, dstLeafName, false); err != nil {
		return err
	}
	srcFs.dirCache.FlushDir(srcRemote)
	return nil
}

func (f *Fs) assembleDownload(info *api.DownloadUrlPathInfo) (string, map[string]string, string) {
	if info == nil {
		return "", nil, ""
	}
	if u := info.GetDirectUrl(); u != "" {
		return u, info.GetAdditionalHeaders(), info.GetUserAgent()
	}
	p := info.GetDownloadUrlPath()
	p = strings.ReplaceAll(p, "{SCHEME}", f.scheme)
	p = strings.ReplaceAll(p, "{HOST}", f.host)
	p = strings.ReplaceAll(p, "{PREVIEW}", "false")
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p, nil, ""
	}
	if p == "" {
		return "", nil, ""
	}
	return strings.TrimRight(f.origin, "/") + p, nil, ""
}

func (f *Fs) downloadInfo(ctx context.Context, fsPath string, cached *api.DownloadUrlPathInfo) (string, map[string]string, string, error) {
	if u, headers, ua := f.assembleDownload(cached); u != "" {
		return u, headers, ua, nil
	}
	var info *api.DownloadUrlPathInfo
	err := f.call(ctx, func(ctx context.Context) error {
		var err error
		info, err = f.client.GetDownloadUrlPath(ctx, &api.GetDownloadUrlPathRequest{Path: fsPath, GetDirectUrl: true})
		return err
	})
	if err != nil {
		return "", nil, "", err
	}
	u, headers, ua := f.assembleDownload(info)
	if u == "" {
		return "", nil, "", errors.New("clouddrive2: empty download url")
	}
	return u, headers, ua, nil
}

func (o *Object) setMetaData(info *api.CloudDriveFile, fsPath string) error {
	if fileIsDir(info) {
		return fs.ErrorIsDir
	}
	o.fsPath = fsPath
	o.size = info.GetSize()
	o.modTime = fileTime(info)
	if hashes := info.GetFileHashes(); hashes != nil {
		o.md5sum = strings.ToLower(hashes[hashTypeMD5])
		o.sha1sum = strings.ToLower(hashes[hashTypeSHA1])
		if o.sha1sum == "" {
			o.sha1sum = strings.ToLower(hashes[hashTypePikPak])
		}
	}
	if info.DownloadUrlPath != nil {
		o.dURL, o.headers, o.ua = o.fs.assembleDownload(info.DownloadUrlPath)
	}
	return nil
}

func (o *Object) readMetaData(ctx context.Context) error {
	info, err := o.fs.readMetaDataForPath(ctx, o.remote)
	if err != nil {
		return err
	}
	_, directoryID, err := o.fs.dirCache.FindPath(ctx, o.remote, false)
	if err != nil {
		return err
	}
	return o.setMetaData(info, filePath(info, directoryID))
}

func (o *Object) Fs() fs.Info                                 { return o.fs }
func (o *Object) String() string                              { return o.remote }
func (o *Object) Remote() string                              { return o.remote }
func (o *Object) Size() int64                                 { return o.size }
func (o *Object) ModTime(_ context.Context) time.Time         { return o.modTime }
func (o *Object) SetModTime(context.Context, time.Time) error { return fs.ErrorCantSetModTime }
func (o *Object) Storable() bool                              { return true }
func (o *Object) ID() string                                  { return o.fsPath }

func (o *Object) Hash(_ context.Context, t hash.Type) (string, error) {
	switch t {
	case hash.MD5:
		return o.md5sum, nil
	case hash.SHA1:
		return o.sha1sum, nil
	default:
		return "", hash.ErrUnsupported
	}
}

func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	if o.fsPath == "" {
		if err := o.readMetaData(ctx); err != nil {
			return nil, err
		}
	}
	fs.FixRangeOption(options, o.size)
	var resp *http.Response
	err := o.fs.pacer.Call(func() (bool, error) {
		targetURL, headers, ua, err := o.fs.downloadInfo(ctx, o.fsPath, nil)
		if err != nil {
			if o.dURL != "" {
				targetURL, headers, ua, err = o.dURL, o.headers, o.ua, nil
			}
		}
		if err != nil {
			return shouldRetry(ctx, err)
		}
		extra := map[string]string{}
		o.fs.mu.Lock()
		token := o.fs.token
		o.fs.mu.Unlock()
		if token != "" {
			extra["Authorization"] = "Bearer " + token
		}
		for k, v := range headers {
			extra[k] = v
		}
		if ua != "" {
			extra["User-Agent"] = ua
		}
		opts := rest.Opts{Method: http.MethodGet, RootURL: targetURL, Options: options, ExtraHeaders: extra}
		resp, err = o.fs.dl.Call(ctx, &opts)
		retry, err := shouldRetry(ctx, err)
		if err != nil && fserrors.ShouldRetryHTTP(resp, []int{429, 500, 502, 503, 504}) {
			retry = true
		}
		return retry, err
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (o *Object) Remove(ctx context.Context) error {
	if o.fsPath == "" {
		if err := o.readMetaData(ctx); err != nil {
			return err
		}
	}
	if err := o.fs.deletePath(ctx, o.fsPath); err != nil {
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
	name := o.fs.opt.Enc.FromStandardName(leaf)
	fsPath := joinAPI(directoryID, leaf)
	_, err = o.fs.findFile(ctx, directoryID, leaf)
	if err == nil {
		if delErr := o.fs.deletePath(ctx, fsPath); delErr != nil {
			return delErr
		}
	} else if !errors.Is(err, fs.ErrorObjectNotFound) {
		return err
	}
	var created *api.CreateFileResult
	err = o.fs.call(ctx, func(ctx context.Context) error {
		var err error
		created, err = o.fs.client.CreateFile(ctx, &api.CreateFileRequest{ParentPath: directoryID, FileName: name})
		return err
	})
	if err != nil {
		return err
	}
	handle := created.GetFileHandle()
	chunk := int(o.fs.opt.UploadChunkSize)
	if chunk <= 0 {
		chunk = defaultChunk
	}
	buf := make([]byte, chunk)
	var pos uint64
	closed := false
	defer func() {
		if !closed {
			_ = o.fs.call(ctx, func(ctx context.Context) error {
				_, err := o.fs.client.CloseFile(ctx, &api.CloseFileRequest{FileHandle: handle})
				return err
			})
		}
	}()
	for {
		n, readErr := io.ReadFull(in, buf)
		if n > 0 {
			closeFile := errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)
			req := &api.WriteFileRequest{
				FileHandle: handle,
				StartPos:   pos,
				Length:     uint64(n),
				Buffer:     buf[:n],
				CloseFile:  closeFile,
			}
			var wres *api.WriteFileResult
			err = o.fs.call(ctx, func(ctx context.Context) error {
				var err error
				wres, err = o.fs.client.WriteToFile(ctx, req)
				return err
			})
			if err != nil {
				return err
			}
			if wres.GetBytesWritten() != 0 && wres.GetBytesWritten() != uint64(n) {
				return fmt.Errorf("clouddrive2: short write: %d/%d", wres.GetBytesWritten(), n)
			}
			pos += uint64(n)
			if closeFile {
				closed = true
			}
		}
		if readErr == io.EOF || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if !closed {
		err = o.fs.call(ctx, func(ctx context.Context) error {
			_, err := o.fs.client.CloseFile(ctx, &api.CloseFileRequest{FileHandle: handle})
			return err
		})
		if err != nil {
			return err
		}
		closed = true
	}
	o.fsPath = fsPath
	o.size = src.Size()
	o.modTime = src.ModTime(ctx)
	return nil
}

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
)
