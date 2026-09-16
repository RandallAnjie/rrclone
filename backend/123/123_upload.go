package _123

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/rclone/rclone/backend/123/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/multipart"
	"github.com/rclone/rclone/lib/rest"
)

type uploadSource struct {
	rs      io.ReadSeeker
	size    int64
	md5sum  string
	sha1sum string
	cleanup func()
}

func (s *uploadSource) Close() {
	if s != nil && s.cleanup != nil {
		s.cleanup()
	}
}

func (f *Fs) prepareUpload(ctx context.Context, in io.Reader, src fs.ObjectInfo) (*uploadSource, error) {
	size := src.Size()
	md5sum, _ := src.Hash(ctx, hash.MD5)
	sha1sum, _ := src.Hash(ctx, hash.SHA1)
	needHash := md5sum == ""
	if size >= 0 && size <= memUploadLimit {
		rw := multipart.NewRW()
		if size > 0 {
			rw.Reserve(size)
		}
		hasher, err := hash.NewMultiHasherTypes(hash.NewHashSet(hash.MD5, hash.SHA1))
		if err != nil {
			_ = rw.Close()
			return nil, err
		}
		n, err := io.Copy(rw, io.TeeReader(in, hasher))
		if err != nil {
			_ = rw.Close()
			return nil, err
		}
		if _, err := rw.Seek(0, io.SeekStart); err != nil {
			_ = rw.Close()
			return nil, err
		}
		sums := hasher.Sums()
		if md5sum == "" {
			md5sum = sums[hash.MD5]
		}
		if sha1sum == "" {
			sha1sum = sums[hash.SHA1]
		}
		return &uploadSource{
			rs:      rw,
			size:    n,
			md5sum:  strings.ToLower(md5sum),
			sha1sum: strings.ToLower(sha1sum),
			cleanup: func() { _ = rw.Close() },
		}, nil
	}

	tmp, err := os.CreateTemp("", "rclone-123-*.upload")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		name := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	var reader io.Reader = in
	var hasher *hash.MultiHasher
	if needHash || sha1sum == "" {
		hasher, err = hash.NewMultiHasherTypes(hash.NewHashSet(hash.MD5, hash.SHA1))
		if err != nil {
			cleanup()
			return nil, err
		}
		reader = io.TeeReader(in, hasher)
	}
	n, err := io.Copy(tmp, reader)
	if err != nil {
		cleanup()
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	if hasher != nil {
		sums := hasher.Sums()
		if md5sum == "" {
			md5sum = sums[hash.MD5]
		}
		if sha1sum == "" {
			sha1sum = sums[hash.SHA1]
		}
	}
	return &uploadSource{
		rs:      tmp,
		size:    n,
		md5sum:  strings.ToLower(md5sum),
		sha1sum: strings.ToLower(sha1sum),
		cleanup: cleanup,
	}, nil
}

func sliceMD5(rs io.ReadSeeker, offset, size int64) (string, error) {
	if _, err := rs.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	h := md5.New()
	if _, err := io.CopyN(h, rs, size); err != nil && err != io.EOF {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (o *Object) uploadSlices(ctx context.Context, src *uploadSource, created *api.UploadCreateResp, leaf string) error {
	if len(created.Data.Servers) == 0 {
		return fmt.Errorf("123: no upload server")
	}
	server := strings.TrimRight(created.Data.Servers[0], "/")
	chunkSize := created.Data.SliceSize
	if chunkSize <= 0 {
		chunkSize = src.size
		if chunkSize <= 0 {
			chunkSize = 1
		}
	}
	total := src.size
	part := int64(1)
	for offset := int64(0); offset < total || (total == 0 && part == 1); {
		size := chunkSize
		if offset+size > total {
			size = total - offset
		}
		if total == 0 {
			size = 0
		}
		sum, err := sliceMD5(src.rs, offset, size)
		if err != nil {
			return err
		}
		if _, err := src.rs.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		var body io.Reader
		if size > 0 {
			body = io.LimitReader(src.rs, size)
		} else {
			body = bytes.NewReader(nil)
		}
		opts := rest.Opts{
			Method:               http.MethodPost,
			RootURL:              server,
			Path:                 "/upload/v2/file/slice",
			MultipartParams:      url.Values{},
			MultipartContentName: "slice",
			MultipartFileName:    fmt.Sprintf("%s.part%d", leaf, part),
			Body:                 body,
			ExtraHeaders:         o.fs.authHeaders(),
		}
		opts.MultipartParams.Set("preuploadID", created.Data.PreuploadID)
		opts.MultipartParams.Set("sliceNo", strconv.FormatInt(part, 10))
		opts.MultipartParams.Set("sliceMD5", sum)
		var info api.BaseResp
		err = o.fs.pacer.Call(func() (bool, error) {
			if err := o.fs.ensureToken(ctx, false); err != nil {
				return false, err
			}
			opts.ExtraHeaders = o.fs.authHeaders()
			if size > 0 {
				if _, err := src.rs.Seek(offset, io.SeekStart); err != nil {
					return false, err
				}
				opts.Body = io.LimitReader(src.rs, size)
			}
			resp, err := o.fs.srv.CallJSON(ctx, &opts, nil, &info)
			if err != nil {
				return shouldRetry(ctx, resp, err)
			}
			if info.GetCode() == api.TooManyRequests {
				return true, info.Err()
			}
			return false, nil
		})
		if err != nil {
			return err
		}
		if err := info.Err(); err != nil {
			return fmt.Errorf("123: slice %d: %w", part, err)
		}
		part++
		offset += size
		if total == 0 {
			break
		}
	}
	for range 5 {
		done, err := o.fs.completeUpload(ctx, created.Data.PreuploadID)
		if err != nil {
			return err
		}
		if done.Data.Completed || done.Data.FileID != 0 {
			if done.Data.FileID != 0 {
				o.id = strconv.FormatInt(done.Data.FileID, 10)
			}
			return nil
		}
	}
	return errors.New("123: upload did not complete")
}

// Update uploads the object
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, _ ...fs.OpenOption) error {
	leaf, directoryID, err := o.fs.dirCache.FindPath(ctx, o.remote, true)
	if err != nil {
		return err
	}
	o.dirID = directoryID
	parentID, err := api.ParseID(directoryID)
	if err != nil {
		return err
	}
	filename := o.fs.opt.Enc.FromStandardName(leaf)
	srcUpload, err := o.fs.prepareUpload(ctx, in, src)
	if err != nil {
		return err
	}
	defer srcUpload.Close()

	if srcUpload.sha1sum != "" {
		if reused, err := o.fs.sha1Reuse(ctx, parentID, filename, srcUpload.sha1sum, srcUpload.size); err == nil && reused.Data.Reuse {
			o.id = strconv.FormatInt(reused.Data.FileID, 10)
			o.size = srcUpload.size
			o.md5sum = srcUpload.md5sum
			o.sha1sum = srcUpload.sha1sum
			o.modTime = src.ModTime(ctx)
			return nil
		}
	}

	created, err := o.fs.createUpload(ctx, parentID, filename, srcUpload.md5sum, srcUpload.size)
	if err != nil {
		return err
	}
	if created.Data.Reuse {
		if created.Data.FileID != 0 {
			o.id = strconv.FormatInt(created.Data.FileID, 10)
		}
		o.size = srcUpload.size
		o.md5sum = srcUpload.md5sum
		o.sha1sum = srcUpload.sha1sum
		o.modTime = src.ModTime(ctx)
		return nil
	}
	if err := o.uploadSlices(ctx, srcUpload, created, filename); err != nil {
		return err
	}
	info, err := o.fs.findInDir(ctx, directoryID, leaf, srcUpload.md5sum)
	if err != nil {
		info, err = o.fs.findInDir(ctx, directoryID, leaf, "")
	}
	if err == nil {
		return o.setMetaData(info)
	}
	o.size = srcUpload.size
	o.md5sum = srcUpload.md5sum
	o.sha1sum = srcUpload.sha1sum
	o.modTime = src.ModTime(ctx)
	return nil
}
