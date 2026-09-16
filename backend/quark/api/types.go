// Package api contains types for the Quark Drive web API.
package api

import "time"

const (
	// DefaultRoot is https://drive.quark.cn/1/clouddrive
	DefaultRoot = "https://drive.quark.cn/1/clouddrive"
	// DefaultReferer is the Quark web referer.
	DefaultReferer = "https://pan.quark.cn"
	// DefaultUA is a Quark desktop client user agent.
	DefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/2.5.20 Chrome/100.0.4896.160 Safari/537.36"
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

// File is a file or folder in a sort response.
type File struct {
	Fid        string `json:"fid"`
	FileName   string `json:"file_name"`
	PdirFid    string `json:"pdir_fid"`
	Size       int64  `json:"size"`
	File       bool   `json:"file"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
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

// SortResp is GET /file/sort
type SortResp struct {
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

// DownloadResp is POST /file/download
type DownloadResp struct {
	BaseResp
	Data []struct {
		DownloadURL string `json:"download_url"`
		Fid         string `json:"fid"`
	} `json:"data"`
}

// DirResp is POST /file
type DirResp struct {
	BaseResp
	Data struct {
		Fid string `json:"fid"`
	} `json:"data"`
}

// HashResp is POST /file/update/hash
type HashResp struct {
	BaseResp
	Data struct {
		Finish bool `json:"finish"`
	} `json:"data"`
}

// UpPreResp is POST /file/upload/pre
type UpPreResp struct {
	BaseResp
	Data struct {
		TaskID    string `json:"task_id"`
		Finish    bool   `json:"finish"`
		Fid       string `json:"fid"`
		AuthInfo  string `json:"auth_info"`
		UploadID  string `json:"upload_id"`
		Bucket    string `json:"bucket"`
		ObjKey    string `json:"obj_key"`
		UploadURL string `json:"upload_url"`
		FormatType string `json:"format_type"`
	} `json:"data"`
}

// UpAuthResp is POST /file/upload/auth
type UpAuthResp struct {
	BaseResp
	Data struct {
		AuthKey string `json:"auth_key"`
	} `json:"data"`
}

// FinishResp is POST /file/upload/finish
type FinishResp struct {
	BaseResp
	Data struct {
		Fid      string `json:"fid"`
		FileName string `json:"file_name"`
		Size     int64  `json:"size"`
	} `json:"data"`
}
