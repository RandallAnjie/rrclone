package drive

import (
	"time"

	"github.com/rclone/rclone/lib/accountrotation"
)

// Status reports the state of the rotation for rotation/status
func (r *accountRotator) Status() accountrotation.Status {
	now := time.Now()
	st := accountrotation.Status{
		Name:     r.name,
		Kind:     r.kind,
		Current:  r.current(),
		Cooldown: r.cooldown.Seconds(),
	}
	r.statsMu.Lock()
	st.Switches, st.LastReason = r.switches, r.lastReason
	if !r.lastSwitch.IsZero() {
		t := r.lastSwitch
		st.LastSwitch = &t
	}
	r.statsMu.Unlock()
	r.pool.mu.Lock()
	for _, file := range r.pool.files {
		a := accountrotation.AccountStatus{File: file, Active: file == st.Current}
		if until := r.pool.cooldowns[file]; until.After(now) {
			u := until
			a.CoolingDown, a.CooldownLeft, a.CooldownUntil = true, until.Sub(now).Seconds(), &u
		}
		st.Accounts = append(st.Accounts, a)
	}
	r.pool.mu.Unlock()
	return st
}

var _ accountrotation.Reporter = (*accountRotator)(nil)
