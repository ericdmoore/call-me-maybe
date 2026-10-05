package ownbook

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var sharedTarget = regexp.MustCompile(`^handset:([a-z0-9][a-z0-9_-]*)$`)

// SharedLocation keeps received cards separate from *88's writer. House
// additions and a handset actually named "house" have distinct paths.
func SharedLocation(dir, target string) (string, string, error) {
	if target == "house" {
		return dir, "house", nil
	}
	if sharedTarget.MatchString(target) {
		return filepath.Join(dir, "handsets"), strings.TrimPrefix(target, "handset:"), nil
	}
	return "", "", fmt.Errorf("invalid phone book target")
}

// Import adds a batch atomically. Retrying a message or re-sharing a card
// preserves existing names and never duplicates a number. The lock covers
// the read/modify/write so overlapping CLI consumers cannot lose entries.
func Import(dir, target string, entries []Entry) (int, error) {
	dir, id, err := SharedLocation(dir, target)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, err
	}
	lock, err := os.OpenFile(Path(dir, id)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 0, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return 0, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	b, err := Load(dir, id)
	if err != nil {
		return 0, err
	}
	added := 0
	for _, entry := range entries {
		entry.UID = fmt.Sprintf("shared-%x", sha256.Sum256([]byte(entry.E164)))
		entry.Named = true
		if b.Upsert(entry) {
			added++
		}
	}
	if added == 0 {
		return 0, nil
	}
	return added, Save(dir, b)
}
