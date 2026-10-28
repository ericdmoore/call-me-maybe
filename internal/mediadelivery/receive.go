// Package mediadelivery owns the opt-in preview lane. It never runs on the call
// path, edits configuration, or determines ownership locally.
package mediadelivery

import (
	"callmemaybe/internal/mediaformat"
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
const MaxAudiobookBytes = mediaformat.MaxAudiobookBytes

type receiptSource interface {
	List(context.Context) ([]Offer, error)
	Receipt(context.Context, Offer, int64, string, string) error
}
type FileSource interface {
	receiptSource
	DownloadTo(context.Context, Offer, io.Writer) error
}
type FileInstall func(context.Context, string, string) error
type prepareInstall func(context.Context, Offer) (func() error, func(), error)

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
	return receive(ctx, source, dir, "preview", func(ctx context.Context, p Offer) (func() error, func(), error) {
		archive, err := source.Download(ctx, p)
		if err != nil {
			return nil, nil, err
		}
		if p.Bytes <= 0 || p.Bytes > MaxPreviewBytes || int64(len(archive)) != p.Bytes {
			return nil, nil, ErrChecksum
		}
		return func() error { return install(ctx, archive, p.SHA256) }, func() {}, nil
	})
}

func ReceiveAudiobook(ctx context.Context, source FileSource, dir string, install FileInstall) error {
	return receive(ctx, source, dir, "audiobook", func(ctx context.Context, p Offer) (func() error, func(), error) {
		if p.Bytes <= 0 || p.Bytes > MaxAudiobookBytes {
			return nil, nil, ErrChecksum
		}
		file, err := os.CreateTemp(dir, ".archive-")
		if err != nil {
			return nil, nil, errors.New("media: private archive staging failed")
		}
		cleanup := func() { file.Close(); os.Remove(file.Name()) }
		hash := sha256.New()
		if err = file.Chmod(0600); err == nil {
			err = source.DownloadTo(ctx, p, &archiveWriter{dst: io.MultiWriter(file, hash), remaining: p.Bytes})
		}
		if err == nil {
			err = file.Sync()
		}
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		info, statErr := file.Stat()
		if statErr != nil || info.Size() != p.Bytes || hex.EncodeToString(hash.Sum(nil)) != p.SHA256 {
			cleanup()
			return nil, nil, ErrChecksum
		}
		if err = file.Close(); err != nil {
			cleanup()
			return nil, nil, errors.New("media: archive staging failed")
		}
		return func() error { return install(ctx, file.Name(), p.SHA256) }, cleanup, nil
	})
}

func receive(ctx context.Context, source receiptSource, dir, kind string, prepare prepareInstall) error {
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
	// A killed downloader cannot run its deferred cleanup. The exclusive journal
	// lock proves no other pass owns these private temporary archives now.
	directory, err := os.Open(dir)
	if err != nil {
		return journalFailure("clean-archive", err)
	}
	defer directory.Close()
	for {
		entries, readErr := directory.ReadDir(64)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".archive-") && entry.Type().IsRegular() {
				if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
					return journalFailure("clean-archive", err)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return journalFailure("clean-archive", readErr)
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
		install, cleanup, err := prepare(ctx, p)
		if err != nil {
			code := "network"
			if errors.Is(err, ErrChecksum) {
				code = "checksum"
			} else if errors.Is(err, syscall.ENOSPC) {
				code = "no_space"
			}
			if receiptErr := report("failed", code); receiptErr != nil {
				return receiptErr
			}
			return err
		}
		apply := func() error {
			defer cleanup()
			if err := report("downloaded", ""); err != nil {
				return err
			}
			if err := report("installing", ""); err != nil {
				return err
			}
			if err := install(); err != nil {
				code := "install"
				if errors.Is(err, mediaformat.ErrUnsupported) {
					code = "unsupported"
				} else if errors.Is(err, syscall.ENOSPC) {
					code = "no_space"
				}
				if receiptErr := report("failed", code); receiptErr != nil {
					return receiptErr
				}
				return errors.New("media: " + kind + " installation failed")
			}
			return report("installed", "")
		}
		if err := apply(); err != nil {
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

// Enforce the receiver's disk bound even for an alternate FileSource.
type archiveWriter struct {
	dst       io.Writer
	remaining int64
}

func (w *archiveWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, ErrChecksum
	}
	n, err := w.dst.Write(p)
	w.remaining -= int64(n)
	return n, err
}
