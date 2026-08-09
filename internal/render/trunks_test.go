package render

import (
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

// Trunks arrive here already compiled, so the fixture carries the defaults
// policy.compileTrunks would have filled in.
func trunkFixture() []policy.Trunk {
	return []policy.Trunk{{
		ID: "voipms", Provider: "voip.ms", Host: "chicago.voip.ms",
		Username: "123456_home", PasswordEnv: "TRUNK_VOIPMS_PASSWORD",
		Context: "from-voipms", Codecs: []string{"ulaw", "g722"},
		FromUser: "123456_home", FromDomain: "chicago.voip.ms", Expiration: 300,
	}, {
		ID: "telnyx", Provider: "telnyx", Host: "sip.telnyx.com",
		Username: "cmm-home", PasswordEnv: "TRUNK_TELNYX_PASSWORD",
		Context: "from-telnyx", Codecs: []string{"ulaw"},
		FromUser: "cmm-home", FromDomain: "sip.telnyx.com", Expiration: 300,
	}}
}

func trunkLines() []TrunkLine {
	return []TrunkLine{
		{Name: "default", Label: "Home", Number: "+15125550100", Trunk: "voipms"},
		{Name: "biz", Label: "Mertaugh Enterprises", Number: "+15125550142", Trunk: "telnyx"},
	}
}

var trunkSecrets = map[string]string{
	"TRUNK_VOIPMS_PASSWORD": "voipms-secret",
	"TRUNK_TELNYX_PASSWORD": "telnyx-secret",
}

// The designated emergency trunk every fixture renders with. Named rather than
// defaulted, exactly as the real thing: nothing in this system picks the trunk
// that carries 911 by position or by sorting.
var testEmergency = Emergency{Trunk: "voipms", Chosen: true}

func buildTrunks(t *testing.T, trunks []policy.Trunk, lines []TrunkLine) *TrunkFragments {
	t.Helper()
	f, err := BuildTrunks(trunks, lines, []string{"kitchen", "office"}, testEmergency, env(trunkSecrets))
	if err != nil {
		t.Fatalf("BuildTrunks: %v", err)
	}
	return f
}

func boolp(b bool) *bool { return &b }

// THE assertion of this milestone.
//
// `line=yes` plus `endpoint=<id>` on the registration is what binds inbound
// traffic to the endpoint. Get it wrong and calls hit the anonymous endpoint
// and vanish: no error, no log line, no symptom at all except a number that
// never rings. Nothing else in the generated file fails that quietly, which is
// why this is asserted per trunk rather than once.
func TestEveryRegistrationBindsInboundToItsEndpoint(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())

	for _, tr := range trunkFixture() {
		block, ok := section(f.PJSIP, "["+tr.ID+"_reg]")
		if !ok {
			t.Fatalf("no registration object for %q", tr.ID)
		}
		if !strings.Contains(block, "\nline=yes\n") {
			t.Errorf("[%s_reg] has no line=yes — inbound would hit the anonymous endpoint:\n%s", tr.ID, block)
		}
		if !strings.Contains(block, "\nendpoint="+tr.ID+"\n") {
			t.Errorf("[%s_reg] does not bind to endpoint=%s:\n%s", tr.ID, tr.ID, block)
		}
	}
}

// The other half of the same claim: with the binding right, an identify block
// is redundant, and an IP allow-list is a list that goes stale silently.
// Generating one would be a second mechanism to keep in agreement with the
// first.
func TestNoIdentifyBlocksAreGenerated(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	if strings.Contains(f.PJSIP, "type=identify") {
		t.Error("an identify block was generated; line=yes + endpoint= makes it redundant")
	}
	if !strings.Contains(f.PJSIP, "No identify blocks are generated") {
		t.Error("the generated file should say why there is no identify block")
	}
}

// Invariant 9: dtmf_mode=rfc4733 on the trunk AND every handset. Without it
// the lobby is deaf and every stranger is dismissed, with no runtime symptom
// other than "nobody can ever get in".
func TestEveryTrunkEndpointSetsRFC4733(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	for _, tr := range trunkFixture() {
		block, ok := section(f.PJSIP, "["+tr.ID+"]")
		if !ok {
			t.Fatalf("no endpoint for %q", tr.ID)
		}
		if !strings.Contains(block, "dtmf_mode=rfc4733") {
			t.Errorf("[%s] has no dtmf_mode=rfc4733 — the lobby would be deaf on it", tr.ID)
		}
	}
}

func TestTrunkPJSIPCarriesTheProviderFields(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	if f.Trunks != 2 {
		t.Errorf("rendered %d trunks, want 2", f.Trunks)
	}
	for _, want := range []string{
		"[voipms_auth]", "username=123456_home", "password=voipms-secret",
		"server_uri=sip:chicago.voip.ms",
		"client_uri=sip:123456_home@chicago.voip.ms",
		"contact_user=123456_home",
		"expiration=300",
		"[voipms_aor]", "contact=sip:chicago.voip.ms",
		"context=from-voipms", "allow=ulaw", "allow=g722",
		"from_user=123456_home", "from_domain=chicago.voip.ms",
		"[telnyx]", "password=telnyx-secret", "context=from-telnyx",
	} {
		if !strings.Contains(f.PJSIP, want) {
			t.Errorf("pjsip missing %q", want)
		}
	}
	// A codec list of one must not pick up the default's second entry.
	telnyx, _ := section(f.PJSIP, "[telnyx]")
	if strings.Contains(telnyx, "allow=g722") {
		t.Error("telnyx allows a codec it did not ask for")
	}
}

// Secrets reach the file from the environment and nowhere else, exactly as
// they do for handsets. A trunks.toml is safe to keep; its output is not.
func TestTrunkRenderRefusesAMissingSecret(t *testing.T) {
	_, err := BuildTrunks(trunkFixture(), trunkLines(), nil, testEmergency,
		env(map[string]string{"TRUNK_VOIPMS_PASSWORD": "x"}))
	if err == nil || !strings.Contains(err.Error(), "TRUNK_TELNYX_PASSWORD") {
		t.Errorf("err = %v, want the missing variable named", err)
	}
}

func TestGeneratedTrunkPJSIPSaysItHoldsPasswords(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	for _, want := range []string{"DO NOT EDIT", "REAL SIP PASSWORDS", "never commit"} {
		if !strings.Contains(f.PJSIP, want) {
			t.Errorf("the generated PJSIP header does not say %q", want)
		}
	}
	if !strings.Contains(f.Dialplan, "DO NOT EDIT") {
		t.Error("the generated dialplan is missing the generated-file header")
	}
	// The dialplan has no secrets in it, so it must not claim to.
	if strings.Contains(f.Dialplan, "REAL SIP PASSWORDS") {
		t.Error("the dialplan fragment claims to hold passwords it does not")
	}
}

// Both become a PJSIP endpoint named after the id, and Asterisk loading two
// blocks with one name is not something an operator would notice.
func TestTrunkIDMayNotCollideWithAHandset(t *testing.T) {
	_, err := BuildTrunks(trunkFixture(), nil, []string{"voipms"}, testEmergency, env(trunkSecrets))
	if err == nil || !strings.Contains(err.Error(), "collides with a handset") {
		t.Errorf("err = %v, want the collision refused", err)
	}
}

// ── the generated dialplan ───────────────────────────────────────────────

// Providers differ on what digits they send and the dialplan match has to be
// exact. Matching all three formats costs two lines per DID and removes the
// "watch asterisk -rvvv to find out which one arrives" step entirely.
func TestDIDRoutesMatchEveryDigitFormat(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	if f.Routes != 2 {
		t.Errorf("generated %d routes, want 2", f.Routes)
	}
	for _, want := range []string{
		"exten => +15125550100,1,Goto(cmm-line-default,${EXTEN},1)",
		"exten => 15125550100,1,Goto(cmm-line-default,${EXTEN},1)",
		"exten => 5125550100,1,Goto(cmm-line-default,${EXTEN},1)",
		"exten => +15125550142,1,Goto(cmm-line-biz,${EXTEN},1)",
	} {
		if !strings.Contains(f.Dialplan, want) {
			t.Errorf("dialplan missing %q", want)
		}
	}
}

// A ten-digit form is only well defined for NANP. Elsewhere "drop the country
// code" is a guess, and a wrong guess in a dialplan is a route that fires for
// somebody else's number.
func TestNonNANPNumbersGetNoNationalForm(t *testing.T) {
	got := didForms("+442075550123")
	if len(got) != 2 || got[0] != "+442075550123" || got[1] != "442075550123" {
		t.Errorf("didForms = %v, want only the two unambiguous forms", got)
	}
}

// Each DID belongs to one trunk's context, because that is the whole point:
// the registration a call arrived on is known before anything else happens.
func TestEachDIDLandsInItsOwnTrunksContext(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	voipms, ok := section(f.Dialplan, "[from-voipms]")
	if !ok {
		t.Fatal("no [from-voipms] context")
	}
	if !strings.Contains(voipms, "5125550100") {
		t.Error("the home DID is not routed in its own trunk's context")
	}
	if strings.Contains(voipms, "5125550142") {
		t.Error("a DID on another provider leaked into this trunk's context")
	}
}

// Never a dropped call: a number nobody routed is answered by the default
// line rather than falling off the end of a context.
func TestEveryTrunkContextFallsBackToTheDefaultLine(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	for _, ctx := range []string{"[from-voipms]", "[from-telnyx]"} {
		block, ok := section(f.Dialplan, ctx)
		if !ok {
			t.Fatalf("no %s context", ctx)
		}
		if !strings.Contains(block, "exten => _X.,1,Goto(cmm-line-default,${EXTEN},1)") {
			t.Errorf("%s has no catch-all — an unrouted DID would stop answering", ctx)
		}
	}
}

// The compatibility gate for the dialplan half. The default line's Stasis call
// takes no arguments at all, which is byte-for-byte what every install had
// before lines existed — and what lineSet.forCall treats as today's behaviour.
func TestDefaultLineContextPassesNoStasisArguments(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	block, ok := section(f.Dialplan, "[cmm-line-default]")
	if !ok {
		t.Fatal("no default line context")
	}
	if !strings.Contains(block, "same => n,Stasis(${DOORMAN_APP})\n") {
		t.Errorf("the default line names itself to doorman:\n%s", block)
	}
	biz, _ := section(f.Dialplan, "[cmm-line-biz]")
	if !strings.Contains(biz, "same => n,Stasis(${DOORMAN_APP},line,biz)\n") {
		t.Errorf("a named line does not pass its name:\n%s", biz)
	}
	// The hangup handler and the pre-Stasis Answer() are load-bearing and easy
	// to lose: answering in the dialplan is what makes the lobby feel instant.
	for _, want := range []string{"hangup_handler_push)=cmm-hangup", "same => n,Answer()"} {
		if !strings.Contains(block, want) {
			t.Errorf("generated line context lost %q", want)
		}
	}
}

// A route needs both halves. A line with a trunk and no number is a real
// state — the DID is not written down anywhere yet — and it must produce a
// context with no route rather than a route to nothing.
func TestALineWithNoNumberGetsAContextButNoRoute(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), []TrunkLine{
		{Name: "default", Trunk: "voipms"},
		{Name: "biz", Trunk: "telnyx", Label: "Biz"},
	})
	if f.Routes != 0 {
		t.Errorf("routes = %d, want none", f.Routes)
	}
	if _, ok := section(f.Dialplan, "[cmm-line-biz]"); !ok {
		t.Error("a line with no number still needs a context to be routed into by hand")
	}
}

func TestTrunkRenderRefusesAmbiguousRouting(t *testing.T) {
	_, err := BuildTrunks(trunkFixture(), []TrunkLine{
		{Name: "default", Number: "+15125550100", Trunk: "voipms"},
		{Name: "biz", Number: "+15125550100", Trunk: "voipms"},
	}, nil, testEmergency, env(trunkSecrets))
	if err == nil || !strings.Contains(err.Error(), "one DID reaches one line") {
		t.Errorf("err = %v, want the ambiguity refused", err)
	}

	_, err = BuildTrunks(trunkFixture(), []TrunkLine{
		{Name: "biz", Number: "+15125550142", Trunk: "nope"},
	}, nil, testEmergency, env(trunkSecrets))
	if err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Errorf("err = %v, want the unknown trunk refused", err)
	}
}

// ── the emergency ladder ─────────────────────────────────────────────────

// The highest-stakes assertion in the repository.
//
// The designated trunk is tried FIRST and unconditionally: E911 is registered
// per DID against a street address, so the trunk whose address is on file is
// the only one that puts the right house on a dispatcher's screen.
func TestEmergencyCallsLeaveByTheDesignatedTrunkFirst(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	block, ok := section(f.Dialplan, "[cmm-emergency]")
	if !ok {
		t.Fatal("no [cmm-emergency] context was generated")
	}
	first := "exten => _911,1,NoOp(Emergency call from ${CALLERID(num)} via voipms)\n" +
		" same => n,Dial(PJSIP/911@voipms,60)\n"
	if !strings.Contains(block, first) {
		t.Errorf("911 does not leave by the designated trunk first:\n%s", block)
	}
	if f.Emergency != "voipms" {
		t.Errorf("Emergency = %q, want voipms", f.Emergency)
	}
}

// Invariant, and the comment in the hand-written dialplan that must stay true:
// _911 never sets a caller ID. An emergency call leaves as the trunk's own
// number, never as a line whose registered address is somebody else's house.
func TestEmergencyContextNeverSetsACallerID(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	block, _ := section(f.Dialplan, "[cmm-emergency]")
	// The comment block above the context says the words; the context itself
	// must not contain the mechanism.
	for _, forbidden := range []string{"Set(CALLERID", "${OUTBOUND_CID}", "${OUTBOUND_TRUNK}"} {
		if strings.Contains(block, forbidden) {
			t.Errorf("the emergency context contains %q:\n%s", forbidden, block)
		}
	}
}

// The deliberate trade, spelled out in .plans/s01-multiple-DIDs: a connected
// call lets a human say their address out loud, a failed call gives them
// nothing. So every other trunk is tried in turn.
func TestEmergencyCallsFallOverToEveryOtherTrunk(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	block, _ := section(f.Dialplan, "[cmm-emergency]")
	if !strings.Contains(block, "same => n,Dial(PJSIP/911@telnyx,60)") {
		t.Errorf("no fallback to the other trunk:\n%s", block)
	}
	// A completed call must not be re-dialled down the next trunk when the far
	// end hangs up first, which is what returns control to the dialplan.
	if strings.Count(block, `same => n,GotoIf($["${DIALSTATUS}"="ANSWER"]?done)`) != 2 {
		t.Errorf("each attempt needs an ANSWER guard or a finished call redials:\n%s", block)
	}
	if want := []string{"telnyx"}; len(f.Fallbacks) != 1 || f.Fallbacks[0] != want[0] {
		t.Errorf("Fallbacks = %v, want %v", f.Fallbacks, want)
	}
}

// Registration state is never consulted, and that is a decision rather than an
// omission. A provider that does not answer OPTIONS looks unreachable while
// working perfectly, and diverting an emergency call off the one trunk whose
// address is filed — on a false negative, at the worst possible moment — is
// exactly the failure this design exists to prevent. Trying it costs
// milliseconds when it really is down.
func TestEmergencyCallsTryTheTrunkRatherThanAskingIfItIsUp(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	block, _ := section(f.Dialplan, "[cmm-emergency]")
	if strings.Contains(block, "DEVICE_STATE") {
		t.Errorf("the designated trunk is gated on a liveness check:\n%s", block)
	}
}

// When nothing can carry it the caller must hear something. Silence on a 911
// call is the one outcome with no recovery: the tone is what tells somebody to
// reach for a mobile.
func TestEmergencyCallsFailLoudly(t *testing.T) {
	f := buildTrunks(t, trunkFixture(), trunkLines())
	block, _ := section(f.Dialplan, "[cmm-emergency]")
	for _, want := range []string{
		"same => n,NoOp(NO TRUNK COULD CARRY THIS EMERGENCY CALL)",
		"same => n,Playback(all-circuits-busy-now)",
		"same => n,Congestion(5)",
		"same => n(done),Hangup()",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the emergency context is missing %q:\n%s", want, block)
		}
	}
}

// A fallback that fires is already a call whose location data may be wrong, so
// the trunks that declare a registered address are tried before the ones that
// do not, and a trunk that declares it has none is tried last of all.
func TestEmergencyFallbacksPreferTrunksWithARegisteredAddress(t *testing.T) {
	trunks := []policy.Trunk{
		{ID: "designated"}, {ID: "none", E911: boolp(false)},
		{ID: "unknown"}, {ID: "registered", E911: boolp(true)},
	}
	got := EmergencyOrder(trunks, "designated")
	want := []string{"designated", "registered", "unknown", "none"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("order = %v, want %v", ids(got), want)
		}
	}
}

// The one output this package refuses to write. A generated dialplan that
// routes every DID beautifully and has no answer for 911 is worse than no
// generated dialplan at all, because the first is installed with confidence.
func TestTrunkRenderRefusesWhenNothingCarriesEmergencyCalls(t *testing.T) {
	_, err := BuildTrunks(trunkFixture(), trunkLines(), nil, Emergency{}, env(trunkSecrets))
	if err == nil || !strings.Contains(err.Error(), "no trunk carries emergency calls") {
		t.Errorf("err = %v, want the undecided emergency trunk refused", err)
	}

	_, err = BuildTrunks(trunkFixture(), trunkLines(), nil,
		Emergency{Trunk: "gone"}, env(trunkSecrets))
	if err == nil || !strings.Contains(err.Error(), "emergency trunk") {
		t.Errorf("err = %v, want an undeclared emergency trunk refused", err)
	}
}

// Whether the answer was chosen or inherited belongs in the file. Config that
// looks wrong is only fixable if it says where the value came from.
func TestEmergencyContextSaysWhereTheDecisionCameFrom(t *testing.T) {
	chosen := buildTrunks(t, trunkFixture(), trunkLines())
	if !strings.Contains(chosen.Dialplan, "911 leaves by voipms (chosen — emergency_trunk in trunks.toml).") {
		t.Error("the generated file does not say the emergency trunk was chosen")
	}
	inferred, err := BuildTrunks(trunkFixture(), trunkLines(), nil,
		Emergency{Trunk: "telnyx"}, env(trunkSecrets))
	if err != nil {
		t.Fatalf("BuildTrunks: %v", err)
	}
	if !strings.Contains(inferred.Dialplan,
		"911 leaves by telnyx (inferred — the primary line's trunk, from policy.toml).") {
		t.Error("the generated file does not say the emergency trunk was inferred")
	}
}

func ids(trunks []policy.Trunk) []string {
	out := make([]string, 0, len(trunks))
	for _, tr := range trunks {
		out = append(out, tr.ID)
	}
	return out
}

// section returns the text of one ini-style block, from its header to the
// next header or the end.
func section(text, header string) (string, bool) {
	start := strings.Index(text, header+"\n")
	if start < 0 {
		return "", false
	}
	rest := text[start+len(header)+1:]
	if next := strings.Index(rest, "\n["); next >= 0 {
		return text[start : start+len(header)+1+next+1], true
	}
	return text[start:], true
}
