package render

import (
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

func env(m map[string]string) Env {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func fixture() []policy.Handset {
	return []policy.Handset{
		{ID: "kitchen", Label: "Kitchen", Endpoint: "PJSIP/kitchen", Number: 101,
			Page: true, Mailbox: "adults", PasswordEnv: "HANDSET_KITCHEN_PASSWORD"},
		{ID: "kids-room", Label: "Kids' room", Endpoint: "PJSIP/kids-room", Number: 105,
			PasswordEnv: "HANDSET_KIDS_ROOM_PASSWORD"},
		// Pseudo-handset: valid policy target, nothing to generate.
		{ID: "conference", Label: "Conference", Endpoint: "Local/600@internal"},
	}
}

var secrets = map[string]string{
	"HANDSET_KITCHEN_PASSWORD":   "kitchen-secret",
	"HANDSET_KIDS_ROOM_PASSWORD": "kids-secret",
}

func TestRenderGeneratesAllThreePlacesFromOneFile(t *testing.T) {
	f, err := Build(fixture(), env(secrets), nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if f.Generated != 2 {
		t.Errorf("generated %d, want 2 (Local pseudo-handset skipped)", f.Generated)
	}
	for _, want := range []string{
		"[kitchen]", "auth=kitchen-auth", "password=kitchen-secret",
		"mailboxes=adults@household", "dtmf_mode=rfc4733", "callerid=Kitchen <101>",
	} {
		if !strings.Contains(f.PJSIP, want) {
			t.Errorf("pjsip missing %q", want)
		}
	}
	for _, want := range []string{
		" same => n,Dial(PJSIP/kitchen,30)",
		"exten => 101,hint,PJSIP/kitchen",
		"exten => 100,1,Set(MEMBERS=)", " same => n,Dial(${MEMBERS:1},30)",
		" same => n,Page(${MEMBERS:1},ib(page-autoanswer^s^1),60)",
	} {
		if !strings.Contains(f.Dialplan, want) {
			t.Errorf("dialplan missing %q", want)
		}
	}
	if strings.Contains(f.PJSIP, "Local/") || strings.Contains(f.Dialplan, "conference") {
		t.Error("pseudo-handset leaked into generated config")
	}
}

func TestRenderRefusesMissingSecrets(t *testing.T) {
	_, err := Build(fixture(), env(map[string]string{
		"HANDSET_KITCHEN_PASSWORD": "x",
		// kids-room secret absent
	}), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "HANDSET_KIDS_ROOM_PASSWORD") {
		t.Errorf("err = %v, want the missing variable named", err)
	}
}

func TestRenderRefusesEndpointIdMismatch(t *testing.T) {
	h := fixture()
	h[0].Endpoint = "PJSIP/cocina"
	_, err := Build(h, env(secrets), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "PJSIP/kitchen") {
		t.Errorf("err = %v, want naming-rule violation", err)
	}
}

func TestGeneratedFilesCarryTheDoNotEditHeader(t *testing.T) {
	f, err := Build(fixture(), env(secrets), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"pjsip": f.PJSIP, "dialplan": f.Dialplan} {
		if !strings.Contains(body, "DO NOT EDIT") {
			t.Errorf("%s fragment missing the generated-file header", name)
		}
	}
}

// Outbound identity reaches the phone plant through the generated PJSIP
// config, because the plain dialplan path never touches doorman.
func TestRenderWritesOutboundCallerIDPerHandset(t *testing.T) {
	f, err := Build(fixture(), env(secrets), map[string]OutboundIdentity{
		"kitchen":   {CID: "+15125550142"},
		"kids-room": {CID: "+15125550100"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"set_var=OUTBOUND_CID=+15125550142",
		"set_var=OUTBOUND_CID=+15125550100",
	} {
		if !strings.Contains(f.PJSIP, want) {
			t.Errorf("pjsip missing %q", want)
		}
	}
	// The Phase 1 world, and the compatibility gate for outbound routing: a
	// caller ID with no trunk beside it is every install that has one provider,
	// and its endpoints must be exactly what they were before trunks existed.
	if strings.Contains(f.PJSIP, "OUTBOUND_TRUNK") {
		t.Error("a trunk was written for lines that name none")
	}
}

// M2.3. The plain dial path never reaches doorman, so the only way a handset
// can leave by its line's provider is for the endpoint to carry it — and it
// has to carry the trunk beside the caller ID, because a provider will not
// present a number its account does not own.
func TestRenderWritesOutboundTrunkBesideTheCallerID(t *testing.T) {
	f, err := Build(fixture(), env(secrets), map[string]OutboundIdentity{
		"kitchen":   {CID: "+15125550142", Trunk: "telnyx"},
		"kids-room": {CID: "+15125550100", Trunk: "voipms"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	kitchen, ok := section(f.PJSIP, "[kitchen]")
	if !ok {
		t.Fatal("no kitchen endpoint")
	}
	for _, want := range []string{
		"set_var=OUTBOUND_CID=+15125550142",
		"set_var=OUTBOUND_TRUNK=telnyx",
	} {
		if !strings.Contains(kitchen, want) {
			t.Errorf("[kitchen] missing %q:\n%s", want, kitchen)
		}
	}
	kids, _ := section(f.PJSIP, "[kids-room]")
	if strings.Contains(kids, "OUTBOUND_TRUNK=telnyx") {
		t.Error("one handset's trunk leaked onto another")
	}
	if !strings.Contains(kids, "set_var=OUTBOUND_TRUNK=voipms") {
		t.Errorf("[kids-room] did not get its own trunk:\n%s", kids)
	}
}

// A line with a trunk and no outbound_cid is a real state: it leaves by its
// provider and presents whatever that provider sends. The trunk must reach the
// endpoint anyway, and an empty OUTBOUND_CID must not.
func TestRenderWritesATrunkWithNoCallerID(t *testing.T) {
	f, err := Build(fixture(), env(secrets), map[string]OutboundIdentity{
		"kitchen": {Trunk: "telnyx"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	kitchen, _ := section(f.PJSIP, "[kitchen]")
	if !strings.Contains(kitchen, "set_var=OUTBOUND_TRUNK=telnyx") {
		t.Errorf("[kitchen] lost its trunk:\n%s", kitchen)
	}
	if strings.Contains(kitchen, "OUTBOUND_CID") {
		t.Errorf("an empty caller id was written as a set_var:\n%s", kitchen)
	}
}

// The compatibility gate: with no outbound identity configured anywhere, the
// generated endpoint is what it always was. A set_var nobody asked for would
// be a channel variable the dialplan reads, and an empty one at that.
func TestRenderWithoutOutboundIdentityIsUnchanged(t *testing.T) {
	with, err := Build(fixture(), env(secrets), map[string]OutboundIdentity{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	without, err := Build(fixture(), env(secrets), nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if with.PJSIP != without.PJSIP {
		t.Error("an empty outbound map changed the generated PJSIP")
	}
	if strings.Contains(without.PJSIP, "OUTBOUND_") {
		t.Error("an outbound channel variable appears with nothing configured")
	}
}

// A handset the map does not name gets nothing, even when its neighbours do:
// this is what makes "the primary line has no outbound_cid" mean "the trunk
// decides" rather than "an empty caller ID".
func TestRenderLeavesUnclaimedHandsetsAlone(t *testing.T) {
	f, err := Build(fixture(), env(secrets), map[string]OutboundIdentity{
		"kitchen": {CID: "+15125550142"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	kids := f.PJSIP[strings.Index(f.PJSIP, "[kids-room]"):]
	kids = kids[:strings.Index(kids, "[kids-room-auth]")]
	if strings.Contains(kids, "OUTBOUND_CID") {
		t.Errorf("unclaimed handset got an outbound caller id:\n%s", kids)
	}
}

// A Grandstream challenges every NOTIFY with the phone's own SIP credentials.
// Without outbound_auth on the endpoint the challenge goes unanswered and
// `doorman provision notify` does nothing (first re-provision, 2026-09-23).
func TestEveryHandsetEndpointCanAnswerAPhonesNotifyChallenge(t *testing.T) {
	handsets := []policy.Handset{{ID: "kitchen", Label: "Kitchen", Endpoint: "PJSIP/kitchen", Number: 101, PasswordEnv: "HANDSET_KITCHEN_PASSWORD"}}
	f, err := Build(handsets, func(string) (string, bool) { return "sip-secret", true }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.PJSIP, "auth=kitchen-auth\noutbound_auth=kitchen-auth\n") {
		t.Fatalf("endpoint must carry outbound_auth beside auth:\n%s", f.PJSIP)
	}
}

// A text typed on a handset is a SIP MESSAGE. It must reach the other
// phone as a message, not ring it as an anonymous call.
func TestHandsetTextsReachTheOtherPhoneAsMessages(t *testing.T) {
	handsets := []policy.Handset{
		{ID: "kitchen", Label: "Kitchen", Endpoint: "PJSIP/kitchen", Number: 101, PasswordEnv: "HANDSET_KITCHEN_PASSWORD"},
		{ID: "theater", Label: "Theater", Endpoint: "PJSIP/theater", Number: 102, PasswordEnv: "HANDSET_THEATER_PASSWORD"},
	}
	f, err := Build(handsets, func(string) (string, bool) { return "sip-secret", true }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.Dialplan, "exten => _X.,1,NoOp(no handset") {
		t.Fatal("a text to an outside number must fail on the phone, not be accepted and dropped")
	}
	if strings.Count(f.PJSIP, "message_context=cmm-messages\n") != 2 {
		t.Fatalf("every endpoint must route SIP MESSAGE to [cmm-messages]:\n%s", f.PJSIP)
	}
	for _, want := range []string{
		"[cmm-messages]\n",
		"exten => 101,1,Gosub(cmm-message-from,s,1)\n same => n,MessageSend(pjsip:kitchen,${MSG_FROM})\n",
		"exten => 102,1,Gosub(cmm-message-from,s,1)\n same => n,MessageSend(pjsip:theater,${MSG_FROM})\n",
		"exten => theater,1,Goto(102,1)\n", // a reply to the raw endpoint id still routes
		"exten => 100,1,Gosub(cmm-message-from,s,1)\n same => n,MessageSend(pjsip:kitchen,${MSG_FROM})\n same => n,MessageSend(pjsip:theater,${MSG_FROM})\n",
		"[cmm-message-from]\n",
		`ExecIf($["${SENDER}" = "theater"]?Set(MSG_FROM="Theater" <sip:102@${FROMDOM}>))`,
	} {
		if !strings.Contains(f.Dialplan, want) {
			t.Errorf("dialplan missing %q:\n%s", want, f.Dialplan)
		}
	}
}

// The ladder rides the endpoint beside the caller ID and trunk, as a list of
// names: the dial strings stay in the dialplan, and a handset with no ladder
// gets no variable, so an endpoint on a box that never wrote [line] failover
// is byte-identical to what it was.
func TestRenderWritesTheFailoverLadderBesideTheTrunk(t *testing.T) {
	f, err := Build(fixture(), env(secrets), map[string]OutboundIdentity{
		"kitchen":   {CID: "+15125550100", Trunk: "voipms", Failover: "telnyx,flowroute"},
		"kids-room": {CID: "+15125550100", Trunk: "voipms"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	kitchen, _ := section(f.PJSIP, "[kitchen]")
	if !strings.Contains(kitchen, "set_var=OUTBOUND_FAILOVER=telnyx,flowroute") {
		t.Errorf("[kitchen] missing the ladder:\n%s", kitchen)
	}
	kids, _ := section(f.PJSIP, "[kids-room]")
	if strings.Contains(kids, "OUTBOUND_FAILOVER") {
		t.Errorf("[kids-room] has a ladder nobody wrote:\n%s", kids)
	}
}

// ── A phone's own voicemail (s21) ────────────────────────────────────────

func TestRenderWritesTheMailboxesWhosePINsAreInEnv(t *testing.T) {
	hs := []policy.Handset{
		{ID: "kitchen", Label: "Kitchen", Endpoint: "PJSIP/kitchen", Number: 101, Mailbox: "whole-house", PasswordEnv: "HANDSET_KITCHEN_PASSWORD"},
		{ID: "theater", Label: "Theater", Endpoint: "PJSIP/theater", Number: 102, Mailbox: "whole-house", PasswordEnv: "HANDSET_THEATER_PASSWORD"},
		{ID: "master-bed", Label: "Master bedroom", Endpoint: "PJSIP/master-bed", Number: 103, Mailbox: "master-bed", Email: "caroline@example.invalid", PasswordEnv: "HANDSET_MASTER_BED_PASSWORD"},
		{ID: "office", Label: "Office", Endpoint: "PJSIP/office", Number: 104, Mailbox: "family", PasswordEnv: "HANDSET_OFFICE_PASSWORD"},
	}
	e := env(map[string]string{
		"HANDSET_KITCHEN_PASSWORD": "a", "HANDSET_THEATER_PASSWORD": "b", "HANDSET_MASTER_BED_PASSWORD": "c", "HANDSET_OFFICE_PASSWORD": "d",
		"VOICEMAIL_WHOLE_HOUSE_PIN": "482913", "VOICEMAIL_MASTER_BED_PIN": "917364",
		// family: no PIN — the hand-written box every older install has.
	})
	f, err := Build(hs, e, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"[household](+)",
		"whole-house => 482913,Whole house", // shared: the id made readable, no email
		"master-bed => 917364,Master bedroom,caroline@example.invalid", // own: the phone's label and address
		"; family: VOICEMAIL_FAMILY_PIN is not set in .env — assumed hand-written in voicemail.conf (office)",
	} {
		if !strings.Contains(f.Voicemail, want) {
			t.Errorf("voicemail_handsets.conf missing %q:\n%s", want, f.Voicemail)
		}
	}
	if strings.Contains(f.Voicemail, "family =>") {
		t.Error("a box with no PIN in .env must not be written — it would duplicate the hand-written one")
	}
	if len(f.Mailboxes) != 3 || f.Mailboxes[0].ID != "family" || f.Mailboxes[0].Generated || !f.Mailboxes[2].Generated {
		t.Errorf("Mailboxes = %+v", f.Mailboxes)
	}
	// The endpoint carries its box for *97, and the room call lands in it.
	kitchen, _ := section(f.PJSIP, "[kitchen]")
	if !strings.Contains(kitchen, "mailboxes=whole-house@household") || !strings.Contains(kitchen, "set_var=CMM_MAILBOX=whole-house") {
		t.Errorf("[kitchen] lacks its box:\n%s", kitchen)
	}
	for _, want := range []string{
		" same => n,Dial(PJSIP/master-bed,30)",
		` same => n,GotoIf($["${DIALSTATUS}"="ANSWER"]?done)`,
		` same => n,GotoIf($["${DIALSTATUS}"="BUSY"]?busy)`,
		" same => n,VoiceMail(master-bed@household,u)",
		" same => n(done),Hangup()",
		" same => n(busy),VoiceMail(master-bed@household,b)",
	} {
		if !strings.Contains(f.Dialplan, want) {
			t.Errorf("dialplan missing %q:\n%s", want, f.Dialplan)
		}
	}
	if got := VoicemailPINEnv("master-bed"); got != "VOICEMAIL_MASTER_BED_PIN" {
		t.Errorf("VoicemailPINEnv = %q", got)
	}
}

// A handset with no mailbox is exactly what it was: no set_var, a Dial and
// nothing after it, and no line in the voicemail file.
func TestRenderWithoutAMailboxIsUnchanged(t *testing.T) {
	f, err := Build(fixture(), env(secrets), nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	kids, _ := section(f.PJSIP, "[kids-room]")
	if strings.Contains(kids, "CMM_MAILBOX") {
		t.Errorf("[kids-room] has a box nobody gave it:\n%s", kids)
	}
	if strings.Contains(f.Dialplan, "VoiceMail(kids-room") || !strings.Contains(f.Dialplan, " same => n,Dial(PJSIP/kids-room,30)\n same => n,Hangup()\n same => n(quiet)") {
		t.Errorf("105 should be a Dial with no voicemail after it:\n%s", f.Dialplan)
	}
}

// ── Do not disturb (s12) ─────────────────────────────────────────────────

func TestRenderHonoursDoNotDisturbInRoomsRingAllAndPage(t *testing.T) {
	hs := []policy.Handset{
		{ID: "kitchen", Label: "Kitchen", Endpoint: "PJSIP/kitchen", Number: 101, Page: true, PageOverride: true, Mailbox: "kitchen", PasswordEnv: "HANDSET_KITCHEN_PASSWORD"},
		{ID: "theater", Label: "Theater", Endpoint: "PJSIP/theater", Number: 102, Page: true, PasswordEnv: "HANDSET_THEATER_PASSWORD"},
	}
	f, err := Build(hs, env(map[string]string{"HANDSET_KITCHEN_PASSWORD": "a", "HANDSET_THEATER_PASSWORD": "b", "VOICEMAIL_KITCHEN_PIN": "123456"}), nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	dnd := func(id string) string { return `${IF($["${DND_` + id + `}"!=""]?${DND_` + id + `}:0)}` }
	for _, want := range []string{
		// A room: the check first, then the minutes and the box (kitchen) or a hangup (theater).
		"exten => 101,1,GotoIf($[" + dnd("kitchen") + " > ${EPOCH}]?quiet)",
		" same => n,Dial(PJSIP/kitchen,30)",
		" same => n(quiet),Playback(call-me-maybe/system/quiet-room)",
		" same => n,Set(LEFT=$[" + dnd("kitchen") + " - ${EPOCH} + 59])",
		" same => n,SayNumber(${MATH(${LEFT}/60,int)})",
		" same => n,Playback(call-me-maybe/system/quiet-minutes)",
		" same => n,VoiceMail(kitchen@household,u)",
		"exten => 102,1,GotoIf($[" + dnd("theater") + " > ${EPOCH}]?quiet)",
		// Ring-all skips a quiet phone and says so when nobody is left.
		"exten => 100,1,Set(MEMBERS=)",
		" same => n,ExecIf($[" + dnd("theater") + " <= ${EPOCH}]?Set(MEMBERS=${MEMBERS}&PJSIP/theater))",
		" same => n,Dial(${MEMBERS:1},30)",
		" same => n(none),Playback(call-me-maybe/system/quiet-page)",
		// The page: the kitchen's override cuts through; anyone else is told.
		`exten => 500,1,Set(MEMBERS=)`,
		` same => n,Set(OVERRIDE=$["${CHANNEL(endpoint)}"="kitchen"])`,
		" same => n,ExecIf($[${OVERRIDE} | " + dnd("theater") + " <= ${EPOCH}]?Set(MEMBERS=${MEMBERS}&PJSIP/theater):Set(QUIET=1))",
		" same => n,ExecIf($[${QUIET}]?Playback(call-me-maybe/system/quiet-page))",
		" same => n,Page(${MEMBERS:1},ib(page-autoanswer^s^1),60)",
	} {
		if !strings.Contains(f.Dialplan, want) {
			t.Errorf("dialplan missing %q:\n%s", want, f.Dialplan)
		}
	}
	if len(f.PageOverrides) != 1 || f.PageOverrides[0] != "kitchen" {
		t.Errorf("PageOverrides = %v", f.PageOverrides)
	}
	// Nobody overrides: the page still works, and QUIET still tells the pager.
	hs[0].PageOverride = false
	f, _ = Build(hs, env(map[string]string{"HANDSET_KITCHEN_PASSWORD": "a", "HANDSET_THEATER_PASSWORD": "b"}), nil, nil)
	if !strings.Contains(f.Dialplan, " same => n,Set(OVERRIDE=0)\n") || len(f.PageOverrides) != 0 {
		t.Errorf("without overrides the page should set OVERRIDE=0:\n%s", f.Dialplan)
	}
}

// A curfew touches ordinary calls to and from the phone; 911 and the
// household's explicitly requested reminder apps remain reachable.
// Ordinary calls enter through [internal] behind a clock; its room
// number is refused while asleep, and ring-all and page leave it out — even
// for a page_override phone, whose override is for a child's DND, not for
// the parent's own bedtime rule.
func TestRenderCurfewGatesOrdinaryCalls(t *testing.T) {
	hs := fixture()
	hs[1].Page = true
	hs[0].PageOverride = true
	windows := []policy.CurfewWindow{
		{ScheduleID: "late-weeknights", Window: &policy.Afterhours{StartMin: 22*60 + 45, EndMin: 5 * 60,
			Days: [7]bool{true, true, true, true, true, false, false}}},
		{ScheduleID: "late-weekends", Window: &policy.Afterhours{StartMin: 23*60 + 30, EndMin: 5 * 60,
			Days: [7]bool{false, false, false, false, false, true, true}}},
		{ScheduleID: "holiday", Window: nil}, // enabled = false: inert
	}
	f, err := Build(hs, env(secrets), nil, map[string][]policy.CurfewWindow{"kids-room": windows})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The endpoint enters through its own context; the kitchen's is untouched.
	if !strings.Contains(f.PJSIP, "[kids-room]\ntype=endpoint\ncontext=curfew-kids-room\n") {
		t.Errorf("kids-room endpoint should enter through curfew-kids-room:\n%s", f.PJSIP)
	}
	if !strings.Contains(f.PJSIP, "[kitchen]\ntype=endpoint\ncontext=internal\n") {
		t.Errorf("kitchen endpoint must keep context=internal:\n%s", f.PJSIP)
	}

	// 911 goes through before the clock is consulted.
	ctx := f.Dialplan[strings.Index(f.Dialplan, "[curfew-kids-room]"):]
	emergency := strings.Index(ctx, "exten => 911,1,Goto(internal,${EXTEN},1)")
	clock := strings.Index(ctx, "GotoIf(")
	if emergency < 0 || clock < 0 || emergency > clock {
		t.Errorf("911 must precede the curfew test in the context:\n%s", ctx)
	}
	for _, want := range []string{
		" same => n,Goto(internal,${EXTEN},1)\n",
		" same => n(asleep),Answer()\n same => n,Playback(call-me-maybe/system/curfew-out)\n same => n,Busy(3)\n",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("curfew context missing %q:\n%s", want, ctx)
		}
	}

	// The windows are written the way Afterhours.Active reads them: a
	// window crossing midnight is its evening on the start days and its
	// morning on the days after; the disabled one is absent.
	for _, spec := range []string{
		"${IFTIME(22:45-23:59,sun&mon&tue&wed&thu,*,*?1:0)}",
		"${IFTIME(00:00-04:59,mon&tue&wed&thu&fri,*,*?1:0)}",
		"${IFTIME(23:30-23:59,fri&sat,*,*?1:0)}",
		"${IFTIME(00:00-04:59,sun&sat,*,*?1:0)}",
	} {
		if !strings.Contains(ctx, spec) {
			t.Errorf("curfew context missing %s:\n%s", spec, ctx)
		}
	}
	if strings.Contains(f.Dialplan, "holiday") {
		t.Error("a switched-off schedule must not reach the dialplan")
	}

	// The room, ring-all and page all consult the same expression.
	if !strings.Contains(f.Dialplan, "exten => 105,1,GotoIf($[(${IFTIME(") || !strings.Contains(f.Dialplan, "]?asleep)\n same => n,GotoIf($[${IF(") {
		t.Errorf("room 105 must test the curfew before do-not-disturb:\n%s", f.Dialplan)
	}
	if !strings.Contains(f.Dialplan, " same => n(asleep),Playback(call-me-maybe/system/curfew-room)\n") {
		t.Error("room 105 asleep label missing")
	}
	ringAll := f.Dialplan[strings.Index(f.Dialplan, "exten => 100,1"):strings.Index(f.Dialplan, "exten => 500,1")]
	if !strings.Contains(ringAll, "<= ${EPOCH} & !(${IFTIME(") || !strings.Contains(ringAll, "&PJSIP/kids-room))") {
		t.Errorf("ring-all must leave an asleep kids-room out:\n%s", ringAll)
	}
	if !strings.Contains(ringAll, "ExecIf($[${IF($[\"${DND_kitchen}\"!=\"\"]?${DND_kitchen}:0)} <= ${EPOCH}]?Set(MEMBERS=${MEMBERS}&PJSIP/kitchen))") {
		t.Errorf("the kitchen's ring-all line must be exactly as before:\n%s", ringAll)
	}
	page := f.Dialplan[strings.Index(f.Dialplan, "exten => 500,1"):]
	if !strings.Contains(page, "ExecIf($[(${OVERRIDE} | ") || !strings.Contains(page, ") & !(${IFTIME(") {
		t.Errorf("a page_override must not pierce a curfew:\n%s", page)
	}
}

// Without curfews — every install from before they existed — the output is
// byte for byte what it was.
func TestRenderWithoutCurfewsIsUnchanged(t *testing.T) {
	a, err := Build(fixture(), env(secrets), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(fixture(), env(secrets), nil, map[string][]policy.CurfewWindow{
		"kids-room": {{ScheduleID: "holiday", Window: nil}}, // present but switched off
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.PJSIP != b.PJSIP || a.Dialplan != b.Dialplan {
		t.Error("a curfew made only of switched-off schedules must render nothing")
	}
	if strings.Contains(a.Dialplan, "curfew") || strings.Contains(a.PJSIP, "curfew") {
		t.Error("no curfew text without curfews")
	}
}

func TestCurfewTimeSpecs(t *testing.T) {
	all := [7]bool{true, true, true, true, true, true, true}
	cases := []struct {
		w    policy.Afterhours
		want []string
	}{
		{policy.Afterhours{StartMin: 13 * 60, EndMin: 15 * 60, Days: all}, []string{"13:00-14:59,*"}},
		{policy.Afterhours{StartMin: 21 * 60, EndMin: 0, Days: [7]bool{true}}, []string{"21:00-23:59,sun"}},
		{policy.Afterhours{StartMin: 21 * 60, EndMin: 7 * 60, Days: [7]bool{false, false, false, false, false, false, true}},
			[]string{"21:00-23:59,sat", "00:00-06:59,sun"}},
	}
	for _, c := range cases {
		got := timeSpecs(&c.w)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("timeSpecs(%+v) = %v, want %v", c.w, got, c.want)
		}
	}
}

// *88 is three Asterisk applications in the generated dialplan, with the
// spool path from PHONEBOOK_DIR and the prompts as separate Playbacks so a
// pack without the clips still gets the beep.
func TestRenderGeneratesAddANumber(t *testing.T) {
	f, err := Build(fixture(), env(secrets), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"exten => *88,1,Answer()\n",
		" same => n,Set(ADD_ID=${CHANNEL(endpoint)})\n",
		" same => n,Playback(call-me-maybe/system/add-number)\n same => n,Read(ADD_NUMBER,,15,,2,10)\n",
		" same => n,Record(/var/spool/call-me-maybe/${ADD_ID}-${EPOCH}-${ADD_NUMBER}.wav,3,15,k)\n",
		" same => n,Playback(call-me-maybe/system/add-done)\n same => n(bye),Hangup()\n",
	} {
		if !strings.Contains(f.Dialplan, want) {
			t.Errorf("dialplan missing %q:\n%s", want, f.Dialplan)
		}
	}
	custom := func(k string) (string, bool) {
		if k == "PHONEBOOK_SPOOL" {
			return "/srv/books/spool/", true
		}
		return secrets[k], secrets[k] != ""
	}
	g, err := Build(fixture(), custom, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.Dialplan, "Record(/srv/books/spool/${ADD_ID}") {
		t.Errorf("PHONEBOOK_DIR not honoured:\n%s", g.Dialplan)
	}
}

func TestPhonebookPathsHonourTheEnvironmentAndTrimSlashes(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	if got := PhonebookDir(none); got != DefaultPhonebookDir {
		t.Errorf("PhonebookDir default = %q", got)
	}
	if got := PhonebookSpool(none); got != DefaultPhonebookSpool {
		t.Errorf("PhonebookSpool default = %q", got)
	}
	set := func(k string) (string, bool) {
		switch k {
		case "PHONEBOOK_DIR":
			return " /srv/books/ ", true
		case "PHONEBOOK_SPOOL":
			return "", true // set but empty: the default
		}
		return "", false
	}
	if got := PhonebookDir(set); got != "/srv/books" {
		t.Errorf("PhonebookDir custom = %q", got)
	}
	if got := PhonebookSpool(set); got != DefaultPhonebookSpool {
		t.Errorf("an empty PHONEBOOK_SPOOL must mean the default, got %q", got)
	}
	if got := curfewContext("mary-kate"); got != "curfew-mary-kate" {
		t.Errorf("curfewContext = %q", got)
	}
	if got := curfewExpr(nil); got != "" {
		t.Errorf("curfewExpr(nil) = %q", got)
	}
}
