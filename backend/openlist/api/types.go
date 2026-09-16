// Package api contains types for the AList / OpenList v3 HTTP API.
package api

import "time"

// Envelope is the common AList v3 JSON envelope.
type Envelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

// LoginData is POST /api/auth/login
type LoginData struct {
	Token string `json:"token"`
}

// Obj is a file or folder in a list response.
type Obj struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	IsDir    bool      `json:"is_dir"`
	Modified time.Time `json:"modified"`
	HashInfo struct {
		MD5  string `json:"md5"`
		SHA1 string `json:"sha1"`
	} `json:"hash_info"`
}

// ListData is POST /api/fs/list
type ListData struct {
	Content  []Obj `json:"content"`
	Total    int   `json:"total"`
	Readme   string `json:"readme"`
	Write    bool  `json:"write"`
}

// GetData is POST /api/fs/get
type GetData struct {
	Obj
	RawURL string `json:"raw_url"`
}
