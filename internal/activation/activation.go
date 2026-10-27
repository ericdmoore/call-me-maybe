// Package activation provides a private local query of the daemon's compiled
// policy revisions. No database, credentials, network listener, or call history
// participates. A file write alone is not evidence of a running policy reload.
package activation

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const DefaultDir = "/var/lib/doorman/sync"

// Serve retries while sync has not yet created its private state directory.
// Failure of this optional observer never interrupts phone service.
func Serve(ctx context.Context, dir string, current func() map[string]string) {
	for ctx.Err() == nil {
		serve(ctx, dir, current)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func serve(ctx context.Context, dir string, current func() map[string]string) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".activation-lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	path := filepath.Join(dir, "active.sock")
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return
	}
	defer listener.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			listener.Close()
		case <-done:
		}
	}()
	for ctx.Err() == nil {
		c, err := listener.Accept()
		if err != nil {
			return
		}
		_ = c.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
		_ = json.NewEncoder(c).Encode(current())
		_ = c.Close()
	}
}

func Read(dir string) (map[string]string, error) {
	c, err := net.DialTimeout("unix", filepath.Join(dir, "active.sock"), 200*time.Millisecond)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var v map[string]string
	err = json.NewDecoder(io.LimitReader(c, 64<<10)).Decode(&v)
	return v, err
}
