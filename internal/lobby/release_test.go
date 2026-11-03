package lobby

import (
	"context"
	"testing"
	"time"
)

func TestReleaseOwnershipCannotBeReclaimedAfterStasisEnd(t *testing.T) {
	var r channelRelease
	if r.detached() || r.failed() {
		t.Fatal("new channel must remain owned")
	}
	r.begin()
	if !r.detached() || !r.failed() || r.detached() {
		t.Fatal("genuine failure must restore ownership")
	}
	r.begin()
	r.confirmed()
	r.begin()
	if r.failed() || !r.detached() {
		t.Fatal("late HTTP failure reclaimed a released channel")
	}
	r.confirmed()
	if !r.detached() {
		t.Fatal("duplicate StasisEnd changed ownership")
	}
}

type leavingARI struct {
	ARI
	leave func()
}

func (a leavingARI) ContinueToDialplan(ctx context.Context, id, dp, ext string, priority int) error {
	if err := a.ARI.ContinueToDialplan(ctx, id, dp, ext, priority); err != nil {
		return err
	}
	a.leave() // Asterisk emits StasisEnd before the HTTP response arrives.
	return ctx.Err()
}

func TestVoicemailStasisEndBeforeHTTPResponseDoesNotHangUpCaller(t *testing.T) {
	var h *harness
	h = startTuned(t, kidsLadderPolicy, "2025550199", nil, func(d *Deps) {
		d.ARI = leavingARI{ARI: d.ARI, leave: func() { h.sess.CallerLeft() }}
	})
	h.now = time.Date(2026, 7, 7, 21, 30, 0, 0, time.Local)
	h.finishPlayback(t)
	dialPin(h, "555001")
	h.fake.expect(t, "SetChannelVar")
	h.fake.expect(t, "Continue")
	h.waitFinished(t)
	for len(h.fake.calls) > 0 {
		c := <-h.fake.calls
		if c.Name == "Hangup" && c.Args[0] == h.sess.ChannelID {
			t.Fatal("cleanup hung up the caller after successful voicemail release")
		}
	}
	if !h.sess.release.detached() {
		t.Fatal("successful handoff lost ownership")
	}
}
