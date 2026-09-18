// Package api contains types for the Baidu Netdisk (百度网盘) REST API.
package api

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Base is the common errno wrapper returned by pan.baidu.com APIs.
type Base struct {
	Errno   int64  `json:"errno"`
	ErrMsg  string `json:"errmsg"`
	ShowMsg string `json:"show_msg"`
}

// Err returns a non-nil error when Errno is not 0.
func (b Base) Err() error {
	if b.Errno == 0 {
		return nil
	}
	msg := b.ErrMsg
	if msg == "" {
		msg = b.ShowMsg
	}
	if msg == "" {
		msg = "baidu api error"
	}
	return &Error{Errno: b.Errno, Message: msg}
}

// Error is a Baidu API errno.
type Error struct {
	Errno   int64
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("baidu errno %d", e.Errno)
	}
	return fmt.Sprintf("baidu errno %d: %s", e.Errno, e.Message)
}

// IsNotFound reports whether the errno means the file or directory is missing.
func IsNotFound(errno int64) bool {
	switch errno {
	case -9, 31066, 31061, 12:
		return true
	default:
		return false
	}
}

// IsLogin reports whether the errno means the cookie or token is invalid.
func IsLogin(errno int64) bool {
	switch errno {
	case -6, -7, 111, 31045, 36009, 36013:
		return true
	default:
		return false
	}
}

// IsRetry reports whether the errno should be retried.
func IsRetry(errno int64) bool {
	switch errno {
	case 31034, 31341, 31362, 31326, -20:
		return true
	default:
		return false
	}
}

// Item is a file or directory in a listing.
type Item struct {
	FsID           int64  `json:"fs_id"`
	Path           string `json:"path"`
	ServerFilename string `json:"server_filename"`
	FileName       string `json:"filename"`
	Size           int64  `json:"size"`
	IsDir          int    `json:"isdir"`
	ServerMtime    int64  `json:"server_mtime"`
	ServerCtime    int64  `json:"server_ctime"`
	LocalMtime     int64  `json:"local_mtime"`
	MD5            string `json:"md5"`
	Dlink          string `json:"dlink"`
}

// Name returns the leaf name of the item.
func (it Item) Name() string {
	if it.ServerFilename != "" {
		return it.ServerFilename
	}
	if it.FileName != "" {
		return it.FileName
	}
	if it.Path != "" {
		p := it.Path
		if i := strings.LastIndex(p, "/"); i >= 0 && i+1 < len(p) {
			return p[i+1:]
		}
		return strings.TrimPrefix(p, "/")
	}
	return ""
}

// ListResponse is the body of method=list / api/list.
type ListResponse struct {
	Base
	List []Item `json:"list"`
}

// FileMetasResponse is the body of method=filemetas.
type FileMetasResponse struct {
	Base
	List []Item `json:"list"`
	Info []Item `json:"info"`
}

// Items returns file meta entries from either list or info.
func (r FileMetasResponse) Items() []Item {
	if len(r.Info) > 0 {
		return r.Info
	}
	return r.List
}

// QuotaResponse is the body of /api/quota.
type QuotaResponse struct {
	Base
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
	Free  int64 `json:"free"`
}

// PrecreateResponse is the body of method=precreate.
type PrecreateResponse struct {
	Base
	UploadID   string `json:"uploadid"`
	ReturnType int    `json:"return_type"`
	BlockList  []int  `json:"block_list"`
	Path       string `json:"path"`
}

// CreateResponse is the body of method=create.
type CreateResponse struct {
	Base
	FsID int64  `json:"fs_id"`
	Path string `json:"path"`
	MD5  string `json:"md5"`
	Size int64  `json:"size"`
}

// ManagerResponse is the body of method=filemanager.
type ManagerResponse struct {
	Base
}

// SuperfileResponse is the body of PCS superfile2.
type SuperfileResponse struct {
	Base
	MD5       string `json:"md5"`
	ErrorCode int64  `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
}

// Err returns a non-nil error when error_code or errno is set.
func (r SuperfileResponse) Err() error {
	if r.ErrorCode != 0 {
		msg := r.ErrorMsg
		if msg == "" {
			msg = r.ErrMsg
		}
		if msg == "" {
			msg = r.ShowMsg
		}
		return &Error{Errno: r.ErrorCode, Message: msg}
	}
	return r.Base.Err()
}

// TokenResponse is the OAuth token JSON from openapi.baidu.com.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// TemplateVariableResponse is /api/gettemplatevariable used to read bdstoken.
type TemplateVariableResponse struct {
	Base
	Result json.RawMessage `json:"result"`
}

// Bdstoken reads the token from result.bdstoken, which may be a string or a one-element array.
func (r TemplateVariableResponse) Bdstoken() string {
	if len(r.Result) == 0 {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal(r.Result, &obj) != nil {
		return ""
	}
	switch v := obj["bdstoken"].(type) {
	case string:
		return v
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	}
	return ""
}
