// Package api has type definitions for the Aliyun Drive open platform
// API.
//
// See https://www.yuque.com/aliyundrive/zpfszx
package api

import (
	"fmt"
	"time"
)

// Error is an error response
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

// Error satisfies the error interface
func (e *Error) Error() string {
	return fmt.Sprintf("aliyundrive error %d %s: %s", e.Status, e.Code, e.Message)
}

// Error codes
const (
	CodeAccessTokenInvalid = "AccessTokenInvalid"
	CodeAccessTokenExpired = "AccessTokenExpired"
	CodeTooManyRequests    = "TooManyRequests"
	CodeNotFoundFile       = "NotFound.File"
	CodeAlreadyExist       = "AlreadyExist.File"
)

// TokenRequest is the body of POST /oauth/access_token
type TokenRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	GrantType    string `json:"grant_type"` // authorization_code or refresh_token
	Code         string `json:"code,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// TokenResponse is the response of POST /oauth/access_token
type TokenResponse struct {
	TokenType    string `json:"token_type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// DriveInfo is the response of /adrive/v1.0/user/getDriveInfo
type DriveInfo struct {
	UserID          string `json:"user_id"`
	DefaultDriveID  string `json:"default_drive_id"`
	ResourceDriveID string `json:"resource_drive_id"`
	BackupDriveID   string `json:"backup_drive_id"`
}

// SpaceInfo is the response of /adrive/v1.0/user/getSpaceInfo
type SpaceInfo struct {
	PersonalSpaceInfo struct {
		UsedSize  int64 `json:"used_size"`
		TotalSize int64 `json:"total_size"`
	} `json:"personal_space_info"`
}

// File types in File.Type
const (
	TypeFile   = "file"
	TypeFolder = "folder"
)

// File is a file or folder
type File struct {
	DriveID      string    `json:"drive_id"`
	FileID       string    `json:"file_id"`
	ParentFileID string    `json:"parent_file_id"`
	Name         string    `json:"name"`
	Size         int64     `json:"size"`
	ContentHash  string    `json:"content_hash"` // SHA1
	Type         string    `json:"type"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// IsDir reports whether f is a folder
func (f *File) IsDir() bool {
	return f.Type == TypeFolder
}

// ListRequest is the body of /adrive/v1.0/openFile/list
type ListRequest struct {
	DriveID      string `json:"drive_id"`
	ParentFileID string `json:"parent_file_id"`
	Limit        int    `json:"limit"`
	Marker       string `json:"marker,omitempty"`
}

// ListResponse is the response of /adrive/v1.0/openFile/list
type ListResponse struct {
	Items      []File `json:"items"`
	NextMarker string `json:"next_marker"`
}

// FileRequest addresses one file
type FileRequest struct {
	DriveID string `json:"drive_id"`
	FileID  string `json:"file_id"`
}

// Name check modes
const (
	CheckRefuse = "refuse"
	CheckIgnore = "ignore"
)

// PartInfo is one part of an upload
type PartInfo struct {
	PartNumber int    `json:"part_number"`
	UploadURL  string `json:"upload_url,omitempty"`
}

// CreateRequest is the body of /adrive/v1.0/openFile/create
type CreateRequest struct {
	DriveID         string      `json:"drive_id"`
	ParentFileID    string      `json:"parent_file_id"`
	Name            string      `json:"name"`
	Type            string      `json:"type"`
	CheckNameMode   string      `json:"check_name_mode"`
	PartInfoList    *[]PartInfo `json:"part_info_list,omitempty"` // [] for an empty file, nil for a folder
	Size            int64       `json:"size,omitempty"`
	LocalModifiedAt string      `json:"local_modified_at,omitempty"`
}

// CreateResponse is the response of /adrive/v1.0/openFile/create
type CreateResponse struct {
	FileID       string     `json:"file_id"`
	UploadID     string     `json:"upload_id"`
	Exist        bool       `json:"exist"`
	RapidUpload  bool       `json:"rapid_upload"`
	PartInfoList []PartInfo `json:"part_info_list"`
}

// UploadURLRequest is the body of /adrive/v1.0/openFile/getUploadUrl
type UploadURLRequest struct {
	DriveID      string     `json:"drive_id"`
	FileID       string     `json:"file_id"`
	UploadID     string     `json:"upload_id"`
	PartInfoList []PartInfo `json:"part_info_list"`
}

// CompleteRequest is the body of /adrive/v1.0/openFile/complete
type CompleteRequest struct {
	DriveID  string `json:"drive_id"`
	FileID   string `json:"file_id"`
	UploadID string `json:"upload_id"`
}

// UpdateRequest is the body of /adrive/v1.0/openFile/update
type UpdateRequest struct {
	DriveID       string `json:"drive_id"`
	FileID        string `json:"file_id"`
	Name          string `json:"name"`
	CheckNameMode string `json:"check_name_mode"`
}

// MoveRequest is the body of /adrive/v1.0/openFile/move
type MoveRequest struct {
	DriveID        string `json:"drive_id"`
	FileID         string `json:"file_id"`
	ToParentFileID string `json:"to_parent_file_id"`
	NewName        string `json:"new_name,omitempty"`
	CheckNameMode  string `json:"check_name_mode"`
}

// CopyRequest is the body of /adrive/v1.0/openFile/copy
type CopyRequest struct {
	DriveID        string `json:"drive_id"`
	FileID         string `json:"file_id"`
	ToParentFileID string `json:"to_parent_file_id"`
	AutoRename     bool   `json:"auto_rename"`
}

// MoveCopyResponse is the response of a move or copy
type MoveCopyResponse struct {
	FileID      string `json:"file_id"`
	Exist       bool   `json:"exist"`
	AsyncTaskID string `json:"async_task_id"`
}

// DownloadURLRequest is the body of /adrive/v1.0/openFile/getDownloadUrl
type DownloadURLRequest struct {
	DriveID   string `json:"drive_id"`
	FileID    string `json:"file_id"`
	ExpireSec int    `json:"expire_sec"`
}

// DownloadURLResponse is the response of /adrive/v1.0/openFile/getDownloadUrl
type DownloadURLResponse struct {
	URL string `json:"url"`
}
