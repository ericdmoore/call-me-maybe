package lobby

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// fakeARI implements the ARI interface, records every call, and lets tests
// drive the state machine by hand — no Asterisk, no network, no trunk. This is
// the mock harness the TypeScript version never had.
type fakeARI struct {
	calls  chan fakeCall
	legSeq atomic.Int64

	// failPlay makes every playback fail to start, which is what a missing
	// sound file looks like from here. The documented behaviour is silence and
	// a call that carries on, so it is worth being able to provoke.
	failPlay atomic.Bool
	// failContinue makes the release into the dialplan fail, which is the one
	// error a caller cannot be left in: the channel is neither ours nor the
	// dialplan's until somebody decides.
	failContinue atomic.Bool
}

type fakeCall struct {
	Name string
	Args []string
}

func newFakeARI() *fakeARI {
	return &fakeARI{calls: make(chan fakeCall, 128)}
}

func (f *fakeARI) record(name string, args ...string) {
	f.calls <- fakeCall{Name: name, Args: args}
}

func (f *fakeARI) AppName() string { return "doorman" }

func (f *fakeARI) Play(_ context.Context, channelID, media, playbackID string) error {
	f.record("Play", channelID, media, playbackID)
	if f.failPlay.Load() {
		return fmt.Errorf("no such sound: %s", media)
	}
	return nil
}

func (f *fakeARI) StopPlayback(_ context.Context, playbackID string) error {
	f.record("StopPlayback", playbackID)
	return nil
}

func (f *fakeARI) Ring(_ context.Context, channelID string) error {
	f.record("Ring", channelID)
	return nil
}

func (f *fakeARI) RingStop(_ context.Context, channelID string) error {
	f.record("RingStop", channelID)
	return nil
}

func (f *fakeARI) Hangup(_ context.Context, channelID string) error {
	f.record("Hangup", channelID)
	return nil
}

func (f *fakeARI) Originate(_ context.Context, p OriginateParams) (string, error) {
	legID := fmt.Sprintf("leg-%d", f.legSeq.Add(1))
	f.record("Originate", p.Endpoint, legID, p.AppArgs, p.CallerID)
	return legID, nil
}

func (f *fakeARI) SetChannelVar(_ context.Context, channelID, name, value string) error {
	f.record("SetChannelVar", channelID, name, value)
	return nil
}

func (f *fakeARI) ContinueToDialplan(_ context.Context, channelID, dpContext, extension string, priority int) error {
	f.record("Continue", channelID, dpContext, extension)
	if f.failContinue.Load() {
		return fmt.Errorf("no such context: %s", dpContext)
	}
	return nil
}

func (f *fakeARI) CreateBridge(context.Context) (string, error) {
	f.record("CreateBridge")
	return "bridge-1", nil
}

func (f *fakeARI) AddToBridge(_ context.Context, bridgeID, channelID string) error {
	f.record("AddToBridge", bridgeID, channelID)
	return nil
}

func (f *fakeARI) DestroyBridge(_ context.Context, bridgeID string) error {
	f.record("DestroyBridge", bridgeID)
	return nil
}

// expect blocks until the fake sees a call with the given name, failing the
// test after a timeout. Calls with other names arriving first fail the test —
// order matters in a phone call.
func (f *fakeARI) expect(t *testing.T, name string) fakeCall {
	t.Helper()
	select {
	case c := <-f.calls:
		if c.Name != name {
			t.Fatalf("expected ARI call %s, got %s(%v)", name, c.Name, c.Args)
		}
		return c
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for ARI call %s", name)
		return fakeCall{}
	}
}

// expectAny blocks until it has seen one call for each name, in any order —
// used where the session legitimately races (e.g. hanging up sibling legs).
func (f *fakeARI) expectAny(t *testing.T, names ...string) []fakeCall {
	t.Helper()
	want := map[string]int{}
	for _, n := range names {
		want[n]++
	}
	var got []fakeCall
	for len(got) < len(names) {
		select {
		case c := <-f.calls:
			if want[c.Name] == 0 {
				t.Fatalf("unexpected ARI call %s(%v), still waiting for %v", c.Name, c.Args, want)
			}
			want[c.Name]--
			got = append(got, c)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out, still waiting for %v", want)
		}
	}
	return got
}

// expectNone asserts no ARI call arrives within the window.
func (f *fakeARI) expectNone(t *testing.T, window time.Duration) {
	t.Helper()
	select {
	case c := <-f.calls:
		t.Fatalf("expected no ARI call, got %s(%v)", c.Name, c.Args)
	case <-time.After(window):
	}
}
