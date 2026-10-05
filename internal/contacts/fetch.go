package contacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"callmemaybe/internal/policy"
)

// The fetcher: a url source becomes bytes on disk, and the merge reads the
// disk. Three rules, all of them about what happens when the network is not
// there:
//
//   - Per-source cache, raw vCard as fetched, 0600. A source that fails keeps
//     its last good copy and the others are unaffected — invariant 4's
//     posture applied to a second kind of input.
//   - A source that has never succeeded contributes nothing and stops
//     nobody. It is reported, not fatal.
//   - Nothing here runs on a call path. The daemon fetches in a goroutine at
//     startup and every refresh; a call reads whatever the last fetch left.
//
// The credential goes in an Authorization header, never the URL, so the URL
// is safe to print and an error nobody unwrapped is safe to log.

// Fetcher obtains url sources and keeps their cache.
type Fetcher struct {
	// Dir holds <id>.vcf and <id>.json per source. Created 0700.
	Dir string
	// Secret resolves token_env. nil means no token is ever sent.
	Secret func(name string) (string, bool)
	// Client is the HTTP client; nil means a default with a 30 s timeout.
	Client *http.Client
	// Now is for tests.
	Now func() time.Time
}

// Outcome is what one fetch did, for the log and the journal.
type Outcome struct {
	ID string
	// Status: fresh (new bytes cached), unchanged (304, cache kept), stale
	// (fetch failed, cache served), failed (fetch failed, nothing cached).
	Status string
	Err    error
}

// meta is the cache's memory of the last fetch, beside the bytes.
type meta struct {
	Where        string    `json:"where"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	FetchedAt    time.Time `json:"fetched_at"`            // bytes last obtained
	CheckedAt    time.Time `json:"checked_at"`            // last time the server agreed the bytes are current
	LastAttempt  time.Time `json:"last_attempt"`          // last try, success or not
	LastError    string    `json:"last_error,omitempty"`  // "" after a success
	FailedSince  time.Time `json:"failed_since,omitzero"` // first failure after the last success
}

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (f *Fetcher) dataPath(id string) string { return filepath.Join(f.Dir, id+".vcf") }
func (f *Fetcher) metaPath(id string) string { return filepath.Join(f.Dir, id+".json") }

func (f *Fetcher) readMeta(id string) (meta, bool) {
	b, err := os.ReadFile(f.metaPath(id))
	if err != nil {
		return meta{}, false
	}
	var m meta
	if json.Unmarshal(b, &m) != nil {
		return meta{}, false
	}
	return m, true
}

func (f *Fetcher) writeMeta(id string, m meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(f.metaPath(id), b)
}

// writeAtomic: the daemon reads the cache while a fetch may be writing it,
// and a half-written address book must never be what a call sees.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Cached is a url source as the cache last left it, with no network: the
// document the daemon starts on and the one `doorman check` reports.
func (f *Fetcher) Cached(src policy.ContactSource, refresh time.Duration) Document {
	d := Document{ID: src.ID, Kind: src.Kind, Where: src.URL}
	m, ok := f.readMeta(src.ID)
	data, err := os.ReadFile(f.dataPath(src.ID))
	if !ok || err != nil || m.FetchedAt.IsZero() {
		if ok && m.LastError != "" {
			d.Err = fmt.Errorf("never fetched — last attempt %s failed: %s", ago(f.now(), m.LastAttempt), m.LastError)
		} else {
			d.Err = fmt.Errorf("never fetched — the daemon fetches at startup and every %s; `doorman check --fetch` fetches now", refresh)
		}
		return d
	}
	d.Data, d.FetchedAt = data, m.FetchedAt
	if m.LastError != "" {
		d.Warning = fmt.Sprintf("serving the copy fetched %s — fetching has failed since %s: %s",
			ago(f.now(), m.FetchedAt), ago(f.now(), m.FailedSince), m.LastError)
	}
	return d
}

// Fetch obtains one url source, conditionally, and returns what the cache
// holds afterwards. Never returns an error for the caller to act on: the
// document says what happened, and Outcome says it for the log.
func (f *Fetcher) Fetch(ctx context.Context, src policy.ContactSource, refresh time.Duration) (Document, Outcome) {
	out := Outcome{ID: src.ID}
	m, _ := f.readMeta(src.ID)
	m.Where = src.URL
	now := f.now()
	m.LastAttempt = now

	fail := func(err error) (Document, Outcome) {
		out.Err = err
		if m.LastError == "" || m.FailedSince.IsZero() {
			m.FailedSince = now
		}
		m.LastError = err.Error()
		_ = f.writeMeta(src.ID, m)
		d := f.Cached(src, refresh)
		if d.Err != nil {
			out.Status = "failed"
		} else {
			out.Status = "stale"
		}
		return d, out
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return fail(safeErr(err))
	}
	req.Header.Set("Accept", "text/vcard, text/x-vcard, text/directory;q=0.9, */*;q=0.1")
	req.Header.Set("User-Agent", "doorman (callmemaybe.cc)")
	if src.TokenEnv != "" {
		tok, ok := "", false
		if f.Secret != nil {
			tok, ok = f.Secret(src.TokenEnv)
		}
		if !ok || tok == "" {
			return fail(fmt.Errorf("%s is not set in the environment, so this source was not fetched", src.TokenEnv))
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if !m.FetchedAt.IsZero() {
		if m.ETag != "" {
			req.Header.Set("If-None-Match", m.ETag)
		}
		if m.LastModified != "" {
			req.Header.Set("If-Modified-Since", m.LastModified)
		}
	}

	resp, err := f.client().Do(req)
	if err != nil {
		return fail(safeErr(err))
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified && !m.FetchedAt.IsZero():
		m.CheckedAt, m.LastError, m.FailedSince = now, "", time.Time{}
		_ = f.writeMeta(src.ID, m)
		out.Status = "unchanged"
		return f.Cached(src, refresh), out
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		// The status line and nothing of the body: a body can echo the
		// request, and the request carried the credential.
		return fail(fmt.Errorf("HTTP %s", resp.Status))
	}

	// Bounded: an address book is kilobytes; a megabyte-scale answer is not
	// one, and must not fill the disk.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20+1))
	if err != nil {
		return fail(safeErr(err))
	}
	if len(data) > 16<<20 {
		return fail(errors.New("response larger than 16 MiB — not an address book"))
	}
	if err := writeAtomic(f.dataPath(src.ID), data); err != nil {
		return fail(err)
	}
	m.ETag, m.LastModified = resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	m.FetchedAt, m.CheckedAt, m.LastError, m.FailedSince = now, now, "", time.Time{}
	if err := f.writeMeta(src.ID, m); err != nil {
		return fail(err)
	}
	out.Status = "fresh"
	return f.Cached(src, refresh), out
}

// safeErr strips the URL a *url.Error carries. The credential is in a
// header, so the URL is not a secret — but the operation and the cause are
// the whole of what a log line needs, and the shorter message is the one
// that fits beside a source id.
func safeErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// ago is "2h ago" / "3d ago" / "just now", for reports.
func ago(now, then time.Time) string {
	if then.IsZero() {
		return "never"
	}
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// Age is ago() for callers outside the package.
func Age(now, then time.Time) string { return ago(now, then) }

var _ = strings.TrimSpace
