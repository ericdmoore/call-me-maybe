package account

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type pendingUpload struct {
	ID        string `json:"id,omitempty"`
	URL       string `json:"url,omitempty"`
	Hash      string `json:"hash"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
}

type Bundle struct {
	ID         string `json:"id"`
	Bytes      int64  `json:"bytes"`
	CreatedAt  int64  `json:"createdAt"`
	ReceivedAt int64  `json:"receivedAt"`
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Upload keeps the encrypted bytes and reservation durable until completion.
// A later run finishes the old reservation before starting another. It never
// claims to have read or decrypted the server's stored object.
func (c *Client) Upload(ctx context.Context, data []byte, createdAt time.Time) (Bundle, error) {
	if len(data) == 0 || len(data) > 200000000 {
		return Bundle{}, errors.New("account: encrypted backup must be between 1 and 200000000 bytes")
	}
	// Sync holds the lock only for a moment at a time; waiting it out keeps the
	// nightly backup from failing on a coincidence.
	s, err := openStoreWait(ctx, c.StateDir, syncLockWait)
	if err != nil {
		return Bundle{}, err
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		return Bundle{}, err
	}
	if v.NeedsLogin || v.Box == "" {
		return Bundle{}, ErrLogin
	}
	hash := hashBytes(data)
	if v.Upload != nil {
		same := v.Upload.Hash == hash && v.Upload.CreatedAt == createdAt.UnixMilli()
		done, err := c.finishUpload(ctx, s, v)
		if err != nil {
			return Bundle{}, err
		}
		if same {
			return done, nil
		}
	}
	if err := saveCiphertext(s.dir, data); err != nil {
		return Bundle{}, err
	}
	v.Upload = &pendingUpload{Hash: hash, Bytes: int64(len(data)), CreatedAt: createdAt.UnixMilli()}
	if err := s.save(v); err != nil {
		return Bundle{}, err
	}
	return c.finishUpload(ctx, s, v)
}

func saveCiphertext(dir string, data []byte) error {
	f, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return ErrState
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return ErrState
	}
	if f.Sync() != nil || f.Close() != nil {
		return ErrState
	}
	if os.Rename(f.Name(), filepath.Join(dir, "upload.age")) != nil {
		return ErrState
	}
	d, err := os.Open(dir)
	if err != nil {
		return ErrState
	}
	defer d.Close()
	if d.Sync() != nil {
		return ErrState
	}
	return nil
}

func readCiphertext(s *store, p *pendingUpload) ([]byte, error) {
	if p.Bytes < 1 || p.Bytes > 200000000 {
		return nil, ErrState
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "upload.age"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrState
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() != p.Bytes {
		return nil, ErrState
	}
	data, err := io.ReadAll(io.LimitReader(f, p.Bytes+1))
	if err != nil || int64(len(data)) != p.Bytes || hashBytes(data) != p.Hash {
		return nil, ErrState
	}
	return data, nil
}

func (c *Client) finishUpload(ctx context.Context, s *store, v *state) (Bundle, error) {
	p := v.Upload
	data, err := readCiphertext(s, p)
	if err != nil {
		return Bundle{}, err
	}
	if p.ID == "" {
		status, headers, body, err := c.authenticated(ctx, s, v, "backup", "/v1/backups/uploads",
			map[string]int64{"declaredBytes": p.Bytes, "createdAt": p.CreatedAt})
		if err != nil {
			return Bundle{}, err
		}
		if status != 201 {
			return Bundle{}, failure(status, headers, body)
		}
		var r struct {
			ID        string            `json:"uploadId"`
			URL       string            `json:"uploadUrl"`
			Headers   map[string]string `json:"uploadHeaders"`
			ExpiresAt int64             `json:"expiresAt"`
			MaxBytes  int64             `json:"maxBytes"`
		}
		if json.Unmarshal(body, &r) != nil || !idShape.MatchString(r.ID) || r.MaxBytes < p.Bytes ||
			r.Headers["Content-Length"] != strconv.FormatInt(p.Bytes, 10) || r.Headers["If-None-Match"] != "*" || len(r.Headers) != 2 {
			return Bundle{}, ErrProtocol
		}
		u, err := url.Parse(r.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" ||
			r.ExpiresAt <= c.now().UnixMilli() || r.ExpiresAt > c.now().Add(16*time.Minute).UnixMilli() {
			return Bundle{}, ErrProtocol
		}
		p.ID, p.URL, p.ExpiresAt = r.ID, r.URL, r.ExpiresAt
		if err := s.save(v); err != nil {
			return Bundle{}, err
		}
	}
	// Complete first on retry: an earlier PUT/completion may have succeeded
	// even when the response was lost. No second PUT is needed in that case.
	done, err := c.completeUpload(ctx, s, v)
	if err == nil {
		return done, nil
	}
	var refused *ServiceError
	if !errors.As(err, &refused) || refused.Code != "not_uploaded" {
		return Bundle{}, err
	}
	if p.ExpiresAt <= c.now().UnixMilli() {
		return Bundle{}, errors.New("account: upload URL expired; reservation must expire before retry")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.URL, bytes.NewReader(data))
	if err != nil {
		return Bundle{}, ErrProtocol
	}
	req.ContentLength = p.Bytes
	req.Header.Set("If-None-Match", "*")
	// The signed URL authorises this request. Never attach a device token or cookie.
	resp, err := c.http.Do(req)
	if err != nil {
		return Bundle{}, ErrNetwork
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPreconditionFailed {
		return Bundle{}, errors.New("account: object upload failed; encrypted bytes retained for retry")
	}
	return c.completeUpload(ctx, s, v)
}

func (c *Client) completeUpload(ctx context.Context, s *store, v *state) (Bundle, error) {
	p := v.Upload
	status, headers, body, err := c.authenticated(ctx, s, v, "backup", "/v1/backups/uploads/complete", map[string]string{"uploadId": p.ID})
	if err != nil {
		return Bundle{}, err
	}
	if status != 200 {
		refusal := failure(status, headers, body)
		if status == 410 || status == 404 || (status == 409 && refusal.(*ServiceError).Code == "size_mismatch") {
			// The server has discarded the reservation. Keep the ciphertext;
			// the next run can declare those exact bytes again.
			p.ID, p.URL, p.ExpiresAt = "", "", 0
			if err := s.save(v); err != nil {
				return Bundle{}, err
			}
		}
		return Bundle{}, refusal
	}
	var answer struct {
		Bundle Bundle `json:"bundle"`
	}
	if json.Unmarshal(body, &answer) != nil || answer.Bundle.ID != p.ID || answer.Bundle.Bytes != p.Bytes ||
		answer.Bundle.CreatedAt != p.CreatedAt || answer.Bundle.ReceivedAt <= 0 {
		return Bundle{}, ErrProtocol
	}
	v.Upload = nil
	if err := s.save(v); err != nil {
		return Bundle{}, err
	}
	_ = os.Remove(filepath.Join(s.dir, "upload.age"))
	return answer.Bundle, nil
}
