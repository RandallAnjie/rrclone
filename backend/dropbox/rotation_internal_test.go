package dropbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/auth"
	"github.com/rclone/rclone/lib/accountrotation"
	"github.com/stretchr/testify/assert"
	"golang.org/x/oauth2"
)

func response(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestClassifyDropboxResponse(t *testing.T) {
	// Rate limit with Retry-After rotates and leaves the body readable
	resp := response(429, `{"error_summary": "too_many_requests/..", "error": {"reason": {".tag": "too_many_requests"}, "retry_after": 300}}`,
		http.Header{"Retry-After": {"300"}})
	v := classifyDropboxResponse(resp, nil)
	assert.True(t, v.Rotate)
	assert.Equal(t, 300*time.Second, v.Cooldown)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "retry_after")

	// Write lock contention is not the account's fault
	v = classifyDropboxResponse(response(429, `{"error_summary": "too_many_write_operations/..."}`, nil), nil)
	assert.False(t, v.Rotate)

	v = classifyDropboxResponse(response(401, `{"error_summary":"invalid_access_token/...","error":{".tag":"invalid_access_token"}}`, nil), nil)
	assert.True(t, v.Rotate)
	assert.Equal(t, "auth error invalid_access_token", v.Reason)

	assert.False(t, classifyDropboxResponse(response(200, `{}`, nil), nil).Rotate)
	assert.False(t, classifyDropboxResponse(response(409, `{"error_summary":"path/not_found/"}`, nil), nil).Rotate)

	// A refresh token Dropbox rejects rotates, a network error doesn't
	rErr := &oauth2.RetrieveError{Response: &http.Response{StatusCode: 400}}
	assert.True(t, classifyDropboxResponse(nil, fmt.Errorf("couldn't fetch token: %w", rErr)).Rotate)
	assert.False(t, classifyDropboxResponse(nil, &oauth2.RetrieveError{Response: &http.Response{StatusCode: 503}}).Rotate)
	assert.False(t, classifyDropboxResponse(nil, errors.New("connection reset")).Rotate)
}

func TestAccountIdentitySameData(t *testing.T) {
	user := accountIdentity{accountID: "dbid:1", rootNS: "100"}
	assert.True(t, accountIdentity{}.sameData(accountIdentity{accountID: "dbid:2"}), "unknown primary isn't checked")
	assert.True(t, user.sameData(accountIdentity{accountID: "dbid:1", rootNS: "100"}))
	assert.False(t, user.sameData(accountIdentity{accountID: "dbid:2", rootNS: "200"}))
	assert.False(t, user.sameData(accountIdentity{accountID: "dbid:2", rootNS: "100"}), "personal roots must be the same account")

	team := accountIdentity{accountID: "dbid:1", rootNS: "900", teamRoot: true}
	assert.True(t, team.sameData(accountIdentity{accountID: "dbid:3", rootNS: "900", teamRoot: true}), "same team space")
	assert.False(t, team.sameData(accountIdentity{accountID: "dbid:3", rootNS: "901", teamRoot: true}))
}

func TestFsShouldRetrySkipsRetryAfterWhenRotated(t *testing.T) {
	ctx := context.Background()
	rateErr := auth.RateLimitAPIError{RateLimitError: &auth.RateLimitError{RetryAfter: 300}}
	rateErr.ErrorSummary = "too_many_requests/.."

	// Without rotation the Retry-After is honoured
	f := &Fs{}
	retry, err := f.shouldRetry(ctx, rateErr)
	assert.True(t, retry)
	assert.NotEqual(t, rateErr, err, "wrapped in a retry-after error")

	// With a healthy account active it retries straight away
	f.rotator = accountrotation.NewRotator("test", accountrotation.NewPool([]string{"a", "b"}), time.Minute, "b", http.DefaultClient,
		func(*http.Response, error) accountrotation.Verdict { return accountrotation.Verdict{} }, nil)
	retry, err = f.shouldRetry(ctx, rateErr)
	assert.True(t, retry)
	assert.Equal(t, rateErr, err)

	// But not for write contention
	writeErr := auth.RateLimitAPIError{RateLimitError: &auth.RateLimitError{RetryAfter: 1}}
	writeErr.ErrorSummary = "too_many_write_operations/.."
	_, err = f.shouldRetry(ctx, writeErr)
	assert.NotEqual(t, writeErr, err)
}
