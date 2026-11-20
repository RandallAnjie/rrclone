package dropbox

// OAuth account rotation for Dropbox (rrclone).
//
// Dropbox rate limits (HTTP 429 too_many_requests) are applied per
// app and per user, so the same Dropbox account authorised under
// several App Keys, or several members of one team, give more
// headroom. Each account lives in its own file and the active one is
// swapped when it is rate limited or its token stops working.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/auth"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/common"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/users"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/lib/accountrotation"
	"github.com/rclone/rclone/lib/oauthutil"
	"golang.org/x/oauth2"
)

const defaultOAuthAccountCooldown = fs.Duration(5 * time.Minute)

// accountIdentity is who an account file authorises
type accountIdentity struct {
	accountID string
	rootNS    string
	teamRoot  bool
}

// sameData reports whether candidate sees the same files as primary
func (primary accountIdentity) sameData(candidate accountIdentity) bool {
	if primary.accountID == "" {
		return true // couldn't check
	}
	if candidate.accountID == primary.accountID {
		return true
	}
	return primary.teamRoot && candidate.teamRoot && primary.rootNS != "" && candidate.rootNS == primary.rootNS
}

func identityOf(acc *users.FullAccount) accountIdentity {
	id := accountIdentity{accountID: acc.AccountId}
	switch x := acc.RootInfo.(type) {
	case *common.TeamRootInfo:
		id.rootNS, id.teamRoot = x.RootNamespaceId, true
	case *common.UserRootInfo:
		id.rootNS = x.RootNamespaceId
	}
	return id
}

// fetchIdentity asks Dropbox who client is, bypassing the rotator
func (f *Fs) fetchIdentity(ctx context.Context, client *http.Client) (accountIdentity, error) {
	u := users.NewContext(dropbox.Config{
		LogLevel:     dropbox.LogOff,
		Client:       client,
		URLGenerator: dropboxURLGeneratorFromOpt(&f.opt),
	})
	acc, err := u.GetCurrentAccountContext(ctx)
	if err != nil {
		return accountIdentity{}, err
	}
	return identityOf(acc), nil
}

// impersonating is true when the token is a team token acting as a member
func (f *Fs) impersonating() bool {
	return f.opt.Impersonate != "" || f.opt.ImpersonateAdmin != ""
}

// setupRotation installs account rotation if more than one account
// file is configured, returning the client the SDK should use.
func (f *Fs) setupRotation(ctx context.Context, accountFiles []string, client *http.Client) *http.Client {
	if len(accountFiles) < 2 {
		return client
	}
	if !f.impersonating() {
		id, err := f.fetchIdentity(ctx, client)
		if err != nil {
			fs.Logf(f, "Couldn't identify OAuth account %q so alternates won't be checked: %v", accountFiles[0], err)
		} else {
			f.primary = id
		}
	}
	f.rotator = accountrotation.NewRotator(f.name, accountrotation.NewPool(accountFiles),
		time.Duration(f.opt.OAuthAccountCooldown), accountFiles[0], client,
		classifyDropboxResponse, f.newAccountClient)
	return &http.Client{Transport: f.rotator}
}

// newAccountClient makes and checks a client for the account in file
func (f *Fs) newAccountClient(ctx context.Context, file string) (*http.Client, error) {
	m := &accountrotation.TokenFileMapper{Base: f.m, File: file}
	client, ts, err := oauthutil.NewClient(ctx, f.name, m, getOauthConfig(m))
	if err != nil {
		return nil, fmt.Errorf("failed to make oauth client: %w", err)
	}
	if _, err := ts.Token(); err != nil {
		return nil, fmt.Errorf("token check failed: %w", err)
	}
	if f.impersonating() || f.primary.accountID == "" {
		return client, nil
	}
	id, err := f.fetchIdentity(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("account check failed: %w", err)
	}
	if !f.primary.sameData(id) {
		return nil, fmt.Errorf("account %q is not the same Dropbox account or team as %q", id.accountID, f.primary.accountID)
	}
	return client, nil
}

// classifyDropboxResponse decides whether a response means the
// account should be rotated out.
func classifyDropboxResponse(resp *http.Response, err error) accountrotation.Verdict {
	if err != nil {
		// The refresh token was rejected
		var rErr *oauth2.RetrieveError
		if errors.As(err, &rErr) && rErr.Response != nil &&
			(rErr.Response.StatusCode == http.StatusBadRequest || rErr.Response.StatusCode == http.StatusUnauthorized) {
			return accountrotation.Verdict{Rotate: true, Reason: "token refresh failed"}
		}
		return accountrotation.Verdict{}
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		// too_many_write_operations is lock contention on the
		// namespace which another account can't help with.
		if !strings.Contains(peekBody(resp), "too_many_requests") {
			return accountrotation.Verdict{}
		}
		v := accountrotation.Verdict{Rotate: true, Reason: "rate limited"}
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			v.Cooldown = time.Duration(secs) * time.Second
		}
		return v
	case http.StatusUnauthorized:
		return accountrotation.Verdict{Rotate: true, Reason: "auth error " + errorTag(peekBody(resp))}
	}
	return accountrotation.Verdict{}
}

// peekBody returns the start of resp.Body, leaving it readable
func peekBody(resp *http.Response) string {
	if resp.Body == nil {
		return ""
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(buf), resp.Body), resp.Body}
	return string(buf)
}

// errorTag pulls the error tag out of a Dropbox error_summary
func errorTag(body string) string {
	var e struct {
		ErrorSummary string `json:"error_summary"`
	}
	if json.Unmarshal([]byte(body), &e) != nil {
		return ""
	}
	tag, _, _ := strings.Cut(e.ErrorSummary, "/")
	return strings.TrimSpace(tag)
}

// shouldRetry is shouldRetry, but a rate limit doesn't wait for its
// Retry-After when the rotator has already moved to a healthy account.
//
// Use it for requests whose body the rotator can't replay (uploads).
func (f *Fs) shouldRetry(ctx context.Context, err error) (bool, error) {
	if f.rotator != nil && f.rotator.Healthy() {
		if _, ok := err.(auth.RateLimitAPIError); ok && strings.Contains(err.Error(), "too_many_requests") {
			if retry, err := shouldRetryExclude(ctx, err); !retry {
				return retry, err
			}
			fs.Debugf(f, "Rate limited, retrying on OAuth account %q", f.rotator.Current())
			return true, err
		}
	}
	return shouldRetry(ctx, err)
}
