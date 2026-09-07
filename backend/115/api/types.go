// Package api contains types for the 115 web API (webapi.115.com).
package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FlexString unmarshals a JSON string or number into a string.
type FlexString string

// UnmarshalJSON implements json.Unmarshaler.
func (s *FlexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = FlexString(v)
		return nil
	}
	*s = FlexString(strings.Trim(string(b), `"`))
	return nil
}

func (s FlexString) String() string {
	return string(s)
}

// FlexInt64 unmarshals a JSON number or numeric string into int64.
type FlexInt64 int64

// UnmarshalJSON implements json.Unmarshaler.
func (n *FlexInt64) UnmarshalJSON(b []byte) error {
	if string(b) == "null" || string(b) == `""` {
		*n = 0
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			*n = 0
			return nil
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			f, err2 := strconv.ParseFloat(s, 64)
			if err2 != nil {
				return err
			}
			*n = FlexInt64(f)
			return nil
		}
		*n = FlexInt64(v)
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*n = FlexInt64(f)
	return nil
}

func (n FlexInt64) Int64() int64 { return int64(n) }

// BaseResponse is the common envelope for webapi.115.com JSON.
type BaseResponse struct {
	State bool   `json:"state"`
	Error string `json:"error,omitempty"`
	Errno any    `json:"errno,omitempty"`
	ErrNo any    `json:"errNo,omitempty"`
	Msg   string `json:"msg,omitempty"`
}

// GetErrno returns the numeric error code from either errno field.
func (r *BaseResponse) GetErrno() int64 {
	if n := parseErrno(r.Errno); n != 0 {
		return n
	}
	return parseErrno(r.ErrNo)
}

// Message returns a human-readable error.
func (r *BaseResponse) Message() string {
	if r.Error != "" {
		return r.Error
	}
	return r.Msg
}

// Err returns nil if State is true, otherwise a formatted error.
func (r *BaseResponse) Err() error {
	if r.State {
		return nil
	}
	msg := r.Message()
	errno := r.GetErrno()
	if msg == "" && errno == 0 {
		return fmt.Errorf("115: request failed")
	}
	if msg == "" {
		return fmt.Errorf("115: error %d", errno)
	}
	if errno == 0 {
		return fmt.Errorf("115: %s", msg)
	}
	return fmt.Errorf("115: %s (errno %d)", msg, errno)
}

func parseErrno(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	case int:
		return int64(x)
	case int64:
		return x
	default:
		return 0
	}
}

// FileInfo is a file or directory from GET /files.
type FileInfo struct {
	AreaID     FlexString `json:"aid"`
	CategoryID FlexString `json:"cid"`
	FileID     FlexString `json:"fid"`
	ParentID   FlexString `json:"pid"`
	Name       string     `json:"n"`
	Type       string     `json:"ico"`
	Size       FlexInt64  `json:"s"`
	Sha1       string     `json:"sha"`
	PickCode   string     `json:"pc"`
	CreateTime FlexString `json:"tp"`
	UpdateTime FlexString `json:"te"`
	Time       string     `json:"t"`
}

// IsDir reports whether this item is a directory.
func (f *FileInfo) IsDir() bool {
	id := f.FileID.String()
	return id == "" || id == "0"
}

// ID returns the file id (files) or category id (directories).
func (f *FileInfo) ID() string {
	if f.IsDir() {
		return f.CategoryID.String()
	}
	return f.FileID.String()
}

// Parent returns the parent directory id.
func (f *FileInfo) Parent() string {
	if f.IsDir() {
		if p := f.ParentID.String(); p != "" {
			return p
		}
	}
	return f.CategoryID.String()
}

// ModTime parses the item modification time.
func (f *FileInfo) ModTime() time.Time {
	for _, raw := range []string{f.UpdateTime.String(), f.CreateTime.String(), f.Time} {
		if ts := parseUnix(raw); !ts.IsZero() {
			return ts
		}
	}
	return time.Time{}
}

func parseUnix(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	if n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// GetFilesResponse is GET /files.
type GetFilesResponse struct {
	BaseResponse
	Count      int64      `json:"count"`
	Offset     int64      `json:"offset"`
	Limit      int64      `json:"limit"`
	CategoryID FlexString `json:"cid"`
	Data       []FileInfo `json:"data"`
}

// GetDirIDResponse is GET /files/getid.
type GetDirIDResponse struct {
	BaseResponse
	CategoryID FlexString `json:"id"`
}

// MkdirResponse is POST /files/add.
type MkdirResponse struct {
	BaseResponse
	CategoryID   FlexString `json:"cid"`
	CategoryName string     `json:"cname"`
	FileID       FlexString `json:"file_id"`
}

// IndexInfoResponse is GET /files/index_info.
type IndexInfoResponse struct {
	BaseResponse
	Data IndexInfoData `json:"data"`
}

// IndexInfoData holds quota information.
type IndexInfoData struct {
	SpaceInfo map[string]SizeInfo `json:"space_info"`
}

// SizeInfo is a quota field.
type SizeInfo struct {
	Size       float64 `json:"size"`
	SizeFormat string  `json:"size_format"`
}

// WebDownloadResponse is GET /files/download.
type WebDownloadResponse struct {
	BaseResponse
	FileURL  string          `json:"file_url"`
	FileName string          `json:"file_name"`
	FileSize FlexInt64       `json:"file_size"`
	PickCode string          `json:"pickcode"`
	Data     json.RawMessage `json:"data"`
}

// DownloadURL is the nested download URL object.
type DownloadURL struct {
	URL string `json:"url"`
}

// GetURLResponse is the encrypted proapi chrome/downurl envelope.
type GetURLResponse struct {
	BaseResponse
	Data json.RawMessage `json:"data"`
}

// DownloadInfo is one entry inside decoded chrome/downurl data.
type DownloadInfo struct {
	FileName string      `json:"file_name"`
	FileSize FlexInt64   `json:"file_size"`
	PickCode string      `json:"pick_code"`
	URL      DownloadURL `json:"url"`
}

// DownloadData maps file id -> download info.
type DownloadData map[string]*DownloadInfo

// UploadInfoResponse is GET proapi /app/uploadinfo.
type UploadInfoResponse struct {
	BaseResponse
	AppID       FlexInt64 `json:"app_id"`
	AppVersion  FlexInt64 `json:"app_version"`
	UploadLimit int64     `json:"size_limit"`
	IspType     int64     `json:"isp_type"`
	UserID      int64     `json:"user_id"`
	UserKey     string    `json:"userkey"`
}

// UploadInitResponse is the decrypted initupload.php JSON.
type UploadInitResponse struct {
	Request   string `json:"request"`
	ErrorCode int    `json:"statuscode"`
	ErrorMsg  string `json:"statusmsg"`
	Status    int    `json:"status"`
	PickCode  string `json:"pickcode"`
	Target    string `json:"target"`
	Version   string `json:"version"`
	Bucket    string `json:"bucket"`
	Object    string `json:"object"`
	Callback  struct {
		Callback    string `json:"callback"`
		CallbackVar string `json:"callback_var"`
	} `json:"callback"`
	SignKey   string `json:"sign_key"`
	SignCheck string `json:"sign_check"`
	SHA1      string `json:"-"`
}

// UploadOssTokenResponse is GET uplb /3.0/gettoken.php.
type UploadOssTokenResponse struct {
	StatusCode      string `json:"StatusCode"`
	AccessKeyID     string `json:"AccessKeyId"`
	AccessKeySecret string `json:"AccessKeySecret"`
	SecurityToken   string `json:"SecurityToken"`
	Expiration      string `json:"Expiration"`
}

// AppVersionResponse is GET appversion.115.com chrome API.
type AppVersionResponse struct {
	State bool `json:"state"`
	Data  struct {
		VersionCode string `json:"version_code"`
		Version     string `json:"version"`
	} `json:"data"`
}
