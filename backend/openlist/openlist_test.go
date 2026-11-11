// Test OpenList filesystem interface
package openlist_test

import (
	"testing"

	"github.com/rclone/rclone/backend/openlist"
	"github.com/rclone/rclone/fstest/fstests"
)

// TestIntegration runs integration tests against a real OpenList server
func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName:      "TestOpenList:",
		NilObject:       (*openlist.Object)(nil),
		SkipInvalidUTF8: true,
	})
}
