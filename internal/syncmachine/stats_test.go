package syncmachine

import (
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"callmemaybe/internal/syncwire"
)

func TestStatsOnlySendBoundedCurrentMeasurements(t *testing.T) {
	stats := &Stats{}
	for range 2 {
		value := stats.Sample(t.TempDir(), "test-build")
		body, err := json.Marshal(value)
		if err != nil || syncwire.Decode("Status", body, &value) != nil {
			t.Fatal("invalid resource status")
		}
		if value.SampledAt < time.Now().Add(-time.Minute).UnixMilli() {
			t.Fatal("stale sample")
		}
		if runtime.GOOS == "linux" {
			if value.DiskTotalBytes == nil || value.DiskFreeBytes == nil || *value.DiskFreeBytes > *value.DiskTotalBytes {
				t.Fatal("missing or inconsistent disk measurement")
			}
			if value.MemoryTotalBytes == nil || value.MemoryAvailableBytes == nil || *value.MemoryAvailableBytes > *value.MemoryTotalBytes {
				t.Fatal("missing or inconsistent memory measurement")
			}
		}
	}
}
