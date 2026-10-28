// Package mediadelivery owns the opt-in preview lane. It never runs on the call
// path, edits configuration, or determines ownership locally.
package mediadelivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// JournalError identifies the failed operation without printing private paths
// or untrusted file contents. Callers can inspect the cause with errors.Is/As.
// Every JournalError still matches ErrJournal for existing retry handling.
type JournalError struct {
	Operation string
	cause     error
}

func (e *JournalError) Error() string                    { return "media: receipt journal " + e.Operation + " failed" }
func (e *JournalError) Unwrap() error                    { return e.cause }
func (e *JournalError) Is(target error) bool             { return target == ErrJournal }
func journalFailure(operation string, cause error) error { return &JournalError{operation, cause} }

const MaxPreviewBytes = 1 << 20
const MaxRecordedBytes int64 = 256 << 20

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
	return receive(ctx, source, dir, install, nil)
}

// ReceiveFiles keeps archives in private scratch storage rather than a byte slice.
// It shares the preview outbox and sequence reconciliation, so retries cannot
// duplicate or regress an existing delivery's installation receipt.
func ReceiveFiles(ctx context.Context, source Source, dir string, install func(context.Context, string, string) error) error {
	return receive(ctx, source, dir, nil, install)
}

func receive(ctx context.Context, source Source, dir string, install Install, installFile func(context.Context, string, string) error) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return journalFailure("create-directory", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return journalFailure("stat-directory", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return journalFailure("validate-directory", nil)
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return journalFailure("open-lock", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return journalFailure("lock", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if installFile != nil {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return journalFailure("read-scratch", err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".archive-") {
				if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
					return journalFailure("remove-scratch", err)
				}
			}
		}
	}
	offers, err := source.List(ctx)
	if err != nil {
		return err
	}
	for _, p := range offers {
		// Delivery IDs are opaque, path-free base64url values validated by account.
		if len(p.Delivery) != 43 || filepath.Base(p.Delivery) != p.Delivery || p.Delivery == "." {
			return journalFailure("validate-offer", nil)
		}
		file := filepath.Join(dir, p.Delivery+".json")
		var old pending
		data, readErr := os.ReadFile(file)
		if readErr == nil {
			if len(data) > 2048 || json.Unmarshal(data, &old) != nil || old.SHA256 != p.SHA256 || old.Sequence < 1 || old.Sequence > 1_000_000_000 {
				return journalFailure("decode-receipt", nil)
			}
			if p.Sequence > old.Sequence || (p.Sequence == old.Sequence && p.State != old.State) {
				return journalFailure("reconcile-receipt", nil)
			}
			if p.Sequence < old.Sequence {
				if err := source.Receipt(ctx, p, old.Sequence, old.State, old.Code); err != nil {
					return err
				}
				p.Sequence = old.Sequence
				p.State = old.State
			}
			if err := os.Remove(file); err != nil {
				return journalFailure("remove-receipt", err)
			}
		} else if !os.IsNotExist(readErr) {
			return journalFailure("read-receipt", readErr)
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
			if err := os.Remove(file); err != nil {
				return journalFailure("remove-receipt", err)
			}
			return nil
		}
		if err := report("downloading", ""); err != nil {
			return err
		}
		err = func() error {
			var archive []byte
			var file string
			var err error
			if installFile != nil {
				transport, ok := source.(interface {
					DownloadFile(context.Context, Offer, string) (string, error)
				})
				if !ok {
					return errors.New("media: file transport unavailable")
				}
				file, err = transport.DownloadFile(ctx, p, dir)
				if err == nil && file == "" {
					err = ErrChecksum
				}
				if file != "" {
					if filepath.Dir(file) != filepath.Clean(dir) || !strings.HasPrefix(filepath.Base(file), ".archive-") {
						return ErrChecksum
					}
					defer os.Remove(file)
					f, openErr := os.Open(file)
					if openErr != nil {
						return ErrChecksum
					}
					stat, statErr := f.Stat()
					h := sha256.New()
					n, hashErr := io.Copy(h, io.LimitReader(f, MaxRecordedBytes+1))
					f.Close()
					if statErr != nil || !stat.Mode().IsRegular() || p.Bytes <= 0 || p.Bytes > MaxRecordedBytes || n != p.Bytes || hashErr != nil || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
						err = ErrChecksum
					}
				}
			} else {
				archive, err = source.Download(ctx, p)
			}
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
			if installFile == nil && (p.Bytes <= 0 || p.Bytes > MaxPreviewBytes || int64(len(archive)) != p.Bytes) {
				if err := report("failed", "checksum"); err != nil {
					return err
				}
				return ErrChecksum
			}
			if err := report("downloaded", ""); err != nil {
				return err
			}
			if err := report("installing", ""); err != nil {
				return err
			}
			if installFile != nil {
				err = installFile(ctx, file, p.SHA256)
			} else {
				err = install(ctx, archive, p.SHA256)
			}
			if err != nil {
				if receiptErr := report("failed", "install"); receiptErr != nil {
					return receiptErr
				}
				return errors.New("media: recording installation failed")
			}
			return nil
		}()
		if err != nil {
			return err
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
		return journalFailure("encode-receipt", err)
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".receipt-")
	if err != nil {
		return journalFailure("create-receipt", err)
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return journalFailure("write-receipt", err)
	}
	if closeErr != nil {
		return journalFailure("close-receipt", closeErr)
	}
	if err := os.Rename(f.Name(), file); err != nil {
		return journalFailure("publish-receipt", err)
	}
	d, err := os.Open(filepath.Dir(file))
	if err != nil {
		return journalFailure("open-directory", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return journalFailure("sync-directory", err)
	}
	return nil
}
