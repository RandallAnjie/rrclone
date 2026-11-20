package authorize

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/lib/accountrotation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A backend whose config "authorizes" at once, recording the client
func init() {
	fs.Register(&fs.RegInfo{
		Name: "authorizetestbackend",
		NewFs: func(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
			return nil, fs.ErrorNotImplemented
		},
		Config: func(ctx context.Context, name string, m configmap.Mapper, in fs.ConfigIn) (*fs.ConfigOut, error) {
			id, _ := m.Get("client_id")
			m.Set("token", `{"access_token":"tok-for-`+id+`","refresh_token":"r","expiry":"2030-01-01T00:00:00Z"}`)
			return nil, nil
		},
		Options: []fs.Option{{Name: "client_id"}, {Name: "client_secret"}, {Name: "token"}},
	})
}

func TestAuthorizeAccountFile(t *testing.T) {
	ctx := context.Background()
	accountFile = filepath.Join(t.TempDir(), "accounts", "app1.json")
	t.Cleanup(func() { accountFile = "" })

	require.NoError(t, authorizeAccountFile(ctx, []string{"authorizetestbackend", "APPKEY", "APPSECRET"}))
	clientID, clientSecret, token, structured, err := accountrotation.ReadFile(accountFile)
	require.NoError(t, err)
	assert.True(t, structured)
	assert.Equal(t, "APPKEY", clientID)
	assert.Equal(t, "APPSECRET", clientSecret)
	assert.Contains(t, token, `"access_token":"tok-for-APPKEY"`)

	// The rotation reads it like any account file
	m := &accountrotation.TokenFileMapper{Base: configmap.Simple{}, File: accountFile}
	v, ok := m.Get("client_id")
	assert.True(t, ok)
	assert.Equal(t, "APPKEY", v)

	// The backend's own app: no credentials in the file
	require.NoError(t, authorizeAccountFile(ctx, []string{"authorizetestbackend"}))
	clientID, _, _, _, err = accountrotation.ReadFile(accountFile)
	require.NoError(t, err)
	assert.Equal(t, "", clientID)

	assert.Error(t, authorizeAccountFile(ctx, []string{"authorizetestbackend", "blob"}), "config blobs aren't supported")
}
