package baidu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rclone/rclone/backend/baidu/api"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCookies(t *testing.T) {
	bduss, stoken := parseCookies("BDUSS=aaa; STOKEN=bbb; BAIDUID=zzz", "", "")
	assert.Equal(t, "aaa", bduss)
	assert.Equal(t, "bbb", stoken)

	bduss, stoken = parseCookies("bduss=xxx; stoken=yyy", "fallback", "")
	assert.Equal(t, "xxx", bduss)
	assert.Equal(t, "yyy", stoken)

	bduss, stoken = parseCookies("", "fromfield", "stfromfield")
	assert.Equal(t, "fromfield", bduss)
	assert.Equal(t, "stfromfield", stoken)

	bduss, stoken = parseCookies("BDUSS_BFESS=cookiebfess; STOKEN_BFESS=stbfess", "", "")
	assert.Equal(t, "cookiebfess", bduss)
	assert.Equal(t, "stbfess", stoken)
}

func TestPanJoin(t *testing.T) {
	assert.Equal(t, "/foo", panJoin("/", "foo"))
	assert.Equal(t, "/a/b", panJoin("/a", "b"))
	assert.Equal(t, "/a/b", panJoin("/a/", "b"))
	assert.Equal(t, "/leaf", panJoin("", "leaf"))
}

func TestIsHexMD5(t *testing.T) {
	assert.True(t, isHexMD5("d41d8cd98f00b204e9800998ecf8427e"))
	assert.False(t, isHexMD5("not-an-md5"))
	assert.False(t, isHexMD5(""))
	assert.False(t, isHexMD5("zz1d8cd98f00b204e9800998ecf8427e"))
}

func TestAPIErrorHelpers(t *testing.T) {
	assert.True(t, api.IsNotFound(-9))
	assert.True(t, api.IsNotFound(31066))
	assert.False(t, api.IsNotFound(0))
	assert.True(t, api.IsLogin(-6))
	assert.True(t, api.IsRetry(31034))
	err := api.Base{Errno: 31066, ErrMsg: "not found"}.Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "31066")
	assert.NoError(t, api.Base{Errno: 0}.Err())
	assert.Error(t, api.SuperfileResponse{ErrorCode: 31326, ErrorMsg: "busy"}.Err())
	assert.NoError(t, api.SuperfileResponse{}.Err())
}

func TestItemName(t *testing.T) {
	assert.Equal(t, "bar", api.Item{ServerFilename: "bar"}.Name())
	assert.Equal(t, "baz", api.Item{Path: "/foo/baz"}.Name())
	assert.Equal(t, "only", api.Item{FileName: "only"}.Name())
}

func TestHashBlocksEmpty(t *testing.T) {
	md5s, err := hashBlocks(strings.NewReader(""), 0, defaultBlockSize)
	require.NoError(t, err)
	require.Len(t, md5s, 1)
	assert.Equal(t, "d41d8cd98f00b204e9800998ecf8427e", md5s[0])
}

func TestNewFsRequiresAuth(t *testing.T) {
	_, err := NewFs(t.Context(), "pan", "", configmap.Simple{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cookie")
}

func TestWritePath(t *testing.T) {
	cookie := &Fs{}
	assert.Equal(t, "/api/precreate", cookie.writePath("precreate"))
	assert.Equal(t, "/api/create", cookie.writePath("create"))
	assert.Equal(t, "/api/filemanager", cookie.writePath("filemanager"))

	open := &Fs{opt: Options{AccessToken: "tok"}}
	assert.True(t, open.useOpenAPI())
	assert.Equal(t, "/rest/2.0/xpan/file", open.writePath("precreate"))
	assert.Equal(t, "/rest/2.0/xpan/file", open.writePath("filemanager"))
}

func TestBdstokenParse(t *testing.T) {
	var out api.TemplateVariableResponse
	require.NoError(t, json.Unmarshal([]byte(`{"errno":0,"result":{"bdstoken":"abc"}}`), &out))
	assert.Equal(t, "abc", out.Bdstoken())

	require.NoError(t, json.Unmarshal([]byte(`{"errno":0,"result":{"bdstoken":["xyz"]}}`), &out))
	assert.Equal(t, "xyz", out.Bdstoken())
}

func TestHashBlocksChunks(t *testing.T) {
	data := []byte("abcdefgh")
	md5s, err := hashBlocks(strings.NewReader(string(data)), int64(len(data)), 3)
	require.NoError(t, err)
	require.Len(t, md5s, 3)
	assert.Equal(t, md5Of([]byte("abc")), md5s[0])
	assert.Equal(t, md5Of([]byte("def")), md5s[1])
	assert.Equal(t, md5Of([]byte("gh")), md5s[2])
}

func TestHashBlocksKnown(t *testing.T) {
	data := []byte("hello baidu")
	md5s, err := hashBlocks(strings.NewReader(string(data)), int64(len(data)), defaultBlockSize)
	require.NoError(t, err)
	require.Len(t, md5s, 1)
	assert.Equal(t, md5Of(data), md5s[0])
}
