package account

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"callmemaybe/internal/mediadelivery"
)

const mediaPath = "/v1/store/sandbox/packs"
const MaxPreviewBytes = mediadelivery.MaxPreviewBytes

var mediaID = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var mediaHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

// MediaSource is an operator-selected HTTPS origin, never a URL supplied by a
// service response. Credentials remain inside account, and redirects are refused.
// Constructing one does not enable background downloads or change ordinary sync.
type MediaSource struct {
	account   *Client
	origin    string
	downloads map[string]string
}

type MediaOffer = mediadelivery.Offer

func (c *Client) SandboxMedia(origin string) (*MediaSource, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, ErrProtocol
	}
	return &MediaSource{account: c, origin: strings.TrimSuffix(origin, "/"), downloads: map[string]string{}}, nil
}

func (s *MediaSource) request(ctx context.Context, method, path string, data []byte, limit int64) ([]byte, error) {
	_, _, token, err := s.account.syncCredential(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, s.origin+path, bytes.NewReader(data))
	if err != nil {
		return nil, ErrProtocol
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := *s.account.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, ErrNetwork
	}
	defer resp.Body.Close()
	// A media service cannot revoke or promote the account's sync credentials.
	// Only the authoritative account/sync protocol settles rotation and removal.
	if resp.StatusCode != http.StatusOK {
		return nil, &ServiceError{Status: resp.StatusCode, Code: "media_refused"}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, ErrNetwork
	}
	if int64(len(body)) > limit {
		return nil, ErrProtocol
	}
	return body, nil
}

func (s *MediaSource) List(ctx context.Context) ([]MediaOffer, error) {
	data, err := s.request(ctx, http.MethodGet, mediaPath, nil, 64<<10)
	if err != nil {
		return nil, err
	}
	var wire struct {
		Schema string `json:"schema"`
		Packs  []struct {
			MediaOffer
			Download struct {
				URL           string `json:"url"`
				ExpiresAt     int64  `json:"expiresAt"`
				Authorization string `json:"authorization"`
			} `json:"download"`
			Receipts string `json:"receipts"`
		} `json:"packs"`
	}
	if json.Unmarshal(data, &wire) != nil || wire.Schema != "sandbox-pack-delivery-v1" || len(wire.Packs) > 16 {
		return nil, ErrProtocol
	}
	offers := make([]MediaOffer, 0, len(wire.Packs))
	seen := map[string]bool{}
	for _, p := range wire.Packs {
		u, e := url.Parse(p.Download.URL)
		if e != nil || u.Scheme+"://"+u.Host != s.origin || u.User != nil || u.Fragment != "" || u.Path != mediaPath+"/download" || p.Download.Authorization != "sync-bearer" || p.Download.ExpiresAt <= time.Now().UnixMilli() || p.Receipts != mediaPath+"/receipts" || !mediaID.MatchString(p.Delivery) || seen[p.Delivery] || !mediaHash.MatchString(p.SHA256) || p.Bytes <= 0 || p.Bytes > MaxPreviewBytes || p.Sequence < 0 || p.Sequence >= 1_000_000_000 || len(p.SKU) == 0 || len(p.SKU) > 128 || len(p.Version) == 0 || len(p.Version) > 64 || !mediaState(p.State) {
			return nil, ErrProtocol
		}
		seen[p.Delivery] = true
		s.downloads[p.Delivery+"/"+p.SHA256] = u.RequestURI()
		offers = append(offers, p.MediaOffer)
	}
	return offers, nil
}
func mediaState(s string) bool {
	switch s {
	case "offered", "downloading", "downloaded", "installing", "installed", "failed":
		return true
	}
	return false
}

// Download returns bounded, independently hash-verified bytes. Signed addresses
// stay in this source and are never marshalled, persisted, or included in errors.
func (s *MediaSource) Download(ctx context.Context, p MediaOffer) ([]byte, error) {
	download := s.downloads[p.Delivery+"/"+p.SHA256]
	if !strings.HasPrefix(download, mediaPath+"/download?") || p.Bytes <= 0 || p.Bytes > MaxPreviewBytes || !mediaHash.MatchString(p.SHA256) {
		return nil, ErrProtocol
	}
	data, err := s.request(ctx, http.MethodGet, download, nil, p.Bytes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != p.Bytes || hex.EncodeToString(sum[:]) != p.SHA256 {
		return nil, mediadelivery.ErrChecksum
	}
	return data, nil
}

func (s *MediaSource) Receipt(ctx context.Context, p MediaOffer, sequence int64, state, code string) error {
	if !mediaID.MatchString(p.Delivery) || !mediaHash.MatchString(p.SHA256) || sequence < 1 || sequence > 1_000_000_000 || !mediaState(state) || state == "offered" {
		return ErrProtocol
	}
	if state == "failed" {
		switch code {
		case "network", "checksum", "invalid_pack", "no_space", "unsupported", "install":
		default:
			return ErrProtocol
		}
	} else if code != "" {
		return ErrProtocol
	}
	data, _ := json.Marshal(struct {
		Delivery string `json:"delivery"`
		SHA256   string `json:"sha256"`
		Sequence int64  `json:"sequence"`
		State    string `json:"state"`
		Error    string `json:"error,omitempty"`
	}{p.Delivery, p.SHA256, sequence, state, code})
	body, err := s.request(ctx, http.MethodPost, mediaPath+"/receipts", data, 2048)
	if err != nil {
		return err
	}
	var result struct {
		Received bool `json:"received"`
	}
	if json.Unmarshal(body, &result) != nil || !result.Received {
		return ErrProtocol
	}
	return nil
}
