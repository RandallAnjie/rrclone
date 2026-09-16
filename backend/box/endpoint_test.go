package box

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBoxAPIRoot(t *testing.T) {
	assert.Equal(t, rootURL, boxAPIRoot(""))
	assert.Equal(t, "https://api.box.example.com/2.0", boxAPIRoot("https://api.box.example.com"))
	assert.Equal(t, "https://api.box.example.com/2.0", boxAPIRoot("https://api.box.example.com/2.0"))
	assert.Equal(t, "https://api.box.example.com/2.0", boxAPIRoot("api.box.example.com"))
}

func TestBoxUploadRoot(t *testing.T) {
	assert.Equal(t, uploadURL, boxUploadRoot(""))
	assert.Equal(t, "https://upload.box.example.com/api/2.0", boxUploadRoot("https://upload.box.example.com"))
	assert.Equal(t, "https://upload.box.example.com/api/2.0", boxUploadRoot("https://upload.box.example.com/api/2.0"))
}
