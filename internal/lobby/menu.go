package lobby

import (
	"time"

	events "callmemaybe/internal/observation"
	"callmemaybe/internal/policy"
)

// The front menu: "press 1 for orders, 2 for our hours, 3 for the baker" —
// what a public line answers with instead of the PIN lobby.
//
// A small loop over the compiled [[menus]] graph, deliberately not
// internal/story: the story interpreter brings a parser into the call path's
// import graph, and gathers one digit where a challenge answer is several. It
// follows the story's rules rather than its code — no input and wrong input
// are different events, * repeats, and there is a budget.
//
// Two budgets, per call, because a held channel costs something real: a
// provider plan may carry two simultaneous calls, and the house line on the
// same trunk cannot get one in while a robot sits silent through the repeats
// or a toddler holds down *. Whichever is reached first ends the menu where
// silence ends it — the line's on_no_input, behind the challenge when the
// line says so.
const (
	// MenuMaxPlays bounds how many times one call is offered a menu: the
	// first play, every repeat, every replay after a wrong key or a
	// recording.
	MenuMaxPlays = 6
	// MenuMaxDuration bounds the wall clock from the first menu to the
	// handoff.
	MenuMaxDuration = 3 * time.Minute
)

// isMenuKey is a key a menu acts on: a digit, or * to hear it again. # is
// reserved, and A to D are on nobody's keypad.
func isMenuKey(k string) bool {
	return k == "*" || (len(k) == 1 && k[0] >= '0' && k[0] <= '9')
}

// walkMenu runs the line's front menu until the caller is handed somewhere:
// an extension's ring plan, a mailbox, a goodbye, or the line's disposition.
//
// seed is a key the caller already pressed — over the welcome prompt, for an
// admitted caller — taken as a choice at the front menu. known is the name
// that admitted them, "" for a stranger, and decides where silence and the
// ceilings send them: an admitted caller always reaches the house, as in
// noInput. passed says the challenge needs no asking on this call.
//
// Everything that changes as the caller moves — which menu, how many plays,
// whether the challenge has been answered — is a local. A hang-up is
// ctx.Done unwinding the current wait; cleanup in Run is still the only
// teardown.
func (s *Session) walkMenu(seed, known string, passed bool) {
	cur, ok := s.pol.Menu(s.pol.Line().Menu)
	if !ok {
		// compileMenus refuses a [line] menu that names nothing, so this is
		// unreachable from a loaded policy. Silence's ending is the safe one.
		s.disposition(known, "no-digits", passed)
		return
	}

	maxPlays := s.deps.Cfg.MenuMaxPlays
	if maxPlays == 0 {
		maxPlays = MenuMaxPlays
	}
	maxDuration := s.deps.Cfg.MenuMaxDuration
	if maxDuration == 0 {
		maxDuration = MenuMaxDuration
	}
	// The real clock, not Deps.Now: Now is the afterhours clock and a test
	// fixes it, while this is how long a channel has been held.
	deadline := time.Now().Add(maxDuration)

	plays, silent := 0, 0
	for {
		if s.ctx.Err() != nil {
			return
		}
		if plays >= maxPlays || !time.Now().Before(deadline) {
			s.log.Info("menu ceiling reached", "menu", cur.ID, "plays", plays)
			s.disposition(known, "menu-ceiling", passed)
			return
		}
		plays++
		s.rec.Menu = cur.ID

		key := seed
		seed = ""
		if !isMenuKey(key) {
			// The menu clip barges: a caller who knows "3" need not sit
			// through the list.
			key = s.playMedia(s.prompts.Media("menus/"+cur.ID), true)
			if s.ctx.Err() != nil {
				return
			}
			if !isMenuKey(key) {
				key = s.menuKey(min(s.deps.Cfg.FirstDigitTimeout, time.Until(deadline)))
				if s.ctx.Err() != nil {
					return
				}
			}
		}

		switch {
		case key == "":
			silent++
			if silent > cur.Repeat {
				s.log.Info("nobody chose, ending the menu", "menu", cur.ID)
				s.disposition(known, "no-digits", passed)
				return
			}
			continue
		case key == "*":
			silent = 0
			continue
		}
		silent = 0

		choice, ok := cur.Choice(key)
		var ext policy.ResolvedExtension
		if ok && choice.Kind == policy.ChoiceExtension {
			// A disabled extension leaves its choice in place and inert, the
			// way enabled = false does everywhere else: offered again, as a
			// key with no choice would be.
			ext, ok = s.pol.LookupExtensionID(choice.Target)
		}
		if !ok {
			// A wrong key is not a guess at anything, so no limiter hears of
			// it; it costs only a play. Never logged: it is an entered digit.
			s.log.Info("no such choice, offering the menu again", "menu", cur.ID)
			continue
		}

		if choice.Challenge && !passed {
			if !s.challenge() {
				return
			}
			passed = true
		}
		s.rec.Choice = string(choice.Kind) + ":" + choice.Target

		switch choice.Kind {
		case policy.ChoiceExtension:
			s.rec.Extension = ext.Label
			s.observe(events.AdmissionDecided, "menu-extension")
			s.reach(ext)
			return
		case policy.ChoiceVoicemail:
			s.sendToVoicemail(choice.Target)
			return
		case policy.ChoiceMenu:
			next, ok := s.pol.Menu(choice.Target)
			if !ok {
				continue // refused at load; offered again rather than dropped
			}
			cur = next
		case policy.ChoiceRecording:
			// A recording never leaves Stasis, so it is the one leaf with a
			// "next": the menu that offered it, or a goodbye. Back to the
			// menu, a key pressed over the recording is a choice there.
			then := choice.Then
			key := s.playMedia(s.prompts.Media("recordings/"+choice.Target), then == policy.ThenMenu)
			if s.ctx.Err() != nil {
				return
			}
			if then == policy.ThenGoodbye {
				s.dismiss("recording")
				return
			}
			seed = key
		}
	}
}

// menuKey waits for one key a menu acts on, ignoring the rest. "" means the
// window passed, or the call ended — the caller checks ctx.
func (s *Session) menuKey(window time.Duration) string {
	if window <= 0 {
		return ""
	}
	timer := time.NewTimer(window)
	defer timer.Stop()
	for {
		select {
		case ev := <-s.events:
			if ev.kind == evDtmf && isMenuKey(ev.value) {
				return ev.value
			}
		case <-timer.C:
			return ""
		case <-s.ctx.Done():
			return ""
		}
	}
}
