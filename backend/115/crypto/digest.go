// Package crypto implements the 115 web/app request digest helpers.
package crypto

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"io"
	"strings"
)

const hashPreSize int64 = 128 * 1024

// DigestResult holds hashes used by 115 rapid upload and OSS PUT.
type DigestResult struct {
	Size    int64
	PreID   string
	QuickID string
	MD5     string
}

// Digest hashes r, filling result with the first-128KiB SHA1 (PreID),
// full-file SHA1 (QuickID) and base64 MD5 used as OSS Content-MD5.
func Digest(r io.Reader, result *DigestResult) error {
	hs, hm := sha1.New(), md5.New()
	w := io.MultiWriter(hs, hm)
	n, err := io.CopyN(w, r, hashPreSize)
	if err != nil && err != io.EOF {
		return err
	}
	result.Size = n
	result.PreID = strings.ToUpper(hex.EncodeToString(hs.Sum(nil)))
	if err == nil {
		n, err = io.Copy(w, r)
		if err != nil {
			return err
		}
		result.Size += n
		result.QuickID = strings.ToUpper(hex.EncodeToString(hs.Sum(nil)))
	} else {
		result.QuickID = result.PreID
	}
	result.MD5 = base64.StdEncoding.EncodeToString(hm.Sum(nil))
	return nil
}
