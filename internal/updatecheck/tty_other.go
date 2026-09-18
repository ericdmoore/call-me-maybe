//go:build !linux && !darwin

package updatecheck

import "os"

// Unknown terminal APIs fail closed: no background networking.
func isTerminal(*os.File) bool { return false }
