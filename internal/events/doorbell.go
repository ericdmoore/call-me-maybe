package events

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"
)

type Location struct {
	ID      string `json:"id"`
	Through int64  `json:"through,string"`
}
type Doorbell struct {
	Type       string     `json:"type"`
	Version    int        `json:"version"`
	JournalID  string     `json:"journal_id"`
	Generation string     `json:"generation"`
	Locations  []Location `json:"locations"`
}
type DoorbellOptions struct {
	MaxBytes   int64
	URL, Token string
	Timeout    time.Duration
	Log        *slog.Logger
}

// Ring runs independently of the journal writer. Persisted progress is tied to
// the target, so changing endpoints announces existing committed availability.
func Ring(ctx context.Context, path string, o DoorbellOptions) {
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	client := &http.Client{Timeout: o.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	target := digest(o.URL)
	delay := 250 * time.Millisecond
	backoff := time.Second
	var db *sql.DB
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()
	warned := false
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		var err error
		if db == nil {
			db, err = openDoorbellDB(path, o.MaxBytes)
		}
		if err == nil {
			err = ringDB(ctx, db, o, client, target)
		}
		if err != nil {
			if db != nil {
				_ = db.Close()
				db = nil
			}
			if !warned {
				o.Log.Warn("journal doorbell unavailable; will retry", "err", safeDeliveryError(err))
				warned = true
			}
			delay = backoff + time.Duration(rand.Int64N(int64(backoff/4)+1))
			backoff = min(backoff*2, time.Minute)
		} else {
			delay = 250 * time.Millisecond
			backoff = time.Second
			warned = false
		}
	}
}
func ringOnce(ctx context.Context, path string, o DoorbellOptions, client *http.Client, target string) error {
	db, err := openDoorbellDB(path, o.MaxBytes)
	if err != nil {
		return err
	}
	defer db.Close()
	return ringDB(ctx, db, o, client, target)
}
func openDoorbellDB(path string, maxBytes int64) (*sql.DB, error) {
	db, err := connect(path, true)
	if err != nil {
		return nil, err
	}
	var pageSize int64
	err = db.QueryRow("PRAGMA page_size").Scan(&pageSize)
	_ = db.Close()
	if err != nil {
		return nil, err
	}
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	return connect(path, false, "synchronous(FULL)", "wal_autocheckpoint(64)", "journal_size_limit(0)", fmt.Sprintf("max_page_count(%d)", maxBytes/4/pageSize))
}
func ringDB(ctx context.Context, db *sql.DB, o DoorbellOptions, client *http.Client, target string) error {
	// A single statement sees one committed snapshot. No transaction is held
	// across the HTTP request; slow consumers cannot pin the WAL.
	var b Doorbell
	var high, through int64
	err := retryBusy(ctx, func() error {
		return db.QueryRowContext(ctx, `SELECT journal_id,generation,high,COALESCE((SELECT through FROM delivery_state WHERE target=?),0) FROM metadata`, target).Scan(&b.JournalID, &b.Generation, &high, &through)
	})
	if err != nil {
		return fmt.Errorf("read doorbell progress: %w", err)
	}
	if high <= through {
		return nil
	}
	b.Type = "journal.available"
	b.Version = 1
	b.Locations = []Location{{ID: "primary", Through: high}}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "doorman")
	if o.Token != "" {
		req.Header.Set("Authorization", "Bearer "+o.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &deliveryError{Status: resp.StatusCode}
	}
	// Retry only the idempotent acknowledgement, not the already-delivered HTTP
	// request. The writer may hold the lock longer than one busy timeout on disk.
	err = retryBusy(ctx, func() error {
		_, err := db.ExecContext(ctx, `INSERT INTO delivery_state(target,through) VALUES(?,?) ON CONFLICT(target) DO UPDATE SET through=MAX(through,excluded.through)`, target, high)
		return err
	})
	if err != nil {
		return fmt.Errorf("save doorbell progress: %w", err)
	}
	return nil
}

type deliveryError struct{ Status int }

func (e *deliveryError) Error() string { return fmt.Sprintf("doorbell HTTP status %d", e.Status) }
func safeDeliveryError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
