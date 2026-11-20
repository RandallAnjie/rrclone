package cloudreve

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rclone/rclone/backend/cloudreve/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/lib/readers"
	"github.com/rclone/rclone/lib/rest"
)

const (
	// maxBufferedChunk is the largest chunk buffered in memory so it can
	// be retried. Bigger chunks (a relayed upload sends the whole file as
	// one) are streamed without retries.
	maxBufferedChunk = 256 * 1024 * 1024

	// octetStream is the Content-Type the official client sends with
	// data, which some providers sign into their upload URLs
	octetStream = "application/octet-stream"
)

// Waiting for Upyun, which tells Cloudreve about an upload asynchronously
var (
	upyunPollInterval = 500 * time.Millisecond
	upyunPollTimeout  = time.Minute
)

// errUnsupportedPolicy is returned by uploadToPolicy for uploads rclone
// can't send to the storage provider itself
var errUnsupportedPolicy = errors.New("unsupported storage policy")

// upload uploads in as remote, replacing the existing file if
// overwrite is set, and returns the new file.
func (f *Fs) upload(ctx context.Context, in io.Reader, remote string, size int64, modTime time.Time, overwrite bool, options ...fs.OpenOption) (*api.File, error) {
	if size < 0 {
		return nil, errors.New("cloudreve: can't upload files of unknown size")
	}
	s, err := f.createSession(ctx, remote, size, modTime, overwrite)
	if err != nil {
		return nil, err
	}
	err = f.uploadToPolicy(ctx, s, remote, in, size)
	if errors.Is(err, errUnsupportedPolicy) || (err != nil && size == 0) {
		// The server can store small files for us whatever the policy
		f.cancelSession(ctx, s)
		fs.Debugf(f, "%s: uploading through Cloudreve to storage policy %q: %v", remote, s.StoragePolicy.Type, err)
		file, cErr := f.uploadContent(ctx, remote, in, size, overwrite)
		if cErr != nil {
			return nil, fmt.Errorf("cloudreve: storage policy type %q needs relayed uploads (enable relay on the policy) and uploading through Cloudreve failed: %w", s.StoragePolicy.Type, cErr)
		}
		if err := f.patchModTime(ctx, f.uri(remote), file.PrimaryEntity, modTime); err != nil {
			fs.Debugf(f, "%s: couldn't set modification time: %v", remote, err)
			return file, nil
		}
		return f.getInfo(ctx, remote)
	}
	if err != nil {
		f.cancelSession(ctx, s)
		return nil, err
	}
	return f.getInfo(ctx, remote)
}

// createSession starts an upload session for remote
func (f *Fs) createSession(ctx context.Context, remote string, size int64, modTime time.Time, overwrite bool) (*api.UploadSession, error) {
	req := &api.UploadSessionRequest{
		URI:          f.uri(remote),
		Size:         size,
		LastModified: modTime.UnixMilli(),
		PolicyID:     f.opt.PolicyID,
	}
	if overwrite {
		req.EntityType = "version"
	}
	if f.opt.ClientEncryption {
		req.EncryptionSupported = []string{api.CipherAES256CTR}
	}
	opts := rest.Opts{Method: "PUT", Path: "/file/upload"}
	var s api.UploadSession
	err := f.call(ctx, f.srv, &opts, req, &s)
	switch apiCode(err) {
	case api.CodeObjectExist:
		if !overwrite {
			return f.createSession(ctx, remote, size, modTime, true)
		}
	case api.CodeParentNotExist:
		if dir := path.Dir(remote); dir != "." {
			if mErr := f.mkdir(ctx, dir); mErr != nil {
				return nil, mErr
			}
			err = f.call(ctx, f.srv, &opts, req, &s)
		}
	case api.CodeLockConflict:
		if f.cancelStaleSession(ctx, remote) {
			return f.createSession(ctx, remote, size, modTime, overwrite)
		}
	}
	if err != nil {
		return nil, err
	}
	if s.StoragePolicy == nil {
		return nil, errors.New("cloudreve: upload session has no storage policy")
	}
	return &s, nil
}

// cancelStaleSession deletes the upload session holding remote, which
// an interrupted upload left locked until the session expires. It
// returns whether it deleted one.
func (f *Fs) cancelStaleSession(ctx context.Context, remote string) bool {
	file, err := f.getInfo(ctx, remote)
	if err != nil {
		return false
	}
	id := file.Metadata[api.MetaUploadSessionID]
	if id == "" {
		return false
	}
	fs.Infof(f, "%s: cancelling the unfinished upload session %s", remote, id)
	opts := rest.Opts{Method: "DELETE", Path: "/file/upload"}
	err = f.call(ctx, f.srv, &opts, &api.DeleteUploadSessionRequest{ID: id, URI: f.uri(remote)}, nil)
	if err != nil {
		fs.Debugf(f, "%s: failed to cancel upload session %s: %v", remote, id, err)
		return false
	}
	return true
}

// cancelSession deletes a failed upload session, logging any error
func (f *Fs) cancelSession(ctx context.Context, s *api.UploadSession) {
	opts := rest.Opts{Method: "DELETE", Path: "/file/upload"}
	err := f.call(context.WithoutCancel(ctx), f.srv, &opts, &api.DeleteUploadSessionRequest{ID: s.SessionID, URI: s.URI}, nil)
	if err != nil {
		fs.Debugf(f, "Failed to delete upload session %s: %v", s.SessionID, err)
	}
}

// uploadContent stores in at remote in one request through the
// Cloudreve server. The server limits the size and the modification
// time is set to now.
func (f *Fs) uploadContent(ctx context.Context, remote string, in io.Reader, size int64, overwrite bool) (*api.File, error) {
	if !overwrite {
		opts := rest.Opts{Method: "POST", Path: "/file/create"}
		var file api.File
		err := f.call(ctx, f.srv, &opts, &api.CreateFileRequest{URI: f.uri(remote), Type: "file", ErrOnConflict: true}, &file)
		if err == nil && size == 0 {
			return f.getInfo(ctx, remote)
		}
		if err != nil && apiCode(err) != api.CodeObjectExist {
			return nil, err
		}
	}
	opts := rest.Opts{
		Method:        "PUT",
		Path:          "/file/content",
		Parameters:    url.Values{"uri": {f.uri(remote)}},
		ContentLength: &size,
		ContentType:   octetStream,
	}
	var r api.Response
	_, err := f.sendChunk(ctx, f.srv, &opts, in, nil, &r)
	if err != nil {
		return nil, err
	}
	return f.getInfo(ctx, remote)
}

// encryptReader encrypts in as Cloudreve expects: AES-256-CTR over
// the whole file, with the IV as the initial big endian counter.
func encryptReader(in io.Reader, m *api.EncryptMetadata) (io.Reader, error) {
	if m.Algorithm != api.CipherAES256CTR {
		return nil, fmt.Errorf("cloudreve: unsupported encryption %q", m.Algorithm)
	}
	block, err := aes.NewCipher(m.KeyPlainText)
	if err != nil {
		return nil, fmt.Errorf("cloudreve: bad encryption key: %w", err)
	}
	if len(m.IV) != aes.BlockSize {
		return nil, fmt.Errorf("cloudreve: bad encryption IV length %d", len(m.IV))
	}
	return cipher.StreamReader{S: cipher.NewCTR(block, m.IV), R: in}, nil
}

// chunkFunc uploads chunk index of n bytes at offset. If rewind is
// not nil the chunk may be retried after calling it.
type chunkFunc func(index int, offset, n int64, chunk io.Reader, rewind func() error) error

// forEachChunk splits in into chunks of chunkSize (0 for one chunk)
// and calls fn for each. An empty file is one empty chunk, for which
// Cloudreve hands out one part to upload.
func forEachChunk(in io.Reader, size, chunkSize int64, fn chunkFunc) error {
	if chunkSize <= 0 || chunkSize > size {
		chunkSize = size
	}
	var buf []byte
	if chunkSize <= maxBufferedChunk {
		buf = make([]byte, chunkSize)
	}
	for index, offset := 0, int64(0); offset < size || index == 0; index++ {
		n := min(chunkSize, size-offset)
		var err error
		if buf != nil {
			rr := readers.NewRepeatableLimitReaderBuffer(in, buf, n)
			err = fn(index, offset, n, rr, func() error {
				_, err := rr.Seek(0, io.SeekStart)
				return err
			})
		} else {
			err = fn(index, offset, n, io.LimitReader(in, n), nil)
		}
		if err != nil {
			return fmt.Errorf("chunk %d: %w", index, err)
		}
		offset += n
	}
	return nil
}

// sendChunk sends one request with body chunk, retrying it if rewind
// is set. If out is an *api.Response its code is checked. If out is
// nil the response body is discarded.
func (f *Fs) sendChunk(ctx context.Context, srv *rest.Client, opts *rest.Opts, chunk io.Reader, rewind func() error, out any) (resp *http.Response, err error) {
	first := true
	do := func() (bool, error) {
		if !first {
			if err := rewind(); err != nil {
				return false, err
			}
		}
		first = false
		opts.Body = chunk
		if out != nil {
			resp, err = srv.CallJSON(ctx, opts, nil, out)
			if r, ok := out.(*api.Response); ok && err == nil {
				err = r.Err()
			}
		} else {
			resp, err = srv.Call(ctx, opts)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
		return f.shouldRetry(ctx, resp, err)
	}
	if rewind != nil {
		err = f.pacer.Call(do)
	} else {
		err = f.pacer.CallNoRetry(do)
	}
	return resp, err
}

// uploadToPolicy sends the data for upload session s
func (f *Fs) uploadToPolicy(ctx context.Context, s *api.UploadSession, remote string, in io.Reader, size int64) error {
	policy := s.StoragePolicy
	if !policy.Relay && policy.Type == api.PolicyOneDrive && size == 0 {
		return errUnsupportedPolicy // OneDrive sessions can't take an empty file
	}
	if s.EncryptMetadata != nil {
		var err error
		if in, err = encryptReader(in, s.EncryptMetadata); err != nil {
			return err
		}
	}
	if policy.Relay {
		return f.uploadRelay(ctx, s, in, size)
	}
	switch policy.Type {
	case api.PolicyLocal:
		return f.uploadRelay(ctx, s, in, size)
	case api.PolicyRemote:
		return f.uploadRemote(ctx, s, in, size)
	case api.PolicyOneDrive:
		return f.uploadOneDrive(ctx, s, in, size)
	case api.PolicyS3, api.PolicyKS3, api.PolicyCOS, api.PolicyOBS, api.PolicyOSS:
		return f.uploadMultipart(ctx, s, in, size)
	case api.PolicyQiniu:
		return f.uploadQiniu(ctx, s, in, size)
	case api.PolicyUpyun:
		return f.uploadUpyun(ctx, s, remote, in, size)
	}
	return errUnsupportedPolicy
}

// uploadRelay sends the chunks to the Cloudreve server
func (f *Fs) uploadRelay(ctx context.Context, s *api.UploadSession, in io.Reader, size int64) error {
	return forEachChunk(in, size, s.ChunkSize, func(index int, offset, n int64, chunk io.Reader, rewind func() error) error {
		opts := rest.Opts{
			Method:        "POST",
			Path:          fmt.Sprintf("/file/upload/%s/%d", s.SessionID, index),
			ContentLength: &n,
			ContentType:   octetStream,
		}
		_, err := f.sendChunk(ctx, f.srv, &opts, chunk, rewind, &api.Response{})
		return err
	})
}

// uploadRemote sends the chunks to a Cloudreve slave node
func (f *Fs) uploadRemote(ctx context.Context, s *api.UploadSession, in io.Reader, size int64) error {
	if len(s.UploadURLs) == 0 {
		return errors.New("no upload URL for slave node")
	}
	return forEachChunk(in, size, s.ChunkSize, func(index int, offset, n int64, chunk io.Reader, rewind func() error) error {
		u, err := withQuery(s.UploadURLs[0], "chunk", strconv.Itoa(index))
		if err != nil {
			return err
		}
		opts := rest.Opts{
			Method:        "POST",
			RootURL:       u,
			ContentLength: &n,
			ContentType:   octetStream,
			ExtraHeaders:  map[string]string{"Authorization": s.Credential},
		}
		_, err = f.sendChunk(ctx, f.plain, &opts, chunk, rewind, &api.Response{})
		return err
	})
}

// putOneDriveRange sends n bytes at offset to a OneDrive upload session
func (f *Fs) putOneDriveRange(ctx context.Context, sessionURL string, offset, n, size int64, chunk io.Reader, rewind func() error) (*http.Response, error) {
	opts := rest.Opts{
		Method:        "PUT",
		RootURL:       sessionURL,
		ContentLength: &n,
		ContentType:   octetStream,
		ExtraHeaders:  map[string]string{"Content-Range": fmt.Sprintf("bytes %d-%d/%d", offset, offset+n-1, size)},
	}
	return f.sendChunk(ctx, f.plain, &opts, chunk, rewind, nil)
}

// oneDriveNextOffset asks a OneDrive upload session what it needs next
func (f *Fs) oneDriveNextOffset(ctx context.Context, sessionURL string) (int64, error) {
	opts := rest.Opts{Method: "GET", RootURL: sessionURL}
	var status api.OneDriveStatus
	err := f.pacer.Call(func() (bool, error) {
		resp, err := f.plain.CallJSON(ctx, &opts, nil, &status)
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return 0, err
	}
	if len(status.NextExpectedRanges) == 0 {
		return 0, errors.New("OneDrive session expects no more data")
	}
	start, _, _ := strings.Cut(status.NextExpectedRanges[0], "-")
	return strconv.ParseInt(start, 10, 64)
}

// uploadOneDrive sends the chunks to a OneDrive upload session
func (f *Fs) uploadOneDrive(ctx context.Context, s *api.UploadSession, in io.Reader, size int64) error {
	if len(s.UploadURLs) == 0 {
		return errors.New("no OneDrive upload URL")
	}
	sessionURL := s.UploadURLs[0]
	err := forEachChunk(in, size, s.ChunkSize, func(index int, offset, n int64, chunk io.Reader, rewind func() error) error {
		resp, err := f.putOneDriveRange(ctx, sessionURL, offset, n, size, chunk, rewind)
		if err == nil || resp == nil || resp.StatusCode != http.StatusRequestedRangeNotSatisfiable || rewind == nil {
			return err
		}
		// OneDrive already has some of this chunk from an attempt whose
		// reply was lost, so send only what it is missing.
		next, nextErr := f.oneDriveNextOffset(ctx, sessionURL)
		if nextErr != nil || next < offset {
			return err
		}
		if next >= offset+n {
			return nil
		}
		if err := rewind(); err != nil {
			return err
		}
		if _, err := io.CopyN(io.Discard, chunk, next-offset); err != nil {
			return err
		}
		_, err = f.putOneDriveRange(ctx, sessionURL, next, offset+n-next, size, chunk, nil)
		return err
	})
	if err != nil {
		return err
	}
	return f.callback(ctx, "POST", api.PolicyOneDrive, s)
}

// callback tells Cloudreve a direct upload has finished
func (f *Fs) callback(ctx context.Context, method, policyType string, s *api.UploadSession) error {
	opts := rest.Opts{
		Method: method,
		Path:   fmt.Sprintf("/callback/%s/%s/%s", policyType, s.SessionID, s.CallbackSecret),
	}
	return f.call(ctx, f.srv, &opts, nil, nil)
}

// partsXML is the body completing an S3 style multipart upload. ETags
// keep their quotes unescaped, as in the official client.
func partsXML(parts []api.CompletedPart) string {
	var b strings.Builder
	b.WriteString("<CompleteMultipartUpload>")
	for _, part := range parts {
		fmt.Fprintf(&b, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", part.PartNumber, part.ETag)
	}
	b.WriteString("</CompleteMultipartUpload>")
	return b.String()
}

// uploadMultipart sends the chunks to presigned S3 style multipart
// upload URLs (S3, KS3, OSS, COS, OBS) and completes the upload.
func (f *Fs) uploadMultipart(ctx context.Context, s *api.UploadSession, in io.Reader, size int64) error {
	policyType := s.StoragePolicy.Type
	var parts []api.CompletedPart
	err := forEachChunk(in, size, s.ChunkSize, func(index int, offset, n int64, chunk io.Reader, rewind func() error) error {
		if index >= len(s.UploadURLs) {
			return fmt.Errorf("only %d upload URLs for the parts", len(s.UploadURLs))
		}
		opts := rest.Opts{
			Method:        "PUT",
			RootURL:       s.UploadURLs[index],
			ContentLength: &n,
			ContentType:   octetStream,
		}
		resp, err := f.sendChunk(ctx, f.plain, &opts, chunk, rewind, nil)
		if err != nil {
			return err
		}
		etag := resp.Header.Get("ETag")
		if etag == "" {
			return errors.New("no ETag in part upload response")
		}
		parts = append(parts, api.CompletedPart{PartNumber: index + 1, ETag: etag})
		return nil
	})
	if err != nil {
		return err
	}
	if s.CompleteURL == "" {
		return errors.New("no URL to complete the multipart upload")
	}

	opts := rest.Opts{Method: "POST", RootURL: s.CompleteURL, ContentType: octetStream, ExtraHeaders: map[string]string{}}
	body := ""
	switch policyType {
	case api.PolicyOSS:
		// OSS lists the parts itself
		opts.ExtraHeaders["x-oss-complete-all"] = "yes"
		opts.ExtraHeaders["x-oss-forbid-overwrite"] = "true"
	case api.PolicyCOS:
		opts.ExtraHeaders["x-cos-forbid-overwrite"] = "true"
		body = partsXML(parts)
	default:
		body = partsXML(parts)
	}
	n := int64(len(body))
	opts.ContentLength = &n
	var respBody []byte
	err = f.pacer.Call(func() (bool, error) {
		opts.Body = strings.NewReader(body)
		resp, err := f.plain.Call(ctx, &opts)
		if err == nil {
			respBody, err = rest.ReadBody(resp)
		}
		// S3 can report a failed completion with status 200, and OSS a
		// failed callback to Cloudreve with status 203
		if err == nil && resp.StatusCode != http.StatusOK {
			return false, fmt.Errorf("completing multipart upload failed: HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
		}
		if err == nil && bytes.Contains(respBody, []byte("<Error>")) {
			err = fmt.Errorf("completing multipart upload failed: %s", strings.TrimSpace(string(respBody)))
		}
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}

	switch policyType {
	case api.PolicyS3, api.PolicyKS3, api.PolicyCOS:
		return f.callback(ctx, "GET", policyType, s)
	}
	// OSS and OBS call Cloudreve back themselves and relay its reply
	return checkCallbackReply(respBody)
}

// checkCallbackReply checks a Cloudreve callback reply relayed by the
// storage provider, if it is one.
func checkCallbackReply(body []byte) error {
	var r api.Response
	if len(bytes.TrimSpace(body)) == 0 || bytes.HasPrefix(bytes.TrimSpace(body), []byte("<")) {
		return nil
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil // not a Cloudreve reply
	}
	return r.Err()
}

// uploadQiniu sends the chunks with Qiniu's multipart upload v2 API
func (f *Fs) uploadQiniu(ctx context.Context, s *api.UploadSession, in io.Reader, size int64) error {
	if len(s.UploadURLs) == 0 {
		return errors.New("no Qiniu upload URL")
	}
	base := strings.TrimRight(s.UploadURLs[0], "/")
	auth := map[string]string{"Authorization": "UpToken " + s.Credential}
	var parts []api.QiniuPart
	err := forEachChunk(in, size, s.ChunkSize, func(index int, offset, n int64, chunk io.Reader, rewind func() error) error {
		opts := rest.Opts{
			Method:        "PUT",
			RootURL:       fmt.Sprintf("%s/%d", base, index+1),
			ContentLength: &n,
			ContentType:   octetStream,
			ExtraHeaders:  auth,
		}
		var r struct {
			ETag string `json:"etag"`
		}
		if _, err := f.sendChunk(ctx, f.plain, &opts, chunk, rewind, &r); err != nil {
			return err
		}
		if r.ETag == "" {
			return errors.New("no etag in Qiniu part response")
		}
		parts = append(parts, api.QiniuPart{ETag: r.ETag, PartNumber: index + 1})
		return nil
	})
	if err != nil {
		return err
	}
	opts := rest.Opts{Method: "POST", RootURL: base, ExtraHeaders: auth}
	var reply map[string]any
	err = f.pacer.Call(func() (bool, error) {
		resp, err := f.plain.CallJSON(ctx, &opts, &api.QiniuComplete{MimeType: s.MimeType, Parts: parts}, &reply)
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return err
	}
	// Qiniu relays Cloudreve's callback reply
	if code, ok := reply["code"].(float64); ok && code != api.CodeOK {
		msg, _ := reply["msg"].(string)
		return &api.Error{Code: int(code), Msg: msg}
	}
	return nil
}

// uploadUpyun sends the file in one form upload, then waits for Upyun
// to tell Cloudreve about it.
func (f *Fs) uploadUpyun(ctx context.Context, s *api.UploadSession, remote string, in io.Reader, size int64) error {
	if len(s.UploadURLs) == 0 {
		return errors.New("no Upyun upload URL")
	}
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	_ = w.WriteField("policy", s.UploadPolicy)
	_ = w.WriteField("authorization", s.Credential)
	if s.MimeType != "" {
		_ = w.WriteField("content-type", s.MimeType)
	}
	// The file must be the last field of the form
	if _, err := w.CreateFormFile("file", path.Base(remote)); err != nil {
		return err
	}
	headLen := form.Len()
	if err := w.Close(); err != nil {
		return err
	}
	head, tail := form.Bytes()[:headLen], form.Bytes()[headLen:]
	length := int64(len(head)) + size + int64(len(tail))
	opts := rest.Opts{
		Method:        "POST",
		RootURL:       s.UploadURLs[0],
		ContentLength: &length,
		ContentType:   w.FormDataContentType(),
	}
	body := io.MultiReader(bytes.NewReader(head), io.LimitReader(in, size), bytes.NewReader(tail))
	if _, err := f.sendChunk(ctx, f.plain, &opts, body, nil, nil); err != nil {
		return err
	}
	deadline := time.Now().Add(upyunPollTimeout)
	for {
		file, err := f.getInfo(ctx, remote)
		if err == nil && file.Metadata[api.MetaUploadSessionID] != s.SessionID {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for Upyun to confirm the upload")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(upyunPollInterval):
		}
	}
}

// withQuery returns raw with key=value appended to its query, leaving
// the rest of a possibly signed URL untouched.
func withQuery(raw, key, value string) (string, error) {
	if _, err := url.Parse(raw); err != nil {
		return "", fmt.Errorf("bad upload URL %q: %w", raw, err)
	}
	sep := "?"
	if strings.Contains(raw, "?") {
		sep = "&"
	}
	return raw + sep + url.QueryEscape(key) + "=" + url.QueryEscape(value), nil
}
