package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// bellHandshake bounds the WebSocket opening handshake. An established bell
// has no deadline of its own: the caller's heartbeat decides when it is dead.
const bellHandshake = 15 * time.Second

// BellRefused is the service declining the bell for the credential this box
// holds (401 unauthorized, 403 sync_required). The same credential would get
// the same answer, so a caller waits for SyncCredentialTag to change rather
// than retrying. It carries only the status and a recognised code.
type BellRefused struct {
	Status int
	Code   string
}

func (e *BellRefused) Error() string {
	return fmt.Sprintf("account: bell refused for this credential (%d, %s)", e.Status, e.Code)
}

// DialBell opens the sync bell: an outbound WebSocket on which the service
// only ever says "sync now". The credential is the sync token, sent in the
// Authorization header exactly as for every other sync endpoint, and never
// leaves this package. The returned connection carries no authority: nothing
// read from it is configuration.
//
// A refused upgrade is an ordinary HTTP answer and is reported the way Sync
// reports one: box_removed is recorded and returned as ErrRemoved, 401 and
// 403 as *BellRefused, anything else (404 from a service without a bell, a 426
// when something on the path stripped the upgrade, 429, 5xx) as the
// categorical *ServiceError. Transport failures are ErrNetwork; no error
// carries a URL or a server body.
func (c *Client) DialBell(ctx context.Context) (*websocket.Conn, error) {
	box, _, token, err := c.syncCredential(ctx)
	if err != nil {
		return nil, err
	}
	d := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: bellHandshake,
		ReadBufferSize: 1024, WriteBufferSize: 1024}
	if t, ok := c.http.Transport.(*http.Transport); ok {
		d.Proxy, d.TLSClientConfig = t.Proxy, t.TLSClientConfig
	}
	target := "wss://" + strings.TrimPrefix(c.origin, "https://")
	if strings.HasPrefix(c.origin, "http://") {
		target = "ws://" + strings.TrimPrefix(c.origin, "http://")
	}
	conn, resp, err := d.DialContext(ctx, target+"/v1/sync/bell", http.Header{"Authorization": {"Bearer " + token}})
	if err == nil {
		return conn, nil
	}
	if resp == nil {
		return nil, ErrNetwork
	}
	// The dialer has already buffered a short prefix of the refusal body.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if removed(resp.StatusCode, body) {
		applied, err := c.settleRemoved(ctx, box)
		if err != nil {
			return nil, err
		}
		if !applied {
			return nil, ErrSuperseded
		}
		return nil, ErrRemoved
	}
	refusal := failure(resp.StatusCode, resp.Header, body).(*ServiceError)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &BellRefused{Status: refusal.Status, Code: refusal.Code}
	}
	return nil, refusal
}

// SyncCredentialTag names the sync credential without revealing it: a short
// digest that changes when a login or a rotation stores a new one. A caller
// refused for one credential compares tags instead of asking the service
// again. The tag is not a secret worth logging either; nobody logs it.
func (c *Client) SyncCredentialTag(ctx context.Context) (string, error) {
	_, _, token, err := c.syncCredential(ctx)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8]), nil
}
