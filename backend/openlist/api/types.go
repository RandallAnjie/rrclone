// Package api has type definitions for the OpenList (and AList) API.
//
// See https://www.oplist.org/api/
package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// CodeOK is the Response.Code of success
const CodeOK = 200

// Response is the envelope of every API response
type Response struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// Error is returned for a response with a Code other than CodeOK
type Error struct {
	Code    int
	Message string
}

// Error satisfies the error interface
func (e *Error) Error() string {
	return fmt.Sprintf("openlist error %d: %s", e.Code, e.Message)
}

// LoginRequest is the body of POST /api/auth/login
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	OtpCode  string `json:"otp_code,omitempty"`
}

// LoginResponse is the data of a login
type LoginResponse struct {
	Token string `json:"token"`
}

// Object is a file or folder
type Object struct {
	Name     string            `json:"name"`
	Size     int64             `json:"size"`
	IsDir    bool              `json:"is_dir"`
	Modified time.Time         `json:"modified"`
	Created  time.Time         `json:"created"`
	Sign     string            `json:"sign"`
	HashInfo map[string]string `json:"hash_info"`
}

// ListRequest is the body of POST /api/fs/list
type ListRequest struct {
	Path     string `json:"path"`
	Password string `json:"password,omitempty"`
	Page     int    `json:"page"`
	PerPage  int    `json:"per_page"`
	Refresh  bool   `json:"refresh"`
}

// ListResponse is the data of POST /api/fs/list
type ListResponse struct {
	Content []Object `json:"content"`
	Total   int64    `json:"total"`
	Write   bool     `json:"write"`
}

// GetRequest is the body of POST /api/fs/get
type GetRequest struct {
	Path     string `json:"path"`
	Password string `json:"password,omitempty"`
}

// GetResponse is the data of POST /api/fs/get
type GetResponse struct {
	Object
	RawURL   string `json:"raw_url"`
	Provider string `json:"provider"`
}

// MkdirRequest is the body of POST /api/fs/mkdir
type MkdirRequest struct {
	Path string `json:"path"`
}

// RenameRequest is the body of POST /api/fs/rename
type RenameRequest struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Overwrite bool   `json:"overwrite"`
}

// MoveCopyRequest is the body of POST /api/fs/move and /api/fs/copy
type MoveCopyRequest struct {
	SrcDir    string   `json:"src_dir"`
	DstDir    string   `json:"dst_dir"`
	Names     []string `json:"names"`
	Overwrite bool     `json:"overwrite"`
}

// TaskInfo describes a background task
type TaskInfo struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	State    int     `json:"state"`
	Status   string  `json:"status"`
	Progress float64 `json:"progress"`
	Error    string  `json:"error"`
}

// Task states in TaskInfo.State
const (
	TaskPending   = 0
	TaskRunning   = 1
	TaskSucceeded = 2
	TaskCanceling = 3
	TaskCanceled  = 4
	TaskErrored   = 5 // may be retried
	TaskFailing   = 6
	TaskFailed    = 7
)

// TasksResponse is the data of a move or copy which made tasks
type TasksResponse struct {
	Message string     `json:"message"`
	Tasks   []TaskInfo `json:"tasks"`
}

// RemoveRequest is the body of POST /api/fs/remove
type RemoveRequest struct {
	Dir   string   `json:"dir"`
	Names []string `json:"names"`
}

// MeResponse is the data of GET /api/me
type MeResponse struct {
	Username string `json:"username"`
	BasePath string `json:"base_path"`
}
