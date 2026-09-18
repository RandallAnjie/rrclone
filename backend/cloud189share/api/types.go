// Package api contains types for the Cloud 189 share API.
package api

import (
	"fmt"
	"strconv"
	"time"
)

const (
	// DefaultRoot is https://cloud.189.cn
	DefaultRoot = "https://cloud.189.cn"
)

// ShareInfo is GET /api/open/share/getShareInfoByCodeV2.action
type ShareInfo struct {
	ResCode    int    `json:"res_code"`
	ResMessage string `json:"res_message"`
	ShareID    int64  `json:"shareId"`
	FileID     int64  `json:"fileId"`
	IsFolder   bool   `json:"isFolder"`
	ShareMode  int    `json:"shareMode"`
	FileName   string `json:"fileName"`
}

// AccessResp is GET /api/open/share/checkAccessCode.action
type AccessResp struct {
	ResCode    int    `json:"res_code"`
	ResMessage string `json:"res_message"`
	ShareID    int64  `json:"shareId"`
}

// File is a file in a share listing.
type File struct {
	ID         int64  `json:"id"`
	LastOpTime string `json:"lastOpTime"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
}

// Folder is a folder in a share listing.
type Folder struct {
	ID         int64  `json:"id"`
	LastOpTime string `json:"lastOpTime"`
	Name       string `json:"name"`
}

// Files is GET /api/open/share/listShareDir.action
type Files struct {
	ResCode    int    `json:"res_code"`
	ResMessage string `json:"res_message"`
	FileListAO struct {
		Count      int      `json:"count"`
		FileList   []File   `json:"fileList"`
		FolderList []Folder `json:"folderList"`
	} `json:"fileListAO"`
}

// Err returns a non-nil error when ResCode is not 0.
func (f Files) Err() error {
	if f.ResCode == 0 {
		return nil
	}
	if f.ResMessage == "" {
		return fmt.Errorf("cloud189share api error %d", f.ResCode)
	}
	return fmt.Errorf("cloud189share api error %d: %s", f.ResCode, f.ResMessage)
}

// DownResp is GET /api/open/file/getFileDownloadUrl.action
type DownResp struct {
	ResCode         int    `json:"res_code"`
	ResMessage      string `json:"res_message"`
	FileDownloadURL string `json:"fileDownloadUrl"`
	DownloadURL     string `json:"downloadUrl"`
}

// URL returns the download URL.
func (d DownResp) URL() string {
	if d.FileDownloadURL != "" {
		return d.FileDownloadURL
	}
	return d.DownloadURL
}

// IDString formats an int64 id.
func IDString(id int64) string {
	return strconv.FormatInt(id, 10)
}

// ParseCNTime parses lastOpTime as China Standard Time.
func ParseCNTime(s string) time.Time {
	loc := time.FixedZone("CST", 8*3600)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04:05 -0700"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t
		}
	}
	return time.Time{}
}
