package story_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/story"
)

// teller drives the interpreter without an Asterisk, the same way
// fake_ari_test.go drives the lobby state machine.
type teller struct {
	played []string
	// digits are returned by successive Gathers; "" means a timeout.
	digits []string
	// barge, if set, interrupts playback of the clip whose name contains it.
	barge      string
	bargeDigit string
	gathers    int
	cancel     context.CancelFunc
	// hangupAfter cancels the context once this many clips have played.
	hangupAfter int
}

func (f *teller) Play(ctx context.Context, clip string) (string, error) {
	f.played = append(f.played, clip)
	if f.hangupAfter > 0 && len(f.played) >= f.hangupAfter && f.cancel != nil {
		f.cancel()
	}
	if f.barge != "" && strings.Contains(clip, f.barge) {
		return f.bargeDigit, nil
	}
	return "", nil
}

func (f *teller) Gather(ctx context.Context, timeout time.Duration) (string, error) {
	if f.gathers >= len(f.digits) {
		return "", nil // nothing more to say: a timeout
	}
	d := f.digits[f.gathers]
	f.gathers++
	return d, nil
}

func (f *teller) heard(clip string) bool {
	for _, p := range f.played {
		if p == clip {
			return true
		}
	}
	return false
}

func TestAWalkThroughTheStory(t *testing.T) {
	st := parse(t, src)
	f := &teller{digits: []string{"1", "1"}} // bluff, then invent a name

	res, err := st.Play(context.Background(), f, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !f.heard("entrance-01") || !f.heard("bluff-01") || !f.heard("question-01") {
		t.Errorf("did not walk the expected path: %v", f.played)
	}
	// question has a <goto/> to politely-out, which ends.
	if res.Node != "politely-out" {
		t.Errorf("ended at %q, want politely-out", res.Node)
	}
	if res.Ended != "end" {
		t.Errorf("ended = %q", res.Ended)
	}
}

// A digit during playback is the answer: a listener who knows the story should
// not have to sit through the scene.
func TestBargeInDuringPlaybackCounts(t *testing.T) {
	st := parse(t, src)
	f := &teller{barge: "entrance-01", bargeDigit: "3"}

	res, err := st.Play(context.Background(), f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.heard("entrance-02") {
		t.Error("playback should have stopped at the barge-in")
	}
	if res.Node != "politely-out" {
		t.Errorf("barge-in digit 3 should have gone to politely-out, got %q", res.Node)
	}
}

// The star key repeats the scene from anywhere, with no author involvement and
// no state. A child needs it.
func TestStarRepeatsTheNode(t *testing.T) {
	st := parse(t, src)
	f := &teller{digits: []string{"*", "3"}}

	if _, err := st.Play(context.Background(), f, nil); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range f.played {
		if p == "entrance-01" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("entrance played %d times, want 2 (once, then repeated by *)", n)
	}
}

// The VoiceXML distinction: saying nothing replays the scene, saying the wrong
// thing replays only the choices.
func TestNoInputRepeatsAndWrongInputReprompts(t *testing.T) {
	st := parse(t, src)

	// on-timeout: repeat — the whole scene comes back.
	quiet := &teller{digits: []string{"", "3"}}
	if _, err := st.Play(context.Background(), quiet, nil); err != nil {
		t.Fatal(err)
	}
	scenes := 0
	for _, p := range quiet.played {
		if p == "entrance-01" {
			scenes++
		}
	}
	if scenes < 2 {
		t.Errorf("a timeout should replay the scene, played: %v", quiet.played)
	}

	// on-invalid: reprompt — only the choices, not the scene.
	wrong := &teller{digits: []string{"7", "3"}}
	if _, err := st.Play(context.Background(), wrong, nil); err != nil {
		t.Fatal(err)
	}
	scenes = 0
	for _, p := range wrong.played {
		if p == "entrance-01" {
			scenes++
		}
	}
	if scenes != 1 {
		t.Errorf("a wrong digit should reprompt, not replay the scene: %v", wrong.played)
	}
	if !wrong.heard("choice/entrance-1") {
		t.Errorf("the choices should have been repeated: %v", wrong.played)
	}
}

// Running out of retries follows on-timeout, which a children's story points
// at a gentle ending rather than a hangup.
func TestExhaustedRetriesFollowTheRule(t *testing.T) {
	st := parse(t, src)
	// bluff has retries=1 and on-timeout="#politely-out".
	f := &teller{digits: []string{"1", "", ""}}

	res, err := st.Play(context.Background(), f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Node != "politely-out" {
		t.Errorf("ended at %q, want the on-timeout anchor politely-out", res.Node)
	}
}

// Hanging up is the commonest ending and arrives only as a cancelled context.
func TestHangupEndsTheStory(t *testing.T) {
	st := parse(t, src)
	ctx, cancel := context.WithCancel(context.Background())
	f := &teller{cancel: cancel, hangupAfter: 1, digits: []string{"1", "1", "1"}}

	res, err := st.Play(ctx, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Ended != "hangup" {
		t.Errorf("ended = %q, want hangup", res.Ended)
	}
	if len(f.played) > 2 {
		t.Errorf("playback continued after the caller left: %v", f.played)
	}
}

// ── the limits ──────────────────────────────────────────────────────────

const loop = `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
<goto anchor="#b"/>
N: Round.

## b
<goto anchor="#a"/>
N: And round.
`

// A cycle is legitimate in this genre — "you wander back to the entrance" is
// the shape of it — so it cannot be rejected at build time and gets a budget.
func TestACycleIsValidButBudgeted(t *testing.T) {
	st := parse(t, loop) // parses and validates cleanly

	_, err := st.Play(context.Background(), &teller{}, nil)
	if !errors.Is(err, story.ErrLimit) {
		t.Fatalf("an infinite cycle should hit the transition budget, got %v", err)
	}
}

func TestTheWallClockIsEnforced(t *testing.T) {
	st := parse(t, loop)

	// A clock that jumps a minute per reading reaches an hour quickly.
	now := time.Now()
	clock := func() time.Time {
		now = now.Add(time.Minute)
		return now
	}
	res, err := st.Play(context.Background(), &teller{}, clock)
	if !errors.Is(err, story.ErrLimit) {
		t.Fatalf("want ErrLimit, got %v", err)
	}
	if res.Elapsed < story.MaxDuration {
		t.Errorf("stopped after %v, before the %v limit", res.Elapsed, story.MaxDuration)
	}
	if res.Transitions > story.MaxTransitions {
		t.Error("the clock limit should have fired before the transition budget")
	}
}

func TestLimitsAreGenerousEnoughForARealStory(t *testing.T) {
	if story.MaxDuration < 45*time.Minute {
		t.Errorf("MaxDuration = %v; a child will listen longer than that, and being "+
			"cut off mid-story is worse than a long call", story.MaxDuration)
	}
	if story.MaxTransitions < 1000 {
		t.Errorf("MaxTransitions = %d is too tight for a long browse", story.MaxTransitions)
	}
}

func TestSfxPlaysFromThePacksOwnDirectory(t *testing.T) {
	st := parse(t, `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
<sfx name="door-creak"/>
N: The door gives.
<end/>
`)
	f := &teller{}
	if _, err := st.Play(context.Background(), f, nil); err != nil {
		t.Fatal(err)
	}
	if !f.heard("sfx/door-creak") {
		t.Errorf("sfx did not play, or escaped the pack: %v", f.played)
	}
}
