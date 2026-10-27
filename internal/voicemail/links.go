package voicemail

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A recording link is a file in the links directory named for an
// unguessable token: 128 bits from crypto/rand, base64url without padding,
// so 22 characters. It is a hard link to the message's PCM .wav (a copy
// when the two are on different filesystems) and deliberately not a path
// into INBOX: app_voicemail renumbers msgNNNN when an earlier message is
// deleted, and a link by path would one day play somebody else's message.
// The cost, stated in the RUNBOOK: the link keeps playing for its full
// lifetime even after the message is deleted from the box, and is pruned
// after that.

const (
	tokenBytes = 16
	tokenLen   = 22 // base64url of 16 bytes, unpadded
	linkExt    = ".wav"
)

var tokenEncoding = base64.RawURLEncoding.Strict()

// NewToken draws a token from r (crypto/rand when nil).
func NewToken(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, tokenBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return tokenEncoding.EncodeToString(b), nil
}

// ValidToken is the whole shape check: length, alphabet, and that it
// decodes to exactly 128 bits with no stray trailing bits. Anything that
// fails it never reaches the filesystem.
func ValidToken(s string) bool {
	if len(s) != tokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	b, err := tokenEncoding.DecodeString(s)
	return err == nil && len(b) == tokenBytes
}

// ensureDir makes the links directory 0750 for its owner (the asterisk
// user) when it does not exist yet; one that exists keeps the mode its
// operator gave it.
func ensureDir(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return os.Chmod(dir, 0o750)
}

// MakeLink puts the recording at wav into dir under a fresh token and
// returns the token. A hard link shares the recording's inode, so its mode
// is the recording's own and is not changed here — changing it would change
// the message in the box too; the 0750 directory is what keeps it private.
// A copy is 0640.
func MakeLink(dir, wav string, r io.Reader) (string, error) {
	if err := ensureDir(dir); err != nil {
		return "", err
	}
	token, err := NewToken(r)
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, token+linkExt)
	if err := os.Link(wav, target); err == nil {
		return token, nil
	}
	if err := copyFile(wav, target); err != nil {
		return "", err
	}
	return token, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
	}
	return err
}

// LinkPath is where a valid token's recording lives, and false for any
// string that is not a token.
func LinkPath(dir, token string) (string, bool) {
	if !ValidToken(token) {
		return "", false
	}
	return filepath.Join(dir, token+linkExt), true
}

// Prune removes links older than maxAge and reports how many went. Age is
// the file's modification time, which for a hard link is the recording's
// own: a link lasts maxAge from the message, not from the last time
// anybody played it. Only files named like a link are considered; anything
// else in the directory is somebody's, and stays.
func Prune(dir string, maxAge time.Duration, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, linkExt) || !ValidToken(strings.TrimSuffix(name, linkExt)) || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			if os.Remove(filepath.Join(dir, name)) == nil {
				removed++
			}
		}
	}
	return removed, nil
}
