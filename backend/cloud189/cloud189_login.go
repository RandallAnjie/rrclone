package cloud189

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"

	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/lib/rest"
	"golang.org/x/net/publicsuffix"
)

const (
	defaultAuthRoot = "https://open.e.189.cn"
	defaultLoginURL = "https://cloud.189.cn/api/portal/loginUrl.action?redirectURL=https%3A%2F%2Fcloud.189.cn%2Fmain.action"
	b64map          = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	hexDigits       = "0123456789abcdefghijklmnopqrstuvwxyz"
)

func (f *Fs) applyCookie(cookie string) {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return
	}
	f.opt.Cookie = cookie
	f.srv.SetHeader("Cookie", cookie)
	f.dl.SetHeader("Cookie", cookie)
	if f.m != nil {
		f.m.Set("cookie", cookie)
	}
}

func (f *Fs) ensureSession(ctx context.Context) error {
	if strings.TrimSpace(f.opt.Cookie) != "" {
		if _, err := f.capacity(ctx); err == nil {
			return nil
		} else if strings.TrimSpace(f.opt.User) == "" || strings.TrimSpace(f.opt.Pass) == "" {
			return err
		}
	}
	if strings.TrimSpace(f.opt.User) == "" || strings.TrimSpace(f.opt.Pass) == "" {
		return errors.New("cloud189: cookie is invalid and user/pass are not set")
	}
	if err := f.passwordLogin(ctx); err != nil {
		return err
	}
	if _, err := f.capacity(ctx); err != nil {
		return err
	}
	return nil
}

func (f *Fs) authRoot() string {
	if strings.TrimSpace(f.opt.AuthEndpoint) != "" {
		return strings.TrimRight(f.opt.AuthEndpoint, "/")
	}
	ep := strings.TrimRight(f.opt.Endpoint, "/")
	if ep != "" && ep != "https://cloud.189.cn" {
		return ep
	}
	return defaultAuthRoot
}

func (f *Fs) loginStartURL() string {
	ep := strings.TrimRight(f.opt.Endpoint, "/")
	if ep != "" && ep != "https://cloud.189.cn" {
		return ep + "/api/portal/loginUrl.action?redirectURL=https%3A%2F%2Fcloud.189.cn%2Fmain.action"
	}
	return defaultLoginURL
}

func (f *Fs) passwordLogin(ctx context.Context) error {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return err
	}
	httpClient := fshttp.NewClient(ctx)
	httpClient.Jar = jar
	login := rest.NewClient(httpClient)
	login.SetHeader("User-Agent", defaultUA)

	startOpts := rest.Opts{Method: http.MethodGet, RootURL: f.loginStartURL()}
	resp, err := login.Call(ctx, &startOpts)
	if err != nil {
		return fmt.Errorf("cloud189 login url: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	finalURL := resp.Request.URL
	if strings.Contains(finalURL.Path, "/web/main") || strings.Contains(finalURL.Path, "/main.action") {
		f.applyCookie(jarCookies(jar, finalURL, mustURL("https://cloud.189.cn/")))
		return nil
	}
	q := finalURL.Query()
	lt := q.Get("lt")
	reqID := q.Get("reqId")
	appID := q.Get("appId")
	if appID == "" {
		appID = "cloud"
	}
	headers := map[string]string{
		"lt":      lt,
		"reqid":   reqID,
		"Referer": finalURL.String(),
		"Origin":  f.authRoot(),
	}

	appForm := url.Values{}
	appForm.Set("version", "2.0")
	appForm.Set("appKey", appID)
	var appConf struct {
		Result string `json:"result"`
		Msg    string `json:"msg"`
		Data   struct {
			AccountType string `json:"accountType"`
			ReturnURL   string `json:"returnUrl"`
			MailSuffix  string `json:"mailSuffix"`
			ClientType  int    `json:"clientType"`
			IsOauth2    bool   `json:"isOauth2"`
			ParamID     string `json:"paramId"`
			AppKey      string `json:"appKey"`
		} `json:"data"`
	}
	if err := f.formJSON(ctx, login, f.authRoot()+"/api/logbox/oauth2/appConf.do", appForm, headers, &appConf); err != nil {
		return fmt.Errorf("cloud189 appConf: %w", err)
	}
	if appConf.Result != "" && appConf.Result != "0" {
		return fmt.Errorf("cloud189 appConf: %s", appConf.Msg)
	}

	encForm := url.Values{}
	encForm.Set("appId", appID)
	var encConf struct {
		Result int `json:"result"`
		Data   struct {
			Pre    string `json:"pre"`
			PubKey string `json:"pubKey"`
		} `json:"data"`
	}
	if err := f.formJSON(ctx, login, f.authRoot()+"/api/logbox/config/encryptConf.do", encForm, headers, &encConf); err != nil {
		return fmt.Errorf("cloud189 encryptConf: %w", err)
	}
	if encConf.Result != 0 {
		return errors.New("cloud189: encryptConf failed")
	}
	userEnc, err := rsaEncryptHex(encConf.Data.PubKey, []byte(f.opt.User))
	if err != nil {
		return fmt.Errorf("cloud189 encrypt user: %w", err)
	}
	passEnc, err := rsaEncryptHex(encConf.Data.PubKey, []byte(f.opt.Pass))
	if err != nil {
		return fmt.Errorf("cloud189 encrypt pass: %w", err)
	}

	loginForm := url.Values{}
	loginForm.Set("version", "v2.0")
	loginForm.Set("apToken", "")
	loginForm.Set("appKey", appID)
	loginForm.Set("accountType", orDefault(appConf.Data.AccountType, "02"))
	loginForm.Set("userName", encConf.Data.Pre+userEnc)
	loginForm.Set("epd", encConf.Data.Pre+passEnc)
	loginForm.Set("captchaType", "")
	loginForm.Set("validateCode", "")
	loginForm.Set("smsValidateCode", "")
	loginForm.Set("captchaToken", "")
	loginForm.Set("returnUrl", appConf.Data.ReturnURL)
	loginForm.Set("mailSuffix", appConf.Data.MailSuffix)
	loginForm.Set("dynamicCheck", "FALSE")
	loginForm.Set("clientType", strconv.Itoa(appConf.Data.ClientType))
	if appConf.Data.ClientType == 0 {
		loginForm.Set("clientType", "1")
	}
	loginForm.Set("cb_SaveName", "3")
	loginForm.Set("isOauth2", strconv.FormatBool(appConf.Data.IsOauth2))
	loginForm.Set("state", "")
	loginForm.Set("paramId", appConf.Data.ParamID)

	var submit struct {
		Result json.RawMessage `json:"result"`
		Msg    string          `json:"msg"`
		ToURL  string          `json:"toUrl"`
	}
	if err := f.formJSON(ctx, login, f.authRoot()+"/api/logbox/oauth2/loginSubmit.do", loginForm, headers, &submit); err != nil {
		return fmt.Errorf("cloud189 loginSubmit: %w", err)
	}
	if !loginResultOK(submit.Result) {
		msg := submit.Msg
		if msg == "" {
			msg = string(submit.Result)
		}
		return fmt.Errorf("cloud189 login: %s", msg)
	}
	if submit.ToURL != "" {
		toOpts := rest.Opts{Method: http.MethodGet, RootURL: submit.ToURL}
		toResp, err := login.Call(ctx, &toOpts)
		if err == nil && toResp != nil {
			_, _ = io.Copy(io.Discard, toResp.Body)
			_ = toResp.Body.Close()
		}
	}
	cookie := jarCookies(jar,
		mustURL("https://cloud.189.cn/"),
		mustURL(strings.TrimRight(f.opt.Endpoint, "/")+"/"),
		finalURL,
		mustURL(f.authRoot()+"/"),
		mustURL(f.authRoot()+"/api/logbox/oauth2/loginSubmit.do"),
		mustURL(submit.ToURL),
	)
	if cookie == "" {
		return errors.New("cloud189: login succeeded but no session cookie was returned")
	}
	f.applyCookie(cookie)
	return nil
}

func (f *Fs) formJSON(ctx context.Context, cli *rest.Client, rawURL string, form url.Values, headers map[string]string, dest any) error {
	body := form.Encode()
	opts := rest.Opts{
		Method:       http.MethodPost,
		RootURL:      rawURL,
		Body:         strings.NewReader(body),
		ContentType:  "application/x-www-form-urlencoded",
		ExtraHeaders: headers,
	}
	_, err := cli.CallJSON(ctx, &opts, nil, dest)
	return err
}

func loginResultOK(raw json.RawMessage) bool {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	return s == "0" || s == ""
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func mustURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	return u
}

func jarCookies(jar http.CookieJar, urls ...*url.URL) string {
	seen := map[string]string{}
	var names []string
	for _, u := range urls {
		if u == nil || jar == nil {
			continue
		}
		for _, c := range jar.Cookies(u) {
			if _, ok := seen[c.Name]; !ok {
				names = append(names, c.Name)
			}
			seen[c.Name] = c.Value
		}
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+seen[name])
	}
	return strings.Join(parts, "; ")
}

func rsaEncryptHex(pubKey string, plaintext []byte) (string, error) {
	pub, err := parseRSAPublicKey(pubKey)
	if err != nil {
		return "", err
	}
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, pub, plaintext)
	if err != nil {
		return "", err
	}
	return b64ToHex(base64.StdEncoding.EncodeToString(enc)), nil
}

func parseRSAPublicKey(pubKey string) (*rsa.PublicKey, error) {
	pubKey = strings.TrimSpace(pubKey)
	if strings.Contains(pubKey, "BEGIN") {
		block, _ := pem.Decode([]byte(pubKey))
		if block == nil {
			return nil, errors.New("invalid PEM public key")
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		pub, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("not an RSA public key")
		}
		return pub, nil
	}
	der, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(pubKey, "\n", ""))
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("not an RSA public key")
	}
	return pub, nil
}

func b64ToHex(a string) string {
	var d strings.Builder
	e := 0
	c := 0
	for i := 0; i < len(a); i++ {
		m := a[i]
		if m == '=' {
			continue
		}
		v := strings.IndexByte(b64map, m)
		if v < 0 {
			continue
		}
		switch e {
		case 0:
			e = 1
			d.WriteByte(hexDigits[v>>2])
			c = 3 & v
		case 1:
			e = 2
			d.WriteByte(hexDigits[c<<2|v>>4])
			c = 15 & v
		case 2:
			e = 3
			d.WriteByte(hexDigits[c])
			d.WriteByte(hexDigits[v>>2])
			c = 3 & v
		default:
			e = 0
			d.WriteByte(hexDigits[c<<2|v>>4])
			d.WriteByte(hexDigits[15&v])
		}
	}
	if e == 1 {
		d.WriteByte(hexDigits[c<<2])
	}
	return d.String()
}
