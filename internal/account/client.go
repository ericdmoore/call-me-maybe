package account

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

const APIOrigin = "https://api.dialdoorman.cc"
const ApprovalURL = "https://login.dialdoorman.cc/device"
const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

var tokenShape = regexp.MustCompile(`^dd_(sync|backup)_[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{16}_[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{32}$`)
var idShape = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type Client struct {
	StateDir string
	origin   string
	http     *http.Client
	now      func() time.Time
	wait     func(context.Context, time.Duration) error
}

func New(stateDir string) *Client {
	return &Client{StateDir: stateDir, origin: APIOrigin,
		http: &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		now:  time.Now, wait: wait,
	}
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ServiceError contains only a recognised categorical code and numeric metadata.
// Neither server bodies nor net/http errors (which contain URLs) escape this package.
type ServiceError struct {
	Status     int
	Code       string
	RetryAfter time.Duration
}

func (e *ServiceError) Error() string {
	return fmt.Sprintf("account: service refused request (%d, %s)", e.Status, e.Code)
}

// Is lets a caller ask errors.Is(err, ErrRemoved) whether the refusal came
// from the network just now or from the state a previous refusal recorded.
func (e *ServiceError) Is(target error) bool {
	return target == ErrRemoved && e.Status == 401 && e.Code == "box_removed"
}

func failure(status int, header http.Header, body []byte) error {
	var data struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &data)
	code := "service_error"
	switch data.Error {
	case "bad_request", "unauthorized", "box_removed", "unknown", "expired", "revoked", "reuse", "capability", "denied", "consumed", "slow_down", "rate_limited",
		"no_backup_plan", "held_in_grace", "too_large", "invalid_size", "upload_in_progress", "unknown_upload", "not_uploaded", "size_mismatch", "entitlements_unavailable", "generation_conflict", "recovery_unavailable", "unknown_generation", "unknown_bundle",
		"sync_required", "upgrade_required":
		code = data.Error
	}
	seconds, _ := strconv.Atoi(header.Get("Retry-After"))
	if seconds < 0 || seconds > 86400 {
		seconds = 0
	}
	return &ServiceError{status, code, time.Duration(seconds) * time.Second}
}

func (c *Client) post(ctx context.Context, path, token string, value any) (int, http.Header, []byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return 0, nil, nil, ErrProtocol
	}
	return c.request(ctx, http.MethodPost, path, token, data, 64*1024)
}

func (c *Client) request(ctx context.Context, method, path, token string, data []byte, limit int64) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, nil, ErrProtocol
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, ErrNetwork
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return 0, nil, nil, ErrProtocol
	}
	return resp.StatusCode, resp.Header, body, nil
}

func userCode(key, salt []byte) string {
	sum := sha256.Sum256(append(append([]byte{}, key...), salt...))
	code := make([]byte, 12)
	for i := range code {
		bit := i * 5
		pair := uint16(sum[bit/8])<<8 | uint16(sum[bit/8+1])
		code[i] = alphabet[(pair>>uint(11-bit%8))&31]
	}
	return string(code)
}

func validToken(value, capability string) bool {
	m := tokenShape.FindStringSubmatch(value)
	return len(m) == 2 && m[1] == capability
}

// Login resumes a pending ceremony. Only the public, key-committing user code
// reaches display; device codes, keys and token values stay in private state.
func (c *Client) Login(ctx context.Context, display func(string)) (Status, error) {
	s, err := openStore(c.StateDir)
	if err != nil {
		return Status{}, err
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		return Status{}, err
	}
	if v.Box != "" && !v.NeedsLogin {
		return Status{}, errors.New("account: already enrolled; use doorman account status")
	}
	if v.Device == nil || !v.Device.ExpiresAt.After(c.now()) {
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return Status{}, ErrState
		}
		v.TransportKey = base64.RawURLEncoding.EncodeToString(key.Bytes())
		v.Device = nil
		if err := s.save(v); err != nil {
			return Status{}, err
		}
		status, headers, body, err := c.post(ctx, "/v1/login/box/device", "", map[string]string{
			"transportPublicKey": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		})
		if err != nil {
			return Status{}, err
		}
		if status != 200 {
			return Status{}, failure(status, headers, body)
		}
		var d struct {
			DeviceCode   string `json:"deviceCode"`
			UserCode     string `json:"userCode"`
			UserCodeSalt string `json:"userCodeSalt"`
			ExpiresIn    int    `json:"expiresIn"`
			Interval     int    `json:"interval"`
		}
		if json.Unmarshal(body, &d) != nil {
			return Status{}, ErrProtocol
		}
		salt, err := base64.RawURLEncoding.DecodeString(d.UserCodeSalt)
		if err != nil || len(salt) != 32 || userCode(key.PublicKey().Bytes(), salt) != d.UserCode ||
			len(d.DeviceCode) != 43 || d.ExpiresIn < 1 || d.ExpiresIn > 900 || d.Interval < 5 || d.Interval > 60 {
			return Status{}, ErrProtocol
		}
		v.Device = &device{d.DeviceCode, d.UserCode, c.now().Add(time.Duration(d.ExpiresIn) * time.Second), d.Interval}
		if err := s.save(v); err != nil {
			return Status{}, err
		}
	}
	code := v.Device.UserCode
	if len(code) != 12 {
		return Status{}, ErrState
	}
	display(code[:4] + "-" + code[4:8] + "-" + code[8:])
	for c.now().Before(v.Device.ExpiresAt) {
		if err := c.wait(ctx, time.Duration(v.Device.Interval)*time.Second); err != nil {
			return Status{}, err
		}
		status, headers, body, err := c.post(ctx, "/v1/login/box/device/poll", "", map[string]string{"deviceCode": v.Device.Code})
		if err != nil {
			return Status{}, err
		}
		if status == 202 {
			continue
		}
		if status == 429 {
			backoff := failure(status, headers, body).(*ServiceError).RetryAfter
			v.Device.Interval += 5
			if int(backoff.Seconds()) > v.Device.Interval {
				v.Device.Interval = int(backoff.Seconds())
			}
			if err := s.save(v); err != nil {
				return Status{}, err
			}
			continue
		}
		if status != 200 {
			if status == 410 {
				v.Device = nil
				if err := s.save(v); err != nil {
					return Status{}, err
				}
			}
			return Status{}, failure(status, headers, body)
		}
		var done struct {
			Box    string            `json:"box"`
			House  string            `json:"house"`
			Tokens map[string]string `json:"tokens"`
		}
		if json.Unmarshal(body, &done) != nil || !idShape.MatchString(done.Box) || !idShape.MatchString(done.House) ||
			!validToken(done.Tokens["sync"], "sync") || !validToken(done.Tokens["backup"], "backup") {
			return Status{}, ErrProtocol
		}
		v.Box, v.House, v.EnrolledAt, v.NeedsLogin, v.Device = done.Box, done.House, c.now(), false, nil
		v.RemovedAt = time.Time{}
		v.Tokens = map[string]credential{"sync": {Current: done.Tokens["sync"]}, "backup": {Current: done.Tokens["backup"]}}
		v.Upload = nil
		if err := s.save(v); err != nil {
			return Status{}, err
		}
		return Status{Box: v.Box, House: v.House, EnrolledAt: v.EnrolledAt, Capabilities: []string{"sync", "backup"}}, nil
	}
	return Status{}, errors.New("account: approval expired; run doorman login again")
}

func (c *Client) authenticated(ctx context.Context, s *store, v *state, capability, path string, body any) (int, http.Header, []byte, error) {
	return c.authenticatedVia(s, v, capability, func(token string) (int, http.Header, []byte, error) {
		return c.post(ctx, path, token, body)
	})
}

func (c *Client) authenticatedVia(s *store, v *state, capability string, send func(string) (int, http.Header, []byte, error)) (int, http.Header, []byte, error) {
	t := v.Tokens[capability]
	token := t.Current
	if t.Pending != "" {
		token = t.Pending
	}
	if !v.RemovedAt.IsZero() {
		return 0, nil, nil, ErrRemoved
	}
	if v.NeedsLogin || !validToken(token, capability) {
		return 0, nil, nil, ErrLogin
	}
	status, headers, data, err := send(token)
	if err != nil {
		return 0, nil, nil, err
	}
	if removed(status, data) {
		if err := c.markRemoved(s, v); err != nil {
			return 0, nil, nil, err
		}
	} else if status == 401 {
		// Never fall back to the predecessor: an ambiguous first use may
		// already have committed the successor on the server.
		v.NeedsLogin = true
		if err := s.save(v); err != nil {
			return 0, nil, nil, err
		}
	} else if status >= 200 && status < 300 && t.Pending != "" {
		v.Tokens[capability] = credential{Current: t.Pending}
		if err := s.save(v); err != nil {
			return 0, nil, nil, err
		}
	}
	return status, headers, data, nil
}

func (c *Client) Rotate(ctx context.Context, capability string) error {
	if capability != "backup" && capability != "sync" {
		return errors.New("account: choose backup or sync")
	}
	s, err := openStore(c.StateDir)
	if err != nil {
		return err
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		return err
	}
	status, headers, data, err := c.authenticated(ctx, s, v, capability, "/v1/login/box/rotate", map[string]string{"capability": capability})
	if err != nil {
		return err
	}
	if status != 200 {
		return failure(status, headers, data)
	}
	var response struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(data, &response) != nil || !validToken(response.Token, capability) {
		return ErrProtocol
	}
	t := v.Tokens[capability]
	t.Pending = response.Token
	v.Tokens[capability] = t
	return s.save(v)
}
