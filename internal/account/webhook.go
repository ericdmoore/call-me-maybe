package account

import (
	"callmemaybe/internal/syncwire"
	"callmemaybe/internal/webhookconfig"
	"encoding/base64"
)

// OpenWebhook uses only this enrolment's private transport key. No plaintext
// webhook destination is persisted in the account or sync command ledger.
func (c *Client) OpenWebhook(box string, item syncwire.QueueItem) (webhookconfig.Config, error) {
	s, err := openStore(c.StateDir)
	if err != nil {
		return webhookconfig.Config{}, err
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		return webhookconfig.Config{}, err
	}
	if v.Box != box || v.NeedsLogin || !v.RemovedAt.IsZero() {
		return webhookconfig.Config{}, ErrLogin
	}
	key, err := base64.RawURLEncoding.DecodeString(v.TransportKey)
	if err != nil {
		return webhookconfig.Config{}, ErrState
	}
	return decryptWebhook(key, box, item)
}
