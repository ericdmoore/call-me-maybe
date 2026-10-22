//go:build linux || darwin

package contentinventory

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"callmemaybe/internal/syncwire"
	"golang.org/x/sys/unix"
)

const readFlags = os.O_RDONLY | syscall.O_NONBLOCK

func allocation(info fs.FileInfo) (string, int64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, false
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), stat.Blocks * 512, true
}

func disk(path string, report *syncwire.ContentInventory) {
	var stat unix.Statfs_t
	if unix.Statfs(path, &stat) != nil {
		return
	}
	block := int64(stat.Bsize)
	if block <= 0 || uint64(stat.Blocks) > uint64(maxBytes/block) ||
		stat.Bfree > stat.Blocks || stat.Bavail > stat.Bfree {
		return
	}
	total := int64(stat.Blocks) * block
	used := int64(stat.Blocks-stat.Bfree) * block
	available := int64(stat.Bavail) * block
	reserved := int64(stat.Bfree-stat.Bavail) * block
	report.CapacityBytes, report.UsedBytes = &total, &used
	report.AvailableBytes, report.ReservedBytes = &available, &reserved
}
