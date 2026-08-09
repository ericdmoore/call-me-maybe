package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"callmemaybe/internal/lobby"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/render"
)

// Outbound identity: which of this box's numbers a call leaves as.
//
// Inbound, the dialplan names the line and doorman looks up a policy. Outbound
// there is nothing to look up from — a handset picking up and dialling says
// nothing about which venture it is calling for. So the answer has to be
// configured, and it is configured in two places that resolve to one rule:
//
//	*4 selection  →  the handset's default  →  the primary line
//
// Both chains bottom out at policy.toml, the same file that carries 911, so
// "which line am I on when I have not said" has one answer rather than two
// that can drift apart. See .plans/s01-multiple-DIDs/readme.md.
//
// The claim is written in the *line's* file — [line] outbound_handsets — and
// not as a `line = "biz"` key on each handset. handsets.toml is one shared
// hardware inventory and cannot hold something that is true of only one line;
// a claim in the line's own file disappears when that file is deleted, which
// is the rollback story for everything else here too; and "two lines claim the
// same phone" becomes a question something can answer, because this is the
// only thing in the system that reads every line at once.

// lineIdentity pairs a line name with the [line] section it resolved to. The
// three callers — the daemon, `doorman check`, and `doorman render` — reach
// their policies by different routes and agree on this much.
type lineIdentity struct {
	Name string
	policy.LineIdentity
}

// outboundLine is one line as the outbound side sees it.
type outboundLine struct {
	Name  string
	Label string
	// CID is what a call placed as this line presents, or "" for whatever the
	// trunk defaults to — which is what every outbound call did before any of
	// this existed.
	CID string
	// Trunk is the provider a call placed as this line leaves by, or "" for
	// the dialplan's DEFAULT_TRUNK. It travels beside CID everywhere the two
	// go, because a provider will not carry a number its account does not own.
	Trunk string
	// Number is the line's own DID. Not an outbound value at all — it is here
	// because it is the only evidence this box has about which numbers a trunk
	// owns, and that is what makes a caller ID checkable at all.
	Number string
	// Handsets are the ids that call as this line without being asked.
	Handsets []string
}

// outboundConflict is one handset claimed by two lines. Not an error at
// runtime — the phone must keep working — but never a silent one either: the
// wrong customer sees the wrong number and nothing about the call looks wrong
// from this end.
type outboundConflict struct {
	Handset string
	Winner  string
	Loser   string
}

// outboundPlan answers "what does this phone present" for every phone.
type outboundPlan struct {
	// Lines is every line, primary first. This is also the order the *4 menu
	// offers them in, so the digit that reaches a line is stable across
	// restarts and does not move when a policy file is added.
	Lines []outboundLine
	// Primary is the default line — policy.toml — and the answer for any
	// handset nobody claimed.
	Primary outboundLine
	// Conflicts are handsets more than one line claimed, in the order found.
	Conflicts []outboundConflict

	byHandset map[string]outboundLine
}

// newOutboundPlan resolves the claims. ids must be primary-first, which is
// what policy.DiscoverLines produces: iterating in that order means the
// primary line wins a contested handset, and the result never depends on map
// iteration or on where a block sits in a file.
func newOutboundPlan(ids []lineIdentity) outboundPlan {
	plan := outboundPlan{byHandset: map[string]outboundLine{}}
	for _, id := range ids {
		l := outboundLine{
			Name:     id.Name,
			Label:    id.Label,
			CID:      id.OutboundCID,
			Trunk:    id.Trunk,
			Number:   id.Number,
			Handsets: id.OutboundHandsets,
		}
		plan.Lines = append(plan.Lines, l)
		if id.Name == policy.DefaultLine {
			plan.Primary = l
		}
		for _, h := range id.OutboundHandsets {
			if prev, taken := plan.byHandset[h]; taken {
				plan.Conflicts = append(plan.Conflicts,
					outboundConflict{Handset: h, Winner: prev.Name, Loser: l.Name})
				continue
			}
			plan.byHandset[h] = l
		}
	}
	return plan
}

// lineFor returns the line a handset calls as, and whether it was claimed
// rather than falling back to the primary.
func (p outboundPlan) lineFor(handsetID string) (outboundLine, bool) {
	if l, ok := p.byHandset[handsetID]; ok {
		return l, true
	}
	return p.Primary, false
}

// identities maps handset id to what that phone calls with — caller ID and
// trunk together — for `doorman render` to write into the generated PJSIP
// config. Handsets that would carry neither are absent rather than
// present-and-empty: no entry means no set_var, which means the endpoint is
// byte-identical to what it was before this feature existed.
func (p outboundPlan) identities(handsetIDs []string) map[string]render.OutboundIdentity {
	out := make(map[string]render.OutboundIdentity, len(handsetIDs))
	for _, id := range handsetIDs {
		line, _ := p.lineFor(id)
		if v := (render.OutboundIdentity{CID: line.CID, Trunk: line.Trunk}); v != (render.OutboundIdentity{}) {
			out[id] = v
		}
	}
	return out
}

// consoleLines is the *4 menu.
func (p outboundPlan) consoleLines() []lobby.ConsoleLine {
	out := make([]lobby.ConsoleLine, 0, len(p.Lines))
	for _, l := range p.Lines {
		out = append(out, lobby.ConsoleLine{
			Name: l.Name, Label: l.Label, CallerID: l.CID, Trunk: l.Trunk,
		})
	}
	return out
}

// ── outbound_cid against the trunk that will carry it ────────────────────

// cidVerdict is what can be said statically about one line presenting one
// number down one trunk.
type cidVerdict int

const (
	// cidWrongTrunk is provable and refused: the number this line presents is
	// declared, in this very config, as a number that lives at a different
	// provider. A trunk will not carry a caller ID its account does not own —
	// it rejects the call or silently rewrites it as anti-spoofing — so this is
	// a support ticket waiting to be raised by a customer who saw the wrong
	// number.
	cidWrongTrunk cidVerdict = iota
	// cidUnknownNumber is not provable either way and is reported, not
	// refused. A DID you own but do not answer here has no [line] number, so
	// there is nothing to match it against — a completely legitimate config,
	// and indistinguishable from a typo.
	cidUnknownNumber
	// cidNoTrunk is a line that presents a number but names no provider, with
	// a trunks.toml in play. The call leaves by the dialplan's DEFAULT_TRUNK,
	// which nothing here can read, so nothing here can check it.
	cidNoTrunk
)

// cidCheck is one line's outbound_cid weighed against the trunk that will
// carry it.
type cidCheck struct {
	Verdict cidVerdict
	Line    string
	CID     string
	// Trunk is where a call as this line leaves by, "" for the dialplan default.
	Trunk string
	// Owner is the line that declares CID as its own [line] number, and
	// OwnerTrunk is the provider that number lives at. Both empty when nothing
	// declares it.
	Owner, OwnerTrunk string
}

// checkCIDs is the whole of what can be verified about outbound caller ID
// without asking a provider — and being clear about the rest.
//
// You cannot query VoIP.ms or Telnyx from here to ask which DIDs an account
// owns, and there is no shape of config that would make that possible. What
// this box does know is which numbers it answers and which provider each
// arrives on, because [line] number and [line] trunk say so. That is enough for
// the one mistake worth catching: presenting a number that this same config
// says lives somewhere else.
//
// Everything else is reported rather than refused. A caller ID matching no
// declared number may be a DID you own and do not answer here, which is
// ordinary; refusing it would make the check wrong more often than the config.
//
// Silent without a trunks.toml. With one provider there is nothing to mismatch,
// and a box with one number must never read the word "trunk" to check itself.
func (p outboundPlan) checkCIDs(trunks *policy.Trunks) []cidCheck {
	if !trunks.Present() {
		return nil
	}

	// Every number this box answers, and the provider it arrives on. A number
	// can legitimately be declared by more than one line, so this is a list.
	owners := map[string][]outboundLine{}
	for _, l := range p.Lines {
		if l.Number != "" {
			owners[l.Number] = append(owners[l.Number], l)
		}
	}

	var out []cidCheck
	for _, l := range p.Lines {
		if l.CID == "" {
			continue
		}
		if l.Trunk == "" {
			out = append(out, cidCheck{Verdict: cidNoTrunk, Line: l.Name, CID: l.CID})
			continue
		}
		declared := owners[l.CID]
		if len(declared) == 0 {
			out = append(out, cidCheck{Verdict: cidUnknownNumber,
				Line: l.Name, CID: l.CID, Trunk: l.Trunk})
			continue
		}
		// One line declaring it on this trunk settles it, however many others
		// answer the same number elsewhere.
		owned := false
		for _, o := range declared {
			if o.Trunk == l.Trunk {
				owned = true
				break
			}
		}
		if !owned {
			out = append(out, cidCheck{Verdict: cidWrongTrunk, Line: l.Name, CID: l.CID,
				Trunk: l.Trunk, Owner: declared[0].Name, OwnerTrunk: declared[0].Trunk})
		}
	}
	return out
}

// wrong filters the checks down to the ones that are provably wrong, which are
// the only ones anything refuses over.
func wrongTrunkCIDs(checks []cidCheck) []cidCheck {
	var out []cidCheck
	for _, c := range checks {
		if c.Verdict == cidWrongTrunk {
			out = append(out, c)
		}
	}
	return out
}

// describeCIDCheck is one line of explanation, shared by `doorman check` and
// `doorman render` so the two cannot describe the same problem differently.
func describeCIDCheck(c cidCheck) string {
	switch c.Verdict {
	case cidWrongTrunk:
		return fmt.Sprintf("line %s presents %s down trunk %s, but this config declares "+
			"%s as line %s's number on trunk %s", c.Line, c.CID, c.Trunk, c.CID, c.Owner, c.OwnerTrunk)
	case cidUnknownNumber:
		return fmt.Sprintf("line %s presents %s, which no [line] number declares — "+
			"fine if trunk %s owns it, unverifiable from here either way", c.Line, c.CID, c.Trunk)
	default:
		return fmt.Sprintf("line %s presents %s but names no trunk, so the call leaves "+
			"by the dialplan's DEFAULT_TRUNK — which nothing here can read", c.Line, c.CID)
	}
}

// anyCID reports whether outbound identity is configured at all. When it is
// not, every outbound path in the system is exactly what it was before —
// nothing is rendered, nothing is set, and the trunk decides. That is worth
// being able to say in one place, because it is the compatibility gate.
func (p outboundPlan) anyCID() bool {
	for _, l := range p.Lines {
		if l.CID != "" {
			return true
		}
	}
	return false
}

// describe renders the outbound block for `doorman check`: which number each
// line presents, which phones default to it, and where a phone nobody claimed
// ends up. handsetIDs is the inventory, so the fallback can be shown per phone
// rather than described in the abstract.
func (p outboundPlan) describe(handsetIDs []string) string {
	var b strings.Builder
	add := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	add("Outbound caller ID\n\n")
	width, cidWidth := 0, 0
	for _, l := range p.Lines {
		if len(l.Name) > width {
			width = len(l.Name)
		}
		if n := len(orDefault(l.CID, noOutboundCID)); n > cidWidth {
			cidWidth = n
		}
	}
	// The trunk column appears only once something routes on one — the same
	// rule the LINE column follows in `doorman calls`, so a box with one
	// provider never reads the word "trunk".
	routed := p.anyTrunk()
	for i, l := range p.Lines {
		suffix := ""
		if l.Name == policy.DefaultLine {
			suffix = "   (the primary line: what an unclaimed handset presents, and 911's route)"
		}
		if routed {
			add("  %d. %-*s  %-*s  via %s%s\n", i+1, width, l.Name,
				cidWidth, orDefault(l.CID, noOutboundCID), orDefault(l.Trunk, noOutboundTrunk), suffix)
			continue
		}
		add("  %d. %-*s  %s%s\n", i+1, width, l.Name, orDefault(l.CID, noOutboundCID), suffix)
	}
	add("\n  The digits above are the `*4` console menu, in that order.\n")
	if routed {
		add("  A call as a line leaves by that line's trunk, on both paths: the phones\n")
		add("  below carry it as a set_var, and `*4` sets it per call.\n")
	}

	claimed := map[string][]string{}
	var unclaimed []string
	for _, id := range handsetIDs {
		if line, isClaimed := p.lineFor(id); isClaimed {
			claimed[line.Name] = append(claimed[line.Name], id)
		} else {
			unclaimed = append(unclaimed, id)
		}
	}
	if len(claimed) > 0 {
		add("\n  Handsets that call as a line without being asked:\n")
		names := make([]string, 0, len(claimed))
		for name := range claimed {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			sort.Strings(claimed[name])
			add("    %-*s  %s\n", width, name, strings.Join(claimed[name], ", "))
		}
	}
	if len(unclaimed) > 0 {
		sort.Strings(unclaimed)
		add("\n  Unclaimed, so they present the primary line:\n    %s\n",
			strings.Join(unclaimed, ", "))
	}
	return b.String()
}

const (
	noOutboundCID   = "(none — presents whatever the trunk sends)"
	noOutboundTrunk = "(the dialplan's DEFAULT_TRUNK)"
)

// anyTrunk reports whether any line routes outbound on a trunk. Its absence is
// the compatibility gate for this half: no line naming a trunk means no
// set_var, no console variable, and every outbound call leaving exactly where
// the dialplan has always sent it.
func (p outboundPlan) anyTrunk() bool {
	for _, l := range p.Lines {
		if l.Trunk != "" {
			return true
		}
	}
	return false
}

// renderLines reads every line's [line] section for `doorman render`, in
// DiscoverLines' order: the primary line first, then the rest sorted. Both
// halves of render want it — outbound caller ID per handset, and the DID route
// per line — and both want the primary first.
//
// A missing policy file is not an error: `doorman render` generates the phone
// plant, and the runbook has an operator do that before the rules exist. A
// policy file that is present and will not load *is* an error — rendering from
// a half-read policy would bake a caller ID nobody chose into the config that
// decides what every customer sees, and unlike the daemon there is a person
// standing right here who can fix it.
//
// trunks is passed through so that a line naming a provider that does not
// exist fails here rather than producing a DID route in no context at all.
func renderLines(policyPath, handsetsPath string, trunks *policy.Trunks) ([]lineIdentity, error) {
	if _, err := os.Stat(policyPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	files, _ := policy.DiscoverLines(policyPath)
	ids := make([]lineIdentity, 0, len(files))
	for _, lf := range files {
		// AllowPlaceholders because render has no interest in extensions, and
		// a freshly copied policy.toml still carries the example PINs that
		// `doorman init` is about to replace. Refusing to generate a phone
		// plant over one would be an unhelpful place to be strict.
		p, err := policy.LoadSplitWith(lf.Path, handsetsPath, policy.Options{
			AllowPlaceholders: true,
			Trunks:            trunks,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", lf.Path, err)
		}
		ids = append(ids, lineIdentity{Name: lf.Name, LineIdentity: p.Line()})
	}
	return ids, nil
}

// announceOutbound says what the box calls out as, every boot.
//
// The plan asks for this by name and the reason is worth restating: the
// primary line is both the default outbound identity and 911's route, and a
// default nobody announces is a default somebody is surprised by. Silent when
// nothing is configured, because then nothing has changed and there is nothing
// to be surprised by.
//
// A contested handset warns rather than refuses. `doorman check` and `doorman
// render` are where an operator fixes it; the daemon's job is to keep
// answering the phone, so it says which line won and carries on. A caller ID
// on the wrong trunk gets the same treatment for the same reason: the call
// still goes out, the provider decides what to do with it, and refusing to
// start over a config the phone can survive is the trade invariant 4 forbids.
func announceOutbound(lines *lineSet, trunks *policy.Trunks, log *slog.Logger) {
	plan := lines.outbound()
	for _, c := range plan.Conflicts {
		log.Warn("two lines claim the same handset for outbound calls",
			"handset", c.Handset, "using", c.Winner, "ignoring", c.Loser,
			"hint", "remove it from one line's [line] outbound_handsets — `doorman check` refuses this")
	}
	for _, c := range wrongTrunkCIDs(plan.checkCIDs(trunks)) {
		log.Warn("a line presents a caller id its own trunk does not own",
			"line", c.Line, "presents", maskCID(c.CID), "trunk", c.Trunk,
			"declaredOn", c.OwnerTrunk,
			"hint", "the provider will reject or silently rewrite it — `doorman check` refuses this")
	}
	if !plan.anyCID() && !plan.claims() && !plan.anyTrunk() {
		return
	}
	log.Info("outbound identity", "primaryLine", policy.DefaultLine,
		"presents", maskCID(plan.Primary.CID), "trunk", orDefault(plan.Primary.Trunk, "(the dialplan default)"),
		"hint", "a handset no line claims presents the primary line, and so does 911")
	for _, l := range plan.Lines {
		if l.Name == policy.DefaultLine && len(l.Handsets) == 0 {
			continue
		}
		log.Info("outbound line", "line", l.Name, "presents", maskCID(l.CID),
			"trunk", orDefault(l.Trunk, "(the dialplan default)"),
			"handsets", strings.Join(l.Handsets, ", "))
	}
}

// claims reports whether any line claims any handset.
func (p outboundPlan) claims() bool {
	for _, l := range p.Lines {
		if len(l.Handsets) > 0 {
			return true
		}
	}
	return false
}

// maskCID is how an outbound caller ID reaches a log line — the same rule and
// the same reasoning as lobby.maskCID. `doorman check` prints these in full,
// because an operator is reading its output on purpose; journald is the place
// invariant 1 is about.
func maskCID(cid string) string {
	if cid == "" {
		return "(the trunk default)"
	}
	return policy.Redact(cid)
}
