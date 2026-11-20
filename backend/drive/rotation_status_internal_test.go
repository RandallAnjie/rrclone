package drive

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/rclone/rclone/lib/accountrotation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRotatorStatus(t *testing.T) {
	newClient := func(ctx context.Context, file string) (*http.Client, error) { return &http.Client{}, nil }
	r := newAccountRotator("service account", newServiceAccountPool("sa1.json", []string{"sa2.json"}), time.Minute, "sa1.json", &http.Client{}, newClient)
	r.name = "drive-status-test"
	accountrotation.Register(r.name, r)

	st := r.Status()
	assert.Equal(t, "service account", st.Kind)
	assert.Equal(t, "sa1.json", st.Current)
	assert.Zero(t, st.Switches)

	require.NoError(t, r.rotate(context.Background(), &Fs{}, "userRateLimitExceeded", errors.New("quota")))
	st = r.Status()
	assert.Equal(t, "sa2.json", st.Current)
	assert.Equal(t, int64(1), st.Switches)
	assert.Equal(t, "userRateLimitExceeded", st.LastReason)
	require.Len(t, st.Accounts, 2)
	assert.True(t, st.Accounts[0].CoolingDown)
	assert.InDelta(t, 60, st.Accounts[0].CooldownLeft, 2)
	assert.True(t, st.Accounts[1].Active)

	var found bool
	for _, s := range accountrotation.Statuses() {
		if s.Name == "drive-status-test" {
			found = true
			assert.Equal(t, "sa2.json", s.Current)
		}
	}
	assert.True(t, found, "drive rotation is in rotation/status")
}
