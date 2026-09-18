package oss_test

import (
	"testing"

	_ "github.com/rclone/rclone/backend/oss"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fstest/fstests"
	"github.com/stretchr/testify/require"
)

func TestRegistered(t *testing.T) {
	ri, err := fs.Find("oss")
	require.NoError(t, err)
	require.Equal(t, "oss", ri.Name)
}

func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName: "TestOSS:",
	})
}
