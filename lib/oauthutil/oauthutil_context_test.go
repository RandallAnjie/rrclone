package oauthutil

import (
	"context"
	"net/http"
	"testing"

	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestNewClientWithBaseClientDetachesCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), struct{}{}, "ok"))
	cancel()

	baseClient := &http.Client{}
	mapper := configmap.Simple{"token": `{"access_token":"tok","refresh_token":"rtok","token_type":"Bearer"}`}

	_, ts, err := NewClientWithBaseClient(parent, "test", mapper, &Config{}, baseClient)
	require.NoError(t, err)
	require.NotNil(t, ts)

	assert.NoError(t, ts.ctx.Err())
	assert.Equal(t, "ok", ts.ctx.Value(struct{}{}))
	assert.Equal(t, baseClient, ts.ctx.Value(oauth2.HTTPClient))
}

func TestNewClientCredentialsClientDetachesCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	baseClient := &http.Client{}

	_, ts, err := NewClientCredentialsClient(parent, "test", configmap.Simple{}, &Config{ClientCredentialFlow: true}, baseClient)
	require.NoError(t, err)
	require.NotNil(t, ts)

	assert.NoError(t, ts.ctx.Err())
	assert.Equal(t, baseClient, ts.ctx.Value(oauth2.HTTPClient))
}
