// Package api contains types for the Quark share API.
package api

import "time"

const (
	// DefaultRoot is https://drive-pc.quark.cn/1/clouddrive
	DefaultRoot = "https://drive-pc.quark.cn/1/clouddrive"
	// DefaultReferer is the Quark web referer.
	DefaultReferer = "https://pan.quark.cn"
	// DefaultUA is a Quark desktop client user agent.
	DefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/3.14.2 Chrome/112.0.5615.165 Electron/24.1.3.8 Safari/537.36 Channel/pckk_other_ch"
)

// BaseResp is the common envelope for Quark JSON.
type BaseResp struct {
	Status  int    `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// OK reports whether the call succeeded.
func (b BaseResp) OK() bool {
	return b.Status == 0 || (b.Status == 200 && b.Code == 0)
}

// TokenResp is POST /share/sharepage/token
type TokenResp struct {
	BaseResp
	Data struct {
		Stoken string `json:"stoken"`
	} `json:"data"`
}

// File is a file or folder in a share listing.
type File struct {
	Fid       string `json:"fid"`
	FileName  string `json:"file_name"`
	PdirFid   string `json:"pdir_fid"`
	Size      int64  `json:"size"`
	File      bool   `json:"file"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// IsDir reports whether the item is a folder.
func (f File) IsDir() bool { return !f.File }

// ID returns the file id.
func (f File) ID() string { return f.Fid }

// Parent returns the parent folder id.
func (f File) Parent() string { return f.PdirFid }

// ModTime returns the modification time.
func (f File) ModTime() time.Time {
	if f.UpdatedAt == 0 {
		return time.Time{}
	}
	return time.UnixMilli(f.UpdatedAt)
}

// DetailResp is POST /share/sharepage/detail
type DetailResp struct {
	BaseResp
	Data struct {
		List []File `json:"list"`
	} `json:"data"`
	Metadata struct {
		Size  int `json:"_size"`
		Page  int `json:"_page"`
		Total int `json:"_total"`
	} `json:"metadata"`
}

// DownloadResp is POST /share/sharepage/download
type DownloadResp struct {
	BaseResp
	Data []struct {
		DownloadURL string `json:"download_url"`
		Fid         string `json:"fid"`
	} `json:"data"`
}
