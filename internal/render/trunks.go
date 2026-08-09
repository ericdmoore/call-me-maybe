package render

// Trunk rendering: the provider side of the phone plant.
//
// Same job as the handset side and the same reasoning. A trunk is six fields
// that become four PJSIP objects, and the fourth of them — the registration —
// carries two settings that are the difference between a phone that rings and
// one that silently does not. Written once by hand that is fine. Written per
// provider, by somebody adding their second one at 10pm, it is a data problem.
//
// The inbound dialplan is generated here too, and that is a deliberate choice
// rather than a convenience. With several providers there is one context per
// registration and one route per DID inside it, so the thing grows as
// providers × numbers — and every one of those lines is mechanical, derived
// entirely from `[line] number` and `[line] trunk`. Hand-writing it means
// keeping two files in agreement about which number belongs to which line,
// which is exactly the drift `doorman render` exists to remove.

import (
	"fmt"
	"sort"
	"strings"

	"callmemaybe/internal/policy"
)

// TrunkLine is one line as the trunk side sees it: the name the dialplan
// passes to Stasis, the DID it answers, and the provider that DID arrives on.
//
// It is a mirror of the parts of policy.LineIdentity this package needs, plus
// the line's name, which the identity does not carry — the same arrangement
// lobby.OriginateParams has with the ARI client, and for the same reason:
// render should not have to grow a dependency on how lines are discovered.
type TrunkLine struct {
	// Name is the Stasis argument — "default" for the primary line.
	Name string
	// Label is the human name, for the comment above the route.
	Label string
	// Number is the DID in E.164, or "" when the line does not say which
	// number it answers. A line with no number gets a context but no route.
	Number string
	// Trunk is the trunks.toml id this line's number arrives on, or "".
	Trunk string
}

// TrunkFragments holds the generated trunk file contents.
type TrunkFragments struct {
	// PJSIP is the auth/registration/aor/endpoint objects — pjsip_trunks.conf.
	PJSIP string
	// Dialplan is the inbound contexts, the per-line contexts and the
	// emergency ladder — extensions_trunks.conf.
	Dialplan string
	// Trunks is how many providers were rendered.
	Trunks int
	// Routes is how many DID routes were generated across every trunk. Zero
	// with trunks present is a real and reportable state: the registrations
	// will work and every call will land on the default line.
	Routes int
	// Emergency is the trunk 911 leaves by, and Fallbacks are the trunks it
	// tries after that one, in order. Reported back so `doorman render` can
	// print the most consequential thing it just generated rather than leaving
	// it to be read out of a file.
	Emergency string
	Fallbacks []string
}

// Emergency is where a 911 call leaves by, already resolved by the caller.
//
// Resolved rather than derived here, because the rule spans two files: an
// explicit emergency_trunk in trunks.toml wins, and unset it is the primary
// line's [line] trunk — plain policy.toml, the file that is already the
// default for everything unqualified. render sees lines one at a time and has
// no business re-deriving it. cmd/doorman resolves it once, prints it to the
// operator, and passes the same answer here, so the file and the terminal
// cannot disagree about the most safety-critical route in the system.
type Emergency struct {
	// Trunk is the id 911 leaves by. Empty is refused: a generated dialplan
	// with no answer for 911 is the one output this package will not write.
	Trunk string
	// Chosen distinguishes an explicit emergency_trunk from the inferred
	// primary line, for the comment above the generated context. Config that
	// looks wrong is only fixable if it says where the value came from.
	Chosen bool
}

// LineContext is the dialplan context that runs one line. Generated names are
// prefixed so they cannot collide with a context somebody wrote by hand, the
// same way cmm-hangup already does.
func LineContext(name string) string { return "cmm-line-" + name }

// EmergencyContext is the generated context 911 is routed through. The
// hand-written dialplan reaches it with DIALPLAN_EXISTS, so the name is part of
// the interface between the two files and not an internal detail.
const EmergencyContext = "cmm-emergency"

// registrationBinding emits the two settings that make a registration-based
// trunk work, with the reason attached.
//
// This is the single highest-consequence detail in the file and it has exactly
// one source. `line=yes` plus `endpoint=` bind inbound traffic arriving on this
// registration to the endpoint below, which is what removes the need for an
// identify block, a provider IP allow-list, and any port forward. Get it wrong
// and calls hit the anonymous endpoint and vanish — no error, no log line,
// just a number that never rings.
func registrationBinding(endpointID string) string {
	return "; line=yes + endpoint= bind inbound calls arriving on this\n" +
		"; registration to the endpoint below. Without BOTH, inbound hits the\n" +
		"; anonymous endpoint and vanishes silently — no identify block and no\n" +
		"; provider IP allow-list is needed, or wanted, when these are right.\n" +
		"line=yes\n" +
		"endpoint=" + endpointID + "\n"
}

const inboundNote = `; The DID routes come from every policy file's [line] number and [line] trunk;
; the contexts come from trunks.toml. Asterisk reads none of this until
; extensions.conf #includes it — see docs/RUNBOOK.md, "Add a second provider" —
; and the hand-written [inbound-trunk] must come out when it does.
;
; The last context in this file is [cmm-emergency], which is how 911 leaves the
; building once this file is included. Until it is, the hand-written _911 dials
; one trunk, exactly as it always has: extensions.conf reaches this context
; through DIALPLAN_EXISTS, so #including the file IS the switch and there is no
; second edit for anybody to forget.

`

const noIdentifyNote = `; No identify blocks are generated, and that is the design rather than an
; omission. Inbound is bound to its endpoint by line=yes + endpoint= on each
; registration below, so matching a provider's source address is redundant —
; and an IP allow-list is a list that goes stale silently the day a provider
; adds a media server. A provider that can only do IP authentication needs a
; hand-written block and is out of scope here; see docs/RUNBOOK.md.

`

// BuildTrunks renders the provider side. Secrets come exclusively through env,
// exactly as they do for handsets: trunks.toml names a variable and never
// holds a password, so the file is safe to commit and the generated output
// never is.
//
// handsetIDs is the inventory, used only to refuse a trunk id that collides
// with a handset id. Both become a PJSIP endpoint named after the id, and two
// endpoints with one name is a config Asterisk loads without complaining about
// in any way an operator would notice.
//
// em is where 911 leaves by, and an empty one is refused. Once several trunks
// exist, "whichever the dialplan happens to say" stops being an answer, and a
// generated dialplan that routes every DID beautifully and has nothing to say
// about an emergency call is worse than no generated dialplan at all.
func BuildTrunks(trunks []policy.Trunk, lines []TrunkLine, handsetIDs []string, em Emergency, env Env) (*TrunkFragments, error) {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if len(trunks) == 0 {
		return nil, fmt.Errorf("render: no trunks to render")
	}

	handset := make(map[string]bool, len(handsetIDs))
	for _, id := range handsetIDs {
		handset[id] = true
	}
	known := make(map[string]policy.Trunk, len(trunks))
	for _, tr := range trunks {
		known[tr.ID] = tr
	}

	// Checked before anything is written. Everything else in this file is
	// recoverable by editing config and re-rendering; a dialplan installed with
	// no route for 911 is discovered by somebody who needed it.
	switch _, ok := known[em.Trunk]; {
	case em.Trunk == "":
		fail("no trunk carries emergency calls — set emergency_trunk in trunks.toml, " +
			"or give plain policy.toml a [line] trunk. With one provider there was " +
			"nothing to choose; with several this is the one thing that may not be undecided")
	case !ok:
		fail("the emergency trunk %q is not declared in trunks.toml", em.Trunk)
	}

	var pjsip, plan strings.Builder
	pjsip.WriteString(header("trunks.toml", true))
	pjsip.WriteString(noIdentifyNote)
	plan.WriteString(header("trunks.toml", false))
	plan.WriteString(inboundNote)

	// Routes first, grouped by trunk, so a missing secret and a misrouted DID
	// are both reported before anything is written.
	routes := map[string][]TrunkLine{}
	seenDID := map[string]string{}
	for _, l := range lines {
		if l.Number == "" || l.Trunk == "" {
			continue
		}
		if _, ok := known[l.Trunk]; !ok {
			// Normally caught at policy load, where the message names the file.
			// Here as a belt: render writes the file inbound calls depend on.
			fail("line %q names trunk %q, which trunks.toml does not declare", l.Name, l.Trunk)
			continue
		}
		key := l.Trunk + " " + l.Number
		if prev, dup := seenDID[key]; dup {
			fail("lines %q and %q both answer %s on trunk %q — one DID reaches one line",
				prev, l.Name, policy.Redact(l.Number), l.Trunk)
			continue
		}
		seenDID[key] = l.Name
		routes[l.Trunk] = append(routes[l.Trunk], l)
	}

	generated, routeCount := 0, 0
	for _, tr := range trunks {
		if handset[tr.ID] {
			fail("trunk id %q collides with a handset of the same id — both generate "+
				"a PJSIP endpoint named %s, and only one of them would work", tr.ID, tr.ID)
			continue
		}
		secret, ok := env(tr.PasswordEnv)
		if !ok || secret == "" {
			fail("trunk %q: %s is not set — add it to .env", tr.ID, tr.PasswordEnv)
			continue
		}

		writeTrunkPJSIP(&pjsip, tr, secret)
		routeCount += writeTrunkContext(&plan, tr, routes[tr.ID])
		generated++
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("render: %s", strings.Join(problems, "; "))
	}

	writeLineContexts(&plan, lines)
	order := EmergencyOrder(trunks, em.Trunk)
	writeEmergencyContext(&plan, order, em)

	fallbacks := make([]string, 0, len(order))
	for _, tr := range order[1:] {
		fallbacks = append(fallbacks, tr.ID)
	}
	return &TrunkFragments{
		PJSIP:     pjsip.String(),
		Dialplan:  plan.String(),
		Trunks:    generated,
		Routes:    routeCount,
		Emergency: em.Trunk,
		Fallbacks: fallbacks,
	}, nil
}

func writeTrunkPJSIP(b *strings.Builder, tr policy.Trunk, secret string) {
	title := tr.ID
	if tr.Provider != "" {
		title = tr.Provider + " — " + tr.ID
	}
	rule := 56 - len([]rune(title))
	if rule < 3 {
		rule = 3
	}
	fmt.Fprintf(b, "; ── %s %s\n\n", title, strings.Repeat("─", rule))

	fmt.Fprintf(b, "[%s_auth]\ntype=auth\nauth_type=userpass\nusername=%s\npassword=%s\n\n",
		tr.ID, tr.Username, secret)

	fmt.Fprintf(b, "[%s_reg]\ntype=registration\n", tr.ID)
	fmt.Fprintf(b, "outbound_auth=%s_auth\n", tr.ID)
	fmt.Fprintf(b, "server_uri=sip:%s\n", tr.Host)
	fmt.Fprintf(b, "client_uri=sip:%s@%s\n", tr.FromUser, tr.Host)
	fmt.Fprintf(b, "contact_user=%s\n", tr.FromUser)
	b.WriteString("retry_interval=60\nforbidden_retry_interval=600\n")
	fmt.Fprintf(b, "expiration=%d\n", tr.Expiration)
	b.WriteString(registrationBinding(tr.ID))
	b.WriteString("\n")

	fmt.Fprintf(b, "[%s_aor]\ntype=aor\ncontact=sip:%s\nqualify_frequency=60\n\n", tr.ID, tr.Host)

	fmt.Fprintf(b, "[%s]\ntype=endpoint\ncontext=%s\ndisallow=all\n", tr.ID, tr.Context)
	for _, c := range tr.Codecs {
		fmt.Fprintf(b, "allow=%s\n", c)
	}
	fmt.Fprintf(b, "outbound_auth=%s_auth\naors=%s_aor\n", tr.ID, tr.ID)
	fmt.Fprintf(b, "from_user=%s\nfrom_domain=%s\n", tr.FromUser, tr.FromDomain)
	// Transcoding is wasted CPU on a Pi, but media stays on the box so calls
	// can be bridged and recorded.
	b.WriteString("direct_media=no\nrtp_symmetric=yes\nforce_rport=yes\nrewrite_contact=yes\n")
	// Invariant 9. Without it the lobby is deaf, every stranger is dismissed,
	// and there is no runtime symptom other than "nobody can ever get in".
	b.WriteString("dtmf_mode=rfc4733\n\n")
}

// writeTrunkContext emits one trunk's inbound context and returns how many DID
// routes it contains.
func writeTrunkContext(b *strings.Builder, tr policy.Trunk, lines []TrunkLine) int {
	fmt.Fprintf(b, "[%s]\n", tr.Context)
	if tr.Provider != "" {
		fmt.Fprintf(b, "; Calls arriving on the %s registration.\n", tr.Provider)
	}

	sorted := append([]TrunkLine(nil), lines...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Number < sorted[j].Number })

	count := 0
	for _, l := range sorted {
		label := l.Label
		if label == "" {
			label = "line " + l.Name
		}
		fmt.Fprintf(b, ";\n; %s  →  line %s\n", label, l.Name)
		for _, form := range didForms(l.Number) {
			fmt.Fprintf(b, "exten => %s,1,Goto(%s,${EXTEN},1)\n", form, LineContext(l.Name))
		}
		count++
	}

	b.WriteString(";\n")
	b.WriteString("; Every other number arriving here. An unrouted DID is answered by the\n")
	b.WriteString("; default line rather than falling off the end of a context: a number\n")
	b.WriteString("; somebody is paying for must never stop answering over a config edit.\n")
	fmt.Fprintf(b, "exten => _X.,1,Goto(%s,${EXTEN},1)\n\n", LineContext(policy.DefaultLine))
	return count
}

// writeLineContexts emits what each line actually does — identical to the
// hand-written [inbound-trunk] these replace, down to the default line's
// Stasis call taking no arguments at all. That last detail is the whole
// compatibility story for the dialplan: a call on the default line reaches
// doorman exactly as it did before any of this existed.
func writeLineContexts(b *strings.Builder, lines []TrunkLine) {
	b.WriteString("; ── One context per line ──────────────────────────────────────\n")
	b.WriteString("; What each line does once its trunk has routed the call here. The\n")
	b.WriteString("; default line's Stasis call takes no arguments, which is exactly what\n")
	b.WriteString("; every install had before lines existed.\n\n")

	seen := map[string]bool{}
	for _, l := range lines {
		if seen[l.Name] {
			continue
		}
		seen[l.Name] = true

		fmt.Fprintf(b, "[%s]\n", LineContext(l.Name))
		if l.Name == policy.DefaultLine {
			b.WriteString("exten => _X.,1,NoOp(Inbound from ${CALLERID(num)} to ${EXTEN})\n")
		} else {
			fmt.Fprintf(b, "exten => _X.,1,NoOp(Inbound from ${CALLERID(num)} to ${EXTEN} on line %s)\n", l.Name)
		}
		b.WriteString(" same => n,Set(CHANNEL(hangup_handler_push)=cmm-hangup,s,1)\n")
		// Answered here rather than in ARI so the audio path is already up when
		// the greeting starts.
		b.WriteString(" same => n,Answer()\n")
		fmt.Fprintf(b, " same => n,Stasis(${DOORMAN_APP}%s)\n", stasisArgs(l.Name))
		b.WriteString(" same => n,Hangup()\n\n")
	}
}

// ── Emergency calls ──────────────────────────────────────────────────────

// EmergencyOrder is the order 911 tries trunks in: the designated one, then
// every other, the ones most likely to have a street address on file first.
//
// The designated trunk is first because that is the whole decision — E911 is
// registered per DID against an address, and only its provider has this
// household's. The rest is the fallback, and it needs an order because a
// fallback that fires is already a call whose location data may be wrong. A
// trunk that declares e911 = true is the best of the remaining answers and a
// trunk that declares e911 = false is the worst, so they are tried in that
// order, stable within each group so the generated file reads the same way
// twice running. Nothing safety-critical is decided by file order: the
// designated trunk is named, never picked.
func EmergencyOrder(trunks []policy.Trunk, designated string) []policy.Trunk {
	out := make([]policy.Trunk, 0, len(trunks))
	rest := make([]policy.Trunk, 0, len(trunks))
	for _, tr := range trunks {
		if tr.ID == designated {
			out = append(out, tr)
			continue
		}
		rest = append(rest, tr)
	}
	sort.SliceStable(rest, func(i, j int) bool { return e911Rank(rest[i]) < e911Rank(rest[j]) })
	return append(out, rest...)
}

// e911Rank orders a trunk by what it says about its own registered address.
// Unknown sorts between declared and declared-absent: it might have one, and a
// trunk that has said it has none is the last thing to try.
func e911Rank(tr policy.Trunk) int {
	switch {
	case tr.E911 == nil:
		return 1
	case *tr.E911:
		return 0
	default:
		return 2
	}
}

// writeEmergencyContext generates the one route in this system that somebody's
// life may depend on.
//
// Three decisions are baked into the shape of it and each is deliberate.
//
// **No caller ID is set, anywhere in here.** E911 is registered per DID against
// a street address, so an emergency call leaves as the trunk's own number and
// never as a line's outbound_cid. OUTBOUND_CID and OUTBOUND_TRUNK are both
// ignored: which trunk carries 911 is a property of the house, not of whichever
// line the nearest handset happens to call as, and whoever grabs that handset
// has no idea which line they are on.
//
// **The trunk is tried, not asked.** There is no DEVICE_STATE check before the
// Dial. A provider that does not answer OPTIONS looks unreachable while working
// perfectly, and diverting an emergency call away from the one trunk whose
// address is filed — on a false negative, at the worst possible moment — is
// precisely the failure this whole design exists to prevent. An unregistered
// trunk fails the Dial in milliseconds with CHANUNAVAIL, which is the same
// answer arrived at honestly.
//
// **Falling over is a trade, not a nicety.** If the designated trunk cannot
// carry the call, the next one may put the wrong address on a dispatcher's
// screen, which is genuinely bad. A connected call lets a human say their
// address out loud; a failed call gives them nothing. Connection first.
func writeEmergencyContext(b *strings.Builder, order []policy.Trunk, em Emergency) {
	how := "inferred — the primary line's trunk, from policy.toml"
	if em.Chosen {
		how = "chosen — emergency_trunk in trunks.toml"
	}

	b.WriteString("; ── Emergency calls ──────────────────────────────────────────\n")
	fmt.Fprintf(b, "; 911 leaves by %s (%s).\n", em.Trunk, how)
	b.WriteString(";\n")
	b.WriteString("; NO caller ID is set here and none may ever be added. E911 is registered\n")
	b.WriteString("; per DID against a street address, so an emergency call leaves as the\n")
	b.WriteString("; trunk's own number — never as a business line whose registered address is\n")
	b.WriteString("; somebody else's house. OUTBOUND_CID and OUTBOUND_TRUNK are both ignored:\n")
	b.WriteString("; whoever grabs the nearest handset has no idea which line they are on.\n")
	b.WriteString(";\n")
	b.WriteString("; The trunks below are tried in order and the call takes the first that\n")
	b.WriteString("; connects. The designated one is first; the rest are the fallback, ordered\n")
	b.WriteString("; by what each trunk declares about having an address on file (e911 in\n")
	b.WriteString("; trunks.toml), because a fallback that fires is already a call whose\n")
	b.WriteString("; location data may be wrong. That is a deliberate trade: a connected call\n")
	b.WriteString("; lets a human say their address out loud, a failed call gives them nothing.\n")
	b.WriteString(";\n")
	b.WriteString("; Nothing here asks whether a trunk is registered — it tries it. A provider\n")
	b.WriteString("; that does not answer OPTIONS looks unreachable while working perfectly,\n")
	b.WriteString("; and an unregistered trunk fails the Dial in milliseconds anyway.\n")
	b.WriteString(";\n")
	b.WriteString("; And the honest framing: this is a supplementary phone. It stops working in\n")
	b.WriteString("; a power cut and on a bad internet day. Nobody should make it a household's\n")
	b.WriteString("; only route to emergency services.\n")

	fmt.Fprintf(b, "[%s]\n", EmergencyContext)
	fmt.Fprintf(b, "exten => _911,1,NoOp(Emergency call from ${CALLERID(num)} via %s)\n", em.Trunk)
	for i, tr := range order {
		if i > 0 {
			fmt.Fprintf(b, " same => n,NoOp(FALLING OVER to %s: the trunks before it could not carry this call)\n", tr.ID)
		}
		fmt.Fprintf(b, " same => n,Dial(PJSIP/911@%s,60)\n", tr.ID)
		b.WriteString(" same => n,GotoIf($[\"${DIALSTATUS}\"=\"ANSWER\"]?done)\n")
	}
	// Loud, and audible. A caller who has just dialled 911 must hear something
	// rather than silence: the tone is what tells them to reach for a mobile.
	b.WriteString(" same => n,NoOp(NO TRUNK COULD CARRY THIS EMERGENCY CALL)\n")
	b.WriteString(" same => n,Playback(all-circuits-busy-now)\n")
	b.WriteString(" same => n,Congestion(5)\n")
	b.WriteString(" same => n(done),Hangup()\n\n")
}

func stasisArgs(line string) string {
	if line == policy.DefaultLine {
		return ""
	}
	return ",line," + line
}

// didForms is every digit string a provider might present for one DID.
//
// Providers differ and there is no standard: VoIP.ms sends ten digits, others
// send eleven, others full +E.164. The dialplan match has to be exact — an
// exact extension is what beats the _X. fallback — so until now the runbook
// had an operator watch `asterisk -rvvv` to find out which. Matching all three
// costs two extra lines per DID and deletes the step: whichever arrives, one
// of these is it, and the two that never fire cost nothing.
//
// The ten-digit form is emitted only for NANP numbers, where "drop the country
// code" is well defined. Elsewhere a national format is a guess, and a wrong
// guess in a dialplan is a route that fires for somebody else's number.
func didForms(e164 string) []string {
	digits := strings.TrimPrefix(e164, "+")
	forms := []string{e164, digits}
	if strings.HasPrefix(e164, "+1") && len(digits) == 11 {
		forms = append(forms, digits[1:])
	}
	return forms
}
