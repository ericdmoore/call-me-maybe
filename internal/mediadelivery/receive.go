// Package mediadelivery owns the opt-in preview lane. It never runs on the call
// path, edits configuration, or determines ownership locally.
package mediadelivery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Offer carries no credential, URL or installation path.
type Offer struct {
	Delivery string `json:"delivery"`
	SKU      string `json:"sku"`
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	State    string `json:"state"`
	Sequence int64  `json:"sequence"`
}

var ErrChecksum = errors.New("media: archive integrity check failed")

type Source interface {
	List(context.Context) ([]Offer, error)
	Download(context.Context, Offer) ([]byte, error)
	Receipt(context.Context, Offer, int64, string, string) error
}
type Install func(context.Context, []byte, string) error

var ErrJournal = errors.New("media: cannot safely reconcile delivery receipt")

type pending struct {
	Sequence int64  `json:"sequence"`
	State    string `json:"state"`
	Code     string `json:"code,omitempty"`
	SHA256   string `json:"sha256"`
}

// Receive performs one media pass. An fsynced outbox precedes every receipt;
// interruption replays the same sequence instead of inventing a new transition.
// The installer must be atomic and idempotent for the exact archive hash.
func Receive(ctx context.Context, source Source, dir string, install Install) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ErrJournal
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ErrJournal
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrJournal
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return ErrJournal
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	offers, err := source.List(ctx)
	if err != nil {
		return err
	}
	for _, p := range offers {
		// Delivery IDs are opaque, path-free base64url values validated by account.
		if len(p.Delivery) != 43 || filepath.Base(p.Delivery) != p.Delivery || p.Delivery == "." {
			return ErrJournal
		}
		file := filepath.Join(dir, p.Delivery+".json")
		var old pending
		data, readErr := os.ReadFile(file)
		if readErr == nil {
			if len(data) > 2048 || json.Unmarshal(data, &old) != nil || old.SHA256 != p.SHA256 || old.Sequence < 1 || old.Sequence > 1_000_000_000 {
				return ErrJournal
			}
			if p.Sequence > old.Sequence || (p.Sequence == old.Sequence && p.State != old.State) {
				return ErrJournal
			}
			if p.Sequence < old.Sequence {
				if err := source.Receipt(ctx, p, old.Sequence, old.State, old.Code); err != nil {
					return err
				}
				p.Sequence = old.Sequence
				p.State = old.State
			}
			if err := os.Remove(file); err != nil {
				return ErrJournal
			}
		} else if !os.IsNotExist(readErr) {
			return ErrJournal
		}
		if p.State == "installed" {
			continue
		}
		report := func(state, code string) error {
			next := pending{p.Sequence + 1, state, code, p.SHA256}
			if err := save(file, next); err != nil {
				return err
			}
			if err := source.Receipt(ctx, p, next.Sequence, state, code); err != nil {
				return err
			}
			p.Sequence = next.Sequence
			p.State = state
			if os.Remove(file) != nil {
				return ErrJournal
			}
			return nil
		}
		if err := report("downloading", ""); err != nil {
			return err
		}
		archive, err := source.Download(ctx, p)
		if err != nil {
			code := "network"
			if errors.Is(err, ErrChecksum) {
				code = "checksum"
			}
			if receiptErr := report("failed", code); receiptErr != nil {
				return receiptErr
			}
			return err
		}
		if err := report("downloaded", ""); err != nil {
			return err
		}
		if err := report("installing", ""); err != nil {
			return err
		}
		if err := install(ctx, archive, p.SHA256); err != nil {
			if receiptErr := report("failed", "install"); receiptErr != nil {
				return receiptErr
			}
			return errors.New("media: preview installation failed")
		}
		if err := report("installed", ""); err != nil {
			return err
		}
	}
	return nil
}
func save(file string, p pending) error {
	data, err := json.Marshal(p)
	if err != nil {
		return ErrJournal
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".receipt-")
	if err != nil {
		return ErrJournal
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrJournal
	}
	if os.Rename(f.Name(), file) != nil {
		return ErrJournal
	}
	d, err := os.Open(filepath.Dir(file))
	if err != nil {
		return ErrJournal
	}
	defer d.Close()
	if d.Sync() != nil {
		return ErrJournal
	}
	return nil
}
