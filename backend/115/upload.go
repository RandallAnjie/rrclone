package _115

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rclone/rclone/backend/115/api"
	"github.com/rclone/rclone/backend/115/crypto"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/multipart"
	"github.com/rclone/rclone/lib/readers"
	"github.com/rclone/rclone/lib/rest"
)

type uploadSource struct {
	rs      io.ReadSeeker
	cleanup func()
	digest  *crypto.DigestResult
}

func (s *uploadSource) Close() {
	if s != nil && s.cleanup != nil {
		s.cleanup()
	}
}

func (f *Fs) prepareUpload(in io.Reader, size int64) (*uploadSource, error) {
	if size >= 0 && size <= memUploadLimit {
		rw := multipart.NewRW()
		if size > 0 {
			rw.Reserve(size)
		}
		n, err := io.Copy(rw, in)
		if err != nil {
			_ = rw.Close()
			return nil, err
		}
		if _, err := rw.Seek(0, io.SeekStart); err != nil {
			_ = rw.Close()
			return nil, err
		}
		dr := &crypto.DigestResult{}
		if err := crypto.Digest(rw, dr); err != nil {
			_ = rw.Close()
			return nil, err
		}
		if dr.Size != n {
			_ = rw.Close()
			return nil, fmt.Errorf("115: digest size %d != copied %d", dr.Size, n)
		}
		if _, err := rw.Seek(0, io.SeekStart); err != nil {
			_ = rw.Close()
			return nil, err
		}
		return &uploadSource{
			rs:      rw,
			digest:  dr,
			cleanup: func() { _ = rw.Close() },
		}, nil
	}

	tmp, err := os.CreateTemp("", "rclone-115-*.upload")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		name := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	if _, err := io.Copy(tmp, in); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	dr := &crypto.DigestResult{}
	if err := crypto.Digest(tmp, dr); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	return &uploadSource{rs: tmp, digest: dr, cleanup: cleanup}, nil
}

func (f *Fs) loadUploadInfo(ctx context.Context) error {
	f.upload.once.Do(func() {
		opts := rest.Opts{
			Method:  http.MethodGet,
			RootURL: proAPIRoot,
			Path:    "/app/uploadinfo",
		}
		var info api.UploadInfoResponse
		err := f.pacer.Call(func() (bool, error) {
			resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			f.upload.err = err
			return
		}
		if info.UserID == 0 || info.UserKey == "" {
			f.upload.err = fmt.Errorf("115: uploadinfo missing user id/key")
			return
		}
		f.upload.mu.Lock()
		f.upload.userID = info.UserID
		f.upload.userKey = info.UserKey
		f.upload.sizeLimit = info.UploadLimit
		f.upload.mu.Unlock()
	})
	return f.upload.err
}

func (f *Fs) uploadDigestRange(rs io.ReadSeeker, rangeSpec string) (string, error) {
	var start, end int64
	if _, err := fmt.Sscanf(rangeSpec, "%d-%d", &start, &end); err != nil {
		return "", err
	}
	if _, err := rs.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	h := sha1.New()
	if _, err := io.CopyN(h, rs, end-start+1); err != nil && err != io.EOF {
		return "", err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
}

func (f *Fs) initUpload(ctx context.Context, dirID, fileName string, dr *crypto.DigestResult, rs io.ReadSeeker) (*api.UploadInitResponse, error) {
	if err := f.loadUploadInfo(ctx); err != nil {
		return nil, err
	}
	ecdhCipher, err := crypto.NewEcdhCipher()
	if err != nil {
		return nil, err
	}
	f.upload.mu.Lock()
	userID := f.upload.userID
	userKey := f.upload.userKey
	appVer := f.opt.AppVer
	sizeLimit := f.upload.sizeLimit
	f.upload.mu.Unlock()
	if sizeLimit > 0 && dr.Size > sizeLimit {
		return nil, fmt.Errorf("115: file size %d exceeds account upload limit %d", dr.Size, sizeLimit)
	}

	target := "U_1_" + dirID
	fileSize := strconv.FormatInt(dr.Size, 10)
	form := url.Values{}
	form.Set("appid", "0")
	form.Set("appversion", appVer)
	form.Set("userid", strconv.FormatInt(userID, 10))
	form.Set("filename", fileName)
	form.Set("filesize", fileSize)
	form.Set("fileid", dr.QuickID)
	form.Set("target", target)
	form.Set("sig", crypto.UploadSignature(userID, userKey, target, dr.QuickID))
	form.Set("topupload", "true")

	signKey, signVal := "", ""
	var result api.UploadInitResponse
	for i := 0; i < 3; i++ {
		t := time.Now().UnixMilli()
		token, err := ecdhCipher.EncodeToken(t)
		if err != nil {
			return nil, err
		}
		form.Set("t", strconv.FormatInt(t, 10))
		form.Set("token", crypto.UploadToken(userID, dr.QuickID, dr.PreID, strconv.FormatInt(t, 10), fileSize, signKey, signVal, appVer))
		if signKey != "" && signVal != "" {
			form.Set("sign_key", signKey)
			form.Set("sign_val", signVal)
		}
		encrypted, err := ecdhCipher.Encrypt([]byte(form.Encode()))
		if err != nil {
			return nil, err
		}
		encBody := encrypted
		opts := rest.Opts{
			Method:       http.MethodPost,
			RootURL:      uplbRoot,
			Path:         "/4.0/initupload.php",
			Parameters:   url.Values{"k_ec": {token}},
			Body:         bytes.NewReader(encBody),
			ContentType:  "application/x-www-form-urlencoded",
			IgnoreStatus: true,
			GetBody: func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(encBody)), nil
			},
		}
		var resp *http.Response
		err = f.pacer.Call(func() (bool, error) {
			resp, err = f.srv.Call(ctx, &opts)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return nil, err
		}
		respBody, err := rest.ReadBody(resp)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("115 initupload: HTTP %s: %s", resp.Status, respBody)
		}
		plain, err := ecdhCipher.Decrypt(respBody)
		if err != nil {
			return nil, fmt.Errorf("115 initupload decrypt: %w", err)
		}
		if err := json.Unmarshal(plain, &result); err != nil {
			return nil, fmt.Errorf("115 initupload json: %w body=%q", err, plain)
		}
		if result.Status == 7 && result.SignCheck != "" {
			signKey = result.SignKey
			signVal, err = f.uploadDigestRange(rs, result.SignCheck)
			if err != nil {
				return nil, err
			}
			continue
		}
		result.SHA1 = dr.QuickID
		return &result, nil
	}
	return &result, fmt.Errorf("115 initupload: too many sign_check retries")
}

func (f *Fs) getOSSToken(ctx context.Context) (*api.UploadOssTokenResponse, error) {
	opts := rest.Opts{
		Method:  http.MethodGet,
		RootURL: uplbRoot,
		Path:    "/3.0/gettoken.php",
	}
	var info api.UploadOssTokenResponse
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	if info.StatusCode != "" && info.StatusCode != "200" {
		return nil, fmt.Errorf("115 oss token status %s", info.StatusCode)
	}
	if info.AccessKeyID == "" || info.AccessKeySecret == "" {
		return nil, fmt.Errorf("115: empty OSS token")
	}
	return &info, nil
}

func (f *Fs) putOSS(ctx context.Context, init *api.UploadInitResponse, rs io.ReadSeeker, size int64, contentMD5, contentType string) error {
	token, err := f.getOSSToken(ctx)
	if err != nil {
		return err
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	callback := base64.StdEncoding.EncodeToString([]byte(init.Callback.Callback))
	callbackVar := base64.StdEncoding.EncodeToString([]byte(init.Callback.CallbackVar))
	objectPath := init.Object
	if !strings.HasPrefix(objectPath, "/") {
		objectPath = "/" + objectPath
	}
	resource := "/" + init.Bucket + objectPath

	ossHeaders := make(http.Header)
	ossHeaders.Set("x-oss-security-token", token.SecurityToken)
	ossHeaders.Set("x-oss-callback", callback)
	ossHeaders.Set("x-oss-callback-var", callbackVar)
	sig := crypto.OSSSignV1(http.MethodPut, contentMD5, contentType, date, resource, token.AccessKeySecret, ossHeaders)

	host := init.Bucket + "." + ossEndpoint
	escaped := strings.ReplaceAll(url.PathEscape(strings.TrimPrefix(objectPath, "/")), "%2F", "/")
	rootURL := "https://" + host + "/" + escaped

	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return err
	}
	opts := rest.Opts{
		Method:        http.MethodPut,
		RootURL:       rootURL,
		Path:          "",
		Body:          readers.NoCloser(rs),
		ContentType:   contentType,
		ContentLength: &size,
		ExtraHeaders: map[string]string{
			"Date":                 date,
			"Content-MD5":          contentMD5,
			"Authorization":        "OSS " + token.AccessKeyID + ":" + sig,
			"x-oss-security-token": token.SecurityToken,
			"x-oss-callback":       callback,
			"x-oss-callback-var":   callbackVar,
			"*User-Agent":          ossUserAgent,
		},
		GetBody: func() (io.ReadCloser, error) {
			if _, err := rs.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			return io.NopCloser(readers.NoCloser(rs)), nil
		},
	}
	var resp *http.Response
	err = f.pacer.CallNoRetry(func() (bool, error) {
		resp, err = f.oss.Call(ctx, &opts)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	body, err := rest.ReadBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("115 OSS PUT HTTP %s: %s", resp.Status, body)
	}
	var parsed api.BaseResponse
	if len(body) > 0 && json.Unmarshal(body, &parsed) == nil {
		if err := parsed.Err(); err != nil {
			return fmt.Errorf("115 OSS callback: %w", err)
		}
	}
	return nil
}

// Update uploads to the remote path, replacing any existing object
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	_ = options
	size := src.Size()
	if size == 0 {
		return fs.ErrorCantUploadEmptyFiles
	}
	leaf, dirID, err := o.fs.dirCache.FindPath(ctx, o.remote, true)
	if err != nil {
		return err
	}
	if o.id != "" {
		_ = o.fs.deleteIDs(ctx, o.id, dirID)
		o.id = ""
	} else if existing, err := o.fs.NewObject(ctx, o.remote); err == nil {
		_ = existing.Remove(ctx)
	}

	srcUpload, err := o.fs.prepareUpload(in, size)
	if err != nil {
		return err
	}
	defer srcUpload.Close()

	if srcHash, err := src.Hash(ctx, hash.SHA1); err == nil && srcHash != "" {
		if !strings.EqualFold(srcHash, srcUpload.digest.QuickID) {
			fs.Debugf(o, "source SHA1 %s != hashed %s", srcHash, srcUpload.digest.QuickID)
		}
	}

	init, err := o.fs.initUpload(ctx, dirID, o.fs.opt.Enc.FromStandardName(leaf), srcUpload.digest, srcUpload.rs)
	if err != nil {
		return err
	}
	switch init.Status {
	case 2:
		fs.Debugf(o, "115 rapid upload succeeded")
	case 1:
		mime := fs.MimeTypeFromName(leaf)
		if mime == "" {
			mime = "application/octet-stream"
		}
		if err := o.fs.putOSS(ctx, init, srcUpload.rs, srcUpload.digest.Size, srcUpload.digest.MD5, mime); err != nil {
			return err
		}
	default:
		if init.ErrorMsg != "" {
			return fmt.Errorf("115 initupload status %d: %s", init.Status, init.ErrorMsg)
		}
		return fmt.Errorf("115 initupload unexpected status %d", init.Status)
	}

	o.fs.dirCache.FlushDir(path.Dir(o.remote))
	var info *api.FileInfo
	for i := 0; i < 8; i++ {
		info, err = o.fs.findInDir(ctx, dirID, leaf, srcUpload.digest.QuickID)
		if err == nil {
			return o.setMetaData(info)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("upload completed but file not listed yet: %w", err)
}
