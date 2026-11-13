// Package api has type definitions for the Baidu Netdisk open platform
// (xpan) API.
//
// See https://pan.baidu.com/union/doc/
package api

import "fmt"

// Errno values in the responses
const (
	ErrnoOK           = 0
	ErrnoParam        = 2
	ErrnoAuth         = -6
	ErrnoBadName      = -7
	ErrnoExist        = -8
	ErrnoNotExist     = -9
	ErrnoTokenInvalid = 111
	ErrnoTokenExpired = 20016
	ErrnoTokenOther   = 20017
	ErrnoFrequency    = 31034 // too many requests
	ErrnoNotFoundMeta = 31066
	ErrnoNoSpace      = -10
)

// Error is an error response
type Error struct {
	Errno  int    `json:"errno"`
	Errmsg string `json:"errmsg"`
}

// Error satisfies the error interface
func (e *Error) Error() string {
	return fmt.Sprintf("baidu error %d: %s", e.Errno, e.Errmsg)
}

// Status is the common part of every response
type Status struct {
	Errno  int    `json:"errno"`
	Errmsg string `json:"errmsg"`
}

// Err returns the response as an error, or nil if it isn't one
func (s *Status) Err() error {
	if s.Errno == ErrnoOK {
		return nil
	}
	return &Error{Errno: s.Errno, Errmsg: s.Errmsg}
}

// File is a file or folder
type File struct {
	FsID           int64  `json:"fs_id"`
	Path           string `json:"path"`
	ServerFilename string `json:"server_filename"`
	Size           int64  `json:"size"`
	IsDir          int    `json:"isdir"`
	ServerMtime    int64  `json:"server_mtime"`
	LocalMtime     int64  `json:"local_mtime"`
}

// ListResponse is the response of method=list
type ListResponse struct {
	Status
	List []File `json:"list"`
}

// UserInfo is the response of method=uinfo
type UserInfo struct {
	Status
	VipType int `json:"vip_type"` // 0 normal, 1 VIP, 2 SVIP
}

// QuotaResponse is the response of /api/quota
type QuotaResponse struct {
	Status
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}

// FileMetas is the response of method=filemetas
type FileMetas struct {
	Status
	List []struct {
		FsID  int64  `json:"fs_id"`
		Dlink string `json:"dlink"`
	} `json:"list"`
}

// ManageItem is one entry of the filelist of method=filemanager
type ManageItem struct {
	Path    string `json:"path"`
	Dest    string `json:"dest,omitempty"`
	Newname string `json:"newname,omitempty"`
	Ondup   string `json:"ondup,omitempty"`
}

// ManageResponse is the response of method=filemanager
type ManageResponse struct {
	Status
	Info []struct {
		Errno int    `json:"errno"`
		Path  string `json:"path"`
	} `json:"info"`
}

// Return types of method=precreate
const (
	ReturnTypeUpload = 1 // upload the blocks in BlockList
	ReturnTypeDone   = 2 // rapid upload, the file already exists
)

// PrecreateResponse is the response of method=precreate
type PrecreateResponse struct {
	Status
	ReturnType int    `json:"return_type"`
	UploadID   string `json:"uploadid"`
	BlockList  []int  `json:"block_list"` // indices of blocks to upload
	Info       File   `json:"info"`
}

// CreateResponse is the response of method=create
type CreateResponse struct {
	Status
	File
}

// UploadServers is the response of method=locateupload
type UploadServers struct {
	ErrorCode int `json:"error_code"`
	Servers   []struct {
		Server string `json:"server"`
	} `json:"servers"`
	BakServers []struct {
		Server string `json:"server"`
	} `json:"bak_servers"`
}

// SliceResponse is the response of a superfile2 block upload
type SliceResponse struct {
	Status
	Md5 string `json:"md5"`
}
