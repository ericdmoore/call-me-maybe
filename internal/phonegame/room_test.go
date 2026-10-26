package phonegame

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"callmemaybe/internal/ari"
)

const rules = `def start(player, deck, keep, opening):
    return {"state": {"phase": "answer", "player": player, "request": 1, "deck": deck}, "commands": [{"op":"say", "line":"test.prompt"}, {"op":"ask_digit", "player":player, "keys":"10", "timeout_s":20}]}
def step(state,event):
    return {"state":dict(state,phase="done"),"commands":[{"op":"say","line":"test.done"}]}
def room_changed(state,players):
    return dict(state,joined_count=len(players))
`

func wav(rate uint32) []byte {
	b := make([]byte, 46)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 38)
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], 2)
	return b
}
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func fixture(t *testing.T) *Pack {
	t.Helper()
	dir := t.TempDir()
	p := Pack{Schema: 1, ID: "free-test", Entry: "game.star", Sources: map[string]string{"game.star": hash([]byte(rules))}, Clips: map[string]Clip{}, MediaPrefix: "games/free-test", Tiers: 1, PerTier: 1, Questions: []Question{{ID: "q1", Tier: 1, Answer: "1"}}, Openings: []string{"test.prompt"}, InitialArgs: []string{"$player", "$deck", "$keep_wrong", "$opening"}}
	for _, id := range []string{"room.lobby", "room.pick", "room.invite", "room.waiting", "room.joined", "room.declined", "room.unavailable", "room.full", "room.solo", "room.group", "room.social", "room.talk", "room.back", "room.left", "room.slot-1", "room.slot-2", "room.slot-3", "room.slot-4", "room.slot-5", "room.slot-6", "room.slot-7", "room.slot-8", "test.prompt", "test.done"} {
		for file, rate := range map[string]uint32{id + ".wav": 8000, id + ".wav16": 16000} {
			if err := os.WriteFile(filepath.Join(dir, file), wav(rate), 0600); err != nil {
				t.Fatal(err)
			}
		}
		p.Clips[id] = Clip{File: id + ".wav", SHA256: hash(wav(8000)), SHA25616: hash(wav(16000))}
	}
	b, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(dir, "game.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "game.star"), []byte(rules), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

type fakeAPI struct {
	mu                           sync.Mutex
	m                            *Manager
	joined, hung, muted, unmuted []string
	plays                        []string
	destroyed                    bool
	busy                         bool
	originated                   int
}

func (f *fakeAPI) Answer(context.Context, string) error { return nil }
func (f *fakeAPI) ChannelVar(_ context.Context, id, _ string) (string, error) {
	if id == "host" {
		return "kitchen", nil
	}
	return "office", nil
}
func (f *fakeAPI) CreateBridge(context.Context) (string, error) { return "bridge", nil }
func (f *fakeAPI) AddToBridge(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joined = append(f.joined, id)
	return nil
}
func (f *fakeAPI) DestroyBridge(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed = true
	return nil
}
func (f *fakeAPI) Play(_ context.Context, _, media, id string) error {
	f.mu.Lock()
	f.plays = append(f.plays, media)
	f.mu.Unlock()
	f.m.Event(ari.Event{Type: "PlaybackFinished", Playback: &ari.Playback{ID: id}})
	return nil
}
func (f *fakeAPI) PlayBridge(ctx context.Context, b, m, id string) error {
	return f.Play(ctx, b, m, id)
}
func (f *fakeAPI) StopPlayback(context.Context, string) error { return nil }
func (f *fakeAPI) MuteInput(_ context.Context, id string, mute bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if mute {
		f.muted = append(f.muted, id)
	} else {
		f.unmuted = append(f.unmuted, id)
	}
	return nil
}
func (f *fakeAPI) Originate(_ context.Context, p ari.OriginateParams) (string, error) {
	f.mu.Lock()
	f.originated++
	f.mu.Unlock()
	f.m.Event(ari.Event{Type: "StasisStart", Args: strings.Split(p.AppArgs, ","), Channel: &ari.Channel{ID: "guest"}})
	return "guest", nil
}
func (f *fakeAPI) Hangup(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hung = append(f.hung, id)
	return nil
}
func (f *fakeAPI) Endpoint(context.Context, string, string) (ari.EndpointState, error) {
	channels := []string{}
	if f.busy {
		channels = append(channels, "existing-call")
	}
	return ari.EndpointState{State: "online", ChannelIDs: channels}, nil
}

type input struct{ channel, key string }

func drive(t *testing.T, f *fakeAPI, inputs []input) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := New(ctx, f, fixture(t), []Handset{{ID: "kitchen", Endpoint: "PJSIP/kitchen", Number: 101}, {ID: "office", Endpoint: "PJSIP/office", Number: 102}})
	if err != nil {
		t.Fatal(err)
	}
	f.m = m
	windows := make(chan struct{}, 1)
	m.onWindow = func(channel, keys string) {
		if len(inputs) == 0 {
			t.Error("unexpected input window")
			cancel()
			return
		}
		next := inputs[0]
		inputs = inputs[1:]
		if next.channel != channel {
			t.Error("wrong player has the keypad")
		}
		if len(inputs) == 0 {
			windows <- struct{}{}
		}
		if next.key == "leave" {
			m.Event(ari.Event{Type: "StasisEnd", Channel: &ari.Channel{ID: channel}})
			return
		}
		if next.key == "sideline" {
			m.Event(ari.Event{Type: "ChannelDtmfReceived", Channel: &ari.Channel{ID: "guest"}, Digit: "1"})
			next.key = "1"
		}
		m.Event(ari.Event{Type: "ChannelDtmfReceived", Channel: &ari.Channel{ID: channel}, Digit: next.key, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)})
	}
	m.Event(ari.Event{Type: "StasisStart", Args: []string{"new"}, Channel: &ari.Channel{ID: "host"}})
	select {
	case <-windows:
	case <-time.After(3 * time.Second):
		t.Fatal("room stuck before final input")
	}
	// Wait for the final playback and deferred cleanup, without racing the actor.
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("room did not clean up")
	}
	m.Close()
	if len(inputs) != 0 {
		t.Fatal("not all expected prompts were reached")
	}
}
func TestAcceptedInvitationAndHotSeatAuthority(t *testing.T) {
	f := &fakeAPI{}
	drive(t, f, []input{{"host", "2"}, {"host", "2"}, {"guest", "1"}, {"host", "1"}, {"host", "sideline"}})
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Join(f.joined, ",") != "host,guest" || len(f.muted) != 1 || !f.destroyed || len(f.hung) != 2 {
		t.Fatalf("bad lifecycle: %+v", f)
	}
	if !strings.Contains(strings.Join(f.plays, ","), "room.group") {
		t.Fatal("group mode not announced")
	}
}
func TestDeclineNeverAdmitsGuest(t *testing.T) {
	f := &fakeAPI{}
	drive(t, f, []input{{"host", "2"}, {"host", "2"}, {"guest", "0"}, {"host", "0"}})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.joined) != 1 || !strings.Contains(strings.Join(f.plays, ","), "room.declined") {
		t.Fatal("declined guest was admitted")
	}
}
func TestBusyRoomIsNotOriginated(t *testing.T) {
	f := &fakeAPI{busy: true}
	drive(t, f, []input{{"host", "2"}, {"host", "2"}, {"host", "0"}})
	if f.originated != 0 {
		t.Fatal("rang an occupied room")
	}
}
func TestTransferredHostIsNeverHungUp(t *testing.T) {
	f := &fakeAPI{}
	drive(t, f, []input{{"host", "leave"}})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hung) != 0 || !f.destroyed {
		t.Fatal("transferred call was hung up")
	}
}
func TestSocialTalkRestoresSidelineMute(t *testing.T) {
	f := &fakeAPI{}
	drive(t, f, []input{{"host", "2"}, {"host", "2"}, {"guest", "1"}, {"host", "1"}, {"host", "9"}, {"host", "2"}, {"host", "0"}, {"host", "1"}})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.muted) != 2 || len(f.unmuted) != 1 {
		t.Fatal("help microphone routes not restored")
	}
}
func TestPackRejectsTamperedSourceAndMissingMedia(t *testing.T) {
	p := fixture(t)
	if _, e := p.Start("host"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(p.root, "game.star"), []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(p.root); e == nil {
		t.Fatal("accepted tampered rules")
	}
	p = fixture(t)
	if e := os.Remove(filepath.Join(p.root, "test.prompt.wav16")); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(p.root); e == nil {
		t.Fatal("accepted missing wideband audio")
	}
}

func TestKeypadDiscardsBufferedAndForeignDigits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &Manager{}
	r := &room{m: m, ctx: ctx, host: "host", events: make(chan ari.Event, 8), gone: map[string]bool{}}
	r.events <- ari.Event{Type: "ChannelDtmfReceived", Channel: &ari.Channel{ID: "host"}, Digit: "1"}
	m.onWindow = func(string, string) {
		r.events <- ari.Event{Type: "ChannelDtmfReceived", Channel: &ari.Channel{ID: "guest"}, Digit: "1"}
		r.events <- ari.Event{Type: "ChannelDtmfReceived", Channel: &ari.Channel{ID: "host"}, Digit: "1", Timestamp: time.Now().Add(-time.Second).Format(time.RFC3339Nano)}
		r.events <- ari.Event{Type: "ChannelDtmfReceived", Channel: &ari.Channel{ID: "host"}, Digit: "2", Timestamp: time.Now().Format(time.RFC3339Nano)}
	}
	d, e := r.digit("host", "12", time.Second)
	if e != nil || d == nil || *d != "2" {
		t.Fatal("stale or foreign keypad input accepted")
	}
}
