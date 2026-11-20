package dropbox

import (
	"testing"

	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDropboxCustomRoots(t *testing.T) {
	for _, test := range []struct {
		name                     string
		endpoint, content, notif string
		want                     dropboxRoots
		ok                       bool
	}{
		{name: "unset"},
		{
			name:     "api host derives siblings",
			endpoint: "https://api.dropbox.example.com",
			want:     dropboxRoots{"https://api.dropbox.example.com", "https://content.dropbox.example.com", "https://notify.dropbox.example.com"},
			ok:       true,
		}, {
			name:     "single proxy shares the root",
			endpoint: "dropbox-proxy.example.com/",
			want:     dropboxRoots{"https://dropbox-proxy.example.com", "https://dropbox-proxy.example.com", "https://dropbox-proxy.example.com"},
			ok:       true,
		}, {
			name:     "all explicit",
			endpoint: "https://api.example.com",
			content:  "https://content.example.com",
			notif:    "https://n.example.com/longpoll/",
			want:     dropboxRoots{"https://api.example.com", "https://content.example.com", "https://n.example.com/longpoll"},
			ok:       true,
		}, {
			name:  "only notify keeps defaults for the rest",
			notif: "https://notify.proxy.example.com",
			want:  dropboxRoots{"https://api.dropboxapi.com", "https://content.dropboxapi.com", "https://notify.proxy.example.com"},
			ok:    true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := dropboxCustomRoots(test.endpoint, test.content, test.notif)
			assert.Equal(t, test.ok, ok)
			if ok {
				assert.Equal(t, test.want, got)
			}
		})
	}
}

func TestDropboxURLGenerator(t *testing.T) {
	assert.Nil(t, dropboxURLGeneratorFromOpt(&Options{}))

	gen := dropboxURLGeneratorFromOpt(&Options{Endpoint: "https://api.dropbox.example.com"})
	require.NotNil(t, gen)
	assert.Equal(t, "https://api.dropbox.example.com/2/files/list_folder", gen("api", "files", "list_folder"))
	assert.Equal(t, "https://content.dropbox.example.com/2/files/download", gen("content", "files", "download"))
	assert.Equal(t, "https://notify.dropbox.example.com/2/files/list_folder/longpoll", gen("notify", "files", "list_folder/longpoll"))

	gen = dropboxURLGeneratorFromOpt(&Options{Endpoint: "https://proxy.example.com/dbx", NotifyEndpoint: "https://proxy.example.com/dbx-notify"})
	require.NotNil(t, gen)
	assert.Equal(t, "https://proxy.example.com/dbx/2/files/upload", gen("content", "files", "upload"))
	assert.Equal(t, "https://proxy.example.com/dbx-notify/2/files/list_folder/longpoll", gen("notify", "files", "list_folder/longpoll"))
}

func TestCheckEndpoints(t *testing.T) {
	assert.NoError(t, checkEndpoints(&Options{}))
	assert.NoError(t, checkEndpoints(&Options{Endpoint: "api.example.com", ContentEndpoint: "http://localhost:8080/content"}))
	assert.Error(t, checkEndpoints(&Options{Endpoint: "ftp://api.example.com"}))
	assert.Error(t, checkEndpoints(&Options{ContentEndpoint: "https://"}))
	assert.Error(t, checkEndpoints(&Options{NotifyEndpoint: "https://n.example.com/?x=1"}))
	assert.Error(t, checkEndpoints(&Options{Endpoint: "https://bad host"}))
}

func TestDropboxTokenURL(t *testing.T) {
	assert.Equal(t, "", dropboxTokenURL(""))
	assert.Equal(t, "", dropboxTokenURL("https://api.dropboxapi.com/"))
	assert.Equal(t, "https://api.dropbox.example.com/oauth2/token", dropboxTokenURL("api.dropbox.example.com"))
}

func TestGetOauthConfigTokenURL(t *testing.T) {
	// Default config is untouched without an endpoint
	assert.Same(t, dropboxConfig, getOauthConfig(configmap.Simple{}))

	c := getOauthConfig(configmap.Simple{"endpoint": "https://api.dropbox.example.com"})
	assert.Equal(t, "https://api.dropbox.example.com/oauth2/token", c.TokenURL)
	assert.Equal(t, dropboxConfig.AuthURL, c.AuthURL)
	assert.Equal(t, dropboxConfig.Scopes, c.Scopes)

	// Impersonation adds scopes without touching the shared default
	n := len(dropboxConfig.Scopes)
	c = getOauthConfig(configmap.Simple{"impersonate": "a@example.com"})
	assert.Contains(t, c.Scopes, "members.read")
	assert.Len(t, dropboxConfig.Scopes, n)
	assert.Equal(t, dropboxConfig.TokenURL, c.TokenURL)
}
