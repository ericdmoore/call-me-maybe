// Package render generates the per-handset Asterisk configuration from
// handsets.toml, making the inventory file the single source of truth.
// Before this existed, adding a phone meant hand-editing three places that
// had to agree (PJSIP blocks, dialplan numbers/hints, the policy handset
// list) — the most error-prone procedure in the runbook. Now it is one file
// and one command.
//
// Only the repetitive per-phone material is generated. The trunk, transports,
// ARI, and feature dialplan stay hand-authored: they are written once and
// carry judgement; the handset blocks are written N times and carry none.
package render

import (
	"fmt"
	"sort"
	"strings"

	"callmemaybe/internal/policy"
)

// Env resolves a secret by name — process environment layered over .env.
type Env func(key string) (string, bool)

// Fragments holds the generated file contents.
type Fragments struct {
	// PJSIP is the endpoint/auth/aor blocks — pjsip_handsets.conf.
	PJSIP string
	// Dialplan is the [handsets-internal] context (numbers, hints, ring-all,
	// page-all) — extensions_handsets.conf.
	Dialplan string
	// Generated counts the PJSIP handsets rendered; pseudo-handsets
	// (Local/... endpoints) are listed in the map but not generated.
	Generated int
	// Voicemail is the [household](+) mailbox lines — voicemail_handsets.conf
	// — one per box a handset names whose PIN is in .env, and a comment for
	// each box that is not, which is assumed to be written by hand.
	Voicemail string
	// Mailboxes is what Voicemail was built from, for `doorman check` and
	// `doorman render` to report.
	Mailboxes []Mailbox
	// PageOverrides are the handsets whose pages reach a quiet room, in id
	// order — empty is a house where nobody can, which `check` warns about.
	PageOverrides []string
}

// A phone's own voicemail (s21): `mailbox` on a handset is a box the tool
// makes. render writes the mailbox line, with the PIN from
// VOICEMAIL_<BOX>_PIN in .env exactly as handset passwords come from
// HANDSET_<ID>_PASSWORD; gives the endpoint CMM_MAILBOX so `*97` opens that
// box without asking; and lets a room call that rings out fall into it. A
// box whose PIN is not in .env is left alone and named in a comment — that
// is every install from before this existed, where `family` lives in the
// hand-written voicemail.conf and must go on working untouched.

// Mailbox is one voicemail box as the inventory describes it.
type Mailbox struct {
	ID string
	// Handsets name it, in inventory order. One handset makes it that
	// phone's own box; several make it a shared one.
	Handsets []string
	// Label is the display name written to Asterisk: the one handset's
	// label, or the id made readable when the box is shared.
	Label string
	// Email is the first address any of its handsets gives, or "".
	Email string
	// EnvVar names the .env variable holding its PIN, and Generated says
	// whether that variable was set — that is, whether render wrote it.
	EnvVar    string
	Generated bool
	pin       string
}

// VoicemailPINEnv is the .env variable holding a mailbox's PIN.
func VoicemailPINEnv(box string) string {
	return "VOICEMAIL_" + strings.ToUpper(strings.ReplaceAll(box, "-", "_")) + "_PIN"
}

// Mailboxes lists every box the handsets name, in id order, with what
// render knows about each.
func Mailboxes(handsets []policy.Handset, env Env) []Mailbox {
	byID := map[string]*Mailbox{}
	var order []string
	for _, h := range handsets {
		if h.Mailbox == "" {
			continue
		}
		m, ok := byID[h.Mailbox]
		if !ok {
			m = &Mailbox{ID: h.Mailbox, EnvVar: VoicemailPINEnv(h.Mailbox)}
			byID[h.Mailbox] = m
			order = append(order, h.Mailbox)
		}
		m.Handsets = append(m.Handsets, h.ID)
		if m.Email == "" {
			m.Email = h.Email
		}
	}
	sort.Strings(order)
	out := make([]Mailbox, 0, len(order))
	for _, id := range order {
		m := byID[id]
		if len(m.Handsets) == 1 {
			for _, h := range handsets {
				if h.ID == m.Handsets[0] {
					m.Label = h.Label
				}
			}
		}
		if m.Label == "" {
			m.Label = humanise(id)
		}
		if v, ok := env(m.EnvVar); ok && v != "" {
			m.Generated, m.pin = true, v
		}
		out = append(out, *m)
	}
	return out
}

// humanise turns "whole-house" into "Whole house" for a display name.
func humanise(id string) string {
	s := strings.NewReplacer("-", " ", "_", " ").Replace(id)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Do not disturb (s12) is a time-boxed value Asterisk owns: the global
// variable DND_<handset> holds an expiry, set by *78NN on the phone itself
// and cleared by *79, by time, or by an Asterisk restart. Everything below
// only reads it. A global rather than the AstDB because ARI cannot read
// DB() without asterisk.conf opening every "dangerous" function to every
// ARI user. dndExpiry is the expression that reads it safely — unset is 0,
// never an empty operand that makes the expression parser warn and answer
// false by accident.
func DNDVar(id string) string { return "DND_" + strings.ReplaceAll(id, "-", "_") }

func dndExpiry(id string) string {
	v := DNDVar(id)
	return "${IF($[\"${" + v + "}\"!=\"\"]?${" + v + "}:0)}"
}

// systemMedia is where the house's own phrases live (the bundled pack's
// system/ directory), the same prefix internal/lobby plays announcements
// from; a test in cmd/doorman keeps the two agreeing.
const systemMedia = "call-me-maybe/system"

// buildVoicemail renders voicemail_handsets.conf from the boxes.
func buildVoicemail(boxes []Mailbox) string {
	var b strings.Builder
	b.WriteString(header("handsets.toml", false))
	b.WriteString("; THIS FILE CONTAINS VOICEMAIL PINS. Install it root-owned and mode 0640,\n")
	b.WriteString("; and never commit it. voicemail.conf reaches it with\n")
	b.WriteString(";   #tryinclude \"voicemail_handsets.conf\"\n")
	b.WriteString("; and [household](+) below appends to the hand-written context there, so\n")
	b.WriteString("; a box written by hand (family, the hunt's) keeps working beside these.\n")
	b.WriteString("; A box named here without VOICEMAIL_<BOX>_PIN in .env is not written: it\n")
	b.WriteString("; is assumed to be one of the hand-written ones.\n\n")
	b.WriteString("[household](+)\n")
	for _, m := range boxes {
		if !m.Generated {
			fmt.Fprintf(&b, "; %s: %s is not set in .env — assumed hand-written in voicemail.conf (%s)\n",
				m.ID, m.EnvVar, strings.Join(m.Handsets, ", "))
			continue
		}
		line := fmt.Sprintf("%s => %s,%s", m.ID, m.pin, strings.ReplaceAll(m.Label, ",", " "))
		if m.Email != "" {
			line += "," + m.Email
		}
		fmt.Fprintf(&b, "%s\n", line)
	}
	return b.String()
}

// header is the banner every generated file carries. from names the source of
// truth so the fix — edit that, re-render — is on the screen of whoever opened
// the wrong file. secrets adds the line that matters for the PJSIP fragments:
// they hold real passwords, and a generated file is the one somebody assumes
// is safe to paste into an issue.
func header(from string, secrets bool) string {
	var b strings.Builder
	b.WriteString("; ══════════════════════════════════════════════════════════════\n")
	fmt.Fprintf(&b, "; GENERATED by \"doorman render\" from %s — DO NOT EDIT.\n", from)
	fmt.Fprintf(&b, "; Hand edits are lost on the next render; change %s instead.\n", from)
	if secrets {
		b.WriteString("; THIS FILE CONTAINS REAL SIP PASSWORDS. Install it root-owned and\n")
		b.WriteString("; mode 0640, and never commit it — not to a private repo either.\n")
	}
	b.WriteString("; ══════════════════════════════════════════════════════════════\n\n")
	return b.String()
}

// The two channel variables the dialplan reads to place an outbound call: what
// it presents, and which provider it leaves by. Each name appears in exactly
// three places and they have to agree: here, internal/lobby/console.go, and
// the shared [cmm-outbound] context in asterisk/extensions.conf. There is no
// shared constant because the third of those is not Go — the same arrangement
// "voicemail-drop" has always had.
const (
	outboundCIDVar   = "OUTBOUND_CID"
	outboundTrunkVar = "OUTBOUND_TRUNK"
	// outboundFailoverVar is the ladder: trunk ids, comma-separated, in the
	// order [cmm-outbound] tries them after OUTBOUND_TRUNK fails. Each step
	// is a generated [cmm-failover-<id>] context (trunks.go), so the dialplan
	// holds the dial strings and this variable holds only names.
	outboundFailoverVar = "OUTBOUND_FAILOVER"
)

// OutboundIdentity is what one handset presents and where its calls leave by.
//
// The two travel together in one value on purpose. A provider will not let you
// present a number its account does not own — it rejects the call or silently
// rewrites the caller ID as anti-spoofing — so the caller ID and the trunk are
// one decision, not two settings that happen to sit near each other. Two maps
// could disagree; a pair cannot.
type OutboundIdentity struct {
	// CID is what the callee sees, in E.164, or "" for whatever the trunk
	// sends — which is what every outbound call did before lines existed.
	CID string
	// Trunk is the PJSIP endpoint the call leaves by: a trunks.toml id. Empty
	// means the dialplan's DEFAULT_TRUNK decides, which is every install with
	// one provider and no trunks.toml at all.
	Trunk string
	// Failover is the ladder of trunk ids, comma-joined, a call from this
	// handset falls over to when Trunk cannot carry it. Empty is no ladder,
	// which is every line that did not write [line] failover.
	Failover string
}

// set reports whether this handset has any outbound identity to write.
func (o OutboundIdentity) set() bool { return o.CID != "" || o.Trunk != "" || o.Failover != "" }

// Build renders both fragments. Secrets come exclusively through env — the
// generated PJSIP file contains real passwords and must be treated like
// pjsip.conf itself (root-owned, mode 0640, never committed).
//
// outbound maps handset id to the identity that phone calls with — the caller
// ID it presents and the trunk it leaves by — resolved from [line]
// outbound_cid, [line] trunk and [line] outbound_handsets across every line.
// It is generated rather than configured in the dialplan because the plain
// _NXXNXXXXXX path never reaches doorman: a handset dialling a number talks to
// Asterisk and nothing else, which is exactly the property that keeps outbound
// calling working when doorman is down. A handset with no entry gets no
// set_var, and its endpoint is byte-identical to what it was before per-line
// identity existed.
func Build(handsets []policy.Handset, env Env, outbound map[string]OutboundIdentity) (*Fragments, error) {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	var pjsip, plan strings.Builder
	pjsip.WriteString(header("handsets.toml", true))
	plan.WriteString(header("handsets.toml", false))
	plan.WriteString("[handsets-internal]\n")

	var all, pageMembers, overrides []string
	var messageRoutes []messageRoute
	generated := 0

	for _, h := range handsets {
		// Pseudo-handsets (Local/600@internal and friends) are legitimate
		// policy targets but have no SIP registration to generate.
		if !strings.HasPrefix(h.Endpoint, "PJSIP/") {
			continue
		}
		resource := strings.TrimPrefix(h.Endpoint, "PJSIP/")
		if resource != h.ID {
			// We generate the endpoint named after the id; an endpoint that
			// disagrees would silently never match what policy dials.
			fail("handset %q endpoint %q must be PJSIP/%s (render names the endpoint after the id)", h.ID, h.Endpoint, h.ID)
			continue
		}
		if h.PasswordEnv == "" {
			fail("handset %q needs password_env (the .env variable holding its SIP password)", h.ID)
			continue
		}
		secret, ok := env(h.PasswordEnv)
		if !ok || secret == "" {
			fail("handset %q: %s is not set — add it to .env", h.ID, h.PasswordEnv)
			continue
		}

		label := h.Label
		if label == "" {
			label = h.ID
		}

		fmt.Fprintf(&pjsip, "; ── %s ──\n[%s]\n", label, h.ID)
		pjsip.WriteString("type=endpoint\ncontext=internal\ndisallow=all\nallow=ulaw\nallow=g722\n")
		// outbound_auth as well as auth: a Grandstream challenges every NOTIFY
		// the box sends it (401, digest, the phone's own SIP credentials), and
		// without this Asterisk never answers the challenge, so a check-sync
		// is politely refused and `doorman provision notify` does nothing.
		// The same auth object serves both directions.
		fmt.Fprintf(&pjsip, "auth=%s-auth\noutbound_auth=%s-auth\naors=%s\n", h.ID, h.ID, h.ID)
		pjsip.WriteString("direct_media=no\nforce_rport=yes\nrewrite_contact=yes\nrtp_symmetric=yes\ndtmf_mode=rfc4733\n")
		// A text typed on a handset arrives as a SIP MESSAGE. Without its own
		// context it runs the call dialplan, where Dial() rings the other
		// phone as an anonymous call and delivers nothing (first re-provision,
		// 2026-09-24). [cmm-messages] below hands it to the phone as a message.
		pjsip.WriteString("message_context=cmm-messages\n")
		if h.Number > 0 {
			fmt.Fprintf(&pjsip, "callerid=%s <%d>\n", label, h.Number)
		} else {
			fmt.Fprintf(&pjsip, "callerid=%s\n", label)
		}
		if h.Mailbox != "" {
			fmt.Fprintf(&pjsip, "mailboxes=%s@household\n", h.Mailbox)
			// The phone's own box, for *97: VoiceMailMain(${CMM_MAILBOX}@household,s)
			// opens it without asking which, or for a PIN — a handset on the
			// LAN is already trusted to call as the house and page every room.
			fmt.Fprintf(&pjsip, "set_var=CMM_MAILBOX=%s\n", h.Mailbox)
		}
		if out := outbound[h.ID]; out.set() {
			// Read by [cmm-outbound], the one context both outbound paths go
			// through, and deliberately NOT by _911: which trunk carries an
			// emergency call is a property of the house — the trunk whose
			// street address is registered — never of whichever line the
			// nearest handset happens to call as.
			if out.CID != "" {
				fmt.Fprintf(&pjsip, "set_var=%s=%s\n", outboundCIDVar, out.CID)
			}
			if out.Trunk != "" {
				fmt.Fprintf(&pjsip, "set_var=%s=%s\n", outboundTrunkVar, out.Trunk)
			}
			if out.Failover != "" {
				fmt.Fprintf(&pjsip, "set_var=%s=%s\n", outboundFailoverVar, out.Failover)
			}
		}
		fmt.Fprintf(&pjsip, "\n[%s-auth]\ntype=auth\nauth_type=userpass\nusername=%s\npassword=%s\n\n", h.ID, h.ID, secret)
		fmt.Fprintf(&pjsip, "[%s]\ntype=aor\nmax_contacts=2\nremove_existing=yes\nqualify_frequency=60\n\n", h.ID)
		generated++

		if h.Number > 0 {
			// A quiet room is told about, inside the house: how many minutes
			// remain, exactly, then its mailbox if it has one. Outside
			// callers never reach this — the lobby's ladders are doorman's,
			// and a child's DND is not a stranger's information.
			fmt.Fprintf(&plan, "exten => %d,1,GotoIf($[%s > ${EPOCH}]?quiet)\n", h.Number, dndExpiry(h.ID))
			fmt.Fprintf(&plan, " same => n,Dial(%s,30)\n", h.Endpoint)
			if h.Mailbox != "" {
				// A room call that rings out lands in the room's own box —
				// busy gets the busy greeting — and a call that was answered
				// ends when the far end hangs up, without a detour through
				// voicemail on its way out.
				plan.WriteString(" same => n,GotoIf($[\"${DIALSTATUS}\"=\"ANSWER\"]?done)\n")
				plan.WriteString(" same => n,GotoIf($[\"${DIALSTATUS}\"=\"BUSY\"]?busy)\n")
				fmt.Fprintf(&plan, " same => n,VoiceMail(%s@household,u)\n", h.Mailbox)
				plan.WriteString(" same => n(done),Hangup()\n")
				fmt.Fprintf(&plan, " same => n(busy),VoiceMail(%s@household,b)\n", h.Mailbox)
				plan.WriteString(" same => n,Hangup()\n")
			} else {
				plan.WriteString(" same => n,Hangup()\n")
			}
			fmt.Fprintf(&plan, " same => n(quiet),Playback(%s/quiet-room)\n", systemMedia)
			// Two steps, because $[…] divides in floating point (SayNumber
			// read "15.000000") and MATH takes exactly one operation, so the
			// sum is $[…]'s and the rounding division is MATH's (first box,
			// 2026-09-25, both ways).
			fmt.Fprintf(&plan, " same => n,Set(LEFT=$[%s - ${EPOCH} + 59])\n", dndExpiry(h.ID))
			plan.WriteString(" same => n,SayNumber(${MATH(${LEFT}/60,int)})\n")
			fmt.Fprintf(&plan, " same => n,Playback(%s/quiet-minutes)\n", systemMedia)
			if h.Mailbox != "" {
				fmt.Fprintf(&plan, " same => n,VoiceMail(%s@household,u)\n", h.Mailbox)
			}
			plan.WriteString(" same => n,Hangup()\n")
			fmt.Fprintf(&plan, "exten => %d,hint,%s\n", h.Number, h.Endpoint)
			messageRoutes = append(messageRoutes, messageRoute{number: h.Number, id: h.ID, label: label})
		}
		all = append(all, h.Endpoint)
		if h.Page {
			pageMembers = append(pageMembers, h.Endpoint)
		}
		if h.PageOverride {
			overrides = append(overrides, h.ID)
		}
	}

	if generated == 0 {
		fail("no PJSIP handsets to render")
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("render: %s", strings.Join(problems, "; "))
	}

	sort.Strings(all)
	sort.Strings(pageMembers)
	boxes := Mailboxes(handsets, env)

	sort.Strings(overrides)
	plan.WriteString("\n; Ring every handset — every handset that is not quiet (*78NN).\n")
	plan.WriteString("exten => 100,1,Set(MEMBERS=)\n")
	for _, ep := range all {
		id := strings.TrimPrefix(ep, "PJSIP/")
		fmt.Fprintf(&plan, " same => n,ExecIf($[%s <= ${EPOCH}]?Set(MEMBERS=${MEMBERS}&%s))\n", dndExpiry(id), ep)
	}
	plan.WriteString(" same => n,GotoIf($[\"${MEMBERS}\"=\"\"]?none)\n")
	plan.WriteString(" same => n,Dial(${MEMBERS:1},30)\n")
	plan.WriteString(" same => n,Hangup()\n")
	fmt.Fprintf(&plan, " same => n(none),Playback(%s/quiet-page)\n", systemMedia)
	plan.WriteString(" same => n,Hangup()\n")
	if len(pageMembers) > 0 {
		plan.WriteString("\n; Page: members auto-answer on speaker (page = true in handsets.toml).\n")
		plan.WriteString("; A quiet room is skipped, and the pager is told — unless the pager is a\n")
		plan.WriteString("; page_override phone, whose page reaches every room regardless: a\n")
		plan.WriteString("; parent's page is what makes do-not-disturb safe to hand to a child.\n")
		plan.WriteString("exten => 500,1,Set(MEMBERS=)\n")
		plan.WriteString(" same => n,Set(QUIET=0)\n")
		if len(overrides) > 0 {
			var conds []string
			for _, id := range overrides {
				conds = append(conds, "\"${CHANNEL(endpoint)}\"=\""+id+"\"")
			}
			fmt.Fprintf(&plan, " same => n,Set(OVERRIDE=$[%s])\n", strings.Join(conds, " | "))
		} else {
			plan.WriteString(" same => n,Set(OVERRIDE=0)\n")
		}
		for _, ep := range pageMembers {
			id := strings.TrimPrefix(ep, "PJSIP/")
			fmt.Fprintf(&plan, " same => n,ExecIf($[${OVERRIDE} | %s <= ${EPOCH}]?Set(MEMBERS=${MEMBERS}&%s):Set(QUIET=1))\n", dndExpiry(id), ep)
		}
		fmt.Fprintf(&plan, " same => n,ExecIf($[${QUIET}]?Playback(%s/quiet-page))\n", systemMedia)
		plan.WriteString(" same => n,GotoIf($[\"${MEMBERS}\"=\"\"]?none)\n")
		plan.WriteString(" same => n,Page(${MEMBERS:1},ib(page-autoanswer^s^1),60)\n")
		plan.WriteString(" same => n,Hangup()\n")
		plan.WriteString(" same => n(none),Hangup()\n")
	}

	// Texts between handsets. Every endpoint above names this context for
	// SIP MESSAGE, so a message to a room number reaches that phone as a
	// message, and 100 reaches every phone.
	sort.Slice(messageRoutes, func(i, j int) bool { return messageRoutes[i].number < messageRoutes[j].number })
	plan.WriteString("\n; Texts between handsets (SIP MESSAGE): a room number, or 100 for everyone.\n[cmm-messages]\n")
	for _, r := range messageRoutes {
		fmt.Fprintf(&plan, "exten => %d,1,Gosub(cmm-message-from,s,1)\n same => n,MessageSend(pjsip:%s,${MSG_FROM})\n", r.number, r.id)
		// A phone replies to the From it was given. Before the rewrite below
		// that was the sender's endpoint id, so the reply must route too.
		fmt.Fprintf(&plan, "exten => %s,1,Goto(%d,1)\n", r.id, r.number)
	}
	if len(messageRoutes) > 0 {
		plan.WriteString("exten => 100,1,Gosub(cmm-message-from,s,1)\n")
		for _, r := range messageRoutes {
			fmt.Fprintf(&plan, " same => n,MessageSend(pjsip:%s,${MSG_FROM})\n", r.id)
		}
	}
	// No catch-all on purpose. Asterisk answers a MESSAGE to an extension
	// this context does not have with 404, and the handset shows an error —
	// which is the truth about an outside number typed into the messages
	// app: the house number does not text from handsets (s15). A NoOp here
	// would accept the message and drop it in silence.

	// The From a text carries is the sender's endpoint id; the phone shows
	// it and replies to it. Rewritten to the sender's room number and label,
	// so the receiving phone names the room from its phone book and a reply
	// to it routes like any other text.
	plan.WriteString("\n[cmm-message-from]\nexten => s,1,Set(MSGFROM=${MESSAGE(from)})\n")
	plan.WriteString(" same => n,Set(SENDER=${CUT(MSGFROM,@,1)})\n same => n,Set(SENDER=${CUT(SENDER,:,2)})\n")
	plan.WriteString(" same => n,Set(FROMDOM=${CUT(MSGFROM,@,2)})\n same => n,Set(FROMDOM=${CUT(FROMDOM,>,1)})\n")
	plan.WriteString(" same => n,Set(MSG_FROM=${MSGFROM})\n")
	for _, r := range messageRoutes {
		fmt.Fprintf(&plan, " same => n,ExecIf($[\"${SENDER}\" = \"%s\"]?Set(MSG_FROM=\"%s\" <sip:%d@${FROMDOM}>))\n", r.id, r.label, r.number)
	}
	plan.WriteString(" same => n,Return()\n")

	return &Fragments{PJSIP: pjsip.String(), Dialplan: plan.String(), Generated: generated,
		Voicemail: buildVoicemail(boxes), Mailboxes: boxes, PageOverrides: overrides}, nil
}

// messageRoute is one handset's number → endpoint for [cmm-messages].
type messageRoute struct {
	number int
	id     string
	label  string
}
