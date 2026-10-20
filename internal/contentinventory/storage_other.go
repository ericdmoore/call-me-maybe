//go:build !linux && !darwin

package contentinventory

import (
	"callmemaybe/internal/syncwire"
	"io/fs"
	"os"
)

const readFlags = os.O_RDONLY

func allocation(fs.FileInfo) (string, int64, bool) { return "", 0, false }
func disk(string, *syncwire.ContentInventory)      {}
