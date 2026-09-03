package flags

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallFlagFallsBackToRcloneEnv(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("stats", "1m0s", "help")
	t.Setenv("RCLONE_STATS", "173ms")

	installFlag(fs, "stats", "")

	got := fs.Lookup("stats")
	require.NotNil(t, got)
	assert.Equal(t, "173ms", got.Value.String())
	assert.Equal(t, "173ms", got.DefValue)
}

func TestInstallFlagPrefersRrcloneEnv(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("stats", "1m0s", "help")
	t.Setenv("RRCLONE_STATS", "200ms")
	t.Setenv("RCLONE_STATS", "173ms")

	installFlag(fs, "stats", "")

	got := fs.Lookup("stats")
	require.NotNil(t, got)
	assert.Equal(t, "200ms", got.Value.String())
}
