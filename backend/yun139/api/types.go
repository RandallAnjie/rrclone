// Package api contains types for China Mobile Yun 139.
package api

import "time"

const (
	// DefaultRoot is https://yun.139.com
	DefaultRoot = "https://yun.139.com"
)

// BaseResp is the common envelope.
type BaseResp struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Catalog is a folder.
type Catalog struct {
	CatalogID   string `json:"catalogID"`
	CatalogName string `json:"catalogName"`
	CreateTime  string `json:"createTime"`
	UpdateTime  string `json:"updateTime"`
}

// Content is a file.
type Content struct {
	ContentID   string `json:"contentID"`
	ContentName string `json:"contentName"`
	ContentSize int64  `json:"contentSize"`
	CreateTime  string `json:"createTime"`
	UpdateTime  string `json:"updateTime"`
	Digest      string `json:"digest"`
}

// DiskResp is POST /orchestration/personalCloud/catalog/v1.0/getDisk
type DiskResp struct {
	BaseResp
	Data struct {
		GetDiskResult struct {
			NodeCount   int       `json:"nodeCount"`
			CatalogList []Catalog `json:"catalogList"`
			ContentList []Content `json:"contentList"`
		} `json:"getDiskResult"`
	} `json:"data"`
}

// DownloadResp is POST /orchestration/personalCloud/uploadAndDownload/v1.0/downloadRequest
type DownloadResp struct {
	BaseResp
	Data struct {
		DownloadURL string `json:"downloadURL"`
	} `json:"data"`
}

// ParseTime parses 139 timestamps.
func ParseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "20060102150405", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.FixedZone("CST", 8*3600)); err == nil {
			return t
		}
	}
	return time.Time{}
}
