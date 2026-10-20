//go:build !linux

package syncmachine

import (
	"callmemaybe/internal/syncwire"
	"time"
)

type Stats struct{}

func (s *Stats) Sample(path, version string) syncwire.Status {
	return syncwire.Status{Schema: 1, Version: version, SampledAt: time.Now().UnixMilli()}
}
