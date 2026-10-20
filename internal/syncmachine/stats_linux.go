//go:build linux

package syncmachine

import (
	"os"
	"strconv"
	"strings"
	"time"

	"callmemaybe/internal/syncwire"
	"golang.org/x/sys/unix"
)

// Stats samples current resource pressure only; it does not retain a history.
type Stats struct{ total, idle uint64 }

func (s *Stats) Sample(path, version string) syncwire.Status {
	result := syncwire.Status{Schema: 1, Version: version, SampledAt: time.Now().UnixMilli()}
	var disk unix.Statfs_t
	if unix.Statfs(path, &disk) == nil {
		total, free := int64(disk.Blocks)*int64(disk.Bsize), int64(disk.Bavail)*int64(disk.Bsize)
		if total >= 0 && free >= 0 && total <= 9_007_199_254_740_991 {
			result.DiskTotalBytes, result.DiskFreeBytes = &total, &free
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 3 {
				continue
			}
			n, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil || n < 0 || n > 9_007_199_254_740_991/1024 {
				continue
			}
			value := n * 1024
			switch fields[0] {
			case "MemTotal:":
				result.MemoryTotalBytes = &value
			case "MemAvailable:":
				result.MemoryAvailableBytes = &value
			}
		}
	}
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		line, _, _ := strings.Cut(string(data), "\n")
		fields := strings.Fields(line)
		if len(fields) >= 9 && fields[0] == "cpu" {
			total, idle := uint64(0), uint64(0)
			// Guest fields already contribute to user/nice; do not double count.
			for i, field := range fields[1:9] {
				n, _ := strconv.ParseUint(field, 10, 64)
				total += n
				if i == 3 || i == 4 {
					idle += n
				}
			}
			if s.total > 0 && total > s.total && idle >= s.idle && idle-s.idle <= total-s.total {
				value := float32(1 - float64(idle-s.idle)/float64(total-s.total))
				result.CpuUtilization = &value
			}
			s.total, s.idle = total, idle
		}
	}
	return result
}
