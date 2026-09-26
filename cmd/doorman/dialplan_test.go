package main

import (
	"os"
	"strings"
	"testing"

	"callmemaybe/internal/lobby"
	"callmemaybe/internal/render"
)

// The shipped dialplan is the one interface in this system that is not Go, and
// the compiler has nothing to say about it. These are the guarantees that live
// on the other side of that boundary — the ones whose failure mode is a working
// phone that does the wrong thing.
//
// Deliberately a small set. This is not a parser and must not become one:
// every assertion here is a property somebody could break with a plausible
// edit, and each names the consequence rather than the syntax.

func shippedDialplan(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("../../asterisk/extensions.conf")
	if err != nil {
		t.Fatalf("reading the shipped dialplan: %v", err)
	}
	return string(body)
}

// dialplanContext returns one context's body, header excluded, up to the next
// one. Comments between contexts belong to whatever follows them, which is
// wrong for prose and exactly right for "what can this context reach".
func dialplanContext(t *testing.T, body, name string) string {
	t.Helper()
	start := strings.Index(body, "\n["+name+"]\n")
	if start < 0 {
		t.Fatalf("the shipped dialplan has no [%s] context", name)
	}
	rest := body[start+len(name)+4:]
	if next := strings.Index(rest, "\n["); next >= 0 {
		return rest[:next]
	}
	return rest
}

// The console releases the handset into a context that exists, with the name
// internal/lobby compiled into it. Get this wrong and every *4 call dies at the
// moment it is placed, with an ARI error and a dropped channel.
func TestTheShippedDialplanDefinesTheConsoleReleaseContext(t *testing.T) {
	body := shippedDialplan(t)
	if !strings.Contains(body, "\n[outbound-console]\n") {
		t.Error("the console releases callers into [outbound-console] and the dialplan has none")
	}
	if !strings.Contains(body, "\n[voicemail-drop]\n") {
		t.Error("the lobby releases callers into [voicemail-drop] and the dialplan has none")
	}
}

// The *4 console's second guard, and the reason it is a second one: doorman
// refuses emergency numbers, and the context it releases into contains no
// pattern that could carry one anyway. Two independent things have to be wrong
// before an emergency call leaves as somebody else's number.
//
// _NXXNXXXXXX and _1NXXNXXXXXX cannot match three digits, so this is really an
// assertion that nobody has added a pattern that can.
func TestTheConsoleContextCannotReachAnEmergencyNumber(t *testing.T) {
	body := shippedDialplan(t)
	for _, name := range []string{"outbound-console", "cmm-outbound"} {
		ctx := dialplanContext(t, body, name)
		for number := range map[string]bool{"911": true, "112": true, "999": true, "000": true} {
			if strings.Contains(ctx, "exten => _"+number) || strings.Contains(ctx, "exten => "+number) {
				t.Errorf("[%s] can dial %s — the console must not be able to place an emergency call", name, number)
			}
		}
		// A wildcard would match one, which is the same defect wearing a hat.
		if strings.Contains(ctx, "exten => _X.") || strings.Contains(ctx, "exten => _.") {
			t.Errorf("[%s] has a catch-all pattern, which can match an emergency number", name)
		}
	}
}

// Invariant, and the comment above _911 says so in the file itself: an
// emergency call leaves as the trunk's own number. E911 is registered per DID
// against a street address, so presenting a business line would put somebody
// else's address on a dispatcher's screen.
func TestTheShippedEmergencyRouteSetsNoCallerID(t *testing.T) {
	body := shippedDialplan(t)
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "exten => _911") {
			continue
		}
		if strings.Contains(line, "OUTBOUND_CID") || strings.Contains(line, "CALLERID") {
			t.Errorf("_911 touches the caller ID: %s", line)
		}
		return
	}
	t.Error("the shipped dialplan has no _911 route at all")
}

// The compatibility gate for M2.3, in the file that decides it. With no
// trunks.toml there is no generated [cmm-emergency], so _911 dials one trunk
// exactly as it always has — DEFAULT_TRUNK is a name for the endpoint the file
// used to hard-code, not a change of behaviour.
func TestEmergencyRoutingFallsBackToOneTrunkWithNoGeneratedContext(t *testing.T) {
	body := shippedDialplan(t)
	if !strings.Contains(body, "DEFAULT_TRUNK=voipms") {
		t.Error("[globals] does not define DEFAULT_TRUNK, so an unrouted channel has no trunk")
	}
	if !strings.Contains(body, "exten => _911,1,GotoIf($[${DIALPLAN_EXISTS(cmm-emergency,911,1)}]?cmm-emergency,911,1)") {
		t.Error("_911 does not reach the generated emergency context when there is one")
	}
	if !strings.Contains(body, "same => n,Dial(PJSIP/911@${DEFAULT_TRUNK},60)") {
		t.Error("_911 has no single-trunk fallback for an install with no trunks.toml")
	}
}

// One outbound implementation, included by both paths. The plain dial and the
// *4 console differing by a copied Dial line is exactly the drift this shape
// exists to remove — and a caller ID leaving by the wrong trunk is invisible
// from this end.
func TestBothOutboundPathsShareOneContext(t *testing.T) {
	body := shippedDialplan(t)
	for _, name := range []string{"internal", "outbound-console"} {
		if !strings.Contains(dialplanContext(t, body, name), "include => cmm-outbound") {
			t.Errorf("[%s] does not go through the shared outbound context", name)
		}
	}
	ctx := dialplanContext(t, body, "cmm-outbound")
	for _, want := range []string{
		"Set(CMM_TRUNK=${DEFAULT_TRUNK})",
		"ExecIf($[\"${OUTBOUND_TRUNK}\" != \"\"]?Set(CMM_TRUNK=${OUTBOUND_TRUNK}))",
		"ExecIf($[\"${OUTBOUND_CID}\" != \"\"]?Set(CALLERID(num)=${OUTBOUND_CID}))",
		"Dial(PJSIP/${EXTEN}@${CMM_TRUNK},60)",
		// Ten digits join the eleven-digit path rather than copying its
		// Dial, so there is one ladder and not two that can drift.
		"exten => _NXXNXXXXXX,1,Goto(cmm-outbound,1${EXTEN},1)",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("[cmm-outbound] is missing %q:\n%s", want, ctx)
		}
	}
	if strings.Contains(ctx, "Dial(PJSIP/1${EXTEN}@${CMM_TRUNK},60)") {
		t.Error("[cmm-outbound] carries a second Dial for ten digits — one ladder, not two")
	}
}

// The failover ladder: only a trunk failure climbs it, every step is a
// generated per-trunk context reached by name, and nothing at the bottom is
// silent. The far end being busy, not answering, or the caller hanging up are
// answers and never retried down another provider — a customer who declined
// the call once must not get it again from a different number.
func TestOutboundLadderClimbsOnlyOnTrunkFailure(t *testing.T) {
	ctx := dialplanContext(t, shippedDialplan(t), "cmm-outbound")
	for _, want := range []string{
		`GotoIf($["${DIALSTATUS}"="CHANUNAVAIL" | "${DIALSTATUS}"="CONGESTION"]?ladder:done)`,
		"same => n(ladder),Set(CMM_LADDER=${OUTBOUND_FAILOVER})",
		`same => n(more),GotoIf($["${CMM_LADDER}"=""]?failed)`,
		`Set(CMM_TRUNK=${CUT(CMM_LADDER,\,,1)})`,
		`Set(CMM_LADDER=${CUT(CMM_LADDER,\,,2-)})`,
		"GotoIf(${DIALPLAN_EXISTS(cmm-failover-${CMM_TRUNK},${EXTEN},1)}?cmm-failover-${CMM_TRUNK},${EXTEN},1)",
		"same => n(failed),NoOp(NO TRUNK COULD CARRY THIS CALL)",
		"same => n,Playback(all-circuits-busy-now)",
		"same => n,Congestion(5)",
		"same => n(done),Hangup()",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("[cmm-outbound] is missing %q:\n%s", want, ctx)
		}
	}
	for _, never := range []string{`"BUSY"`, `"NOANSWER"`, `"CANCEL"`} {
		if strings.Contains(ctx, never) {
			t.Errorf("[cmm-outbound] retries on %s — that is an answer from the far end, not a trunk failure", never)
		}
	}
}

// *97 is the phone's own box, no questions; *98 is the old menu, for any
// box with its PIN. A handset with no box gets the menu on *97 too.
func TestVoicemailKeyOpensThePhonesOwnBox(t *testing.T) {
	body := shippedDialplan(t)
	ctx := dialplanContext(t, body, "features-internal")
	for _, want := range []string{
		`exten => *97,1,Answer()`,
		` same => n,GotoIf($["${CMM_MAILBOX}" != ""]?own)`,
		` same => n,VoiceMailMain(@household)`,
		` same => n(own),VoiceMailMain(${CMM_MAILBOX}@household,s)`,
		`exten => *98,1,Answer()`,
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("[features-internal] is missing %q", want)
		}
	}
}

// *78NN quiets the phone that dialled it — the endpoint comes from the
// channel, never the keypad — for 15, 30 or 45 minutes and nothing else;
// *79 clears it. The state is an Asterisk global, which the generated
// dialplan and doorman both read and only this code writes.
func TestDoNotDisturbCodesWriteAsteriskOwnedState(t *testing.T) {
	ctx := dialplanContext(t, shippedDialplan(t), "features-internal")
	for _, want := range []string{
		`exten => _*78XX,1,Answer()`,
		` same => n,GotoIf($["${MINS}"="15" | "${MINS}"="30" | "${MINS}"="45"]?ok)`,
		` same => n,Playback(call-me-maybe/system/quiet-refused)`,
		` same => n(ok),Set(EP=${CHANNEL(endpoint)})`,
		` same => n,Set(GLOBAL(DND_${REPLACE(EP,-,_)})=$[${EPOCH} + ${MINS} * 60])`,
		` same => n,SayNumber(${MINS})`,
		`exten => *79,1,Answer()`,
		` same => n,Set(GLOBAL(DND_${REPLACE(EP,-,_)})=)`,
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("[features-internal] is missing %q", want)
		}
	}
}

// The system phrases the dialplan and the daemon play live under one
// prefix, and it is the bundled pack's — a swapped lobby voice must not
// silence a do-not-disturb answer or a balance alert.
func TestSystemPhrasesShareOnePrefix(t *testing.T) {
	body := shippedDialplan(t)
	if !strings.Contains(body, "call-me-maybe/system/quiet-for") {
		t.Fatal("the dialplan does not play the system phrases from call-me-maybe/system")
	}
	media, _ := lobby.AnnounceMedia(lobby.AnnounceKindBalance, "1")
	if len(media) == 0 || !strings.HasPrefix(media[0], "sound:call-me-maybe/system/") {
		t.Fatalf("the daemon plays from %v, want sound:call-me-maybe/system/…", media)
	}
}

// A dashed handset id becomes an underscored variable on both sides, or the
// phone that set it and the dialplan that reads it name different things.
func TestDNDVariableNameMatchesTheDialplansReplace(t *testing.T) {
	if got := render.DNDVar("master-bed"); got != "DND_master_bed" {
		t.Errorf("DNDVar = %q", got)
	}
}
