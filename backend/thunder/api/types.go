// Package api contains types for this backend.
package api

import (
	"strconv"
	"strings"
	"time"
)

// FlexInt64 unmarshals JSON numbers or strings.
type FlexInt64 int64

// UnmarshalJSON implements json.Unmarshaler.
func (f *FlexInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `" `)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		var u float64
		u, err = strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		n = int64(u)
	}
	*f = FlexInt64(n)
	return nil
}

// ParseTime parses RFC3339 or unix seconds/millis.
func ParseTime(s string, n int64) time.Time {
	if n > 1e12 {
		return time.UnixMilli(n)
	}
	if n > 0 {
		return time.Unix(n, 0)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

const DefaultRoot = "https://api-pan.xunlei.com/drive/v1"

type File struct {
	IDField     string    `json:"id"`
	NameField   string    `json:"name"`
	PathField   string    `json:"-"`
	ParentField string    `json:"parent_id,omitempty"`
	SizeField   FlexInt64 `json:"size"`
	TypeField   string    `json:"type,omitempty"`
	KindField   string    `json:"kind,omitempty"`
	IsDirRaw    FlexInt64 `json:"is_dir,omitempty"`
	IsdirRaw    FlexInt64 `json:"isdir,omitempty"`
	TimeField   string    `json:"updated_at,omitempty"`
	TimeUnix    FlexInt64 `json:"server_mtime,omitempty"`
	MD5Field    string    `json:"md5,omitempty"`
	URLField    string    `json:"url,omitempty"`
}

func (f File) DisplayName() string { return f.NameField }
func (f *File) SetName(n string)   { f.NameField = n }
func (f File) ID() string {
	if f.IDField != "" {
		return f.IDField
	}
	if f.PathField != "" {
		return f.PathField
	}
	return f.NameField
}
func (f File) Parent() string { return f.ParentField }
func (f File) Size() int64    { return int64(f.SizeField) }
func (f File) IsDir() bool {
	return f.TypeField == "folder" || f.TypeField == "dir" || f.KindField == "drive#folder" || int64(f.IsDirRaw) == 1 || int64(f.IsdirRaw) == 1
}
func (f File) ModTime() time.Time  { return ParseTime(f.TimeField, int64(f.TimeUnix)) }
func (f File) MD5() string         { return f.MD5Field }
func (f File) DownloadURL() string { return f.URLField }

type ListResp struct {
	Items   []File `json:"items"`
	Files   []File `json:"files"`
	List    []File `json:"list"`
	Objects []File `json:"objects"`
	Content []File `json:"content"`
	Data    struct {
		List    []File `json:"list"`
		Content []File `json:"content"`
		Objects []File `json:"objects"`
		Items   []File `json:"items"`
	} `json:"data"`
	Code    int    `json:"code"`
	Errno   int    `json:"errno"`
	Message string `json:"message"`
}

func (r *ListResp) Normalize() {
	if len(r.Items) == 0 {
		switch {
		case len(r.Files) > 0:
			r.Items = r.Files
		case len(r.List) > 0:
			r.Items = r.List
		case len(r.Data.List) > 0:
			r.Items = r.Data.List
		case len(r.Data.Content) > 0:
			r.Items = r.Data.Content
		case len(r.Data.Objects) > 0:
			r.Items = r.Data.Objects
		case len(r.Data.Items) > 0:
			r.Items = r.Data.Items
		case len(r.Objects) > 0:
			r.Items = r.Objects
		case len(r.Content) > 0:
			r.Items = r.Content
		}
	}
}

type DirResp struct {
	Fid  string `json:"fid"`
	IDV  string `json:"id"`
	Data struct {
		Fid string `json:"fid"`
		ID  string `json:"id"`
	} `json:"data"`
}

func (d DirResp) ID() string {
	if d.Data.Fid != "" {
		return d.Data.Fid
	}
	if d.Data.ID != "" {
		return d.Data.ID
	}
	if d.Fid != "" {
		return d.Fid
	}
	return d.IDV
}

type BaseResp struct {
	Code    int    `json:"code"`
	Errno   int    `json:"errno"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

type DownloadResp struct {
	URL  string `json:"url"`
	Data struct {
		URL         string `json:"url"`
		DownloadURL string `json:"download_url"`
		RawURL      string `json:"raw_url"`
		Dlink       string `json:"dlink"`
	} `json:"data"`
	Info []struct {
		Dlink       string `json:"dlink"`
		DownloadURL string `json:"download_url"`
	} `json:"info"`
}

func (d *DownloadResp) Fill() {
	if d.URL == "" {
		switch {
		case d.Data.URL != "":
			d.URL = d.Data.URL
		case d.Data.DownloadURL != "":
			d.URL = d.Data.DownloadURL
		case d.Data.RawURL != "":
			d.URL = d.Data.RawURL
		case d.Data.Dlink != "":
			d.URL = d.Data.Dlink
		case len(d.Info) > 0 && d.Info[0].Dlink != "":
			d.URL = d.Info[0].Dlink
		case len(d.Info) > 0 && d.Info[0].DownloadURL != "":
			d.URL = d.Info[0].DownloadURL
		}
	}
}
