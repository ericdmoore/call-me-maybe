package render

import (
	"regexp"
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

// A Telnyx SIP Connection with credential authentication needs nothing from
// this package that VoIP.ms did not already need, and this file is the proof
// rather than the claim. Everything Telnyx-shaped is in the inventory — host
// sip.telnyx.com, the connection's SIP username and password — and the render
// is the generic one: an outbound registration with line=yes + endpoint=, two
// identify blocks by our own identity, never an IP.
//
// What Telnyx can vary is the inbound DNIS — the connection's "destination
// number format" — which puts the DID in the Request-URI as +E.164 (the
// default), E.164 without the plus, or national ten digits, or replaces it
// with the connection's SIP username. The To header carries the DID. Every
// one of those must identify the trunk, and every DID form must route.
//
// Written from Telnyx's documentation; no live Telnyx registration has been
// tried yet. See docs/RUNBOOK.md, "Adding a Telnyx line".

func telnyxOnly() []policy.Trunk {
	return []policy.Trunk{{
		ID: "telnyx", Provider: "telnyx", Host: "sip.telnyx.com",
		Username: "cmm-home", PasswordEnv: "TRUNK_TELNYX_PASSWORD",
		Context: "from-telnyx", Codecs: []string{"ulaw", "g722"},
		FromUser: "cmm-home", FromDomain: "sip.telnyx.com", Expiration: 300,
		E911: boolp(true),
	}}
}

func telnyxLines() []TrunkLine {
	return []TrunkLine{
		{Name: "default", Label: "Home", Number: "+15125550142", Trunk: "telnyx"},
		{Name: "biz", Label: "Mertaugh Enterprises", Number: "+15125550143", Trunk: "telnyx"},
	}
}

func buildTelnyx(t *testing.T) *TrunkFragments {
	t.Helper()
	f, err := BuildTrunks(telnyxOnly(), telnyxLines(), []string{"kitchen"},
		Emergency{Trunk: "telnyx", Chosen: false}, env(trunkSecrets))
	if err != nil {
		t.Fatalf("BuildTrunks: %v", err)
	}
	return f
}

// identifyRegex pulls the /…/ pattern out of an identify block. Asterisk
// compiles it as a POSIX extended regex; this one is an alternation of
// QuoteMeta'd literals, which means the same thing to Go's engine.
func identifyRegex(t *testing.T, block, key string) *regexp.Regexp {
	t.Helper()
	for _, line := range strings.Split(block, "\n") {
		if v, ok := strings.CutPrefix(line, key); ok {
			return regexp.MustCompile(strings.TrimSuffix(strings.TrimPrefix(v, "/"), "/"))
		}
	}
	t.Fatalf("no %s in:\n%s", key, block)
	return nil
}

func TestATelnyxCredentialTrunkRegistersOutboundAndBindsInbound(t *testing.T) {
	f := buildTelnyx(t)
	reg, ok := section(f.PJSIP, "[telnyx_reg]")
	if !ok {
		t.Fatalf("no registration:\n%s", f.PJSIP)
	}
	for _, want := range []string{
		"outbound_auth=telnyx_auth\n",
		"server_uri=sip:sip.telnyx.com\n",
		"client_uri=sip:cmm-home@sip.telnyx.com\n",
		"contact_user=cmm-home\n",
		"\nline=yes\n", "\nendpoint=telnyx\n",
	} {
		if !strings.Contains(reg, want) {
			t.Errorf("[telnyx_reg] missing %q:\n%s", want, reg)
		}
	}
	auth, _ := section(f.PJSIP, "[telnyx_auth]")
	if !strings.Contains(auth, "username=cmm-home\n") || !strings.Contains(auth, "password=telnyx-secret\n") {
		t.Errorf("credential auth is the connection's username and password:\n%s", auth)
	}
	ep, _ := section(f.PJSIP, "[telnyx]")
	for _, want := range []string{"from_domain=sip.telnyx.com\n", "dtmf_mode=rfc4733\n", "context=from-telnyx\n"} {
		if !strings.Contains(ep, want) {
			t.Errorf("[telnyx] missing %q:\n%s", want, ep)
		}
	}
	// from_user left alone, so CALLERID(num) — the line's outbound_cid — rides
	// in From, which is how Telnyx sees which of the account's numbers to show.
	if strings.Contains(ep, "from_user=") {
		t.Errorf("from_user emitted with the default value:\n%s", ep)
	}
	if strings.Contains(f.PJSIP, "\nmatch=") {
		t.Error("an IP allow-list was generated for Telnyx; identification is by identity only")
	}
}

func TestATelnyxInviteIsIdentifiedInEveryDNISFormat(t *testing.T) {
	f := buildTelnyx(t)
	uriBlock, _ := section(f.PJSIP, "[telnyx-identify-uri]")
	toBlock, _ := section(f.PJSIP, "[telnyx-identify-to]")
	uri := identifyRegex(t, uriBlock, "match_request_uri=")
	to := identifyRegex(t, toBlock, "match_header=To: ")

	for _, did := range []string{"+15125550142", "15125550142", "5125550142", "+15125550143", "5125550143"} {
		if r := "sip:" + did + "@203.0.113.7:5060;transport=udp"; !uri.MatchString(r) {
			t.Errorf("Request-URI %q is not identified", r)
		}
		if h := "<sip:" + did + "@sip.telnyx.com>"; !to.MatchString(h) {
			t.Errorf("To %q is not identified", h)
		}
	}
	// DNIS format "SIP Username": the connection's username in the Request-URI.
	if !uri.MatchString("sip:cmm-home@203.0.113.7:5060") {
		t.Error("a Request-URI carrying the connection's SIP username is not identified")
	}
	// Somebody else's number is not ours to answer.
	if uri.MatchString("sip:+15125550199@203.0.113.7") || to.MatchString("<sip:+15125550199@sip.telnyx.com>") {
		t.Error("a DID no line routes to this trunk identifies it")
	}
}

func TestEveryTelnyxDNISFormatRoutesToItsLine(t *testing.T) {
	f := buildTelnyx(t)
	ctx, ok := section(f.Dialplan, "[from-telnyx]")
	if !ok {
		t.Fatalf("no [from-telnyx]:\n%s", f.Dialplan)
	}
	for did, line := range map[string]string{
		"+15125550142": "default", "15125550142": "default", "5125550142": "default",
		"+15125550143": "biz", "15125550143": "biz", "5125550143": "biz",
	} {
		want := "exten => " + did + ",1,Goto(" + LineContext(line) + ",${EXTEN},1)\n"
		if !strings.Contains(ctx, want) {
			t.Errorf("[from-telnyx] does not route %s to line %s", did, line)
		}
	}
	// "SIP Username" as the DNIS format leaves no digits to route on. The
	// generated dialplan does not guess which line that is — the RUNBOOK says
	// to leave the format on a number — and this pins that it does not.
	if strings.Contains(ctx, "exten => cmm-home,") {
		t.Error("a username route was generated; with several lines on one trunk it would be a guess")
	}
}

// 911 on a Telnyx-only box: the trunk is tried, never asked, and no caller ID
// is set — the generic ladder, unchanged.
func TestTelnyxCarriesEmergencyCallsLikeAnyTrunk(t *testing.T) {
	f := buildTelnyx(t)
	em, ok := section(f.Dialplan, "["+EmergencyContext+"]")
	if !ok {
		t.Fatalf("no emergency context:\n%s", f.Dialplan)
	}
	if !strings.Contains(em, "Dial(PJSIP/911@telnyx,60)") {
		t.Errorf("911 does not leave by telnyx:\n%s", em)
	}
	if strings.Contains(em, "Set(CALLERID") || strings.Contains(em, "DEVICE_STATE") {
		t.Errorf("the emergency context sets a caller ID or asks before trying:\n%s", em)
	}
	if f.Emergency != "telnyx" || len(f.Fallbacks) != 0 {
		t.Errorf("emergency = %q fallbacks = %v", f.Emergency, f.Fallbacks)
	}
}
