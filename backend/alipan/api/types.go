// Package api contains types for the Aliyun Drive Open API.
package api

import (
	"fmt"
	"time"
)

const (
	// DefaultRoot is https://openapi.alipan.com
	DefaultRoot = "https://openapi.alipan.com"
	// RootFolder is the account root folder id.
	RootFolder = "root"
)

// TokenReq is POST /oauth/access_token
type TokenReq struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
}

// TokenResp is an OAuth token response.
type TokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Code         string `json:"code"`
	Message      string `json:"message"`
}

// Err returns a non-nil error when the token payload is an error.
func (t TokenResp) Err() error {
	if t.Code == "" && t.AccessToken != "" {
		return nil
	}
	if t.Message == "" {
		if t.Code == "" {
			return fmt.Errorf("alipan: empty access token")
		}
		return fmt.Errorf("alipan: %s", t.Code)
	}
	return fmt.Errorf("alipan: %s: %s", t.Code, t.Message)
}

// Error is a JSON error envelope.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// DriveInfoResp is POST /adrive/v1.0/user/getDriveInfo
type DriveInfoResp struct {
	DefaultDriveID  string `json:"default_drive_id"`
	ResourceDriveID string `json:"resource_drive_id"`
	BackupDriveID   string `json:"backup_drive_id"`
	UserID          string `json:"user_id"`
	Name            string `json:"name"`
}

// SpaceResp is POST /adrive/v1.0/user/getSpaceInfo
type SpaceResp struct {
	PersonalSpaceInfo struct {
		UsedSize  int64 `json:"used_size"`
		TotalSize int64 `json:"total_size"`
	} `json:"personal_space_info"`
}

// File is a file or folder.
type File struct {
	DriveID      string    `json:"drive_id"`
	FileID       string    `json:"file_id"`
	ParentFileID string    `json:"parent_file_id"`
	Name         string    `json:"name"`
	FileName     string    `json:"file_name"`
	Size         int64     `json:"size"`
	ContentHash  string    `json:"content_hash"`
	Type         string    `json:"type"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// IsDir reports whether the item is a folder.
func (f File) IsDir() bool {
	return f.Type == "folder"
}

// ID returns the file id.
func (f File) ID() string {
	return f.FileID
}

// Parent returns the parent folder id.
func (f File) Parent() string {
	return f.ParentFileID
}

// DisplayName returns the file name.
func (f File) DisplayName() string {
	if f.Name != "" {
		return f.Name
	}
	return f.FileName
}

// ListReq is POST /adrive/v1.0/openFile/list
type ListReq struct {
	DriveID         string `json:"drive_id"`
	ParentFileID    string `json:"parent_file_id"`
	Limit           int    `json:"limit"`
	Marker          string `json:"marker,omitempty"`
	OrderBy         string `json:"order_by,omitempty"`
	OrderDirection  string `json:"order_direction,omitempty"`
	Fields          string `json:"fields,omitempty"`
}

// ListResp is a paged file list.
type ListResp struct {
	Items      []File `json:"items"`
	NextMarker string `json:"next_marker"`
}

// CreateFolderReq is POST /adrive/v1.0/openFile/createFolder
type CreateFolderReq struct {
	DriveID         string `json:"drive_id"`
	ParentFileID    string `json:"parent_file_id"`
	Name            string `json:"name"`
	CheckNameMode   string `json:"check_name_mode"`
	Type            string `json:"type"`
}

// CreateFolderResp is a create-folder response.
type CreateFolderResp struct {
	FileID string `json:"file_id"`
	Type   string `json:"type"`
}

// DownloadReq is POST /adrive/v1.0/openFile/getDownloadUrl
type DownloadReq struct {
	DriveID string `json:"drive_id"`
	FileID  string `json:"file_id"`
}

// DownloadResp is a download URL response.
type DownloadResp struct {
	URL        string `json:"url"`
	Expiration string `json:"expiration"`
}

// MoveReq is POST /adrive/v1.0/openFile/move
type MoveReq struct {
	DriveID        string `json:"drive_id"`
	FileID         string `json:"file_id"`
	ToParentFileID string `json:"to_parent_file_id"`
	CheckNameMode  string `json:"check_name_mode"`
}

// UpdateReq is POST /adrive/v1.0/openFile/update
type UpdateReq struct {
	DriveID string `json:"drive_id"`
	FileID  string `json:"file_id"`
	Name    string `json:"name"`
}

// TrashReq is POST /adrive/v1.0/openFile/recyclebin/trash
type TrashReq struct {
	DriveID string `json:"drive_id"`
	FileID  string `json:"file_id"`
}

// PartInfo is one upload part.
type PartInfo struct {
	PartNumber int    `json:"part_number"`
	UploadURL  string `json:"upload_url,omitempty"`
	Etag       string `json:"etag,omitempty"`
}

// CreateFileReq is POST /adrive/v1.0/openFile/create
type CreateFileReq struct {
	DriveID         string     `json:"drive_id"`
	ParentFileID    string     `json:"parent_file_id"`
	Name            string     `json:"name"`
	Type            string     `json:"type"`
	CheckNameMode   string     `json:"check_name_mode"`
	Size            int64      `json:"size"`
	ContentHash     string     `json:"content_hash,omitempty"`
	ContentHashName string     `json:"content_hash_name,omitempty"`
	PartInfoList    []PartInfo `json:"part_info_list"`
}

// CreateFileResp is a create-file response.
type CreateFileResp struct {
	FileID       string     `json:"file_id"`
	UploadID     string     `json:"upload_id"`
	RapidUpload  bool       `json:"rapid_upload"`
	PartInfoList []PartInfo `json:"part_info_list"`
}

// CompleteReq is POST /adrive/v1.0/openFile/complete
type CompleteReq struct {
	DriveID  string `json:"drive_id"`
	FileID   string `json:"file_id"`
	UploadID string `json:"upload_id"`
}

// CompleteResp is a complete-upload response.
type CompleteResp struct {
	File
}
