package baidu

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/rclone/rclone/backend/baidu/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/rest"
)

const (
	defaultBlockSize int64 = 4 << 20
)

func md5Of(p []byte) string {
	sum := md5.Sum(p)
	return hex.EncodeToString(sum[:])
}

func hashBlocks(r io.Reader, size, chunk int64) ([]string, error) {
	if chunk <= 0 {
		chunk = defaultBlockSize
	}
	if size == 0 {
		return []string{md5Of(nil)}, nil
	}
	buf := make([]byte, chunk)
	var md5s []string
	var read int64
	for size < 0 || read < size {
		n := len(buf)
		if size >= 0 && size-read < int64(n) {
			n = int(size - read)
		}
		got, err := io.ReadFull(r, buf[:n])
		if got > 0 {
			md5s = append(md5s, md5Of(buf[:got]))
			read += int64(got)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if len(md5s) == 0 {
		md5s = []string{md5Of(nil)}
	}
	return md5s, nil
}

func (f *Fs) putUnchecked(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	size := src.Size()
	remote := src.Remote()
	leaf, dirID, err := f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return nil, err
	}
	name := f.opt.Enc.FromStandardName(leaf)
	pan := panJoin(dirID, name)

	var (
		md5s    []string
		payload io.Reader = in
		cleanup func()
	)
	if size < 0 {
		tmp, err := os.CreateTemp("", "rclone-baidu-*.tmp")
		if err != nil {
			return nil, err
		}
		cleanup = func() {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
		n, err := io.Copy(tmp, in)
		if err != nil {
			cleanup()
			return nil, err
		}
		size = n
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			cleanup()
			return nil, err
		}
		payload = tmp
	}
	if cleanup != nil {
		defer cleanup()
	}

	if rs, ok := payload.(io.ReadSeeker); ok {
		md5s, err = hashBlocks(rs, size, defaultBlockSize)
		if err != nil {
			return nil, err
		}
		if _, err := rs.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		payload = rs
	} else {
		var buf bytes.Buffer
		if size > 64<<20 {
			return nil, fmt.Errorf("baidu: upload of unknown-seek %d byte object needs a seekable reader", size)
		}
		n, err := io.Copy(&buf, payload)
		if err != nil {
			return nil, err
		}
		if size < 0 {
			size = n
		}
		md5s, err = hashBlocks(bytes.NewReader(buf.Bytes()), size, defaultBlockSize)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(buf.Bytes())
	}

	blockJSON, err := json.Marshal(md5s)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"path":        {pan},
		"size":        {strconv.FormatInt(size, 10)},
		"isdir":       {"0"},
		"autoinit":    {"1"},
		"rtype":       {"2"},
		"block_list":  {string(blockJSON)},
		"local_mtime": {strconv.FormatInt(src.ModTime(ctx).Unix(), 10)},
	}
	if srcHash, herr := src.Hash(ctx, hash.MD5); herr == nil && isHexMD5(srcHash) {
		form.Set("content-md5", strings.ToLower(srcHash))
	}

	var pre api.PrecreateResponse
	if err := f.postMethod(ctx, "precreate", nil, form, &pre); err != nil {
		return nil, err
	}
	if err := pre.Err(); err != nil {
		return nil, err
	}

	// return_type 2 means the file already exists (rapid upload / seconds pass).
	if pre.ReturnType != 2 && pre.UploadID != "" {
		if err := f.uploadParts(ctx, payload, pan, pre.UploadID, md5s, size); err != nil {
			return nil, err
		}
		create := url.Values{
			"path":        {pan},
			"size":        {strconv.FormatInt(size, 10)},
			"isdir":       {"0"},
			"rtype":       {"2"},
			"uploadid":    {pre.UploadID},
			"block_list":  {string(blockJSON)},
			"local_mtime": {strconv.FormatInt(src.ModTime(ctx).Unix(), 10)},
		}
		var created api.CreateResponse
		if err := f.postMethod(ctx, "create", nil, create, &created); err != nil {
			return nil, err
		}
		if err := created.Err(); err != nil {
			return nil, err
		}
	}

	return f.NewObject(ctx, remote)
}

func (f *Fs) uploadParts(ctx context.Context, in io.Reader, panPath, uploadID string, md5s []string, size int64) error {
	buf := make([]byte, defaultBlockSize)
	var read int64
	for i := range md5s {
		n := len(buf)
		if size-read < int64(n) {
			n = int(size - read)
		}
		if n <= 0 {
			break
		}
		got, err := io.ReadFull(in, buf[:n])
		if got == 0 && err != nil {
			return err
		}
		params := f.withAuth(url.Values{
			"method":   {"upload"},
			"type":     {"tmpfile"},
			"path":     {panPath},
			"uploadid": {uploadID},
			"partseq":  {strconv.Itoa(i)},
		})
		chunk := buf[:got]
		opts := rest.Opts{
			Method:      http.MethodPost,
			RootURL:     pcsRootURL,
			Path:        "/rest/2.0/pcs/superfile2",
			Parameters:  params,
			ContentType: "application/octet-stream",
			ExtraHeaders: map[string]string{
				"User-Agent": f.opt.DownloadUserAgent,
			},
		}
		var out api.SuperfileResponse
		err = f.pacer.Call(func() (bool, error) {
			out = api.SuperfileResponse{}
			opts.Body = bytes.NewReader(chunk)
			resp, err := f.pcs.CallJSON(ctx, &opts, nil, &out)
			if err == nil {
				err = out.Err()
			}
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return fmt.Errorf("baidu upload part %d: %w", i, err)
		}
		read += int64(got)
	}
	return nil
}

// Open the file for read
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	if o.id == 0 {
		obj, err := o.fs.NewObject(ctx, o.remote)
		if err != nil {
			return nil, err
		}
		*o = *obj.(*Object)
	}
	item, err := o.fs.fileMetas(ctx, o.id, o.path)
	if err != nil {
		return nil, err
	}
	dlink := item.Dlink
	if dlink == "" {
		return nil, fmt.Errorf("baidu: no download link for %s", o.remote)
	}
	if token := o.fs.accessToken(); token != "" && !strings.Contains(dlink, "access_token=") {
		sep := "?"
		if strings.Contains(dlink, "?") {
			sep = "&"
		}
		dlink += sep + "access_token=" + url.QueryEscape(token)
	}
	fs.FixRangeOption(options, o.size)
	opts := rest.Opts{
		Method:  http.MethodGet,
		RootURL: dlink,
		Options: options,
		ExtraHeaders: map[string]string{
			"User-Agent": o.fs.opt.DownloadUserAgent,
			"Referer":    "https://pan.baidu.com/disk/home",
		},
	}
	var resp *http.Response
	err = o.fs.pacer.Call(func() (bool, error) {
		resp, err = o.fs.srv.Call(ctx, &opts)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Update the object with the contents of the io.Reader
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	newObj, err := o.fs.putUnchecked(ctx, in, src, options...)
	if err != nil {
		return err
	}
	*o = *newObj.(*Object)
	return nil
}
