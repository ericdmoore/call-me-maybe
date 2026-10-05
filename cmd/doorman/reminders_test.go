package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"callmemaybe/internal/ari"
)

func TestCurfewPreservesReminderCallsButDropsOrdinaryOnes(t *testing.T) {
	var dropped []string
	k := curfewKeeper{
		channels: func(context.Context) ([]ari.Channel, error) {
			return []ari.Channel{
				{ID: "reminder", Name: "PJSIP/kids-room-00000001", AccountCode: "cmm-reminder"},
				{ID: "ordinary", Name: "PJSIP/kids-room-00000002"},
				{ID: "impostor", Name: "PJSIP/kids-room-00000003", AccountCode: "cmm-reminder-fake"},
			}, nil
		},
		hangup: func(_ context.Context, id string) error { dropped = append(dropped, id); return nil },
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	k.drop(context.Background(), []string{"kids-room"})
	if len(dropped) != 2 || dropped[0] != "ordinary" || dropped[1] != "impostor" {
		t.Fatalf("curfew dropped %v", dropped)
	}
}

func TestRemindersCLIHousekeepingAndUsage(t *testing.T) {
	if got := runReminders([]string{"prune", "--spool", t.TempDir()}); got != 0 {
		t.Fatalf("prune exit %d", got)
	}
	for _, args := range [][]string{nil, {"unknown"}, {"prune", "extra"}, {"prune", "--unknown"}} {
		if runReminders(args) != 2 {
			t.Fatalf("usage accepted %v", args)
		}
	}
	if runReminders([]string{"prune", "--spool", "relative"}) != 1 {
		t.Fatal("invalid spool succeeded")
	}
}
