package onedrive

// OAuth account rotation for OneDrive (rrclone).
//
// Microsoft Graph throttles (HTTP 429) per app and user, so one account
// authorised under several app registrations spreads the load. Each
// account lives in its own file and the active one is swapped when it
// is throttled or its token stops working.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/lib/accountrotation"
	"github.com/rclone/rclone/lib/oauthutil"
	"golang.org/x/oauth2"
)

const defaultOAuthAccountCooldown = fs.Duration(5 * time.Minute)

// setupRotation installs account rotation when there is more than one
// account file, returning the client to use for the API.
func (f *Fs) setupRotation(accountFiles []string, client *http.Client) *http.Client {
	if len(accountFiles) < 2 {
		return client
	}
	f.rotator = accountrotation.NewRotator(f.name, accountrotation.NewPool(accountFiles),
		time.Duration(f.opt.OAuthAccountCooldown), accountFiles[0], client,
		classifyGraphResponse, f.newAccountClient)
	return &http.Client{Transport: f.rotator}
}

// newAccountClient makes and checks a client for the account in file
func (f *Fs) newAccountClient(ctx context.Context, file string) (*http.Client, error) {
	m := &accountrotation.TokenFileMapper{Base: f.m, File: file}
	oauthConfig, err := makeOauthConfig(ctx, &f.opt)
	if err != nil {
		return nil, err
	}
	client, ts, err := oauthutil.NewClient(ctx, f.name, m, oauthConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to make oauth client: %w", err)
	}
	if _, err := ts.Token(); err != nil {
		return nil, fmt.Errorf("token check failed: %w", err)
	}
	// The account must reach the configured drive
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.rootURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("drive check failed: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("account can't reach drive %s: HTTP %d", f.driveID, resp.StatusCode)
	}
	return client, nil
}

// classifyGraphResponse decides whether a response means the account
// should be rotated out.
func classifyGraphResponse(resp *http.Response, err error) accountrotation.Verdict {
	if err != nil {
		var rErr *oauth2.RetrieveError
		if errors.As(err, &rErr) && rErr.Response != nil &&
			(rErr.Response.StatusCode == http.StatusBadRequest || rErr.Response.StatusCode == http.StatusUnauthorized) {
			return accountrotation.Verdict{Rotate: true, Reason: "token refresh failed"}
		}
		return accountrotation.Verdict{}
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		v := accountrotation.Verdict{Rotate: true, Reason: "throttled"}
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			v.Cooldown = time.Duration(secs) * time.Second
		}
		return v
	case http.StatusUnauthorized:
		// An expired token is renewed, not a reason to move on
		if strings.Contains(resp.Header.Get("Www-Authenticate"), "expired_token") {
			return accountrotation.Verdict{}
		}
		return accountrotation.Verdict{Rotate: true, Reason: "auth error"}
	}
	return accountrotation.Verdict{}
}
