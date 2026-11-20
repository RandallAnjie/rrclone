package flags

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
)

func TestInstallFlagLegacyEnv(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"legacy", map[string]string{"RCLONE_LEGACY_TEST_FLAG": "legacy"}, "legacy"},
		{"rrclone", map[string]string{"RRCLONE_LEGACY_TEST_FLAG": "new"}, "new"},
		{"both", map[string]string{"RRCLONE_LEGACY_TEST_FLAG": "new", "RCLONE_LEGACY_TEST_FLAG": "legacy"}, "new"},
		{"none", nil, "default"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for k, v := range test.env {
				t.Setenv(k, v)
			}
			flagSet := pflag.NewFlagSet("test", pflag.ContinueOnError)
			var value string
			StringVarP(flagSet, &value, "legacy-test-flag", "", "default", "usage", "")
			assert.Equal(t, test.want, value)
		})
	}
}
