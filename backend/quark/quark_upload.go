package quark

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rclone/rclone/backend/quark/api"
	"github.com/rclone/rclone/lib/rest"
)

func (o *Object) ossUpload(ctx context.Context, rs io.ReadSeeker, size int64, pre api.UpPreResp) error {
	partSize := int64(pre.Metadata.PartSize)
	if partSize <= 0 {
		partSize = defaultPartSize
	}
	n := 1
	if size > 0 {
		n = int((size + partSize - 1) / partSize)
	}
	etags := make([]string, 0, n)
	mimeType := "application/octet-stream"
	if pre.Data.FormatType != "" {
		mimeType = pre.Data.FormatType
	}
	for i := 1; i <= n; i++ {
		offset := int64(i-1) * partSize
		left := size - offset
		if left > partSize {
			left = partSize
		}
		if size == 0 {
			left = 0
		}
		etag, err := o.ossPutPart(ctx, pre, mimeType, i, rs, offset, left)
		if err != nil {
			return err
		}
		etags = append(etags, etag)
	}
	if err := o.ossCommit(ctx, pre, mimeType, etags); err != nil {
		return err
	}
	return o.ossFinish(ctx, pre)
}

func (o *Object) ossAuth(ctx context.Context, pre api.UpPreResp, authMeta string) (string, error) {
	opts := rest.Opts{Method: http.MethodPost, Path: "/file/upload/auth", Parameters: commonQuery()}
	req := map[string]any{
		"auth_info": pre.Data.AuthInfo,
		"auth_meta": authMeta,
		"task_id":   pre.Data.TaskID,
	}
	var info api.UpAuthResp
	if err := o.fs.callJSON(ctx, &opts, &req, &info); err != nil {
		return "", err
	}
	if info.Data.AuthKey == "" {
		return "", fmt.Errorf("quark: empty OSS auth key")
	}
	return info.Data.AuthKey, nil
}

func ossObjectURL(pre api.UpPreResp) (string, error) {
	raw := pre.Data.UploadURL
	if raw == "" {
		return "", fmt.Errorf("quark: empty OSS upload url")
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" {
		u.Scheme = "https"
	}
	host := u.Host
	if pre.Data.Bucket != "" && !strings.HasPrefix(host, pre.Data.Bucket+".") {
		host = pre.Data.Bucket + "." + host
	}
	key := strings.TrimPrefix(pre.Data.ObjKey, "/")
	return u.Scheme + "://" + host + "/" + key, nil
}

func (o *Object) ossPutPart(ctx context.Context, pre api.UpPreResp, mimeType string, partNumber int, rs io.ReadSeeker, offset, size int64) (string, error) {
	timeStr := time.Now().UTC().Format(http.TimeFormat)
	authMeta := fmt.Sprintf("PUT\n\n%s\n%s\nx-oss-date:%s\nx-oss-user-agent:%s\n/%s/%s?partNumber=%d&uploadId=%s",
		mimeType, timeStr, timeStr, ossUserAgent, pre.Data.Bucket, pre.Data.ObjKey, partNumber, pre.Data.UploadID)
	authKey, err := o.ossAuth(ctx, pre, authMeta)
	if err != nil {
		return "", err
	}
	putURL, err := ossObjectURL(pre)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(putURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("partNumber", strconv.Itoa(partNumber))
	q.Set("uploadId", pre.Data.UploadID)
	u.RawQuery = q.Encode()
	if _, err := rs.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	var body io.Reader = http.NoBody
	if size > 0 {
		body = io.LimitReader(rs, size)
	}
	putOpts := rest.Opts{
		Method:        http.MethodPut,
		RootURL:       u.String(),
		Body:          body,
		ContentLength: &size,
		ContentType:   mimeType,
		ExtraHeaders: map[string]string{
			"Authorization":    authKey,
			"Referer":          api.DefaultReferer,
			"x-oss-date":       timeStr,
			"x-oss-user-agent": ossUserAgent,
		},
	}
	var etag string
	err = o.fs.pacer.Call(func() (bool, error) {
		if _, err := rs.Seek(offset, io.SeekStart); err != nil {
			return false, err
		}
		if size > 0 {
			putOpts.Body = io.LimitReader(rs, size)
		}
		resp, err := o.fs.dl.Call(ctx, &putOpts)
		if err != nil {
			return shouldRetry(ctx, resp, err)
		}
		etag = strings.Trim(resp.Header.Get("Etag"), `"`)
		if etag == "" {
			etag = strings.Trim(resp.Header.Get("ETag"), `"`)
		}
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return true, fmt.Errorf("quark: OSS part %d status %d", partNumber, resp.StatusCode)
		}
		return false, nil
	})
	if err != nil {
		return "", err
	}
	if etag == "" {
		return "", fmt.Errorf("quark: empty ETag for part %d", partNumber)
	}
	return etag, nil
}

func (o *Object) ossCommit(ctx context.Context, pre api.UpPreResp, mimeType string, etags []string) error {
	var xmlBody strings.Builder
	xmlBody.WriteString(`<?xml version="1.0" encoding="UTF-8"?><CompleteMultipartUpload>`)
	for i, etag := range etags {
		fmt.Fprintf(&xmlBody, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", i+1, etag)
	}
	xmlBody.WriteString(`</CompleteMultipartUpload>`)
	body := xmlBody.String()
	sum := md5.Sum([]byte(body))
	contentMD5 := base64.StdEncoding.EncodeToString(sum[:])
	callbackJSON, err := json.Marshal(pre.Data.Callback)
	if err != nil {
		return err
	}
	callbackB64 := base64.StdEncoding.EncodeToString(callbackJSON)
	timeStr := time.Now().UTC().Format(http.TimeFormat)
	authMeta := fmt.Sprintf("POST\n%s\napplication/xml\n%s\nx-oss-callback:%s\nx-oss-date:%s\nx-oss-user-agent:%s\n/%s/%s?uploadId=%s",
		contentMD5, timeStr, callbackB64, timeStr, ossUserAgent, pre.Data.Bucket, pre.Data.ObjKey, pre.Data.UploadID)
	authKey, err := o.ossAuth(ctx, pre, authMeta)
	if err != nil {
		return err
	}
	putURL, err := ossObjectURL(pre)
	if err != nil {
		return err
	}
	u, err := url.Parse(putURL)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("uploadId", pre.Data.UploadID)
	u.RawQuery = q.Encode()
	length := int64(len(body))
	postOpts := rest.Opts{
		Method:        http.MethodPost,
		RootURL:       u.String(),
		Body:          strings.NewReader(body),
		ContentLength: &length,
		ContentType:   "application/xml",
		ExtraHeaders: map[string]string{
			"Authorization":    authKey,
			"Content-MD5":      contentMD5,
			"Referer":          api.DefaultReferer,
			"x-oss-callback":   callbackB64,
			"x-oss-date":       timeStr,
			"x-oss-user-agent": ossUserAgent,
		},
	}
	return o.fs.pacer.Call(func() (bool, error) {
		postOpts.Body = strings.NewReader(body)
		resp, err := o.fs.dl.Call(ctx, &postOpts)
		if err == nil && resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			return shouldRetry(ctx, resp, err)
		}
		if resp.StatusCode != http.StatusOK {
			return true, fmt.Errorf("quark: OSS complete status %d", resp.StatusCode)
		}
		return false, nil
	})
}

func (o *Object) ossFinish(ctx context.Context, pre api.UpPreResp) error {
	opts := rest.Opts{Method: http.MethodPost, Path: "/file/upload/finish", Parameters: commonQuery()}
	req := map[string]any{
		"obj_key": pre.Data.ObjKey,
		"task_id": pre.Data.TaskID,
	}
	var info api.FinishResp
	if err := o.fs.callJSON(ctx, &opts, &req, &info); err != nil {
		return err
	}
	if info.Data.Fid != "" {
		o.id = info.Data.Fid
	}
	return nil
}
