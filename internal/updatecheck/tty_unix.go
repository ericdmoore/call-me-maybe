//go:build linux || darwin

package updatecheck

import (
	"os"
	"syscall"
	"unsafe"
)

func isTerminal(f *os.File) bool {
	var term syscall.Termios
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), terminalRequest, uintptr(unsafe.Pointer(&term)))
	return err == 0
}
