// Package push delivers changed directories to phones off the call path.
// The WP826's five-minute minimum polling interval cannot deliver interactive
// edits. Its authenticated XML importer can, without rebooting the phone.
package push

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/provision"
)

var (
	ErrUnavailable = errors.New("handset management unavailable")
	ErrIdentity    = errors.New("handset identity does not match inventory")
	ErrAuth        = errors.New("handset management login rejected")
	ErrImport      = errors.New("handset rejected phonebook import")
)

const maxBody = 2 << 20

// Supported is deliberately narrower than the provisioning model registry:
// this importer was rehearsed on WP826 firmware 1.0.1.71. Other phones retain
// their configured polling until their own management API is verified.
func Supported(model string) bool { return model == "grandstream-wp826" }

// Client owns a transport, not sessions: each upload logs in and out separately.
// No proxy, redirects, cookie persistence, or error containing request/response
// bodies: the handset's session and directory contents must stay on its LAN.
type Client struct{ transport http.RoundTripper }

func NewClient() *Client {
	return &Client{transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: time.Second}).DialContext,
		ResponseHeaderTimeout: 2 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConnsPerHost:   1,
	}}
}

// Upload uses the phone's existing LAN web interface and challenge login.
// The admin password itself is never transmitted. Check the advertised MAC
// before sending its salted proof; discovery is by the inventory's MAC too.
//
// That check is a device-type check, not authentication: a MAC and a model
// string are whatever the answering host says, so anything able to take the
// phone's address on the LAN passes it. The transport is plain HTTP because
// that is the interface this code drives, so the directory's names and numbers
// cross the LAN in clear and such a host would receive them. What stays safe
// is the credential: the login is a hash over a one-time nonce and the admin
// password is random per phone.
func (c *Client) Upload(ctx context.Context, p provision.Phone, ip netip.Addr, book []byte) error {
	if !Supported(p.Model.ID) || !ip.Is4() || !ip.IsPrivate() || len(book) > maxBody || p.AdminPassword == "" {
		return ErrUnavailable
	}
	jar, _ := cookiejar.New(nil)
	s := session{
		base: "http://" + ip.String(), ctx: ctx,
		http: &http.Client{Transport: c.transport, Jar: jar,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	var identity struct {
		Configs []struct{ Pvalue, Value string }
	}
	if err := s.request(http.MethodGet, "/config_get?pvalues=67,phone_model&update_session=false", "", nil, &identity); err != nil {
		return err
	}
	var mac, model string
	for _, v := range identity.Configs {
		switch v.Pvalue {
		case "67":
			mac, _ = policy.NormaliseMAC(v.Value)
		case "phone_model":
			model = v.Value
		}
	}
	wantMAC, ok := policy.NormaliseMAC(p.MAC)
	if !ok || mac != wantMAC || model != "WP826" {
		return ErrIdentity
	}
	var challenge reply
	if err := s.form("/access", url.Values{"access": {hash("admin")}}, &challenge); err != nil {
		return err
	}
	var nonce string
	if challenge.Response != "success" || json.Unmarshal(challenge.Body, &nonce) != nil || nonce == "" {
		return ErrAuth
	}
	var login reply
	if err := s.form("/dologin", url.Values{"username": {"admin"}, "password": {hash(p.AdminPassword + nonce)}}, &login); err != nil {
		return err
	}
	var auth struct{ SID string }
	if login.Response != "success" || json.Unmarshal(login.Body, &auth) != nil || auth.SID == "" {
		return ErrAuth
	}
	u, _ := url.Parse(s.base)
	jar.SetCookies(u, []*http.Cookie{{Name: "sid", Value: auth.SID, Path: "/"}})
	defer func() {
		// Even cancellation should close the management session; bound cleanup
		// separately so a sleeping handset cannot hold the worker indefinitely.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		s.ctx = cleanup
		_ = s.form("/dologout", url.Values{}, nil)
	}()

	var config struct {
		Configs []struct{ Pvalue, Value string }
	}
	if err := s.request(http.MethodGet, "/config_get?pvalues=333&update_session=false", "", nil, &config); err != nil {
		return err
	}
	keepManual := false
	for _, v := range config.Configs {
		if v.Pvalue == "333" && v.Value == "0" {
			keepManual = true
		}
	}
	if !keepManual {
		return ErrImport
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("fname", "phonebook.xml")
	_ = w.WriteField("sid", auth.SID)
	part, err := w.CreateFormFile("file", "phonebook.xml")
	if err != nil {
		return ErrImport
	}
	_, _ = part.Write(book)
	_ = w.Close()
	var result reply
	if err := s.request(http.MethodPost, "/phonebook_upload", w.FormDataContentType(), &body, &result); err != nil {
		return err
	}
	var code string
	if result.Response != "success" || json.Unmarshal(result.Body, &code) != nil || code != "0" {
		return ErrImport
	}
	return nil
}

type reply struct {
	Response string          `json:"response"`
	Body     json.RawMessage `json:"body"`
}

type session struct {
	base string
	ctx  context.Context
	http *http.Client
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (s *session) form(path string, values url.Values, out any) error {
	return s.request(http.MethodPost, path, "application/x-www-form-urlencoded", strings.NewReader(values.Encode()), out)
}

func (s *session) request(method, path, contentType string, body io.Reader, out any) error {
	r, err := http.NewRequestWithContext(s.ctx, method, s.base+"/cgi-bin"+path, body)
	if err != nil {
		return ErrUnavailable
	}
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	r.Header.Set("Origin", s.base)
	r.Header.Set("Referer", s.base+"/login")
	resp, err := s.http.Do(r)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(b) > maxBody {
		return ErrUnavailable
	}
	if out != nil && json.Unmarshal(b, out) != nil {
		return ErrUnavailable
	}
	return nil
}
