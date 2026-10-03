package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"callmemaybe/internal/ari"
	"callmemaybe/internal/policy"
)

const curfewPolicy = `
[house]
handsets = ["kitchen"]

[[schedules]]
id = "school-night"
start = "21:00"
end = "07:00"
days = ["SU", "MO", "TU", "WE", "TH"]

[[handsets]]
id = "kids-room"
endpoint = "PJSIP/kids-room"
curfew = ["school-night"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[extensions]]
pin = "555001"
label = "Kids"
handsets = ["kids-room"]
`

// The keeper drops a handset's calls at the moment it falls asleep — and
// only then: not every minute of the window, not for a handset found already
// asleep at startup, and never the kitchen's.
func TestCurfewKeeperDropsCallsAtTheHourOnly(t *testing.T) {
	p, err := policy.FromTOML([]byte(curfewPolicy))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 7, 20, 59, 0, 0, time.Local) // Tuesday
	var hungUp []string
	k := &curfewKeeper{
		policies: func() []*policy.Policy { return []*policy.Policy{p, nil} },
		channels: func(context.Context) ([]ari.Channel, error) {
			return []ari.Channel{
				{ID: "c1", Name: "PJSIP/kids-room-00000012"},
				{ID: "c2", Name: "PJSIP/kids-room-2-00000013"}, // another handset whose id merely starts the same
				{ID: "c3", Name: "PJSIP/kitchen-00000014"},
			}, nil
		},
		hangup: func(_ context.Context, id string) error { hungUp = append(hungUp, id); return nil },
		now:    func() time.Time { return now },
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx := context.Background()

	k.tick(ctx) // 20:59 — awake, and the first look never drops anything
	now = now.Add(time.Minute)
	k.tick(ctx) // 21:00 — fell asleep
	if len(hungUp) != 1 || hungUp[0] != "c1" {
		t.Fatalf("hung up %v, want exactly the kids' room's own channel", hungUp)
	}
	now = now.Add(time.Minute)
	k.tick(ctx) // 21:01 — still asleep, nothing new
	if len(hungUp) != 1 {
		t.Errorf("a handset already asleep must not have its calls dropped again: %v", hungUp)
	}

	// A keeper that first looks inside the window learns the state and
	// leaves whatever call is up alone — the hour that would have dropped
	// it has passed.
	fresh := *k
	fresh.asleep = nil
	hungUp = nil
	fresh.tick(ctx)
	if len(hungUp) != 0 {
		t.Errorf("first look inside the window dropped %v", hungUp)
	}
}

func TestCurfewKeeperSurvivesAChannelListFailure(t *testing.T) {
	p, err := policy.FromTOML([]byte(curfewPolicy))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 7, 20, 59, 0, 0, time.Local)
	k := &curfewKeeper{
		policies: func() []*policy.Policy { return []*policy.Policy{p} },
		channels: func(context.Context) ([]ari.Channel, error) { return nil, errors.New("ari down") },
		hangup:   func(context.Context, string) error { t.Fatal("nothing to hang up"); return nil },
		now:      func() time.Time { return now },
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	k.tick(context.Background())
	now = now.Add(time.Minute)
	k.tick(context.Background()) // logs and moves on; the next tick is a fresh look
	if !k.asleep["kids-room"] {
		t.Error("state must advance even when the list fails, or the drop would fire every minute")
	}
}
