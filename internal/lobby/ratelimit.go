package lobby

import (
	"sync"
	"time"
)

// RateLimiter tracks failed lobby attempts per caller.
//
// Extensions double as PINs, so the lobby is a 6-digit keypad exposed to the
// PSTN. Six digits is a million combinations, which is plenty against a human
// but not against a machine that can redial. Once a number has burned through
// its budget it skips the greeting entirely and gets "Good day" immediately —
// no prompt, no dial window, nothing to brute-force against.
//
// Deliberately in-memory: a restart forgiving an attacker is fine, and it
// keeps state off the disk.
type RateLimiter struct {
	mu      sync.Mutex
	enabled bool
	max     int
	window  time.Duration
	entries map[string][]time.Time
}

func NewRateLimiter(enabled bool, maxFailures int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		enabled: enabled,
		max:     maxFailures,
		window:  window,
		entries: make(map[string][]time.Time),
	}
}

func (r *RateLimiter) prune(key string, now time.Time) []time.Time {
	cutoff := now.Add(-r.window)
	kept := r.entries[key][:0]
	for _, at := range r.entries[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(r.entries, key)
		return nil
	}
	r.entries[key] = kept
	return kept
}

// Blocked reports whether this caller should be dismissed without a greeting.
func (r *RateLimiter) Blocked(key string, now time.Time) bool {
	if !r.enabled {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prune(key, now)) >= r.max
}

// Failure records a failed attempt and returns the count inside the window.
func (r *RateLimiter) Failure(key string, now time.Time) int {
	if !r.enabled {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[key] = append(r.prune(key, now), now)
	return len(r.entries[key])
}

// Success wipes the slate — a correct extension clears the caller's record.
func (r *RateLimiter) Success(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, key)
}

// Sweep drops entries whose window has fully elapsed. Call periodically so
// memory does not creep on a long-lived process.
func (r *RateLimiter) Sweep(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.entries {
		r.prune(key, now)
	}
}

func (r *RateLimiter) Size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}
