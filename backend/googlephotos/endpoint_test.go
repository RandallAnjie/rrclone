package googlephotos

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPhotosAPIRoot(t *testing.T) {
	assert.Equal(t, rootURL, photosAPIRoot(""))
	assert.Equal(t, "https://photoslibrary.example.com/v1", photosAPIRoot("https://photoslibrary.example.com"))
	assert.Equal(t, "https://photoslibrary.example.com/v1", photosAPIRoot("https://photoslibrary.example.com/v1"))
	assert.Equal(t, "https://photoslibrary.example.com/v1", photosAPIRoot("photoslibrary.example.com"))
}
