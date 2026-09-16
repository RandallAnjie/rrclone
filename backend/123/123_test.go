package _123

import (
	"testing"

	"github.com/rclone/rclone/fstest/fstests"
)

// TestIntegration runs integration tests against the remote
func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName:      "Test123:",
		NilObject:       (*Object)(nil),
		SkipInvalidUTF8: true,
	})
}
