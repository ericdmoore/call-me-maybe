//go:build unix

package policy

import (
	"os"
	"syscall"
)

// copyOwner gives a last good copy the owner of its original, where the
// process is allowed to; a daemon running as the files' owner always is.
func copyOwner(path, from string) {
	info, err := os.Stat(from)
	if err != nil {
		return
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = os.Lchown(path, int(st.Uid), int(st.Gid))
	}
}
