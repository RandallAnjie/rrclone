// Test 123 Pan filesystem interface
package pan123_test

import (
	"testing"

	"github.com/rclone/rclone/backend/pan123"
	"github.com/rclone/rclone/fstest/fstests"
)

// TestIntegration runs integration tests against a real 123 Pan account
func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName:      "Test123Pan:",
		NilObject:       (*pan123.Object)(nil),
		SkipInvalidUTF8: true,
	})
}
