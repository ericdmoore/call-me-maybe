// Package account is the command-side client for the hosted account protocol.
// It never participates in call admission. Its entire directory is excluded from
// backups: a replacement must enrol with its own transport key and credentials.
package account

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var (
	ErrState    = errors.New("account: cannot read or safely persist private state")
	ErrBusy     = errors.New("account: another account operation is running")
	ErrLogin    = errors.New("account: sign in with doorman login")
	ErrProtocol = errors.New("account: invalid service response")
	ErrNetwork  = errors.New("account: service connection failed; credentials were not logged")
)

type credential struct {
	Current string `json:"current"`
	Pending string `json:"pending,omitempty"`
}

type device struct {
	Code      string    `json:"code"`
	UserCode  string    `json:"userCode"`
	ExpiresAt time.Time `json:"expiresAt"`
	Interval  int       `json:"interval"`
}

type state struct {
	Version      int                   `json:"version"`
	Box          string                `json:"box,omitempty"`
	House        string                `json:"house,omitempty"`
	EnrolledAt   time.Time             `json:"enrolledAt,omitempty"`
	TransportKey string                `json:"transportKey"`
	Tokens       map[string]credential `json:"tokens"`
	Device       *device               `json:"device,omitempty"`
	NeedsLogin   bool                  `json:"needsLogin,omitempty"`
	Upload       *pendingUpload        `json:"upload,omitempty"`
}

// Status is deliberately incapable of carrying a credential value.
type Status struct {
	Box           string    `json:"box,omitempty"`
	House         string    `json:"house,omitempty"`
	EnrolledAt    time.Time `json:"enrolledAt,omitempty"`
	NeedsLogin    bool      `json:"needsLogin"`
	Capabilities  []string  `json:"capabilities"`
	PendingUpload string    `json:"pendingUpload,omitempty"`
}

type store struct {
	dir  string
	lock *os.File
}

// A cross-process lock spans selection, use and persistence. Two commands must
// never race a pending successor with its predecessor (which revokes the box).
func openStore(stateDir string) (*store, error) {
	dir := filepath.Join(stateDir, "account")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, ErrState
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrState
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, ErrState
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, ErrBusy
	}
	return &store{dir, f}, nil
}

func (s *store) close() {
	_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	_ = s.lock.Close()
}

func (s *store) load() (*state, error) {
	f, err := os.OpenFile(filepath.Join(s.dir, "tokens.bin"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return &state{Version: 1, Tokens: map[string]credential{}}, nil
	}
	if err != nil {
		return nil, ErrState
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 64*1024 {
		return nil, ErrState
	}
	z, err := gzip.NewReader(f)
	if err != nil {
		return nil, ErrState
	}
	defer z.Close()
	plain, err := io.ReadAll(io.LimitReader(z, 64*1024+1))
	if err != nil || len(plain) > 64*1024 {
		return nil, ErrState
	}
	var result state
	if json.Unmarshal(plain, &result) != nil || result.Version != 1 || result.Tokens == nil {
		return nil, ErrState
	}
	if result.Box != "" && (!idShape.MatchString(result.Box) || !idShape.MatchString(result.House)) {
		return nil, ErrState
	}
	for cap, t := range result.Tokens {
		if !validToken(t.Current, cap) || (t.Pending != "" && !validToken(t.Pending, cap)) {
			return nil, ErrState
		}
	}
	return &result, nil
}

// gzip prevents accidental display, not access. Permissions, atomic replacement
// and fsync provide the actual local protection and crash boundary.
func (s *store) save(value *state) error {
	plain, err := json.Marshal(value)
	if err != nil {
		return ErrState
	}
	var data bytes.Buffer
	z := gzip.NewWriter(&data)
	if _, err := z.Write(plain); err != nil {
		return ErrState
	}
	if z.Close() != nil {
		return ErrState
	}
	f, err := os.CreateTemp(s.dir, ".tokens-*")
	if err != nil {
		return ErrState
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data.Bytes()); err != nil {
		return ErrState
	}
	if f.Sync() != nil || f.Close() != nil {
		return ErrState
	}
	if os.Rename(f.Name(), filepath.Join(s.dir, "tokens.bin")) != nil {
		return ErrState
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return ErrState
	}
	defer d.Close()
	if d.Sync() != nil {
		return ErrState
	}
	return nil
}

func (c *Client) Status() (Status, error) {
	s, err := openStore(c.StateDir)
	if err != nil {
		return Status{}, err
	}
	defer s.close()
	value, err := s.load()
	if err != nil {
		return Status{}, err
	}
	result := Status{Box: value.Box, House: value.House, EnrolledAt: value.EnrolledAt, NeedsLogin: value.NeedsLogin || value.Box == ""}
	for _, cap := range []string{"sync", "backup"} {
		if value.Tokens[cap].Current != "" {
			result.Capabilities = append(result.Capabilities, cap)
		}
	}
	if value.Upload != nil {
		result.PendingUpload = value.Upload.ID
	}
	return result, nil
}
