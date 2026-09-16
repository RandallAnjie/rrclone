// Package api contains types for the 123 Cloud Open Platform API.
package api

import (
	"fmt"
	"strconv"
	"time"
)

const (
	// DefaultRoot is https://open-api.123pan.com
	DefaultRoot = "https://open-api.123pan.com"
	// PlatformHeader is required by the open platform.
	PlatformHeader = "open_platform"
	// DuplicateOverwrite replaces an existing file of the same name on upload.
	DuplicateOverwrite = 2
	// TokenExpired is returned when the Bearer token is no longer valid.
	TokenExpired = 401
	// TooManyRequests is returned when the open API rate limit is hit.
	TooManyRequests = 429
	// ListDone marks the end of a paged file list.
	ListDone = int64(-1)
)

// BaseResp is the common envelope for 123 Open API JSON.
type BaseResp struct {
	Code     int    `json:"code"`
	Message  string `json:"message"`
	XTraceID string `json:"x-traceID"`
}

// GetCode returns the API status code.
func (b BaseResp) GetCode() int {
	return b.Code
}

// Err returns a non-nil error when Code is not 0.
func (b BaseResp) Err() error {
	if b.Code == 0 {
		return nil
	}
	if b.Message == "" {
		return fmt.Errorf("123 api error code %d", b.Code)
	}
	return fmt.Errorf("123 api error %d: %s", b.Code, b.Message)
}

// ParseID parses a decimal file id. An empty string is the account root (0).
func ParseID(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

// TokenReq is POST /api/v1/access_token
type TokenReq struct {
	ClientID     string `json:"clientID"`
	ClientSecret string `json:"clientSecret"`
}

// TokenResp is the access token response.
type TokenResp struct {
	BaseResp
	Data struct {
		AccessToken string `json:"accessToken"`
		ExpiredAt   string `json:"expiredAt"`
	} `json:"data"`
}

// File is a file or folder in a list response.
type File struct {
	FileName     string `json:"filename"`
	Size         int64  `json:"size"`
	CreateAt     string `json:"createAt"`
	UpdateAt     string `json:"updateAt"`
	FileID       int64  `json:"fileId"`
	Type         int    `json:"type"` // 0/2 file, 1 folder
	Etag         string `json:"etag"`
	S3KeyFlag    string `json:"s3KeyFlag"`
	ParentFileID int64  `json:"parentFileId"`
	Category     int    `json:"category"`
	Status       int    `json:"status"`
	Trashed      int    `json:"trashed"`
}

// IsDir reports whether the item is a folder.
func (f File) IsDir() bool {
	return f.Type == 1
}

// ID returns the file id as a decimal string.
func (f File) ID() string {
	return strconv.FormatInt(f.FileID, 10)
}

// Parent returns the parent folder id.
func (f File) Parent() string {
	return strconv.FormatInt(f.ParentFileID, 10)
}

// ModTime parses UpdateAt as China Standard Time.
func (f File) ModTime() time.Time {
	loc := time.FixedZone("CST", 8*3600)
	t, err := time.ParseInLocation("2006-01-02 15:04:05", f.UpdateAt, loc)
	if err != nil {
		return time.Time{}
	}
	return t
}

// FileListResp is GET /api/v2/file/list
type FileListResp struct {
	BaseResp
	Data struct {
		LastFileID int64  `json:"lastFileId"`
		FileList   []File `json:"fileList"`
	} `json:"data"`
}

// UserInfoResp is GET /api/v1/user/info
type UserInfoResp struct {
	BaseResp
	Data struct {
		UID            uint64 `json:"uid"`
		SpaceUsed      int64  `json:"spaceUsed"`
		SpacePermanent int64  `json:"spacePermanent"`
		SpaceTemp      int64  `json:"spaceTemp"`
	} `json:"data"`
}

// DownloadInfoResp is GET /api/v1/file/download_info
type DownloadInfoResp struct {
	BaseResp
	Data struct {
		DownloadURL string `json:"downloadUrl"`
	} `json:"data"`
}

// MkdirReq is POST /upload/v1/file/mkdir
type MkdirReq struct {
	ParentID string `json:"parentID"`
	Name     string `json:"name"`
}

// MkdirResp is the mkdir response.
type MkdirResp struct {
	BaseResp
	Data struct {
		DirID int64 `json:"dirID"`
	} `json:"data"`
}

// MoveReq is POST /api/v1/file/move
type MoveReq struct {
	FileIDs        []int64 `json:"fileIDs"`
	ToParentFileID int64   `json:"toParentFileID"`
}

// RenameReq is PUT /api/v1/file/name
type RenameReq struct {
	FileID   int64  `json:"fileId"`
	FileName string `json:"fileName"`
}

// TrashReq is POST /api/v1/file/trash
type TrashReq struct {
	FileIDs []int64 `json:"fileIDs"`
}

// UploadCreateReq is POST /upload/v2/file/create
type UploadCreateReq struct {
	ParentFileID int64  `json:"parentFileId"`
	Filename     string `json:"filename"`
	Etag         string `json:"etag"`
	Size         int64  `json:"size"`
	Duplicate    int    `json:"duplicate"`
	ContainDir   bool   `json:"containDir"`
}

// UploadCreateResp is the create-upload response.
type UploadCreateResp struct {
	BaseResp
	Data struct {
		FileID      int64    `json:"fileID"`
		PreuploadID string   `json:"preuploadID"`
		Reuse       bool     `json:"reuse"`
		SliceSize   int64    `json:"sliceSize"`
		Servers     []string `json:"servers"`
	} `json:"data"`
}

// UploadCompleteReq is POST /upload/v2/file/upload_complete
type UploadCompleteReq struct {
	PreuploadID string `json:"preuploadID"`
}

// UploadCompleteResp is the complete-upload response.
type UploadCompleteResp struct {
	BaseResp
	Data struct {
		Completed bool  `json:"completed"`
		FileID    int64 `json:"fileID"`
	} `json:"data"`
}

// SHA1ReuseReq is POST /upload/v2/file/sha1_reuse
type SHA1ReuseReq struct {
	ParentFileID int64  `json:"parentFileID"`
	Filename     string `json:"filename"`
	SHA1         string `json:"sha1"`
	Size         int64  `json:"size"`
	Duplicate    int    `json:"duplicate"`
}

// SHA1ReuseResp is the SHA-1 reuse response.
type SHA1ReuseResp struct {
	BaseResp
	Data struct {
		FileID int64 `json:"fileID"`
		Reuse  bool  `json:"reuse"`
	} `json:"data"`
}
