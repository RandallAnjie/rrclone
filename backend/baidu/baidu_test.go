// Test Baidu Netdisk filesystem interface
package baidu_test

import (
	"testing"

	"github.com/rclone/rclone/backend/baidu"
	"github.com/rclone/rclone/fstest/fstests"
)

// TestIntegration runs integration tests against a real Baidu Netdisk
func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName:      "TestBaidu:",
		NilObject:       (*baidu.Object)(nil),
		SkipInvalidUTF8: true,
	})
}
