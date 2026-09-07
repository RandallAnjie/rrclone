package crypto

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"sort"
	"strings"
)

// OSSSignV1 returns the OSS V1 Authorization signature (base64 HMAC-SHA1).
func OSSSignV1(method, contentMD5, contentType, date, canonicalizedResource, accessKeySecret string, ossHeaders http.Header) string {
	var b strings.Builder
	b.WriteString(method)
	b.WriteByte('\n')
	b.WriteString(contentMD5)
	b.WriteByte('\n')
	b.WriteString(contentType)
	b.WriteByte('\n')
	b.WriteString(date)
	b.WriteByte('\n')
	b.WriteString(canonicalizeOSSHeaders(ossHeaders))
	b.WriteString(canonicalizedResource)

	mac := hmac.New(sha1.New, []byte(accessKeySecret))
	_, _ = mac.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func canonicalizeOSSHeaders(h http.Header) string {
	keys := make([]string, 0, len(h))
	vals := map[string]string{}
	for k, vs := range h {
		lk := strings.ToLower(k)
		if !strings.HasPrefix(lk, "x-oss-") {
			continue
		}
		keys = append(keys, lk)
		vals[lk] = strings.TrimSpace(strings.Join(vs, ","))
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(vals[k])
		b.WriteByte('\n')
	}
	return b.String()
}

// OSSStringToSign is exported for tests.
func OSSStringToSign(method, contentMD5, contentType, date, canonicalizedResource string, ossHeaders http.Header) string {
	return method + "\n" + contentMD5 + "\n" + contentType + "\n" + date + "\n" + canonicalizeOSSHeaders(ossHeaders) + canonicalizedResource
}
