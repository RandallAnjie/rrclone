package cloudreve

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/cloudreve/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/fstest/fstests"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
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

// putFile uploads data as remote
func putFile(t *testing.T, f *Fs, remote, data string, modTime time.Time) fs.Object {
	t.Helper()
	src := object.NewStaticObjectInfo(remote, modTime, int64(len(data)), true, nil, nil)
	o, err := f.Put(context.Background(), bytes.NewReader([]byte(data)), src)
	require.NoError(t, err)
	return o
}

// TestFakeIntegration runs the generic backend tests against the fake
// server with chunked, relayed, direct and client side encrypted uploads.
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
		{name: "s3-encrypted", policy: api.PolicyS3, encrypt: true},
		{name: "onedrive", policy: api.PolicyOneDrive},
		{name: "upyun", policy: api.PolicyUpyun},
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
					assert.True(t, modTime.Equal(o.ModTime(ctx)), "modtime %v", o.ModTime(ctx))
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
				if encrypt {
					assert.Positive(t, s.encryptedUploads, "uploads were encrypted client side")
				} else {
					assert.Zero(t, s.encryptedUploads)
				}
				s.mu.Unlock()
			})
		}
	}
}

// TestEmptyFiles checks empty files go the same way as any other,
// except to OneDrive which refuses them.
func TestEmptyFiles(t *testing.T) {
	modTime := time.Date(2021, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, policy := range []string{
		api.PolicyLocal, api.PolicyRemote, api.PolicyOneDrive, api.PolicyS3, api.PolicyKS3,
		api.PolicyOSS, api.PolicyCOS, api.PolicyOBS, api.PolicyQiniu, api.PolicyUpyun,
	} {
		t.Run(policy, func(t *testing.T) {
			s := newFakeServer(t)
			s.policyType = policy
			f := newTestFs(t, s, "")
			o := putFile(t, f, "empty.txt", "", modTime)
			assert.True(t, modTime.Equal(o.ModTime(context.Background())))
			s.mu.Lock()
			defer s.mu.Unlock()
			if policy == api.PolicyOneDrive {
				assert.Positive(t, s.serverWrites)
			} else {
				assert.Zero(t, s.serverWrites)
			}
		})
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
	assert.Contains(t, out.State, state2FA)

	_, err = configure(ctx, "test", m, fs.ConfigIn{State: out.State, Result: "000000"})
	assert.Error(t, err, "wrong code")

	out, err = configure(ctx, "test", m, fs.ConfigIn{State: out.State, Result: " 123456 "})
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

// TestInvalidate checks a refused token is dropped but a newer one kept
func TestInvalidate(t *testing.T) {
	ts := &tokenSource{f: &Fs{}, token: &oauth2.Token{
		AccessToken:  "new",
		RefreshToken: "refresh",
		Expiry:       time.Now().Add(time.Hour),
	}}
	assert.True(t, ts.invalidate("old"))
	assert.True(t, ts.token.Valid(), "another request already replaced the refused token")
	assert.True(t, ts.invalidate("new"))
	assert.False(t, ts.token.Valid())

	ts.token = &oauth2.Token{AccessToken: "new"}
	assert.False(t, ts.invalidate("new"), "no way to get another token")
}

func TestModTimeEncoding(t *testing.T) {
	modTime := time.Date(2020, 1, 2, 3, 4, 5, 678, time.UTC)
	value, err := encodeModTime(modTime, "entity1")
	require.NoError(t, err)
	got, ok := decodeModTime(value, "entity1")
	assert.True(t, ok)
	assert.True(t, modTime.Equal(got))

	_, ok = decodeModTime(value, "entity2")
	assert.False(t, ok, "the content changed since")
	for _, bad := range []string{"", "x", `{"i":"AAAA"}`, `{"i":"eCB5IHo="}`} {
		_, ok = decodeModTime(bad, "z")
		assert.False(t, ok, bad)
	}
}

// TestRelocateKeepsModTime checks moves and copies keep the time which
// the server resets.
func TestRelocateKeepsModTime(t *testing.T) {
	ctx := context.Background()
	s := newFakeServer(t)
	f := newTestFs(t, s, "")
	modTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	o := putFile(t, f, "a/file.txt", "hello", modTime)

	moved, err := f.Move(ctx, o, "b/moved.txt")
	require.NoError(t, err)
	assert.True(t, modTime.Equal(moved.ModTime(ctx)))
	copied, err := f.Copy(ctx, moved, "c/copied.txt")
	require.NoError(t, err)
	assert.True(t, modTime.Equal(copied.ModTime(ctx)))
	renamed, err := f.Move(ctx, copied, "c/renamed.txt")
	require.NoError(t, err)
	assert.True(t, modTime.Equal(renamed.ModTime(ctx)))

	s.mu.Lock()
	assert.False(t, modTime.Equal(s.nodes["c/renamed.txt"].modTime), "the server reset updated_at")
	s.mu.Unlock()
	for _, remote := range []string{"b/moved.txt", "c/renamed.txt"} {
		got, err := f.NewObject(ctx, remote)
		require.NoError(t, err)
		assert.True(t, modTime.Equal(got.ModTime(ctx)), remote)
	}
	entries, err := f.List(ctx, "c")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.True(t, modTime.Equal(entries[0].ModTime(ctx)))
}

// TestCopyOverExisting checks a copy which can't replace its destination
// leaves everything as it was, so rclone can upload a new version instead.
func TestCopyOverExisting(t *testing.T) {
	ctx := context.Background()
	s := newFakeServer(t)
	f := newTestFs(t, s, "")
	modTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	o := putFile(t, f, "a/file.txt", "source", modTime)
	putFile(t, f, "b/file.txt", "same name", modTime)
	putFile(t, f, "b/other.txt", "other name", modTime)

	for _, remote := range []string{"b/file.txt", "b/other.txt"} {
		_, err := f.Copy(ctx, o, remote)
		assert.ErrorIs(t, err, fs.ErrorCantCopy, remote)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Equal(t, "same name", string(s.nodes["b/file.txt"].data))
	assert.Equal(t, "other name", string(s.nodes["b/other.txt"].data))
	assert.Equal(t, []string{"b/file.txt", "b/other.txt"}, s.children("b"))
}

// TestSetModTime checks the recorded time and when it stops applying
func TestSetModTime(t *testing.T) {
	ctx := context.Background()
	s := newFakeServer(t)
	f := newTestFs(t, s, "")
	uploaded := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	o := putFile(t, f, "file.txt", "hello", uploaded)

	modTime := uploaded.Add(time.Hour)
	require.NoError(t, o.SetModTime(ctx, modTime))
	assert.True(t, modTime.Equal(o.ModTime(ctx)))
	got, err := f.NewObject(ctx, "file.txt")
	require.NoError(t, err)
	assert.True(t, modTime.Equal(got.ModTime(ctx)))

	// An upload by rclone replaces the recorded time
	newer := uploaded.Add(2 * time.Hour)
	src := object.NewStaticObjectInfo("file.txt", newer, 3, true, nil, nil)
	require.NoError(t, got.Update(ctx, bytes.NewReader([]byte("new")), src))
	assert.True(t, newer.Equal(got.ModTime(ctx)))

	// So does a new version from any other client
	require.NoError(t, got.SetModTime(ctx, modTime))
	s.mu.Lock()
	s.nodes["file.txt"].entity = "from-the-web"
	s.nodes["file.txt"].modTime = newer
	s.mu.Unlock()
	got, err = f.NewObject(ctx, "file.txt")
	require.NoError(t, err)
	assert.True(t, newer.Equal(got.ModTime(ctx)))
}

// TestUnfinishedUpload checks files still being uploaded stay hidden
// and that an interrupted upload doesn't block the next one.
func TestUnfinishedUpload(t *testing.T) {
	ctx := context.Background()
	s := newFakeServer(t)
	f := newTestFs(t, s, "")
	f.pacer.SetRetries(1) // don't wait for the lock
	modTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	_, err := f.createSession(ctx, "new.txt", 5, modTime, false)
	require.NoError(t, err)
	entries, err := f.List(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, entries)
	_, err = f.NewObject(ctx, "new.txt")
	assert.ErrorIs(t, err, fs.ErrorObjectNotFound)

	o := putFile(t, f, "new.txt", "hello", modTime)
	assert.Equal(t, int64(5), o.Size())

	// A file being replaced is still there with its old content
	_, err = f.createSession(ctx, "new.txt", 3, modTime, true)
	require.NoError(t, err)
	got, err := f.NewObject(ctx, "new.txt")
	require.NoError(t, err)
	assert.Equal(t, int64(5), got.Size())
	src := object.NewStaticObjectInfo("new.txt", modTime, 3, true, nil, nil)
	require.NoError(t, got.Update(ctx, bytes.NewReader([]byte("new")), src))

	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Empty(t, s.sessions)
	assert.Equal(t, "new", string(s.nodes["new.txt"].data))
}

// TestListing checks both kinds of pagination and pages the server
// makes smaller than asked for.
func TestListing(t *testing.T) {
	ctx := context.Background()
	for _, offsetPaging := range []bool{false, true} {
		t.Run(fmt.Sprintf("offset=%v", offsetPaging), func(t *testing.T) {
			s := newFakeServer(t)
			s.offsetPaging = offsetPaging
			f := newTestFs(t, s, "")
			s.mu.Lock()
			s.nodes["dir"] = &fakeNode{id: s.newID(), isDir: true, modTime: time.Now()}
			for i := range 25 {
				n := &fakeNode{id: s.newID(), modTime: time.Now(), entity: emptyEntity}
				if i%5 == 0 {
					n.setMeta(api.MetaUploadSessionID, "session")
				}
				s.nodes[fmt.Sprintf("dir/%02d", i)] = n
			}
			s.mu.Unlock()
			entries, err := f.List(ctx, "dir")
			require.NoError(t, err)
			assert.Len(t, entries, 20)
		})
	}
}

func TestListFile(t *testing.T) {
	ctx := context.Background()
	s := newFakeServer(t)
	f := newTestFs(t, s, "")
	putFile(t, f, "file.txt", "hello", time.Now())
	_, err := f.List(ctx, "file.txt")
	assert.ErrorIs(t, err, fs.ErrorDirNotFound)
}

// TestBatchErrors checks the reason for a failed batch reaches rclone
func TestBatchErrors(t *testing.T) {
	r := api.Response{
		Code: api.CodeBatchIncomplete,
		Msg:  "Not all operations are successful",
		AggregatedError: map[string]api.Response{
			"cloudreve://my/a": {Code: api.CodeObjectExist, Msg: "Object existed"},
		},
	}
	assert.Equal(t, api.CodeObjectExist, apiCode(r.Err()))
	r.AggregatedError = nil
	assert.Equal(t, api.CodeBatchIncomplete, apiCode(r.Err()))

	ctx := context.Background()
	s := newFakeServer(t)
	f := newTestFs(t, s, "")
	o := putFile(t, f, "file.txt", "hello", time.Now())
	require.NoError(t, o.Remove(ctx))
	assert.ErrorIs(t, o.Remove(ctx), fs.ErrorObjectNotFound)
	assert.ErrorIs(t, f.Purge(ctx, "missing"), fs.ErrorDirNotFound)
}

// TestRetries checks which errors are retried
func TestRetries(t *testing.T) {
	ctx := context.Background()
	s := newFakeServer(t)
	f := newTestFs(t, s, "")

	s.mu.Lock()
	s.failCodes = []int{api.CodeDBError, api.CodeLockConflict}
	s.mu.Unlock()
	_, err := f.List(ctx, "")
	require.NoError(t, err)

	put := func(code int) error {
		s.mu.Lock()
		s.failCodes = []int{code}
		s.mu.Unlock()
		src := object.NewStaticObjectInfo("file.txt", time.Now(), 1, true, nil, nil)
		_, err := f.Put(ctx, bytes.NewReader([]byte("x")), src)
		return err
	}
	assert.True(t, fserrors.IsFatalError(put(api.CodeInsufficientCapacity)), "out of space")
	for _, code := range []int{api.CodeFileTooLarge, api.CodeIllegalObjectName, api.CodePolicyNotAllowed} {
		assert.True(t, fserrors.IsNoRetryError(put(code)), "code %d", code)
	}
}

// TestOSSCallbackFailure checks OSS failing to tell Cloudreve about an
// upload isn't taken for success.
func TestOSSCallbackFailure(t *testing.T) {
	s := newFakeServer(t)
	s.policyType, s.ossCallbackFail = api.PolicyOSS, true
	f := newTestFs(t, s, "")
	src := object.NewStaticObjectInfo("file.txt", time.Now(), 5, true, nil, nil)
	_, err := f.Put(context.Background(), bytes.NewReader([]byte("hello")), src)
	require.Error(t, err)
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Empty(t, s.sessions, "the session was cancelled")
	assert.NotContains(t, s.nodes, "file.txt")
}

// TestOneDriveLostReply checks a chunk OneDrive partly stored before
// the connection failed is resumed from where it got to.
func TestOneDriveLostReply(t *testing.T) {
	s := newFakeServer(t)
	s.policyType, s.odLoseReply = api.PolicyOneDrive, true
	f := newTestFs(t, s, "")
	data := make([]byte, 3*s.chunkSize+17)
	_, _ = rand.Read(data)
	putFile(t, f, "file.bin", string(data), time.Now())
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.False(t, s.odLoseReply, "the failure happened")
	assert.True(t, bytes.Equal(data, s.nodes["file.bin"].data))
}

func TestPolicyCommand(t *testing.T) {
	ctx := context.Background()
	s := newFakeServer(t)
	f := newTestFs(t, s, "")
	out, err := f.Command(ctx, "policy", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "p1", out.(*api.StoragePolicy).ID)
	_, err = f.Command(ctx, "nope", nil, nil)
	assert.True(t, errors.Is(err, fs.ErrorCommandNotFound))
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
