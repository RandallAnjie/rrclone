// Package api contains types for the Baidu Netdisk Open API.
package api

import (
	"strconv"
	"time"
)

const (
	// DefaultRoot is https://pan.baidu.com/rest/2.0
	DefaultRoot = "https://pan.baidu.com/rest/2.0"
	// TokenURL is the OAuth token endpoint.
	TokenURL = "https://openapi.baidu.com/oauth/2.0/token"
	// QuotaURL is the quota endpoint.
	QuotaURL = "https://pan.baidu.com/api/quota"
)

// TokenResp is an OAuth token response.
type TokenResp struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// File is a file or folder in a list response.
type File struct {
	FsID           int64  `json:"fs_id"`
	Path           string `json:"path"`
	ServerFilename string `json:"server_filename"`
	Size           int64  `json:"size"`
	Isdir          int    `json:"isdir"`
	ServerMtime    int64  `json:"server_mtime"`
	MD5            string `json:"md5"`
}

// IsDir reports whether the item is a folder.
func (f File) IsDir() bool { return f.Isdir == 1 }

// ID returns the file id as a decimal string.
func (f File) ID() string {
	if f.FsID == 0 {
		return f.Path
	}
	return strconv.FormatInt(f.FsID, 10)
}

// ModTime returns the modification time.
func (f File) ModTime() time.Time {
	if f.ServerMtime == 0 {
		return time.Time{}
	}
	return time.Unix(f.ServerMtime, 0)
}

// ListResp is GET /xpan/file?method=list
type ListResp struct {
	Errno int    `json:"errno"`
	List  []File `json:"list"`
}

// DownloadResp is GET /xpan/multimedia?method=filemetas
type DownloadResp struct {
	Errno int `json:"errno"`
	List  []struct {
		Dlink string `json:"dlink"`
		FsID  int64  `json:"fs_id"`
	} `json:"list"`
}

// QuotaResp is GET /api/quota
type QuotaResp struct {
	Errno int   `json:"errno"`
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}

// CreateResp is POST /xpan/file?method=create
type CreateResp struct {
	Errno int   `json:"errno"`
	FsID  int64 `json:"fs_id"`
	Path  string `json:"path"`
	Isdir int   `json:"isdir"`
}

// ManagerResp is POST /xpan/file?method=filemanager
type ManagerResp struct {
	Errno int `json:"errno"`
}
