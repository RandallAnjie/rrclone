package accountrotation

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/rclone/rclone/fs/rc"
)

// Reporter reports the state of an account rotation
type Reporter interface {
	Status() Status
}

// registry holds the latest Reporter registered for each remote
var registry = struct {
	mu sync.Mutex
	m  map[string]Reporter
}{m: map[string]Reporter{}}

// Register adds the account rotation of the remote name to the reports
// of rotation/status, replacing any earlier one of that name. Rotators
// register themselves; backends with their own rotation call this.
func Register(name string, r Reporter) {
	registry.mu.Lock()
	registry.m[name] = r
	registry.mu.Unlock()
}

func register(r *Rotator) {
	Register(r.name, r)
}

// AccountStatus is the state of one account of a Rotator
type AccountStatus struct {
	File          string     `json:"file"`
	Active        bool       `json:"active"`
	CoolingDown   bool       `json:"cooling_down"`
	CooldownLeft  float64    `json:"cooldown_left"` // seconds
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
}

// Status is the state of the account rotation of a remote
type Status struct {
	Name       string          `json:"name"`
	Kind       string          `json:"kind,omitempty"` // e.g. "service account", default OAuth
	Current    string          `json:"current"`
	Cooldown   float64         `json:"cooldown"` // seconds
	Switches   int64           `json:"switches"`
	LastSwitch *time.Time      `json:"last_switch,omitempty"`
	LastReason string          `json:"last_reason,omitempty"`
	Accounts   []AccountStatus `json:"accounts"`
}

// Status reports the state of r
func (r *Rotator) Status() Status {
	now := time.Now()
	r.statsMu.Lock()
	st := Status{
		Name:       r.name,
		Current:    r.Current(),
		Cooldown:   r.cooldown.Seconds(),
		Switches:   r.switches,
		LastReason: r.lastReason,
	}
	if !r.lastSwitch.IsZero() {
		t := r.lastSwitch
		st.LastSwitch = &t
	}
	r.statsMu.Unlock()
	r.pool.mu.Lock()
	for _, file := range r.pool.files {
		a := AccountStatus{File: file, Active: file == st.Current}
		if until := r.pool.cooldowns[file]; until.After(now) {
			u := until
			a.CoolingDown, a.CooldownLeft, a.CooldownUntil = true, until.Sub(now).Seconds(), &u
		}
		st.Accounts = append(st.Accounts, a)
	}
	r.pool.mu.Unlock()
	return st
}

// Statuses reports every remote with account rotation, by name
func Statuses() []Status {
	registry.mu.Lock()
	rotators := make([]Reporter, 0, len(registry.m))
	for _, r := range registry.m {
		rotators = append(rotators, r)
	}
	registry.mu.Unlock()
	out := make([]Status, 0, len(rotators))
	for _, r := range rotators {
		out = append(out, r.Status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func init() {
	rc.Add(rc.Call{
		Path:  "rotation/status",
		Fn:    rcStatus,
		Title: "Show the OAuth account rotation of remotes.",
		Help: `This shows each remote in use with oauth_account_files rotation:
the account in use, every account with how long it is still cooling
down, and how often and why it last switched.

Returns:

- rotators: list of
    - name: name of the remote
    - current: account file in use
    - cooldown: cooldown after a failure, in seconds
    - switches: number of switches so far
    - last_switch: time of the last switch, if any
    - last_reason: why it last switched, if it has
    - accounts: list of
        - file: account file
        - active: whether it is the one in use
        - cooling_down: whether it is resting after a failure
        - cooldown_left: seconds until it may be used again
        - cooldown_until: time it may be used again, if cooling down
`,
	})
}

func rcStatus(ctx context.Context, in rc.Params) (rc.Params, error) {
	return rc.Params{"rotators": Statuses()}, nil
}
