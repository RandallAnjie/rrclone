package cloudreve

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/cloudreve/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/fstest/fstests"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *fakeServer) remote() string {
	return fmt.Sprintf(":cloudreve,url='%s',email='%s',password='%s':", s.srv.URL, s.email, obscure.MustObscure(s.password))
}

func newTestFs(t *testing.T, s *fakeServer, root string) *Fs {
	t.Helper()
	// configmap.Simple has no defaults, so set the ones that matter
	m := configmap.Simple{"url": s.srv.URL, "email": s.email, "password": obscure.MustObscure(s.password), "client_encryption": "true"}
	f, err := NewFs(context.Background(), "test", root, m)
	require.NoError(t, err)
	return f.(*Fs)
}

// TestFakeIntegration runs the generic backend tests against the fake
// server with chunked, relayed and client side encrypted uploads.
//
// Direct uploads to object storage are covered by TestUploadPolicies
// instead: Cloudreve gives no part URLs for an empty file there, so
// empty files are created through the server with the current time,
// which the generic tests reject.
func TestFakeIntegration(t *testing.T) {
	for _, test := range []struct {
		name    string
		policy  string
		relay   bool
		encrypt bool
	}{
		{name: "local", policy: api.PolicyLocal},
		{name: "relay", policy: api.PolicyS3, relay: true},
		{name: "local-encrypted", policy: api.PolicyLocal, encrypt: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newFakeServer(t)
			s.policyType, s.relay, s.encrypt = test.policy, test.relay, test.encrypt
			fstests.Run(t, &fstests.Opt{
				RemoteName:      s.remote(),
				NilObject:       (*Object)(nil),
				SkipInvalidUTF8: true,
			})
		})
	}
}

// TestUploadPolicies round trips files through every direct upload type
func TestUploadPolicies(t *testing.T) {
	ctx := context.Background()
	for _, policy := range []string{
		api.PolicyLocal, api.PolicyRemote, api.PolicyOneDrive, api.PolicyS3, api.PolicyKS3,
		api.PolicyOSS, api.PolicyCOS, api.PolicyOBS, api.PolicyQiniu, api.PolicyUpyun,
	} {
		for _, encrypt := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/encrypt=%v", policy, encrypt), func(t *testing.T) {
				s := newFakeServer(t)
				s.policyType, s.encrypt = policy, encrypt
				f := newTestFs(t, s, "dir")
				for _, size := range []int{0, 1, int(s.chunkSize) - 1, int(s.chunkSize), 3*int(s.chunkSize) + 17} {
					data := make([]byte, size)
					_, _ = rand.Read(data)
					remote := fmt.Sprintf("sub/file-%d.bin", size)
					modTime := time.Date(2021, 2, 3, 4, 5, 6, 0, time.UTC)
					src := object.NewStaticObjectInfo(remote, modTime, int64(size), true, nil, nil)
					o, err := f.Put(ctx, bytes.NewReader(data), src)
					require.NoError(t, err, "size %d", size)
					assert.Equal(t, int64(size), o.Size())
					if size > 0 && policy != api.PolicyUpyun {
						assert.True(t, modTime.Equal(o.ModTime(ctx)), "modtime %v", o.ModTime(ctx))
					}
					s.mu.Lock()
					stored := s.nodes["dir/"+remote].data
					s.mu.Unlock()
					assert.True(t, bytes.Equal(data, stored), "server holds the plaintext")

					rc, err := o.Open(ctx)
					require.NoError(t, err)
					got, err := io.ReadAll(rc)
					require.NoError(t, rc.Close())
					require.NoError(t, err)
					assert.True(t, bytes.Equal(data, got), "downloaded data matches")

					// Overwrite with new content as a new version
					data2 := append(bytes.Clone(data), 'x')
					src2 := object.NewStaticObjectInfo(remote, modTime.Add(time.Hour), int64(len(data2)), true, nil, nil)
					require.NoError(t, o.Update(ctx, bytes.NewReader(data2), src2))
					s.mu.Lock()
					stored = s.nodes["dir/"+remote].data
					s.mu.Unlock()
					assert.Equal(t, data2, stored)
				}
				s.mu.Lock()
				assert.Empty(t, s.sessions, "no leftover upload sessions")
				if encrypt && policy != api.PolicyUpyun {
					assert.Positive(t, s.encryptedUploads, "uploads were encrypted client side")
				} else {
					assert.Zero(t, s.encryptedUploads)
				}
				s.mu.Unlock()
			})
		}
	}
}

// TestEncryptionMatchesServerOffsets checks client encryption against
// the server's counter arithmetic for decrypting from any offset.
func TestEncryptionMatchesServerOffsets(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	iv := bytes.Repeat([]byte{0xff}, 16) // overflows on the first block
	m := &api.EncryptMetadata{Algorithm: api.CipherAES256CTR, KeyPlainText: key, IV: iv}
	plain := make([]byte, 1000)
	_, _ = rand.Read(plain)
	r, err := encryptReader(bytes.NewReader(plain), m)
	require.NoError(t, err)
	enc, err := io.ReadAll(r)
	require.NoError(t, err)

	// serverDecryptAt mirrors Cloudreve's AES256CTR.initCipherStream
	serverDecryptAt := func(offset int) []byte {
		block, _ := aes.NewCipher(key)
		counter := bytes.Clone(iv)
		blocks := int64(offset / 16)
		for i := 15; i >= 0 && blocks > 0; i-- {
			sum := uint64(counter[i]) + uint64(blocks&0xff)
			counter[i] = byte(sum)
			blocks >>= 8
			carry := sum >> 8
			for j := i - 1; j >= 0 && carry > 0; j-- {
				sum = uint64(counter[j]) + carry
				counter[j] = byte(sum)
				carry = sum >> 8
			}
		}
		stream := cipher.NewCTR(block, counter)
		skip := make([]byte, offset%16)
		stream.XORKeyStream(skip, skip)
		out := make([]byte, len(enc)-offset)
		stream.XORKeyStream(out, enc[offset:])
		return out
	}
	for _, offset := range []int{0, 1, 15, 16, 17, 300, 999} {
		assert.Equal(t, plain[offset:], serverDecryptAt(offset), "offset %d", offset)
	}

	_, err = encryptReader(bytes.NewReader(nil), &api.EncryptMetadata{Algorithm: "rot13"})
	assert.Error(t, err)
}

// TestTokenRefresh checks expired and revoked access tokens are refreshed
func TestTokenRefresh(t *testing.T) {
	ctx := context.Background()
	s := newFakeServer(t)
	f := newTestFs(t, s, "")
	require.NoError(t, f.Mkdir(ctx, "a"))

	// Revoke the access token behind rclone's back
	s.mu.Lock()
	s.access = "revoked"
	s.mu.Unlock()
	_, err := f.List(ctx, "")
	require.NoError(t, err)
	s.mu.Lock()
	assert.Equal(t, 1, s.refreshes, "refreshed after a 401")
	assert.Equal(t, 1, s.logins, "no new password login")

	// Lose the refresh token too: falls back to the password
	s.access, s.refresh = "revoked", "lost"
	s.mu.Unlock()
	_, err = f.List(ctx, "")
	require.NoError(t, err)
	s.mu.Lock()
	assert.Equal(t, 2, s.logins)
	s.mu.Unlock()
}

// TestConfig2FA runs the config state machine for a 2FA account
func TestConfig2FA(t *testing.T) {
	ctx := context.Background()
	s := newFakeServer(t)
	s.totp = "123456"
	m := configmap.Simple{"url": s.srv.URL, "email": s.email, "password": obscure.MustObscure(s.password)}

	out, err := configure(ctx, "test", m, fs.ConfigIn{})
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "2fa", out.State)

	_, err = configure(ctx, "test", m, fs.ConfigIn{State: "2fa", Result: "000000"})
	assert.Error(t, err, "wrong code")

	// A fresh login gives a fresh 2FA session
	_, err = configure(ctx, "test", m, fs.ConfigIn{})
	require.NoError(t, err)
	out, err = configure(ctx, "test", m, fs.ConfigIn{State: "2fa", Result: " 123456 "})
	require.NoError(t, err)
	assert.Nil(t, out)
	assert.Contains(t, m["token"], "access_token")

	// The saved token works without the password
	delete(m, "password")
	_, err = NewFs(ctx, "test", "", m)
	require.NoError(t, err)

	// Runtime logins can't do 2FA so they explain what to do
	m2 := configmap.Simple{"url": s.srv.URL, "email": s.email, "password": obscure.MustObscure(s.password)}
	_, err = NewFs(ctx, "test2", "", m2)
	assert.ErrorContains(t, err, "2FA")
}

func TestURI(t *testing.T) {
	f := &Fs{root: "my dir", opt: Options{Enc: encoder.Base | encoder.EncodeWin | encoder.EncodeBackSlash | encoder.EncodeInvalidUtf8}}
	assert.Equal(t, "cloudreve://my/my%20dir", f.uri(""))
	assert.Equal(t, "cloudreve://my/my%20dir/a%23b&c/50%25", f.uri("a#b&c/50%"), "URL syntax escaped")
	assert.Equal(t, "cloudreve://my/my%20dir/a%EF%BC%9Ab", f.uri("a:b"), "illegal characters encoded")
	f.root = ""
	assert.Equal(t, "cloudreve://my", f.uri(""))

	f.opt.ShareID, f.opt.SharePassword = "AbCd", "pw"
	assert.Equal(t, "cloudreve://AbCd:pw@share/x", f.uri("x"))
}

func TestSiteRoot(t *testing.T) {
	got, err := siteRoot("cloud.example.com/")
	require.NoError(t, err)
	assert.Equal(t, "https://cloud.example.com", got)
	_, err = siteRoot("")
	assert.Error(t, err)
	_, err = siteRoot("ftp://x")
	assert.Error(t, err)
}

func TestWithQuery(t *testing.T) {
	got, err := withQuery("http://slave/api/v4/slave/upload/s1", "chunk", "3")
	require.NoError(t, err)
	assert.Equal(t, "http://slave/api/v4/slave/upload/s1?chunk=3", got)
	got, err = withQuery("http://slave/u?sign=a%2Bb", "chunk", "0")
	require.NoError(t, err)
	assert.Equal(t, "http://slave/u?sign=a%2Bb&chunk=0", got, "signed query left alone")
}
