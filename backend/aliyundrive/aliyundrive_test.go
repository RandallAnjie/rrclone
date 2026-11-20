// Test Aliyun Drive filesystem interface
package aliyundrive_test

import (
	"testing"

	"github.com/rclone/rclone/backend/aliyundrive"
	"github.com/rclone/rclone/fstest/fstests"
)

// TestIntegration runs integration tests against a real Aliyun Drive
func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName:      "TestAliyunDrive:",
		NilObject:       (*aliyundrive.Object)(nil),
		SkipInvalidUTF8: true,
	})
}
