package webhookconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

type Accepted struct {
	Config  Config
	Hash    string
	Managed bool
}
type Store struct {
	path    string
	current atomic.Pointer[Accepted]
}

func Hash(data []byte) string {
	if data == nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func Read(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
		return nil, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(data) > 8192 {
		return nil, ErrInvalid
	}
	return data, nil
}

// Open accepts the current file or the last good private copy. A corrupt edit
// never changes a running endpoint, including across a daemon restart.
func OpenStore(path string) *Store {
	s := &Store{path: path}
	s.current.Store(&Accepted{})
	if s.Reload() != nil {
		if data, err := Read(path + ".last-good"); err == nil {
			if c, err := Parse(data); err == nil {
				s.current.Store(&Accepted{c, Hash(data), true})
			}
		}
	}
	return s
}
func (s *Store) Current() Accepted { return *s.current.Load() }
func (s *Store) Reload() error {
	data, err := Read(s.path)
	if errors.Is(err, os.ErrNotExist) {
		// Removing the file restores the environment default, including on restart.
		if err := os.Remove(s.path + ".last-good"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrInvalid
		}
		s.current.Store(&Accepted{})
		return nil
	}
	if err != nil {
		return ErrInvalid
	}
	hash := Hash(data)
	if s.Current().Managed && s.Current().Hash == hash {
		return nil
	}
	c, err := Parse(data)
	if err != nil {
		return ErrInvalid
	}
	if err := save(s.path+".last-good", data); err != nil {
		return err
	}
	s.current.Store(&Accepted{c, hash, true})
	return nil
}
func save(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".webhook-*")
	if err != nil {
		return ErrInvalid
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil || f.Sync() != nil || f.Close() != nil {
		return ErrInvalid
	}
	if os.Rename(f.Name(), path) != nil {
		return ErrInvalid
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return ErrInvalid
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *Store) Watch(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Reload()
		}
	}
}
