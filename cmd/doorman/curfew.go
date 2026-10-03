package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"callmemaybe/internal/ari"
	"callmemaybe/internal/policy"
)

// curfewKeeper is the third thing a curfew does, and the one neither the
// lobby nor the dialplan can: hang up at the hour. The lobby only sees calls
// it placed, the dialplan only acts when a call starts, and a call that was
// already up at 21:00 — a room-to-room chat, an outbound call — is in
// neither's hands. So the daemon, which owns the clock and an ARI client,
// looks once a minute for a handset that has just fallen asleep and drops
// every channel it is on. Nothing is remembered between restarts: a handset
// found already asleep at startup keeps whatever call it has, because the
// hour that would have dropped it has passed.
type curfewKeeper struct {
	policies func() []*policy.Policy
	channels func(ctx context.Context) ([]ari.Channel, error)
	hangup   func(ctx context.Context, channelID string) error
	now      func() time.Time
	log      *slog.Logger

	asleep map[string]bool // handset id → was asleep at the last tick
}

func (k *curfewKeeper) run(ctx context.Context, every time.Duration) {
	k.tick(ctx) // learn the current state; no call is dropped for being found
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			k.tick(ctx)
		}
	}
}

// tick compares every curfewed handset's state with the last look and drops
// the calls of any that fell asleep in between. A handset present on two
// lines is asleep if either line says so; both read the same inventory.
func (k *curfewKeeper) tick(ctx context.Context) {
	now := k.now()
	current := make(map[string]bool)
	for _, p := range k.policies() {
		if p == nil {
			continue
		}
		for _, id := range p.CurfewedHandsets() {
			current[id] = current[id] || p.Asleep(id, now)
		}
	}
	first := k.asleep == nil
	var fell []string
	for id, asleep := range current {
		if asleep && !first && !k.asleep[id] {
			fell = append(fell, id)
		}
	}
	k.asleep = current
	if len(fell) == 0 {
		return
	}
	k.drop(ctx, fell)
}

// drop hangs up every channel belonging to the named handsets. Only the
// handset id is logged — never the far end's number (invariant 1).
func (k *curfewKeeper) drop(ctx context.Context, ids []string) {
	chans, err := k.channels(ctx)
	if err != nil {
		k.log.Warn("curfew: could not list channels", "err", err)
		return
	}
	for _, id := range ids {
		dropped := 0
		for _, ch := range chans {
			if !channelOf(ch.Name, id) {
				continue
			}
			if err := k.hangup(ctx, ch.ID); err != nil {
				k.log.Warn("curfew: hangup failed", "handset", id, "err", err)
				continue
			}
			dropped++
		}
		k.log.Info("curfew: handset fell asleep", "handset", id, "callsDropped", dropped)
	}
}

// channelOf reports whether an Asterisk channel name belongs to a handset.
// Names are "PJSIP/<id>-<serial>" with a hexadecimal serial, and ids may
// themselves contain dashes ("mary-kate", "grace-2"), so the test is the id,
// a dash, and nothing but hex after it — not a prefix.
func channelOf(name, id string) bool {
	rest, ok := strings.CutPrefix(name, "PJSIP/"+id+"-")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
