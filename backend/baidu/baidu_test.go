package baidu

import (
	"testing"

	"github.com/rclone/rclone/fstest/fstests"
)

func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName:      "TestBaidu:",
		NilObject:       (*Object)(nil),
		SkipInvalidUTF8: true,
	})
}
