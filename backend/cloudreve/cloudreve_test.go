// Test Cloudreve filesystem interface
package cloudreve_test

import (
	"testing"

	"github.com/rclone/rclone/backend/cloudreve"
	"github.com/rclone/rclone/fstest/fstests"
)

// TestIntegration runs integration tests against a real Cloudreve server
func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName:      "TestCloudreve:",
		NilObject:       (*cloudreve.Object)(nil),
		SkipInvalidUTF8: true,
	})
}
