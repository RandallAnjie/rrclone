package alipan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	secpEcdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/rclone/rclone/backend/alipan/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/lib/rest"
)

func (f *Fs) tokenURL() string {
	if strings.TrimSpace(f.opt.TokenEndpoint) != "" {
		return strings.TrimRight(f.opt.TokenEndpoint, "/")
	}
	ep := strings.TrimRight(f.opt.Endpoint, "/")
	if ep != "" && ep != api.DefaultRoot && ep != api.DefaultWebRoot {
		return ep + "/v2/account/token"
	}
	return api.DefaultTokenURL
}

// webRefreshLocked refreshes the unofficial web token. Caller holds tokenMu.
func (f *Fs) webRefreshLocked(ctx context.Context) error {
	if f.opt.RefreshToken == "" {
		if f.token != "" {
			return nil
		}
		return errors.New("alipan: refresh_token is required (from www.alipan.com Local Storage)")
	}
	opts := rest.Opts{
		Method:  http.MethodPost,
		RootURL: f.tokenURL(),
	}
	req := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": f.opt.RefreshToken,
	}
	var info api.TokenResp
	resp, err := f.srv.CallJSON(ctx, &opts, &req, &info)
	if err != nil {
		return err
	}
	_ = resp
	if err := info.Err(); err != nil {
		return err
	}
	f.token = info.AccessToken
	if info.RefreshToken != "" {
		f.opt.RefreshToken = info.RefreshToken
		if f.m != nil {
			f.m.Set("refresh_token", info.RefreshToken)
		}
	}
	exp := info.ExpiresIn
	if exp <= 0 {
		exp = 7200
	}
	f.tokenExp = time.Now().Add(time.Duration(exp) * time.Second)
	return nil
}

func (f *Fs) initWebSession(ctx context.Context) error {
	if f.userID == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(f.userID))
	deviceID := hex.EncodeToString(sum[:])
	priv := secp.PrivKeyFromBytes(sum[:])
	signData := fmt.Sprintf("%s:%s:%s:%d", api.WebAppID, deviceID, f.userID, 0)
	hash := sha256.Sum256([]byte(signData))
	sig := secpEcdsa.SignCompact(priv, hash[:], false)
	pub := priv.PubKey().SerializeUncompressed()
	// SerializeUncompressed is 0x04 || X || Y; the web API wants X || Y.
	pubHex := hex.EncodeToString(pub[1:])
	f.sigMu.Lock()
	f.deviceID = deviceID
	f.signature = hex.EncodeToString(sig)
	f.sigMu.Unlock()

	opts := rest.Opts{
		Method:       http.MethodPost,
		Path:         "/users/v1/users/device/create_session",
		ExtraHeaders: f.authHeaders(),
	}
	req := map[string]any{
		"deviceName":   "rclone",
		"modelName":    "rclone",
		"nonce":        0,
		"pubKey":       pubHex,
		"refreshToken": f.opt.RefreshToken,
	}
	var info map[string]any
	_, err := f.srv.CallJSON(ctx, &opts, &req, &info)
	if err != nil {
		fs.Debugf(nil, "alipan: create_session: %v", err)
	}
	return nil
}
