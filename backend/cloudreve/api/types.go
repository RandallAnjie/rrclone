// Package api has type definitions for the Cloudreve v4 API.
//
// See https://docs.cloudreve.org/api/overview
package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// Response codes returned in Response.Code
const (
	CodeOK                   = 0
	CodeNotFullySuccess      = 203 // partial success, or 2FA required at login
	CodeCheckLogin           = 401
	CodeNoPermission         = 403
	CodeNotFound             = 404
	CodeConflict             = 409
	CodeParamErr             = 40001
	CodeObjectExist          = 40004
	CodeUploadSessionExpired = 40011
	CodeParentNotExist       = 40016
	CodeCredentialInvalid    = 40020
	Code2FACodeErr           = 40022
	CodeCaptchaError         = 40026
	CodeFileNotFound         = 40044
	CodeInsufficientCapacity = 40051
	CodeLockConflict         = 40073
)

// File types in File.Type
const (
	FileTypeFile   = 0
	FileTypeFolder = 1
)

// Storage policy types in StoragePolicy.Type
const (
	PolicyLocal    = "local"
	PolicyRemote   = "remote"
	PolicyOneDrive = "onedrive"
	PolicyS3       = "s3"
	PolicyKS3      = "ks3"
	PolicyOSS      = "oss"
	PolicyCOS      = "cos"
	PolicyOBS      = "obs"
	PolicyQiniu    = "qiniu"
	PolicyUpyun    = "upyun"
)

// CipherAES256CTR is the only file encryption cipher Cloudreve uses
const CipherAES256CTR = "aes-256-ctr"

// Response is the envelope of every API response.
//
// The HTTP status is 200 even for errors, which are reported in Code.
type Response struct {
	Code  int             `json:"code"`
	Data  json.RawMessage `json:"data,omitempty"`
	Msg   string          `json:"msg"`
	Error string          `json:"error,omitempty"`
}

// Error is returned for a response with a non zero Code
type Error struct {
	Code int
	Msg  string
}

// Error satisfies the error interface
func (e *Error) Error() string {
	return fmt.Sprintf("cloudreve error %d: %s", e.Code, e.Msg)
}

// Token is an access and refresh token pair
type Token struct {
	AccessToken    string    `json:"access_token"`
	RefreshToken   string    `json:"refresh_token"`
	AccessExpires  time.Time `json:"access_expires"`
	RefreshExpires time.Time `json:"refresh_expires"`
}

// LoginRequest is the body of POST /session/token
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Captcha  string `json:"captcha,omitempty"`
	Ticket   string `json:"ticket,omitempty"`
}

// LoginResponse is the data of a successful login
type LoginResponse struct {
	Token Token `json:"token"`
}

// TwoFARequest is the body of POST /session/token/2fa
type TwoFARequest struct {
	OTP       string `json:"otp"`
	SessionID string `json:"session_id"`
}

// RefreshRequest is the body of POST /session/token/refresh
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// StoragePolicy describes where and how files are stored
type StoragePolicy struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	MaxSize          int64  `json:"max_size"`
	Relay            bool   `json:"relay,omitempty"`
	ChunkConcurrency int    `json:"chunk_concurrency,omitempty"`
	Encryption       bool   `json:"encryption,omitempty"`
}

// File is a file or folder
type File struct {
	Type          int               `json:"type"`
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Size          int64             `json:"size"`
	Metadata      map[string]string `json:"metadata"`
	Path          string            `json:"path,omitempty"`
	PrimaryEntity string            `json:"primary_entity,omitempty"`
}

// IsDir reports whether the file is a folder
func (f *File) IsDir() bool {
	return f.Type == FileTypeFolder
}

// Pagination is the paging state of a listing
type Pagination struct {
	Page          int    `json:"page"`
	PageSize      int    `json:"page_size"`
	TotalItems    int    `json:"total_items,omitempty"`
	NextPageToken string `json:"next_token,omitempty"`
	IsCursor      bool   `json:"is_cursor,omitempty"`
}

// ListResponse is the data of GET /file
type ListResponse struct {
	Files         []File         `json:"files"`
	Parent        File           `json:"parent"`
	Pagination    *Pagination    `json:"pagination"`
	StoragePolicy *StoragePolicy `json:"storage_policy,omitempty"`
}

// CreateFileRequest is the body of POST /file/create
type CreateFileRequest struct {
	URI           string `json:"uri"`
	Type          string `json:"type"` // "file" or "folder"
	ErrOnConflict bool   `json:"err_on_conflict"`
}

// RenameRequest is the body of POST /file/rename
type RenameRequest struct {
	URI     string `json:"uri"`
	NewName string `json:"new_name"`
}

// MoveRequest is the body of POST /file/move
type MoveRequest struct {
	URIs []string `json:"uris"`
	Dst  string   `json:"dst"`
	Copy bool     `json:"copy"`
}

// DeleteRequest is the body of DELETE /file
type DeleteRequest struct {
	URIs           []string `json:"uris"`
	UnlinkOnly     bool     `json:"unlink,omitempty"`
	SkipSoftDelete bool     `json:"skip_soft_delete,omitempty"`
}

// FileURLRequest is the body of POST /file/url
type FileURLRequest struct {
	URIs     []string `json:"uris"`
	Download bool     `json:"download"`
	NoCache  bool     `json:"no_cache,omitempty"`
}

// EntityURL is one download URL
type EntityURL struct {
	URL string `json:"url"`
}

// FileURLResponse is the data of POST /file/url
type FileURLResponse struct {
	URLs    []EntityURL `json:"urls"`
	Expires *time.Time  `json:"expires"`
}

// Capacity is the data of GET /user/capacity
type Capacity struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}

// UploadSessionRequest is the body of PUT /file/upload
type UploadSessionRequest struct {
	URI                 string   `json:"uri"`
	Size                int64    `json:"size"`
	LastModified        int64    `json:"last_modified,omitempty"` // Unix milliseconds
	MimeType            string   `json:"mime_type,omitempty"`
	PolicyID            string   `json:"policy_id,omitempty"`
	EntityType          string   `json:"entity_type,omitempty"` // "" or "version"
	EncryptionSupported []string `json:"encryption_supported,omitempty"`
}

// EncryptMetadata is the key for client side encryption of an upload
type EncryptMetadata struct {
	Algorithm    string `json:"algorithm"`
	KeyPlainText []byte `json:"key_plain_text"`
	IV           []byte `json:"iv"`
}

// UploadSession is the data of PUT /file/upload
type UploadSession struct {
	SessionID       string           `json:"session_id"`
	UploadID        string           `json:"upload_id"`
	ChunkSize       int64            `json:"chunk_size"` // 0 means the whole file in one chunk
	Expires         int64            `json:"expires"`
	UploadURLs      []string         `json:"upload_urls,omitempty"`
	Credential      string           `json:"credential,omitempty"`
	CompleteURL     string           `json:"completeURL,omitempty"`
	StoragePolicy   *StoragePolicy   `json:"storage_policy,omitempty"`
	URI             string           `json:"uri"`
	CallbackSecret  string           `json:"callback_secret"`
	UploadPolicy    string           `json:"upload_policy,omitempty"`
	EncryptMetadata *EncryptMetadata `json:"encrypt_metadata,omitempty"`
}

// DeleteUploadSessionRequest is the body of DELETE /file/upload
type DeleteUploadSessionRequest struct {
	ID  string `json:"id"`
	URI string `json:"uri"`
}

// ShareCreateRequest is the body of PUT /share
type ShareCreateRequest struct {
	URI       string `json:"uri"`
	IsPrivate bool   `json:"is_private"`
	Expire    int    `json:"expire,omitempty"` // seconds
}

// CompleteMultipartUpload is the XML body completing an S3 style
// multipart upload
type CompleteMultipartUpload struct {
	XMLName struct{}        `xml:"CompleteMultipartUpload"`
	Parts   []CompletedPart `xml:"Part"`
}

// CompletedPart is one part of CompleteMultipartUpload
type CompletedPart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

// QiniuPart is one part of a Qiniu multipart completion
type QiniuPart struct {
	ETag       string `json:"etag"`
	PartNumber int    `json:"partNumber"`
}

// QiniuComplete is the body completing a Qiniu multipart upload
type QiniuComplete struct {
	Parts []QiniuPart `json:"parts"`
}
