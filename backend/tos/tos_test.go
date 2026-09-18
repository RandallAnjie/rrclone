package tos_test

import (
	"testing"

	_ "github.com/rclone/rclone/backend/tos"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fstest/fstests"
	"github.com/stretchr/testify/require"
)

func TestRegistered(t *testing.T) {
	ri, err := fs.Find("tos")
	require.NoError(t, err)
	require.Equal(t, "tos", ri.Name)
}

func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName: "TestTOS:",
	})
}
