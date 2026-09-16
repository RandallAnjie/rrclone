package _123

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rclone/rclone/backend/123/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/lib/rest"
)

const webChunkSize int64 = 16 << 20

func apiSign(path string) (key, value string) {
	if path == "" {
		path = "/"
	}
	table := []byte("adefghlmyijnopkqrstubcvwsz")
	random := fmt.Sprintf("%d", rand.Intn(1e7))
	now := time.Now().In(time.FixedZone("CST", 8*3600))
	timestamp := strconv.FormatInt(now.Unix(), 10)
	nowStr := []byte(now.Format("200601021504"))
	for i := range nowStr {
		d := nowStr[i] - '0'
		if d <= 9 {
			nowStr[i] = table[d]
		}
	}
	timeSign := strconv.FormatUint(uint64(crc32.ChecksumIEEE(nowStr)), 10)
	data := strings.Join([]string{timestamp, random, path, "web", api.WebAppVersion, timeSign}, "|")
	dataSign := strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(data))), 10)
	return timeSign, timestamp + "-" + random + "-" + dataSign
}

func cloneValues(v url.Values) url.Values {
	if v == nil {
		return url.Values{}
	}
	out := make(url.Values, len(v))
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}

func (f *Fs) signRequest(opts *rest.Opts) {
	if !f.web {
		return
	}
	path := opts.Path
	if opts.RootURL != "" {
		if u, err := url.Parse(opts.RootURL); err == nil && u.Path != "" {
			path = u.Path
		}
	}
	k, v := apiSign(path)
	opts.Parameters = cloneValues(opts.Parameters)
	opts.Parameters.Set(k, v)
}

func (f *Fs) loginRoot() string {
	if strings.TrimSpace(f.opt.LoginEndpoint) != "" {
		return strings.TrimRight(f.opt.LoginEndpoint, "/")
	}
	ep := strings.TrimRight(f.opt.Endpoint, "/")
	if ep != "" && ep != api.DefaultRoot && ep != api.DefaultWebRoot {
		return ep
	}
	return api.LoginRoot
}

func (f *Fs) saveToken(token string, expire int64) {
	f.tokenMu.Lock()
	f.token = token
	f.opt.AccessToken = token
	if expire > 0 {
		f.tokenExp = time.Unix(expire, 0)
		if f.tokenExp.Before(time.Now().Add(time.Hour)) {
			f.tokenExp = time.Now().Add(24 * time.Hour)
		}
	} else {
		f.tokenExp = time.Now().Add(24 * time.Hour)
	}
	f.tokenMu.Unlock()
	if f.m != nil {
		f.m.Set("access_token", token)
	}
}

func (f *Fs) webLogin(ctx context.Context) error {
	user := strings.TrimSpace(f.opt.User)
	pass := strings.TrimSpace(f.opt.Pass)
	if user == "" || pass == "" {
		f.tokenMu.Lock()
		has := f.token != ""
		f.tokenMu.Unlock()
		if has {
			return nil
		}
		return errors.New("123: user and pass are required for web login")
	}
	req := api.LoginReq{Password: pass, Remember: true}
	if isEmail(user) {
		req.Mail = user
		req.Type = 2
	} else {
		req.Passport = user
	}
	opts := rest.Opts{
		Method:  http.MethodPost,
		RootURL: f.loginRoot() + "/user/sign_in",
		ExtraHeaders: map[string]string{
			"Origin":       "https://www.123pan.com",
			"Referer":      "https://www.123pan.com/",
			"Platform":     api.WebPlatformHeader,
			"App-Version":  api.WebAppVersion,
			"Content-Type": "application/json",
		},
	}
	var info api.LoginResp
	resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
	if err != nil {
		return fmt.Errorf("123 web login: %w", err)
	}
	_ = resp
	if info.Code != 200 && info.Code != 0 {
		msg := info.Message
		if msg == "" {
			msg = fmt.Sprintf("login code %d", info.Code)
		}
		return fmt.Errorf("123 web login: %s", msg)
	}
	if info.Data.Token == "" {
		return errors.New("123: empty web login token")
	}
	f.saveToken(info.Data.Token, info.Data.Expire)
	return nil
}

func (f *Fs) webListAll(ctx context.Context, dirID string, directoriesOnly, filesOnly bool, fn listAllFn) (found bool, err error) {
	parentID, err := api.ParseID(dirID)
	if err != nil {
		return false, err
	}
	page := 1
	for {
		opts := rest.Opts{
			Method:     http.MethodGet,
			Path:       "/b/api/file/list/new",
			Parameters: url.Values{},
		}
		opts.Parameters.Set("driveId", "0")
		opts.Parameters.Set("limit", strconv.Itoa(f.opt.ListChunk))
		opts.Parameters.Set("next", "0")
		opts.Parameters.Set("orderBy", "file_id")
		opts.Parameters.Set("orderDirection", "desc")
		opts.Parameters.Set("parentFileId", strconv.FormatInt(parentID, 10))
		opts.Parameters.Set("trashed", "false")
		opts.Parameters.Set("Page", strconv.Itoa(page))
		opts.Parameters.Set("OnlyLookAbnormalFile", "0")
		var info api.WebFileListResp
		if err := f.doJSON(ctx, &opts, nil, &info); err != nil {
			return false, err
		}
		if len(info.Data.InfoList) == 0 {
			break
		}
		for i := range info.Data.InfoList {
			item := &info.Data.InfoList[i]
			item.ParentFileID = parentID
			if item.IsDir() {
				if filesOnly {
					continue
				}
			} else if directoriesOnly {
				continue
			}
			item.FileName = f.opt.Enc.ToStandardName(item.FileName)
			if fn(item) {
				return true, nil
			}
		}
		if info.Data.Next == "-1" || len(info.Data.InfoList) < f.opt.ListChunk {
			break
		}
		page++
	}
	return false, nil
}

func (f *Fs) webCreateDir(ctx context.Context, pathID, leaf string) (string, error) {
	parentID, err := api.ParseID(pathID)
	if err != nil {
		return "", err
	}
	opts := rest.Opts{Method: http.MethodPost, Path: "/b/api/file/upload_request"}
	req := map[string]any{
		"driveId":      0,
		"etag":         "",
		"fileName":     f.opt.Enc.FromStandardName(leaf),
		"parentFileId": parentID,
		"size":         0,
		"type":         1,
	}
	var info api.WebMkdirResp
	if err := f.doJSON(ctx, &opts, &req, &info); err != nil {
		id, found, findErr := f.FindLeaf(ctx, pathID, leaf)
		if findErr == nil && found {
			return id, nil
		}
		return "", err
	}
	id := info.Data.FileID
	if id != 0 {
		return strconv.FormatInt(id, 10), nil
	}
	idStr, found, err := f.FindLeaf(ctx, pathID, leaf)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("123: mkdir succeeded but folder id missing")
	}
	return idStr, nil
}

func (f *Fs) webTrash(ctx context.Context, ids ...int64) error {
	list := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		list = append(list, map[string]any{"FileId": id})
	}
	opts := rest.Opts{Method: http.MethodPost, Path: "/b/api/file/trash"}
	req := map[string]any{
		"driveId":           0,
		"operation":         true,
		"fileTrashInfoList": list,
	}
	var info api.BaseResp
	return f.doJSON(ctx, &opts, &req, &info)
}

func (f *Fs) webRename(ctx context.Context, id int64, newLeaf string) error {
	opts := rest.Opts{Method: http.MethodPost, Path: "/b/api/file/rename"}
	req := map[string]any{
		"driveId":  0,
		"fileId":   id,
		"fileName": f.opt.Enc.FromStandardName(newLeaf),
	}
	var info api.BaseResp
	return f.doJSON(ctx, &opts, &req, &info)
}

func (f *Fs) webMove(ctx context.Context, id, newDirID int64) error {
	opts := rest.Opts{Method: http.MethodPost, Path: "/b/api/file/mod_pid"}
	req := map[string]any{
		"fileIdList":   []map[string]any{{"FileId": id}},
		"parentFileId": newDirID,
	}
	var info api.BaseResp
	return f.doJSON(ctx, &opts, &req, &info)
}

func (f *Fs) webDownloadURL(ctx context.Context, o *Object) (string, error) {
	fileID, err := api.ParseID(o.id)
	if err != nil {
		return "", err
	}
	opts := rest.Opts{Method: http.MethodPost, Path: "/b/api/file/download_info"}
	req := map[string]any{
		"driveId":   0,
		"etag":      o.md5sum,
		"fileId":    fileID,
		"fileName":  f.opt.Enc.FromStandardName(o.remote),
		"s3keyFlag": o.s3KeyFlag,
		"size":      o.size,
		"type":      o.fileType,
	}
	var info api.WebDownloadResp
	if err := f.doJSON(ctx, &opts, &req, &info); err != nil {
		return "", err
	}
	raw := info.DownloadURL()
	if raw == "" {
		return "", errors.New("123: empty download url")
	}
	return unwrapDownloadURL(raw)
}

func unwrapDownloadURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return raw, nil
	}
	if params := u.Query().Get("params"); params != "" {
		decoded, err := base64.StdEncoding.DecodeString(params)
		if err == nil {
			if inner, err := url.Parse(string(decoded)); err == nil && inner.Scheme != "" {
				return inner.String(), nil
			}
		}
	}
	return u.String(), nil
}

func (o *Object) webUpdate(ctx context.Context, in io.Reader, src fs.ObjectInfo) error {
	leaf, directoryID, err := o.fs.dirCache.FindPath(ctx, o.remote, true)
	if err != nil {
		return err
	}
	o.dirID = directoryID
	parentID, err := api.ParseID(directoryID)
	if err != nil {
		return err
	}
	filename := o.fs.opt.Enc.FromStandardName(leaf)
	srcUpload, err := o.fs.prepareUpload(ctx, in, src)
	if err != nil {
		return err
	}
	defer srcUpload.Close()

	created, err := o.fs.webUploadRequest(ctx, parentID, filename, srcUpload.md5sum, srcUpload.size)
	if err != nil {
		return err
	}
	if created.Data.Reuse || (created.Data.Key == "" && created.Data.FileID != 0) {
		if created.Data.FileID != 0 {
			o.id = strconv.FormatInt(created.Data.FileID, 10)
		}
		o.size = srcUpload.size
		o.md5sum = srcUpload.md5sum
		o.sha1sum = srcUpload.sha1sum
		o.modTime = src.ModTime(ctx)
		return nil
	}
	if created.Data.Key == "" {
		o.size = srcUpload.size
		o.md5sum = srcUpload.md5sum
		o.sha1sum = srcUpload.sha1sum
		o.modTime = src.ModTime(ctx)
		return nil
	}
	if err := o.webUploadParts(ctx, srcUpload, created); err != nil {
		return err
	}
	complete := rest.Opts{Method: http.MethodPost, Path: "/b/api/file/upload_complete"}
	var done api.BaseResp
	if err := o.fs.doJSON(ctx, &complete, map[string]any{"fileId": created.Data.FileID}, &done); err != nil {
		return err
	}
	if created.Data.FileID != 0 {
		o.id = strconv.FormatInt(created.Data.FileID, 10)
	}
	if info, err := o.fs.findInDir(ctx, directoryID, leaf, srcUpload.md5sum); err == nil {
		return o.setMetaData(info)
	}
	o.size = srcUpload.size
	o.md5sum = srcUpload.md5sum
	o.sha1sum = srcUpload.sha1sum
	o.modTime = src.ModTime(ctx)
	return nil
}

func (f *Fs) webUploadRequest(ctx context.Context, parentID int64, filename, etag string, size int64) (*api.WebUploadResp, error) {
	opts := rest.Opts{Method: http.MethodPost, Path: "/b/api/file/upload_request"}
	req := map[string]any{
		"driveId":      0,
		"duplicate":    api.DuplicateOverwrite,
		"etag":         strings.ToLower(etag),
		"fileName":     filename,
		"parentFileId": parentID,
		"size":         size,
		"type":         0,
	}
	var info api.WebUploadResp
	if err := f.doJSON(ctx, &opts, &req, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (o *Object) webUploadParts(ctx context.Context, src *uploadSource, created *api.WebUploadResp) error {
	size := src.size
	chunk := webChunkSize
	n := 1
	if size > chunk {
		n = int((size + chunk - 1) / chunk)
	}
	for i := 1; i <= n; i++ {
		urls, err := o.fs.webPresign(ctx, created, i, i+1)
		if err != nil {
			return err
		}
		putURL := urls[strconv.Itoa(i)]
		if putURL == "" {
			for _, u := range urls {
				putURL = u
				break
			}
		}
		if putURL == "" {
			return fmt.Errorf("123: missing presigned url for part %d", i)
		}
		offset := int64(i-1) * chunk
		left := size - offset
		if left > chunk {
			left = chunk
		}
		if size == 0 {
			left = 0
		}
		if _, err := src.rs.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		var body io.Reader = http.NoBody
		if left > 0 {
			body = io.LimitReader(src.rs, left)
		}
		putOpts := rest.Opts{
			Method:        http.MethodPut,
			RootURL:       putURL,
			Body:          body,
			ContentLength: &left,
		}
		err = o.fs.pacer.Call(func() (bool, error) {
			if _, err := src.rs.Seek(offset, io.SeekStart); err != nil {
				return false, err
			}
			if left > 0 {
				putOpts.Body = io.LimitReader(src.rs, left)
			}
			resp, err := o.fs.dl.Call(ctx, &putOpts)
			if err == nil && resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return fmt.Errorf("123: upload part %d: %w", i, err)
		}
	}
	return nil
}

func (f *Fs) webPresign(ctx context.Context, created *api.WebUploadResp, start, end int) (map[string]string, error) {
	path := "/b/api/file/s3_upload_object/auth"
	if created.Data.UploadID != "" && end-start > 1 {
		path = "/b/api/file/s3_repare_upload_parts_batch"
	}
	opts := rest.Opts{Method: http.MethodPost, Path: path}
	req := map[string]any{
		"bucket":          created.Data.Bucket,
		"key":             created.Data.Key,
		"partNumberEnd":   end,
		"partNumberStart": start,
		"uploadId":        created.Data.UploadID,
		"StorageNode":     created.Data.StorageNode,
	}
	var info api.WebS3URLs
	if err := f.doJSON(ctx, &opts, &req, &info); err != nil {
		return nil, err
	}
	return info.Data.PreSignedURLs, nil
}

func (f *Fs) webCopy(ctx context.Context, src *Object, remote string) (fs.Object, error) {
	if src.md5sum == "" {
		return nil, fs.ErrorCantCopy
	}
	_, dstLeaf, dstDirectoryID, err := f.createObject(ctx, remote, src.modTime, src.size)
	if err != nil {
		return nil, err
	}
	parentID, err := api.ParseID(dstDirectoryID)
	if err != nil {
		return nil, err
	}
	created, err := f.webUploadRequest(ctx, parentID, f.opt.Enc.FromStandardName(dstLeaf), src.md5sum, src.size)
	if err != nil {
		return nil, err
	}
	if !created.Data.Reuse {
		return nil, fs.ErrorCantCopy
	}
	f.dirCache.FlushDir(path.Dir(remote))
	return f.NewObject(ctx, remote)
}
