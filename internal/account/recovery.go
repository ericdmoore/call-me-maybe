package account

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const RecoveryApprovalURL = "https://login.dialdoorman.cc/backup-recovery"

var ErrRecoveryApproval = errors.New("account: approve recovery with doorman recovery authorize")

type recoveryGrant struct {
	Secret           string        `json:"secret"`
	UserCode         string        `json:"userCode,omitempty"`
	RequestExpiresAt int64         `json:"requestExpiresAt,omitempty"`
	Scope            RecoveryScope `json:"scope"`
}
type RecoveryScope struct {
	ID        string `json:"id"`
	Account   string `json:"account"`
	House     string `json:"house"`
	Box       string `json:"box"`
	ExpiresAt int64  `json:"expiresAt"`
}
type RecoveryGeneration struct {
	ID            string  `json:"id"`
	Predecessor   *string `json:"predecessor"`
	Recipient     string  `json:"recipient"`
	SHA256        string  `json:"sha256"`
	Bytes         int     `json:"bytes"`
	State         string  `json:"state"`
	HealthyCopies int     `json:"healthyCopies"`
}
type RecoveryListing struct {
	Account     string               `json:"account"`
	House       string               `json:"house"`
	Box         string               `json:"box"`
	Generations []RecoveryGeneration `json:"generations"`
	Bundles     []Bundle             `json:"bundles"`
}
type RecoveryMaterial struct {
	Account    string             `json:"account"`
	House      string             `json:"house"`
	Generation RecoveryGeneration `json:"generation"`
	Envelope   []byte             `json:"envelope"`
}

// AuthorizeRecovery never promotes the upload token into a read credential. The
// customer separately approves this saved random secret and its receiver-key code.
func (c *Client) AuthorizeRecovery(ctx context.Context, display func(string)) (RecoveryScope, error) {
	s, err := openStore(c.StateDir)
	if err != nil {
		return RecoveryScope{}, err
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		return RecoveryScope{}, err
	}
	if v.Box == "" || v.NeedsLogin {
		return RecoveryScope{}, ErrLogin
	}
	g := v.Recovery
	if g != nil && g.Scope.ExpiresAt > c.now().UnixMilli() {
		return g.Scope, nil
	}
	if g == nil || (g.RequestExpiresAt != 0 && g.RequestExpiresAt <= c.now().UnixMilli()) {
		secret := make([]byte, 32)
		if _, err = rand.Read(secret); err != nil {
			return RecoveryScope{}, ErrState
		}
		g = &recoveryGrant{Secret: "dd_recovery_" + base64.RawURLEncoding.EncodeToString(secret)}
		v.Recovery = g
		if err = s.save(v); err != nil {
			return RecoveryScope{}, err
		}
	}
	if g.UserCode == "" {
		status, headers, body, err := c.authenticated(ctx, s, v, "backup", "/v1/login/backup-recovery/start", map[string]string{"secret": g.Secret})
		if err != nil {
			return RecoveryScope{}, err
		}
		if status != 200 {
			return RecoveryScope{}, failure(status, headers, body)
		}
		var reply struct {
			UserCode  string `json:"userCode"`
			Salt      string `json:"userCodeSalt"`
			ExpiresAt int64  `json:"requestExpiresAt"`
		}
		if json.Unmarshal(body, &reply) != nil {
			return RecoveryScope{}, ErrProtocol
		}
		salt, e := base64.RawURLEncoding.DecodeString(reply.Salt)
		raw, e2 := base64.RawURLEncoding.DecodeString(v.TransportKey)
		key, e3 := ecdh.X25519().NewPrivateKey(raw)
		if e != nil || e2 != nil || e3 != nil || len(salt) != 32 || userCode(key.PublicKey().Bytes(), salt) != reply.UserCode || reply.ExpiresAt > c.now().Add(16*time.Minute).UnixMilli() {
			return RecoveryScope{}, ErrProtocol
		}
		// A start reply can be lost for longer than the request window. Retire that
		// saved secret so the next invocation can begin a fresh approval.
		if reply.ExpiresAt <= c.now().UnixMilli() {
			v.Recovery = nil
			if err = s.save(v); err != nil {
				return RecoveryScope{}, err
			}
			return RecoveryScope{}, ErrRecoveryApproval
		}
		g.UserCode, g.RequestExpiresAt = reply.UserCode, reply.ExpiresAt
		if err = s.save(v); err != nil {
			return RecoveryScope{}, err
		}
	}
	if len(g.UserCode) != 12 {
		return RecoveryScope{}, ErrState
	}
	display(g.UserCode[:4] + "-" + g.UserCode[4:8] + "-" + g.UserCode[8:])
	for c.now().UnixMilli() < g.RequestExpiresAt {
		if err = c.wait(ctx, 5*time.Second); err != nil {
			return RecoveryScope{}, err
		}
		status, headers, body, err := c.post(ctx, "/v1/login/backup-recovery/poll", g.Secret, struct{}{})
		if err != nil {
			return RecoveryScope{}, err
		}
		if status == 202 {
			continue
		}
		if status != 200 {
			return RecoveryScope{}, failure(status, headers, body)
		}
		var scope RecoveryScope
		if json.Unmarshal(body, &scope) != nil || !idShape.MatchString(scope.ID) || !idShape.MatchString(scope.Account) || scope.House != v.House || scope.Box != v.Box || scope.ExpiresAt <= c.now().UnixMilli() || scope.ExpiresAt > c.now().Add(31*time.Minute).UnixMilli() {
			return RecoveryScope{}, ErrProtocol
		}
		g.Scope = scope
		if err = s.save(v); err != nil {
			return RecoveryScope{}, err
		}
		return scope, nil
	}
	return RecoveryScope{}, ErrRecoveryApproval
}

func (c *Client) recoveryPost(ctx context.Context, path string, input, output any) (RecoveryScope, error) {
	s, err := openStore(c.StateDir)
	if err != nil {
		return RecoveryScope{}, err
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		return RecoveryScope{}, err
	}
	if v.NeedsLogin || v.Box == "" {
		return RecoveryScope{}, ErrLogin
	}
	g := v.Recovery
	if g == nil || g.Scope.ExpiresAt <= c.now().UnixMilli() || g.Scope.Box != v.Box || g.Scope.House != v.House {
		return RecoveryScope{}, ErrRecoveryApproval
	}
	status, headers, body, err := c.post(ctx, "/v1/backups/recovery/"+path, g.Secret, input)
	if err != nil {
		return RecoveryScope{}, err
	}
	if status == 401 {
		g.Scope.ExpiresAt = 0
		g.RequestExpiresAt = 0
		g.UserCode = ""
		v.Recovery = nil
		if err = s.save(v); err != nil {
			return RecoveryScope{}, err
		}
		return RecoveryScope{}, ErrRecoveryApproval
	}
	if status != 200 {
		return RecoveryScope{}, failure(status, headers, body)
	}
	if json.Unmarshal(body, output) != nil {
		return RecoveryScope{}, ErrProtocol
	}
	return g.Scope, nil
}
func (c *Client) RecoveryStatus(ctx context.Context) (RecoveryListing, error) {
	var result RecoveryListing
	scope, err := c.recoveryPost(ctx, "status", struct{}{}, &result)
	if err != nil {
		return result, err
	}
	if result.Account != scope.Account || result.House != scope.House || result.Box != scope.Box {
		return RecoveryListing{}, ErrProtocol
	}
	for _, g := range result.Generations {
		if !idShape.MatchString(g.ID) || g.Bytes < 1 || g.Bytes > 16384 || len(g.SHA256) != 64 || (g.State != "current" && g.State != "pending") {
			return RecoveryListing{}, ErrProtocol
		}
	}
	for _, b := range result.Bundles {
		if !idShape.MatchString(b.ID) || b.Bytes < 1 || b.Bytes > 200000000 {
			return RecoveryListing{}, ErrProtocol
		}
	}
	return result, nil
}
func (c *Client) RecoveryPrepare(ctx context.Context, g RecoveryGeneration, envelope []byte) error {
	var reply struct {
		Generation RecoveryGeneration `json:"generation"`
	}
	_, err := c.recoveryPost(ctx, "prepare", struct {
		ID          string  `json:"id"`
		Predecessor *string `json:"predecessor"`
		Recipient   string  `json:"recipient"`
		Envelope    []byte  `json:"envelope"`
	}{g.ID, g.Predecessor, g.Recipient, envelope}, &reply)
	if err != nil {
		return err
	}
	if reply.Generation.ID != g.ID || reply.Generation.SHA256 != hashBytes(envelope) || reply.Generation.Recipient != g.Recipient || reply.Generation.HealthyCopies != 2 {
		return ErrProtocol
	}
	return nil
}
func (c *Client) RecoveryMaterial(ctx context.Context, id string) (RecoveryMaterial, error) {
	var r RecoveryMaterial
	scope, err := c.recoveryPost(ctx, "material", map[string]string{"id": id}, &r)
	if err != nil {
		return r, err
	}
	if r.Account != scope.Account || r.House != scope.House || r.Generation.ID != id || len(r.Envelope) > 16384 || len(r.Envelope) != r.Generation.Bytes || hashBytes(r.Envelope) != r.Generation.SHA256 {
		return RecoveryMaterial{}, ErrProtocol
	}
	return r, nil
}
func (c *Client) RecoveryConfirm(ctx context.Context, g RecoveryGeneration) error {
	var r struct {
		Generation RecoveryGeneration `json:"generation"`
	}
	_, err := c.recoveryPost(ctx, "confirm", map[string]string{"id": g.ID, "sha256": g.SHA256}, &r)
	if err != nil {
		return err
	}
	if r.Generation.ID != g.ID || r.Generation.SHA256 != g.SHA256 || r.Generation.State != "current" {
		return ErrProtocol
	}
	return nil
}
func (c *Client) RecoveryCancel(ctx context.Context, id string) error {
	var r struct {
		Cancelled bool `json:"cancelled"`
	}
	_, err := c.recoveryPost(ctx, "cancel", map[string]string{"id": id}, &r)
	if err != nil {
		return err
	}
	if !r.Cancelled {
		return ErrProtocol
	}
	return nil
}
func (c *Client) RecoveryDownload(ctx context.Context, b Bundle) ([]byte, error) {
	var r struct {
		ID    string `json:"id"`
		Bytes int64  `json:"bytes"`
		URL   string `json:"downloadUrl"`
	}
	_, err := c.recoveryPost(ctx, "download", map[string]string{"id": b.ID}, &r)
	if err != nil {
		return nil, err
	}
	u, e := url.Parse(r.URL)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || !strings.HasSuffix(u.Hostname(), ".r2.cloudflarestorage.com") || u.Port() != "" || r.ID != b.ID || r.Bytes != b.Bytes || b.Bytes < 1 || b.Bytes > 200000000 {
		return nil, ErrProtocol
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, ErrProtocol
	}
	// Signed URL only; do not forward the grant or a device credential to storage.
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ErrNetwork
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("account: encrypted backup download failed")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, b.Bytes+1))
	if err != nil || int64(len(data)) != b.Bytes {
		return nil, ErrProtocol
	}
	return data, nil
}
