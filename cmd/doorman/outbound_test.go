package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/ari"
	"callmemaybe/internal/policy"
)

// Outbound identity resolution: the router, `doorman check` and `doorman
// render` all read the same plan, so this is where the rule lives.

func id(name, cid string, handsets ...string) lineIdentity {
	return lineIdentity{Name: name, LineIdentity: policy.LineIdentity{
		OutboundCID: cid, OutboundHandsets: handsets,
	}}
}

// routed is a line that answers a DID on a provider and presents a number when
// it calls out — the three keys the outbound-by-trunk rule turns on.
func routed(name, number, trunk, cid string, handsets ...string) lineIdentity {
	return lineIdentity{Name: name, LineIdentity: policy.LineIdentity{
		Number: number, Trunk: trunk, OutboundCID: cid, OutboundHandsets: handsets,
	}}
}

// ── outbound_cid against the trunk that carries it ───────────────────────

// The mistake worth catching, and the only one that is provable without asking
// a provider anything: this config says the number lives at one company and
// the call is leaving by another. The provider rejects it or silently rewrites
// it, and neither is visible from this end.
func TestAWrongTrunkCallerIDIsRefused(t *testing.T) {
	trunks := loadTrunks(t, cliTrunks)
	plan := newOutboundPlan([]lineIdentity{
		routed(policy.DefaultLine, "+15125550100", "voipms", "+15125550100"),
		// The business line answers a Telnyx number and presents the VoIP.ms one.
		routed("biz", "+15125550142", "telnyx", "+15125550100"),
	})
	wrong := wrongTrunkCIDs(plan.checkCIDs(trunks))
	if len(wrong) != 1 {
		t.Fatalf("found %d provable mismatches, want 1: %v", len(wrong), wrong)
	}
	if wrong[0].Line != "biz" || wrong[0].Trunk != "telnyx" || wrong[0].OwnerTrunk != "voipms" {
		t.Errorf("mismatch reported as %+v, want biz presenting a voipms number on telnyx", wrong[0])
	}
	if got := describeCIDCheck(wrong[0]); !strings.Contains(got, "line biz presents") {
		t.Errorf("describeCIDCheck = %q", got)
	}
}

// A line presenting the number it answers on is the common case and the one
// that must never trip the check — including when a second line answers the
// same number on the same provider.
func TestALineMayPresentANumberOnItsOwnTrunk(t *testing.T) {
	trunks := loadTrunks(t, cliTrunks)
	plan := newOutboundPlan([]lineIdentity{
		routed(policy.DefaultLine, "+15125550100", "voipms", "+15125550100"),
		// Presenting the house number from a second line on the same provider:
		// deliberate, and a provider that owns the DID will carry it.
		routed("kids", "+15125550111", "voipms", "+15125550100"),
	})
	if got := plan.checkCIDs(trunks); len(got) != 0 {
		t.Errorf("checkCIDs = %v, want nothing to report", got)
	}
}

// The honest edge of a static check. A DID you own and do not answer here has
// no [line] number to match against, which is an ordinary config and
// indistinguishable from a typo — so it is reported and never refused.
func TestAnUndeclaredCallerIDIsReportedNotRefused(t *testing.T) {
	trunks := loadTrunks(t, cliTrunks)
	plan := newOutboundPlan([]lineIdentity{
		routed(policy.DefaultLine, "+15125550100", "voipms", "+15125550199"),
		// No trunk at all: the call leaves by the dialplan's DEFAULT_TRUNK,
		// which nothing in Go can read.
		routed("biz", "+15125550142", "", "+15125550142"),
	})
	checks := plan.checkCIDs(trunks)
	if len(checks) != 2 {
		t.Fatalf("checkCIDs = %v, want both lines reported", checks)
	}
	if len(wrongTrunkCIDs(checks)) != 0 {
		t.Error("an unverifiable caller ID was treated as provably wrong")
	}
	verdicts := map[cidVerdict]bool{}
	for _, c := range checks {
		verdicts[c.Verdict] = true
	}
	if !verdicts[cidUnknownNumber] || !verdicts[cidNoTrunk] {
		t.Errorf("checks = %v, want one of each unverifiable kind", checks)
	}
}

// The compatibility gate for the check itself: one provider has nothing to
// mismatch, and a box with no trunks.toml must never read the word "trunk".
func TestCallerIDsAreNotCheckedWithoutATrunkInventory(t *testing.T) {
	plan := newOutboundPlan([]lineIdentity{
		routed(policy.DefaultLine, "+15125550100", "", "+15125550199"),
	})
	if got := plan.checkCIDs(&policy.Trunks{}); got != nil {
		t.Errorf("checkCIDs = %v, want silence with no inventory", got)
	}
}

// The pair reaches the endpoint together, because a caller ID and the trunk
// that carries it are one decision.
func TestIdentitiesCarryTheTrunkBesideTheCallerID(t *testing.T) {
	plan := newOutboundPlan([]lineIdentity{
		routed(policy.DefaultLine, "+15125550100", "voipms", "+15125550100"),
		routed("biz", "+15125550142", "telnyx", "+15125550142", "office"),
	})
	got := plan.identities([]string{"kitchen", "office"})
	if got["office"].Trunk != "telnyx" || got["office"].CID != "+15125550142" {
		t.Errorf("office = %+v, want the business line's pair", got["office"])
	}
	if got["kitchen"].Trunk != "voipms" || got["kitchen"].CID != "+15125550100" {
		t.Errorf("kitchen = %+v, want the primary line's pair", got["kitchen"])
	}
}

// The rule in one test: a claimed handset calls as its line, an unclaimed one
// calls as the primary. Both chains bottom out at policy.toml, which is also
// 911's route — that is what makes "which line am I on" one answer.
func TestUnclaimedHandsetsPresentThePrimaryLine(t *testing.T) {
	plan := newOutboundPlan([]lineIdentity{
		id(policy.DefaultLine, "+15125550100"),
		id("biz", "+15125550142", "office"),
	})

	if l, claimed := plan.lineFor("office"); !claimed || l.CID != "+15125550142" {
		t.Errorf("office presents %q (claimed=%v), want the business line", l.CID, claimed)
	}
	if l, claimed := plan.lineFor("kitchen"); claimed || l.CID != "+15125550100" {
		t.Errorf("kitchen presents %q (claimed=%v), want the primary line", l.CID, claimed)
	}
}

// Nothing configured is byte-identical to the world before this feature: no
// entries, so `doorman render` writes no set_var and the trunk decides.
func TestNoOutboundIdentityRendersNothing(t *testing.T) {
	plan := newOutboundPlan([]lineIdentity{id(policy.DefaultLine, "")})
	if plan.anyCID() || plan.claims() {
		t.Fatal("an unconfigured install should have nothing to say about outbound")
	}
	if got := plan.identities([]string{"kitchen", "office"}); len(got) != 0 {
		t.Errorf("identities = %v, want nothing to render", got)
	}
}

// A primary line with a caller ID reaches every phone, claimed or not. This is
// the "house default" the plan asks for: one key, every handset.
func TestThePrimaryCallerIDReachesEveryUnclaimedHandset(t *testing.T) {
	plan := newOutboundPlan([]lineIdentity{
		id(policy.DefaultLine, "+15125550100"),
		id("biz", "+15125550142", "office"),
	})
	got := plan.identities([]string{"kitchen", "office", "kids-room"})
	want := map[string]string{
		"kitchen":   "+15125550100",
		"office":    "+15125550142",
		"kids-room": "+15125550100",
	}
	for h, cid := range want {
		if got[h].CID != cid {
			t.Errorf("%s presents %q, want %q", h, got[h].CID, cid)
		}
	}
}

// A phone presents one number, so two lines claiming it is ambiguous — and
// which one wins must not depend on anything an operator cannot see. The
// primary line wins because it is first, and the loser is reported rather than
// dropped.
func TestAHandsetClaimedTwiceIsReportedAndResolvedDeterministically(t *testing.T) {
	plan := newOutboundPlan([]lineIdentity{
		id(policy.DefaultLine, "+15125550100", "office"),
		id("biz", "+15125550142", "office"),
	})
	if len(plan.Conflicts) != 1 {
		t.Fatalf("conflicts = %v, want the double claim reported", plan.Conflicts)
	}
	c := plan.Conflicts[0]
	if c.Handset != "office" || c.Winner != policy.DefaultLine || c.Loser != "biz" {
		t.Errorf("conflict = %+v, want office won by the primary line", c)
	}
	if l, _ := plan.lineFor("office"); l.Name != policy.DefaultLine {
		t.Errorf("office resolved to %q, want the first claim to win", l.Name)
	}
}

// The console menu is the plan's order, primary first. The digit that reaches
// a line must not move when a policy file is added beside the others.
func TestConsoleMenuIsPrimaryFirst(t *testing.T) {
	lines := newOutboundPlan([]lineIdentity{
		id(policy.DefaultLine, "+15125550100"),
		id("aaa", "+15125550111"),
		id("biz", "+15125550142"),
	}).consoleLines()

	if len(lines) != 3 {
		t.Fatalf("menu has %d entries, want 3", len(lines))
	}
	if lines[0].Name != policy.DefaultLine || lines[1].Name != "aaa" || lines[2].Name != "biz" {
		t.Errorf("menu order = %v, want the primary line first", lines)
	}
}

// `doorman check` has to say what each line presents and where an unclaimed
// phone ends up — a default nobody announces is a default somebody is
// surprised by.
func TestDescribeNamesEveryLineAndTheFallback(t *testing.T) {
	out := newOutboundPlan([]lineIdentity{
		id(policy.DefaultLine, "+15125550100"),
		id("biz", "+15125550142", "office"),
	}).describe([]string{"kitchen", "office"})

	for _, want := range []string{
		"+15125550100", "+15125550142", "office", "kitchen",
		"unclaimed", "*4", "911",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check output should mention %q:\n%s", want, out)
		}
	}
}

// A line with no outbound_cid is named as such rather than shown blank: it
// presents the trunk default, which is a different thing from "no caller ID".
func TestDescribeSaysWhenALineHasNoCallerID(t *testing.T) {
	out := newOutboundPlan([]lineIdentity{
		id(policy.DefaultLine, ""),
		id("biz", "+15125550142", "office"),
	}).describe([]string{"office"})
	if !strings.Contains(out, noOutboundCID) {
		t.Errorf("check output should name the trunk default:\n%s", out)
	}
}

// ── what `doorman render` reads ──────────────────────────────────────────

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const renderHandsets = `
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
password_env = "HANDSET_KITCHEN_PASSWORD"

[[handsets]]
id = "office"
endpoint = "PJSIP/office"
password_env = "HANDSET_OFFICE_PASSWORD"
`

const renderPolicy = `
[line]
outbound_cid = "+15125550100"

[house]
handsets = ["kitchen"]
`

// renderPlan is what `doorman render` does with the lines it reads, minus the
// trunk inventory these cases have no opinion about.
func renderPlan(policyPath, handsetsPath string) (outboundPlan, error) {
	ids, err := renderLines(policyPath, handsetsPath, nil)
	if err != nil {
		return outboundPlan{}, err
	}
	return newOutboundPlan(ids), nil
}

func TestRenderOutboundReadsEveryLine(t *testing.T) {
	dir := t.TempDir()
	handsets := writeFile(t, dir, "handsets.toml", renderHandsets)
	policyPath := writeFile(t, dir, "policy.toml", renderPolicy)
	writeFile(t, dir, "policy.biz.toml", `
[line]
outbound_cid = "+15125550142"
outbound_handsets = ["office"]

[house]
handsets = ["office"]
`)

	plan, err := renderPlan(policyPath, handsets)
	if err != nil {
		t.Fatalf("renderLines: %v", err)
	}
	got := plan.identities([]string{"kitchen", "office"})
	if got["kitchen"].CID != "+15125550100" || got["office"].CID != "+15125550142" {
		t.Errorf("identities = %v", got)
	}
}

// Rendering the phone plant before any rules exist is the order the runbook
// puts them in, so a missing policy file is not an error.
func TestRenderOutboundToleratesAMissingPolicy(t *testing.T) {
	dir := t.TempDir()
	handsets := writeFile(t, dir, "handsets.toml", renderHandsets)

	plan, err := renderPlan(filepath.Join(dir, "policy.toml"), handsets)
	if err != nil {
		t.Fatalf("renderLines: %v", err)
	}
	if plan.anyCID() {
		t.Error("a missing policy file should mean no outbound identity")
	}
}

// A policy that is present and will not load is a different matter: rendering
// from a half-read file would bake a caller ID nobody chose into the config
// that decides what every customer sees.
func TestRenderOutboundRefusesAnInvalidPolicy(t *testing.T) {
	dir := t.TempDir()
	handsets := writeFile(t, dir, "handsets.toml", renderHandsets)
	policyPath := writeFile(t, dir, "policy.toml", "[line\nlabel = \"broken\"\n")

	if _, err := renderPlan(policyPath, handsets); err == nil {
		t.Fatal("render must refuse a policy file it cannot read")
	}
}

// The shipped examples carry the placeholder PIN until `doorman init` runs,
// and render has no interest in extensions. Refusing to generate a phone plant
// over one would be an unhelpful place to be strict.
func TestRenderOutboundAcceptsPlaceholderPINs(t *testing.T) {
	dir := t.TempDir()
	handsets := writeFile(t, dir, "handsets.toml", renderHandsets)
	policyPath := writeFile(t, dir, "policy.toml", renderPolicy+`
[[extensions]]
pin = "`+policy.PlaceholderPIN+`"
label = "Kitchen"
handsets = ["kitchen"]
`)

	plan, err := renderPlan(policyPath, handsets)
	if err != nil {
		t.Fatalf("renderLines: %v", err)
	}
	if plan.Primary.CID != "+15125550100" {
		t.Errorf("primary presents %q", plan.Primary.CID)
	}
}

// ── the router ───────────────────────────────────────────────────────────

// *4 is a console, not a caller. It must not become a session, must not be
// counted against the concurrent-call ceiling — refusing the household its own
// phone during a flood of strangers would be the wrong way round — and must
// receive its own DTMF.
func TestRouteHandsStarFourToTheConsole(t *testing.T) {
	set := testLines(t, "biz")
	reg := newRegistry()
	client := ari.New(ari.Options{BaseURL: "http://127.0.0.1:1", App: "doorman", Log: quiet()})

	route(ari.Event{
		Type:    "StasisStart",
		Args:    []string{"console"},
		Channel: &ari.Channel{ID: "office-1"},
	}, reg, set, client, quiet())

	if reg.callerCount() != 0 {
		t.Errorf("the console became a caller: %d registered", reg.callerCount())
	}
	c := reg.console("office-1")
	if c == nil {
		t.Fatal("no console was registered for the *4 call")
	}
	// Digits reach it rather than being dropped on the floor.
	route(ari.Event{
		Type:    "ChannelDtmfReceived",
		Digit:   "1",
		Channel: &ari.Channel{ID: "office-1"},
	}, reg, set, client, quiet())

	// And StasisEnd is the successful ending — invariant 8. The console
	// unregisters itself through OnFinished either way; what matters here is
	// that the router routed it at all.
	route(ari.Event{
		Type:    "StasisEnd",
		Channel: &ari.Channel{ID: "office-1"},
	}, reg, set, client, quiet())

	deadline := time.After(2 * time.Second)
	for reg.console("office-1") != nil {
		select {
		case <-deadline:
			t.Fatal("the console never tore down after StasisEnd")
		default:
		}
	}
}
