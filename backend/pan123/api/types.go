// Package api has type definitions for the 123 Pan open platform API.
//
// See https://123yunpan.yuque.com/org-wiki-123yunpan-muaork/cr6ced
package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Response codes in Response.Code
const (
	CodeOK           = 0
	CodeUnauthorized = 401
	CodeTooFast      = 429
)

// Response is the envelope of every API response
type Response struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	TraceID string          `json:"x-traceID"`
}

// Error is returned for a response with a non zero Code
type Error struct {
	Code    int
	Message string
}

// Error satisfies the error interface
func (e *Error) Error() string {
	return fmt.Sprintf("123pan error %d: %s", e.Code, e.Message)
}

// AccessTokenRequest is the body of POST /api/v1/access_token
type AccessTokenRequest struct {
	ClientID     string `json:"clientID"`
	ClientSecret string `json:"clientSecret"`
}

// AccessToken is the data of POST /api/v1/access_token
type AccessToken struct {
	AccessToken string    `json:"accessToken"`
	ExpiredAt   time.Time `json:"expiredAt"`
}

// File types in File.Type
const (
	TypeFile   = 0
	TypeFolder = 1
)

// Time is 123 Pan's "2006-01-02 15:04:05" time in China Standard Time
type Time time.Time

var cst = time.FixedZone("CST", 8*60*60)

// UnmarshalJSON parses the 123 Pan time format
func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*t = Time{}
		return nil
	}
	parsed, err := time.ParseInLocation(time.DateTime, s, cst)
	if err != nil {
		return err
	}
	*t = Time(parsed)
	return nil
}

// MarshalJSON writes the 123 Pan time format
func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(t).In(cst).Format(time.DateTime) + `"`), nil
}

// File is a file or folder
type File struct {
	FileID       int64  `json:"fileId"`
	FileName     string `json:"filename"`
	Type         int    `json:"type"`
	Size         int64  `json:"size"`
	Etag         string `json:"etag"` // MD5 of files
	Status       int    `json:"status"`
	ParentFileID int64  `json:"parentFileId"`
	Trashed      int    `json:"trashed"`
	CreateAt     Time   `json:"createAt"`
	UpdateAt     Time   `json:"updateAt"`
}

// IsDir reports whether f is a folder
func (f *File) IsDir() bool {
	return f.Type == TypeFolder
}

// FileList is the data of GET /api/v2/file/list
type FileList struct {
	LastFileID int64  `json:"lastFileId"` // -1 on the last page
	FileList   []File `json:"fileList"`
}

// MkdirRequest is the body of POST /upload/v1/file/mkdir
type MkdirRequest struct {
	Name     string `json:"name"`
	ParentID int64  `json:"parentID"`
}

// MkdirResponse is the data of POST /upload/v1/file/mkdir
type MkdirResponse struct {
	DirID int64 `json:"dirID"`
}

// MoveRequest is the body of POST /api/v1/file/move
type MoveRequest struct {
	FileIDs        []int64 `json:"fileIDs"`
	ToParentFileID int64   `json:"toParentFileID"`
}

// RenameRequest is the body of PUT /api/v1/file/name
type RenameRequest struct {
	FileID   int64  `json:"fileId"`
	FileName string `json:"fileName"`
}

// TrashRequest is the body of POST /api/v1/file/trash
type TrashRequest struct {
	FileIDs []int64 `json:"fileIDs"`
}

// DownloadInfo is the data of GET /api/v1/file/download_info
type DownloadInfo struct {
	DownloadURL string `json:"downloadUrl"`
}

// UserInfo is the data of GET /api/v1/user/info
type UserInfo struct {
	UID            uint64 `json:"uid"`
	SpaceUsed      int64  `json:"spaceUsed"`
	SpacePermanent int64  `json:"spacePermanent"`
	SpaceTemp      int64  `json:"spaceTemp"`
}

// Duplicate policies in CreateRequest.Duplicate
const (
	DuplicateKeepBoth  = 1
	DuplicateOverwrite = 2
)

// CreateRequest is the body of POST /upload/v2/file/create
type CreateRequest struct {
	ParentFileID int64  `json:"parentFileID"`
	Filename     string `json:"filename"`
	Etag         string `json:"etag"`
	Size         int64  `json:"size"`
	Duplicate    int    `json:"duplicate"`
	ContainDir   bool   `json:"containDir"`
}

// CreateResponse is the data of POST /upload/v2/file/create
type CreateResponse struct {
	FileID      int64    `json:"fileID"` // set if Reuse
	PreuploadID string   `json:"preuploadID"`
	Reuse       bool     `json:"reuse"` // the content is already on 123 Pan
	SliceSize   int64    `json:"sliceSize"`
	Servers     []string `json:"servers"`
}

// CompleteRequest is the body of POST /upload/v2/file/upload_complete
type CompleteRequest struct {
	PreuploadID string `json:"preuploadID"`
}

// CompleteResponse is the data of POST /upload/v2/file/upload_complete
type CompleteResponse struct {
	Completed bool  `json:"completed"`
	FileID    int64 `json:"fileID"`
}
