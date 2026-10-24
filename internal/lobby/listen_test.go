package lobby

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/policy"
)

// Dial-a-Story (#155): a menu choice that leads to a shelf, and everything a
// public line puts in front of it.

const libraryPolicy = `
[line]
label = "Midbury Library"
menu  = "front"

[house]
handsets = ["desk"]

[[handsets]]
id = "desk"
endpoint = "PJSIP/desk"

[[people]]
name = "Grandma"
numbers = ["512-555-0100"]

[[shelves]]
id     = "stories"
titles = ["little-red-hen", "three-billy-goats", "the-gingerbread-man"]

[[shelves]]
id     = "grown-ups"
titles = ["moby-dick"]

# On no menu: no caller can reach it.
[[shelves]]
id     = "vault"
titles = ["private-recording"]

[listening]
max_minutes   = 10
max_listeners = 2

[[menus]]
id = "front"

  [[menus.choices]]
  digit            = "1"
  story_of_the_day = "stories"

  [[menus.choices]]
  digit = "2"
  shelf = "stories"

  [[menus.choices]]
  digit     = "3"
  shelf     = "grown-ups"
  challenge = true

  [[menus.choices]]
  digit     = "4"
  recording = "hours"
`

func library(t *testing.T, caller string, tune func(*Deps)) *harness {
	t.Helper()
	return startTuned(t, libraryPolicy, caller, nil, func(d *Deps) {
		d.ChallengeLimiter = NewRateLimiter(true, 3, time.Hour)
		d.Rand = fixedDraw(1, 4) // 3 times 6
		d.Listening = Listening{Group: "library", Count: func(context.Context, string, string) (int, error) { return 0, nil }}
		if tune != nil {
			tune(d)
		}
	})
}

// expectRelease consumes the variables a listener is released with, and the
// release itself, and returns the variables.
func (h *harness) expectRelease(t *testing.T) map[string]string {
	t.Helper()
	vars := map[string]string{}
	for len(vars) < 6 {
		c := h.fake.expect(t, "SetChannelVar")
		if c.Args[0] != h.sess.ChannelID {
			t.Fatalf("set a variable on %s", c.Args[0])
		}
		vars[c.Args[1]] = c.Args[2]
	}
	if c := h.fake.expect(t, "Continue"); c.Args[1] != ListenContext || c.Args[2] != "s" {
		t.Fatalf("released to %v, want [%s] s", c.Args, ListenContext)
	}
	return vars
}

// leaves ends a released call the way Asterisk does — StasisEnd — and checks
// nothing hung the caller up afterwards (invariant 8).
func (h *harness) leaves(t *testing.T) {
	t.Helper()
	h.sess.CallerLeft()
	h.waitFinished(t)
	for {
		select {
		case c := <-h.fake.calls:
			if c.Name == "Hangup" && c.Args[0] == h.sess.ChannelID {
				t.Fatal("the caller was hung up after the listening handoff")
			}
		default:
			return
		}
	}
}

// The done-when, first half: a caller picks the shelf and is released to
// hear it, carrying its titles in order and the line's limits.
func TestShelfChoiceReleasesTheCallerWithItsShelfAndLimits(t *testing.T) {
	h := library(t, "9995550199", nil)
	h.chooseOver(t, "front", "2")
	vars := h.expectRelease(t)
	want := map[string]string{
		"CMM_LISTEN_MODE":    "shelf",
		"CMM_LISTEN_TITLES":  "little-red-hen,three-billy-goats,the-gingerbread-man",
		"CMM_LISTEN_WARNING": "60",
		"CMM_LISTEN_MAX":     "2",
		"CMM_LISTEN_GROUP":   "library",
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s = %q, want %q", k, vars[k], v)
		}
	}
	// Ten minutes from when the call arrived, less the moment it took to
	// choose.
	if s, _ := strconv.Atoi(vars["CMM_LISTEN_SECONDS"]); s < 590 || s > 600 {
		t.Errorf("CMM_LISTEN_SECONDS = %q, want just under 600", vars["CMM_LISTEN_SECONDS"])
	}
	h.leaves(t)
	if rec := h.rec.only(t); rec.Outcome != calls.OutcomeListening || rec.Choice != "shelf:stories" || rec.Menu != "front" {
		t.Fatalf("record = %+v", rec)
	}
}

// The done-when, second: the story of the day is one title, picked by the
// shelf's rule on the line's clock, and the AGI is handed that title alone.
func TestStoryOfTheDayHandsOverOneTitle(t *testing.T) {
	h := library(t, "9995550199", nil)
	h.chooseOver(t, "front", "1")
	vars := h.expectRelease(t)
	pol, _ := policy.FromTOML([]byte(libraryPolicy))
	shelf, _ := pol.Shelf("stories")
	if vars["CMM_LISTEN_MODE"] != "story" || vars["CMM_LISTEN_TITLES"] != shelf.StoryOn(h.now) {
		t.Fatalf("mode %q titles %q, want the story of %s: %q", vars["CMM_LISTEN_MODE"], vars["CMM_LISTEN_TITLES"],
			h.now.Format("2006-01-02"), shelf.StoryOn(h.now))
	}
	h.leaves(t)
	if rec := h.rec.only(t); rec.Outcome != calls.OutcomeListening || rec.Choice != "story_of_the_day:stories" {
		t.Fatalf("record = %+v", rec)
	}
}

// The done-when, third: every key on the line, tried. What any of them can
// release a caller to is exactly the shelves the menu offers — never a title
// from a shelf no menu names, never one from the catalog at large.
func TestATitleOffTheShelfCannotBeReached(t *testing.T) {
	reachable := map[string]bool{}
	for _, key := range "0123456789" {
		h := library(t, "9995550199", nil)
		if c := h.fake.expect(t, "Play"); promptOf(c) != "menus/front" {
			t.Fatalf("played %s", promptOf(c))
		}
		h.dial(string(key))
		h.fake.expect(t, "StopPlayback")
		// Answer the challenge if this key asks it.
		titles := ""
	walk:
		for {
			select {
			case c := <-h.fake.calls:
				switch {
				case c.Name == "Play" && promptOf(c) == "challenge/explain":
					h.sess.PlaybackFinished(c.Args[2])
				case c.Name == "Play" && promptOf(c) == "challenge/6":
					h.sess.PlaybackFinished(c.Args[2])
					h.dial("18")
				case c.Name == "Play" && promptOf(c) == "menus/front":
					break walk // offered the menu again: this key releases nothing
				case c.Name == "Play":
					h.sess.PlaybackFinished(c.Args[2])
				case c.Name == "SetChannelVar" && c.Args[1] == "CMM_LISTEN_TITLES":
					titles = c.Args[2]
				case c.Name == "Continue":
					break walk
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("key %c: the call went nowhere", key)
			}
		}
		for _, title := range strings.Split(titles, ",") {
			if title != "" {
				reachable[title] = true
			}
		}
		h.sess.CallerLeft()
		h.waitFinished(t)
	}
	for _, title := range []string{"little-red-hen", "three-billy-goats", "the-gingerbread-man", "moby-dick"} {
		if !reachable[title] {
			t.Errorf("%s is on an offered shelf and could not be reached", title)
		}
	}
	if reachable["private-recording"] || len(reachable) != 4 {
		t.Fatalf("reachable from the line: %v — want exactly the offered shelves", reachable)
	}
}

// The challenge stands in front of a shelf marked with it: wrong answers end
// the call and never release it; a right one releases it.
func TestTheChallengeStandsInFrontOfAShelf(t *testing.T) {
	t.Run("wrong", func(t *testing.T) {
		h := library(t, "9995550199", func(d *Deps) { d.Rand = fixedDraw(1, 4, 1, 4, 1, 4) })
		h.chooseOver(t, "front", "3")
		h.expectPlays(t, "challenge/explain")
		for i := 0; i < 3; i++ {
			h.question(t, "3", "6")
			h.dial("1#")
			if i < 2 {
				h.expectPlays(t, "challenge/try-again")
			}
		}
		h.expectPlays(t, "good-day")
		h.fake.expect(t, "Hangup")
		h.expectCleanup(t, "Hangup")
		h.waitFinished(t)
		h.fake.expectNone(t, 30*time.Millisecond) // never a variable, never a Continue
		if rec := h.rec.only(t); rec.Outcome != calls.OutcomeDismissed || rec.Reason != "challenge-failed" {
			t.Fatalf("record = %+v", rec)
		}
	})
	t.Run("right", func(t *testing.T) {
		h := library(t, "9995550199", nil)
		h.chooseOver(t, "front", "3")
		h.expectPlays(t, "challenge/explain")
		h.question(t, "3", "6")
		h.dial("18")
		h.expectPlays(t, "challenge/right")
		if vars := h.expectRelease(t); vars["CMM_LISTEN_TITLES"] != "moby-dick" {
			t.Fatalf("released with %v", vars)
		}
		h.leaves(t)
		if rec := h.rec.only(t); rec.Challenge != calls.ChallengePassed || rec.Choice != "shelf:grown-ups" {
			t.Fatalf("record = %+v", rec)
		}
	})
	// An admitted caller's key over the welcome is a choice with the
	// challenge already satisfied, as it is for an extension.
	t.Run("admitted", func(t *testing.T) {
		h := library(t, "5125550100", nil)
		c := h.fake.expect(t, "Play")
		if promptOf(c) != string(PromptWelcomeKnown) {
			t.Fatalf("played %s", promptOf(c))
		}
		h.dial("3")
		h.fake.expect(t, "StopPlayback")
		if vars := h.expectRelease(t); vars["CMM_LISTEN_TITLES"] != "moby-dick" {
			t.Fatalf("released with %v", vars)
		}
		h.leaves(t)
	})
}

// Over the cap, the caller hears that the listening lines are busy and is
// offered the menu again — still doorman's, never released — and a key over
// the phrase is a choice there.
func TestListenerCapOffersTheMenuAgain(t *testing.T) {
	var asked atomic.Int32
	h := library(t, "9995550199", func(d *Deps) {
		d.Listening = Listening{Group: "library", Count: func(_ context.Context, ch, group string) (int, error) {
			if ch != "ch-caller-1" || group != "library" {
				t.Errorf("counted %s in %s", ch, group)
			}
			asked.Add(1)
			return 2, nil
		}}
	})
	h.chooseOver(t, "front", "2")
	h.expectPlays(t, "system/listen-busy", "menus/front")
	if asked.Load() != 1 {
		t.Fatalf("asked the count %d times", asked.Load())
	}
	h.fake.expect(t, "Play") // silence there replays the menu, as anywhere
	h.hangUpAndFinish(t)
	if rec := h.rec.only(t); rec.Outcome != calls.OutcomeAbandoned {
		t.Fatalf("record = %+v, want nobody released", rec)
	}

	h = library(t, "9995550199", func(d *Deps) {
		d.Listening.Count = func(context.Context, string, string) (int, error) { return 5, nil }
	})
	h.chooseOver(t, "front", "2")
	if c := h.fake.expect(t, "Play"); promptOf(c) != "system/listen-busy" {
		t.Fatalf("played %s", promptOf(c))
	}
	h.dial("4")
	h.fake.expect(t, "StopPlayback")
	h.expectPlays(t, "recordings/hours")
	h.hangUpAndFinish(t)
}

// When the count cannot be read, the dialplan's cap is the one that holds:
// the caller is released rather than refused for doorman's lack of an answer.
func TestAnUnreadableCountDefersToTheDialplan(t *testing.T) {
	h := library(t, "9995550199", func(d *Deps) {
		d.Listening.Count = func(context.Context, string, string) (int, error) { return 0, errors.New("ARI unreachable") }
	})
	h.chooseOver(t, "front", "2")
	h.expectRelease(t)
	h.leaves(t)
}

// A release Asterisk refuses leaves the caller ours: back to the menu, and the
// final hangup is not suppressed.
func TestAFailedReleaseOffersTheMenuAgain(t *testing.T) {
	h := library(t, "9995550199", nil)
	h.fake.failContinue.Store(true)
	h.chooseOver(t, "front", "2")
	h.expectRelease(t)
	if c := h.fake.expect(t, "Play"); promptOf(c) != "menus/front" {
		t.Fatalf("played %s, want the menu again", promptOf(c))
	}
	h.sess.CallerGone()
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)
	if rec := h.rec.only(t); rec.Outcome != calls.OutcomeAbandoned {
		t.Fatalf("record = %+v", rec)
	}
}

// A call already near its limit is not released into a story it cannot
// finish.
func TestTooLittleTimeLeftEndsTheCall(t *testing.T) {
	h := library(t, "9995550199", nil)
	if c := h.fake.expect(t, "Play"); promptOf(c) != "menus/front" {
		t.Fatalf("played %s", promptOf(c))
	}
	// Written before the key is posted, and so seen by the session when it
	// reads the key.
	h.sess.held = time.Now().Add(-10*time.Minute + 10*time.Second)
	h.dial("2")
	h.fake.expect(t, "StopPlayback")
	h.expectPlays(t, "good-day")
	h.fake.expect(t, "Hangup")
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)
	if rec := h.rec.only(t); rec.Outcome != calls.OutcomeDismissed || rec.Reason != "listen-time" {
		t.Fatalf("record = %+v", rec)
	}
}

// The busy phrase is a bundled system phrase, rendered with the pack.
func TestListenBusyIsABundledSystemPhrase(t *testing.T) {
	b, err := os.ReadFile("../../prompts/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		System map[string]string `json:"system"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.System["listen-busy"] == "" {
		t.Fatal("listen-busy is not a system phrase in prompts/manifest.json")
	}
}
