package lobby

import (
	"testing"
	"time"
)

func TestLimiterBlocksAfterBudgetSpent(t *testing.T) {
	r := NewRateLimiter(true, 3, time.Minute)
	now := time.Now()
	key := "+15125550100"

	if r.Blocked(key, now) {
		t.Fatal("blocked before any failures")
	}
	r.Failure(key, now)
	r.Failure(key, now)
	if r.Blocked(key, now) {
		t.Fatal("blocked before budget spent")
	}
	r.Failure(key, now)
	if !r.Blocked(key, now) {
		t.Fatal("not blocked after budget spent")
	}
}

func TestLimiterForgivesAfterWindow(t *testing.T) {
	r := NewRateLimiter(true, 3, time.Minute)
	t0 := time.Now()
	key := "+15125550100"
	for range 3 {
		r.Failure(key, t0)
	}
	if !r.Blocked(key, t0) {
		t.Fatal("should be blocked at t0")
	}
	if r.Blocked(key, t0.Add(61*time.Second)) {
		t.Fatal("should be forgiven after the window")
	}
}

func TestLimiterSuccessWipesSlate(t *testing.T) {
	r := NewRateLimiter(true, 3, time.Minute)
	now := time.Now()
	key := "+15125550100"
	for range 3 {
		r.Failure(key, now)
	}
	r.Success(key)
	if r.Blocked(key, now) {
		t.Fatal("success should clear the record")
	}
}

func TestLimiterDisabledIsNoop(t *testing.T) {
	r := NewRateLimiter(false, 3, time.Minute)
	now := time.Now()
	for range 10 {
		r.Failure("x", now)
	}
	if r.Blocked("x", now) {
		t.Fatal("disabled limiter must never block")
	}
}

func TestLimiterSweepDropsExpired(t *testing.T) {
	r := NewRateLimiter(true, 3, time.Minute)
	t0 := time.Now()
	r.Failure("a", t0)
	r.Failure("b", t0)
	if r.Size() != 2 {
		t.Fatalf("size = %d", r.Size())
	}
	r.Sweep(t0.Add(61 * time.Second))
	if r.Size() != 0 {
		t.Fatalf("size after sweep = %d", r.Size())
	}
}
