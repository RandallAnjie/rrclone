package dropbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDropboxCustomRoots(t *testing.T) {
	apiRoot, contentRoot, ok := dropboxCustomRoots("", "")
	assert.False(t, ok)
	assert.Equal(t, "", apiRoot)
	assert.Equal(t, "", contentRoot)

	apiRoot, contentRoot, ok = dropboxCustomRoots("https://api.dropbox.example.com", "")
	assert.True(t, ok)
	assert.Equal(t, "https://api.dropbox.example.com", apiRoot)
	assert.Equal(t, "https://content.dropbox.example.com", contentRoot)

	apiRoot, contentRoot, ok = dropboxCustomRoots("dropbox-proxy.example.com", "")
	assert.True(t, ok)
	assert.Equal(t, "https://dropbox-proxy.example.com", apiRoot)
	assert.Equal(t, "https://dropbox-proxy.example.com", contentRoot)

	apiRoot, contentRoot, ok = dropboxCustomRoots("https://api.example.com", "https://content.example.com")
	assert.True(t, ok)
	assert.Equal(t, "https://api.example.com", apiRoot)
	assert.Equal(t, "https://content.example.com", contentRoot)
}

func TestDropboxURLGenerator(t *testing.T) {
	assert.Nil(t, dropboxURLGeneratorFromOpt(&Options{}))

	gen := dropboxURLGeneratorFromOpt(&Options{Endpoint: "https://api.dropbox.example.com"})
	require.NotNil(t, gen)
	assert.Equal(t, "https://api.dropbox.example.com/2/files/list_folder", gen("api", "files", "list_folder"))
	assert.Equal(t, "https://content.dropbox.example.com/2/files/download", gen("content", "files", "download"))
	assert.Equal(t, "https://notify.dropbox.example.com/2/files/list_folder/longpoll", gen("notify", "files", "list_folder/longpoll"))
}
