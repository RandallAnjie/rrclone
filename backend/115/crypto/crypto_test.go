package crypto

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func TestDigestSmall(t *testing.T) {
	in := bytes.NewReader([]byte("hello"))
	var dr DigestResult
	if err := Digest(in, &dr); err != nil {
		t.Fatal(err)
	}
	if dr.Size != 5 {
		t.Fatalf("size=%d", dr.Size)
	}
	sum := sha1.Sum([]byte("hello"))
	want := strings.ToUpper(hex.EncodeToString(sum[:]))
	if dr.PreID != want || dr.QuickID != want {
		t.Fatalf("pre=%s quick=%s want=%s", dr.PreID, dr.QuickID, want)
	}
	if dr.MD5 == "" {
		t.Fatal("missing md5")
	}
}

func TestDigestOverPreSize(t *testing.T) {
	buf := bytes.Repeat([]byte("a"), int(hashPreSize)+100)
	var dr DigestResult
	if err := Digest(bytes.NewReader(buf), &dr); err != nil {
		t.Fatal(err)
	}
	if dr.Size != int64(len(buf)) {
		t.Fatalf("size=%d", dr.Size)
	}
	if dr.PreID == dr.QuickID {
		t.Fatal("pre-id should differ from full sha1 for files > 128KiB")
	}
}

func TestUploadSignatureStable(t *testing.T) {
	got := UploadSignature(123, "userkey", "U_1_0", "ABCDEF")
	if got != strings.ToUpper(got) || len(got) != 40 {
		t.Fatalf("unexpected sig %q", got)
	}
	again := UploadSignature(123, "userkey", "U_1_0", "ABCDEF")
	if got != again {
		t.Fatal("signature should be deterministic")
	}
}

func TestUploadTokenLength(t *testing.T) {
	got := UploadToken(1, "FID", "PRE", "1700000000000", "12", "", "", "35.4.0")
	if len(got) != 32 {
		t.Fatalf("token len=%d token=%s", len(got), got)
	}
}

func TestEncodeTokenBase64(t *testing.T) {
	c, err := NewEcdhCipher()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := c.EncodeToken(1700000000000)
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
}

func TestEncryptRoundTripBlockSize(t *testing.T) {
	c, err := NewEcdhCipher()
	if err != nil {
		t.Fatal(err)
	}
	ct, err := c.Encrypt([]byte("filesize=1&filename=a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ct)%16 != 0 {
		t.Fatalf("ciphertext not block aligned: %d", len(ct))
	}
}

func TestOSSStringToSign(t *testing.T) {
	h := make(http.Header)
	h.Set("x-oss-magic", "abracadabra")
	h.Set("x-oss-meta-author", "foo@bar.com")
	got := OSSStringToSign("PUT", "eB5eJF1ptWaXm4bijSPyxw==", "text/html", "Thu, 17 Nov 2005 18:49:58 GMT", "/oss-example/nelson", h)
	want := "PUT\neB5eJF1ptWaXm4bijSPyxw==\ntext/html\nThu, 17 Nov 2005 18:49:58 GMT\nx-oss-magic:abracadabra\nx-oss-meta-author:foo@bar.com\n/oss-example/nelson"
	if got != want {
		t.Fatalf("stringToSign mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestOSSSignV1NonEmpty(t *testing.T) {
	sig := OSSSignV1("PUT", "", "application/octet-stream", "Thu, 17 Nov 2005 18:49:58 GMT", "/bucket/obj", "secret", http.Header{"X-Oss-Security-Token": []string{"tok"}})
	if sig == "" {
		t.Fatal("empty signature")
	}
}
