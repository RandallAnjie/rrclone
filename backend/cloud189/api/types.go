// Package api contains types for the China Telecom Cloud 189 web API.
package api

import (
	"fmt"
	"strconv"
	"time"
)

const (
	// DefaultRoot is https://cloud.189.cn
	DefaultRoot = "https://cloud.189.cn"
	// RootFolder is the account root folder id.
	RootFolder = "-11"
)

// File is a file in a list response.
type File struct {
	ID         int64  `json:"id"`
	LastOpTime string `json:"lastOpTime"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
}

// Folder is a folder in a list response.
type Folder struct {
	ID         int64  `json:"id"`
	LastOpTime string `json:"lastOpTime"`
	Name       string `json:"name"`
}

// Files is GET /api/open/file/listFiles.action
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
		return fmt.Errorf("cloud189 api error %d", f.ResCode)
	}
	return fmt.Errorf("cloud189 api error %d: %s", f.ResCode, f.ResMessage)
}

// CreateFolderResp is POST /api/open/file/createFolder.action
type CreateFolderResp struct {
	ResCode    int    `json:"res_code"`
	ResMessage string `json:"res_message"`
	ID         int64  `json:"id"`
	Name       string `json:"name"`
}

// DownResp is GET /api/portal/getFileInfo.action
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

// CapacityResp is GET /api/portal/getUserSizeInfo.action
type CapacityResp struct {
	ResCode           int `json:"res_code"`
	CloudCapacityInfo struct {
		FreeSize  int64 `json:"freeSize"`
		TotalSize int64 `json:"totalSize"`
		UsedSize  int64 `json:"usedSize"`
	} `json:"cloudCapacityInfo"`
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
