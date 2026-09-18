package _115

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/lib/rest"
)

const (
	qrTokenURL  = "https://qrcodeapi.115.com/api/1.0/web/1.0/token/"
	qrStatusURL = "https://qrcodeapi.115.com/get/status/"
	qrLoginURL  = "https://passportapi.115.com/app/1.0/web/1.0/login/qrcode"
)

type qrToken struct {
	UID    string `json:"uid"`
	Time   int64  `json:"time"`
	Sign   string `json:"sign"`
	QRCode string `json:"qrcode"`
}

type qrTokenResp struct {
	State bool    `json:"state"`
	Data  qrToken `json:"data"`
}

type qrStatusResp struct {
	State bool `json:"state"`
	Data  struct {
		Status int    `json:"status"`
		Msg    string `json:"msg"`
	} `json:"data"`
}

type qrLoginResp struct {
	State bool `json:"state"`
	Data  struct {
		Cookie struct {
			UID  string `json:"UID"`
			CID  string `json:"CID"`
			SEID string `json:"SEID"`
			KID  string `json:"KID"`
		} `json:"cookie"`
		Error string `json:"error"`
	} `json:"data"`
	Message string `json:"message"`
}

// Config offers QR login when cookie/uid are not set.
func Config(ctx context.Context, name string, m configmap.Mapper, in fs.ConfigIn) (*fs.ConfigOut, error) {
	cookie, _ := m.Get("cookie")
	uid, _ := m.Get("uid")
	switch in.State {
	case "":
		if strings.TrimSpace(cookie) != "" || strings.TrimSpace(uid) != "" {
			return nil, nil
		}
		return fs.ConfigConfirm("qr", false, "config_qr", "No cookie set. Log in to 115 with a QR code?")
	case "qr":
		if in.Result != "true" {
			return nil, nil
		}
		tok, err := fetchQRToken(ctx)
		if err != nil {
			return fs.ConfigError("", fmt.Sprintf("115 QR: %v", err))
		}
		m.Set("config_qr_uid", tok.UID)
		m.Set("config_qr_time", fmt.Sprint(tok.Time))
		m.Set("config_qr_sign", tok.Sign)
		link := tok.QRCode
		if link == "" {
			link = "https://qrcodeapi.115.com/api/1.0/web/1.0/qrcode?uid=" + url.QueryEscape(tok.UID)
		}
		return fs.ConfigInput("qr_wait", "config_qr_wait", "Scan this 115 QR in the 115 app (or open the URL), then press Enter:\n\n"+link)
	case "qr_wait":
		uid := mustGet(m, "config_qr_uid")
		timeStr := mustGet(m, "config_qr_time")
		sign := mustGet(m, "config_qr_sign")
		m.Set("config_qr_uid", "")
		m.Set("config_qr_time", "")
		m.Set("config_qr_sign", "")
		if uid == "" {
			return fs.ConfigError("", "115 QR session expired; run config again")
		}
		cookie, err := completeQRLogin(ctx, uid, timeStr, sign)
		if err != nil {
			return fs.ConfigError("", fmt.Sprintf("115 QR login: %v", err))
		}
		m.Set("cookie", cookie)
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown state %q", in.State)
	}
}

func mustGet(m configmap.Mapper, key string) string {
	v, _ := m.Get(key)
	return v
}

func fetchQRToken(ctx context.Context) (*qrToken, error) {
	cli := rest.NewClient(fshttp.NewClient(ctx))
	opts := rest.Opts{Method: http.MethodGet, RootURL: qrTokenURL}
	var info qrTokenResp
	resp, err := cli.CallJSON(ctx, &opts, nil, &info)
	if err != nil {
		return nil, err
	}
	_ = resp
	if info.Data.UID == "" {
		return nil, fmt.Errorf("empty QR token")
	}
	return &info.Data, nil
}

func completeQRLogin(ctx context.Context, uid, timeStr, sign string) (string, error) {
	cli := rest.NewClient(fshttp.NewClient(ctx))
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		status, err := pollQRStatus(ctx, cli, uid, timeStr, sign)
		if err != nil {
			return "", err
		}
		if status == 2 {
			break
		}
		if status < 0 {
			return "", fmt.Errorf("QR expired")
		}
		time.Sleep(2 * time.Second)
	}
	form := url.Values{}
	form.Set("account", uid)
	form.Set("app", "web")
	body := form.Encode()
	opts := rest.Opts{
		Method:      http.MethodPost,
		RootURL:     qrLoginURL,
		Body:        strings.NewReader(body),
		ContentType: "application/x-www-form-urlencoded",
	}
	var info qrLoginResp
	resp, err := cli.CallJSON(ctx, &opts, nil, &info)
	if err != nil {
		return "", err
	}
	_ = resp
	c := info.Data.Cookie
	if c.UID == "" || c.CID == "" || c.SEID == "" {
		msg := info.Message
		if info.Data.Error != "" {
			msg = info.Data.Error
		}
		if msg == "" {
			msg = "login did not return cookies"
		}
		return "", fmt.Errorf("%s", msg)
	}
	return cookieHeader(c.UID, c.CID, c.SEID, c.KID), nil
}

func pollQRStatus(ctx context.Context, cli *rest.Client, uid, timeStr, sign string) (int, error) {
	opts := rest.Opts{
		Method:     http.MethodGet,
		RootURL:    qrStatusURL,
		Parameters: url.Values{},
	}
	opts.Parameters.Set("uid", uid)
	opts.Parameters.Set("time", timeStr)
	opts.Parameters.Set("sign", sign)
	var info qrStatusResp
	resp, err := cli.CallJSON(ctx, &opts, nil, &info)
	if err != nil {
		return 0, err
	}
	_ = resp
	return info.Data.Status, nil
}

func parseQRTokenJSON(body []byte) (*qrToken, error) {
	var info qrTokenResp
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	if info.Data.UID == "" {
		return nil, fmt.Errorf("empty QR token")
	}
	return &info.Data, nil
}
