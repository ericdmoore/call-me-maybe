package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/policy"
)

// Fiction, like the 555-01xx numbers: no test here may ever need a real key.
const testTelnyxAPIKey = "KEY-not-a-real-telnyx-key-0123456789"

// telnyxAccountServer answers like the Telnyx API v2 reference says it will:
// one page of numbers, one page of credential connections, bearer key in a
// header. The account below is the fixture every test reads.
//
//	+15125550142  active   → connection house (registers as cmm-home)
//	+15125550143  active   → no connection
//	+15125550144  active   → connection office (registers as office-pbx)
//	+15125550145  port-pending → house
//	+15125550146  active   → connection sip-fqdn (not a credential connection)
//	+15125550199  active   → house, and no policy file names it
func telnyxAccountServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testTelnyxAPIKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"code":"10009","title":"Authentication failed"}]}`))
			return
		}
		switch r.URL.Path {
		case "/phone_numbers":
			_, _ = w.Write([]byte(`{"data":[
				{"id":"1","phone_number":"+15125550142","status":"active","connection_id":"1001","connection_name":"house","emergency_enabled":false},
				{"id":"2","phone_number":"+15125550143","status":"active","connection_id":"","connection_name":""},
				{"id":"3","phone_number":"+15125550144","status":"active","connection_id":"1002","connection_name":"office"},
				{"id":"4","phone_number":"+15125550145","status":"port-pending","connection_id":"1001","connection_name":"house"},
				{"id":"5","phone_number":"+15125550146","status":"active","connection_id":"1003","connection_name":"sip-fqdn"},
				{"id":"6","phone_number":"+15125550199","status":"active","connection_id":"1001","connection_name":"house"}
			],"meta":{"page_number":1,"total_pages":1,"total_results":6,"page_size":250}}`))
		case "/credential_connections":
			_, _ = w.Write([]byte(`{"data":[
				{"id":"1001","connection_name":"house","user_name":"cmm-home","active":true},
				{"id":"1002","connection_name":"office","user_name":"office-pbx","active":true}
			],"meta":{"page_number":1,"total_pages":1,"total_results":2,"page_size":250}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const telnyxNumbersTrunk = `
[[trunks]]
id = "telnyx"
provider = "telnyx"
host = "sip.telnyx.com"
username = "cmm-home"
password_env = "TRUNK_TELNYX_PASSWORD"
api_key_env = "TRUNK_TELNYX_API_KEY"
`

func telnyxSecrets() func(string) (string, bool) {
	return secretsFrom(map[string]string{"TRUNK_TELNYX_API_KEY": testTelnyxAPIKey})
}

func claimOn(trunk string, claims ...numberClaim) map[string][]numberClaim {
	return map[string][]numberClaim{trunk: claims}
}

func answers(e164, line string) numberClaim {
	return numberClaim{E164: e164, Lines: []string{line}, Answers: true}
}

func presents(e164, line string) numberClaim {
	return numberClaim{E164: e164, Lines: []string{line}, Presents: true}
}

func rowFor(t *testing.T, tn trunkNumbers, e164 string) numberRow {
	t.Helper()
	for _, r := range tn.Numbers {
		if r.Number == e164 {
			return r
		}
	}
	t.Fatalf("no row for %s in %+v", e164, tn.Numbers)
	return numberRow{}
}

func TestNumbersWeighsEveryClaimAgainstTheAccount(t *testing.T) {
	srv := telnyxAccountServer(t)
	report := checkNumbers(context.Background(), numbersCheck{
		trunks: trunksFor(t, telnyxNumbersTrunk),
		claims: claimOn("telnyx",
			answers("+15125550142", "default"),
			answers("+15125550143", "biz"),
			answers("+15125550144", "shop"),
			answers("+15125550145", "porting"),
			answers("+15125550146", "fqdn"),
			answers("+15125550177", "typo"),
		),
		secret: telnyxSecrets(), timeout: 5 * time.Second, endpoint: srv.URL,
	})
	tn := report.Trunks[0]
	if tn.State != numbersChecked {
		t.Fatalf("state = %q: %s", tn.State, tn.Detail)
	}

	for _, want := range []struct {
		number  string
		verdict numberVerdict
		problem bool
		says    string
	}{
		{"+15125550142", numberRouted, false, "assigned to connection house (1001), the one this trunk registers as"},
		{"+15125550143", numberNoConn, true, "assigned to no connection"},
		{"+15125550144", numberOtherConn, true, "registers as office-pbx and not as this trunk's cmm-home"},
		{"+15125550145", numberInactive, true, `"port-pending"`},
		{"+15125550146", numberUnverified, false, "cannot be told"},
		{"+15125550177", numberMissing, true, "NOT on this account"},
	} {
		row := rowFor(t, tn, want.number)
		if row.Verdict != want.verdict || row.Problem != want.problem {
			t.Errorf("%s: verdict %q problem %v, want %q %v (%s)",
				want.number, row.Verdict, row.Problem, want.verdict, want.problem, row.Detail)
		}
		if !strings.Contains(row.Detail, want.says) {
			t.Errorf("%s: detail %q does not say %q", want.number, row.Detail, want.says)
		}
	}

	// The number the typo was probably meant to be is right there in the list.
	spare := rowFor(t, tn, "+15125550199")
	if spare.Verdict != numberUnclaimed || spare.Problem {
		t.Errorf("an unclaimed number is listed, never a problem: %+v", spare)
	}
	if report.Problems != 4 || numbersExit(report) != numbersExitProblem {
		t.Errorf("problems = %d exit = %d, want 4 and %d", report.Problems, numbersExit(report), numbersExitProblem)
	}
}

// A caller ID needs the account to hold the number; which connection its
// inbound calls go to is not the outbound call's business.
func TestNumbersOnlyNeedsAPresentedNumberToBeHeld(t *testing.T) {
	srv := telnyxAccountServer(t)
	report := checkNumbers(context.Background(), numbersCheck{
		trunks: trunksFor(t, telnyxNumbersTrunk),
		claims: claimOn("telnyx",
			presents("+15125550143", "biz"), // no connection
			presents("+15125550144", "biz"), // another connection
		),
		secret: telnyxSecrets(), timeout: 5 * time.Second, endpoint: srv.URL,
	})
	for _, e164 := range []string{"+15125550143", "+15125550144"} {
		if row := rowFor(t, report.Trunks[0], e164); row.Problem || row.Verdict != numberRouted {
			t.Errorf("%s presented only: %+v", e164, row)
		}
	}
	if numbersExit(report) != 0 {
		t.Errorf("exit = %d, want 0", numbersExit(report))
	}
}

// The provider doorman cannot ask is a gap in this tool, not a fault in the
// account: reported, every claim listed, and never a failed cron job.
func TestNumbersReportsAProviderItCannotAskWithoutFailing(t *testing.T) {
	report := checkNumbers(context.Background(), numbersCheck{
		trunks:  trunksFor(t, balanceTrunk),
		claims:  claimOn("voipms", answers("+15125550100", "default")),
		secret:  secretsFrom(map[string]string{"TRUNK_VOIPMS_API_PASSWORD": testTrunkAPIPassword}),
		timeout: time.Second,
	})
	tn := report.Trunks[0]
	if tn.State != numbersNoCapability || !strings.Contains(tn.Detail, "telnyx") {
		t.Errorf("voip.ms: state %q detail %q, want no_capability naming what can be asked", tn.State, tn.Detail)
	}
	if row := rowFor(t, tn, "+15125550100"); row.Verdict != numberUnchecked || row.Problem {
		t.Errorf("the claim must be listed, unchecked: %+v", row)
	}
	if numbersExit(report) != 0 {
		t.Errorf("exit = %d, want 0", numbersExit(report))
	}
}

func TestNumbersSeparatesUncheckedFromWrong(t *testing.T) {
	srv := telnyxAccountServer(t)
	for name, tc := range map[string]struct {
		trunks string
		secret func(string) (string, bool)
		state  numbersState
		exit   int
		says   string
	}{
		"key variable unset": {telnyxNumbersTrunk, secretsFrom(nil), numbersError, numbersExitUnchecked, "TRUNK_TELNYX_API_KEY is not set"},
		"key refused": {telnyxNumbersTrunk, secretsFrom(map[string]string{"TRUNK_TELNYX_API_KEY": "KEY-wrong"}),
			numbersError, numbersExitUnchecked, "401"},
		"no key configured": {strings.Replace(telnyxNumbersTrunk, "api_key_env = \"TRUNK_TELNYX_API_KEY\"\n", "", 1),
			secretsFrom(nil), numbersUnconfigured, 0, "add api_key_env"},
		"a login where a key belongs": {strings.Replace(telnyxNumbersTrunk, "api_key_env = \"TRUNK_TELNYX_API_KEY\"\n",
			"api_username = \"owner@example.invalid\"\napi_password_env = \"TRUNK_TELNYX_API_PASSWORD\"\n", 1),
			secretsFrom(map[string]string{"TRUNK_TELNYX_API_PASSWORD": "fiction"}), numbersError, numbersExitUnchecked,
			"Telnyx takes api_key_env"},
		"no provider": {strings.Replace(telnyxNumbersTrunk, "provider = \"telnyx\"\n", "", 1),
			telnyxSecrets(), numbersNoClient, 0, "declares no provider"},
		"unknown provider": {strings.Replace(telnyxNumbersTrunk, "provider = \"telnyx\"", "provider = \"callcentric\"", 1),
			telnyxSecrets(), numbersNoClient, 0, "no client for"},
	} {
		report := checkNumbers(context.Background(), numbersCheck{
			trunks: trunksFor(t, tc.trunks),
			claims: claimOn("telnyx", answers("+15125550142", "default")),
			secret: tc.secret, timeout: 5 * time.Second, endpoint: srv.URL,
		})
		tn := report.Trunks[0]
		if tn.State != tc.state || !strings.Contains(tn.Detail, tc.says) {
			t.Errorf("%s: state %q detail %q, want %q saying %q", name, tn.State, tn.Detail, tc.state, tc.says)
		}
		if got := numbersExit(report); got != tc.exit {
			t.Errorf("%s: exit = %d, want %d", name, got, tc.exit)
		}
		if row := rowFor(t, tn, "+15125550142"); row.Verdict != numberUnchecked {
			t.Errorf("%s: an unchecked trunk's claim must still be listed: %+v", name, row)
		}
		if strings.Contains(tn.Detail, testTelnyxAPIKey) {
			t.Errorf("%s: the key leaked: %s", name, tn.Detail)
		}
	}
}

// trunks.toml declares e911 because doorman could not discover it. Here is a
// provider that can be asked, and a contradiction is worth saying out loud.
func TestNumbersWarnsWhenTheE911DeclarationDisagreesWithTheProvider(t *testing.T) {
	srv := telnyxAccountServer(t)
	report := checkNumbers(context.Background(), numbersCheck{
		trunks: trunksFor(t, telnyxNumbersTrunk+"e911 = true\n"),
		claims: claimOn("telnyx", answers("+15125550142", "default")),
		secret: telnyxSecrets(), timeout: 5 * time.Second, endpoint: srv.URL,
	})
	w := report.Trunks[0].Warnings
	if len(w) != 1 || !strings.Contains(w[0], "e911 = true") || !strings.Contains(w[0], "+15125550142") {
		t.Errorf("warnings = %v, want the e911 contradiction named", w)
	}
	if numbersExit(report) != 0 {
		t.Error("a warning decides nothing")
	}
}

func TestNumberClaimsCollectEveryDIDThePolicyNames(t *testing.T) {
	claims, loose := numberClaims([]lineIdentity{
		{Name: "default", LineIdentity: policy.LineIdentity{Number: "+15125550100", Trunk: "voipms", OutboundCID: "+15125550100"}},
		{Name: "biz", LineIdentity: policy.LineIdentity{Number: "+15125550142", Trunk: "telnyx", OutboundCID: "+15125550143"}},
		{Name: "shop", LineIdentity: policy.LineIdentity{Number: "+15125550142", Trunk: "telnyx"}},
		{Name: "attic", LineIdentity: policy.LineIdentity{Number: "+15125550160", OutboundCID: "+15125550161"}},
		{Name: "bare"},
	})
	if got := claims["voipms"]; len(got) != 1 || !got[0].Answers || !got[0].Presents {
		t.Errorf("a line answering and presenting one number is one claim: %+v", got)
	}
	tx := claims["telnyx"]
	if len(tx) != 2 {
		t.Fatalf("telnyx claims = %+v, want the shared DID once and the caller ID once", tx)
	}
	if tx[0].E164 != "+15125550142" || strings.Join(tx[0].Lines, ",") != "biz,shop" || !tx[0].Answers {
		t.Errorf("shared DID = %+v", tx[0])
	}
	if tx[1].E164 != "+15125550143" || tx[1].Answers || !tx[1].Presents {
		t.Errorf("outbound_cid claim = %+v", tx[1])
	}
	if len(loose) != 2 || loose[0].Number != "+15125550160" || !strings.Contains(loose[0].Detail, "DEFAULT_TRUNK") {
		t.Errorf("a number with no trunk is unattributed, and says why: %+v", loose)
	}
	if !strings.Contains(loose[1].Detail, "presents it") {
		t.Errorf("an unattributed caller ID says it is presented: %+v", loose[1])
	}
}

func TestNumbersPrintsTheVerdictsAndNeverTheKey(t *testing.T) {
	srv := telnyxAccountServer(t)
	report := checkNumbers(context.Background(), numbersCheck{
		trunks: trunksFor(t, telnyxNumbersTrunk),
		claims: claimOn("telnyx", answers("+15125550142", "default"), answers("+15125550177", "typo")),
		secret: telnyxSecrets(), timeout: 5 * time.Second, endpoint: srv.URL,
	})
	report.Where = "./trunks.toml"
	report.Unattributed = []numberRow{{Number: "+15125550160", Detail: "line attic answers it but names no [line] trunk"}}
	report.Exit = numbersExit(report)
	out := capture(t, func() { printNumbers(report) })
	for _, want := range []string{
		"telnyx (telnyx), registers as cmm-home — 6 numbers on the account",
		"✓ +15125550142  line default answers it — on this account, assigned to connection house (1001), the one this trunk registers as",
		"✗ +15125550177  line typo answers it — NOT on this account",
		"· +15125550199  on this account, claimed by no policy file — assigned to connection house (1001)",
		"Numbers no trunk is named for:",
		"exit 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, testTelnyxAPIKey) {
		t.Error("the key reached the output")
	}

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testTelnyxAPIKey) || !strings.Contains(string(raw), `"verdict":"not_on_account"`) {
		t.Errorf("JSON report wrong: %s", raw)
	}
}

func TestNumbersPrintsAnUncheckedRun(t *testing.T) {
	report := numbersReport{Where: "t", Trunks: []trunkNumbers{{
		Trunk: "telnyx", Provider: "telnyx", Username: "u", State: numbersError, Detail: "boom",
	}}, Unchecked: 1}
	out := capture(t, func() { printNumbers(report) })
	if !strings.Contains(out, "could not be checked: boom") || !strings.Contains(out, "exit 3") {
		t.Errorf("unchecked output:\n%s", out)
	}
	report = numbersReport{Where: "t", Trunks: []trunkNumbers{{Trunk: "telnyx", State: numbersChecked, Detail: "0 numbers on the account"}}}
	out = capture(t, func() { printNumbers(report) })
	if !strings.Contains(out, "holds no numbers") || !strings.Contains(out, "exit 0") {
		t.Errorf("empty account output:\n%s", out)
	}
}

// The whole command, from files, against a provider it cannot ask — so no
// network is involved and the exit code is the contract.
func TestRunNumbersFromFiles(t *testing.T) {
	dir := policyDir(t, map[string]string{
		"policy.toml": goodPolicy + "\n[line]\nnumber = \"+15125550100\"\ntrunk = \"voipms\"\n",
		"trunks.toml": balanceTrunk,
	})
	var rc int
	out := capture(t, func() {
		rc = runNumbers([]string{
			"-trunks", filepath.Join(dir, "trunks.toml"),
			"-handsets", filepath.Join(dir, "handsets.toml"),
			"-env", filepath.Join(dir, ".env"),
			filepath.Join(dir, "policy.toml"),
		})
	})
	if rc != 0 || !strings.Contains(out, "+15125550100") || !strings.Contains(out, "cannot ask voip.ms") {
		t.Errorf("rc = %d, output:\n%s", rc, out)
	}

	out = capture(t, func() {
		rc = runNumbers([]string{"-json", "-trunks", filepath.Join(dir, "trunks.toml"),
			"-handsets", filepath.Join(dir, "handsets.toml"), filepath.Join(dir, "policy.toml")})
	})
	var report numbersReport
	if err := json.Unmarshal([]byte(out), &report); err != nil || rc != 0 || len(report.Trunks) != 1 {
		t.Errorf("--json: rc %d err %v:\n%s", rc, err, out)
	}
}

func TestRunNumbersWithoutAnInventoryIsNotAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "trunks.toml")
	var rc int
	out := capture(t, func() { rc = runNumbers([]string{"-trunks", missing}) })
	if rc != 0 || !strings.Contains(out, "nothing is wrong") && !strings.Contains(out, "Nothing is wrong") {
		t.Errorf("rc = %d:\n%s", rc, out)
	}
	out = capture(t, func() { rc = runNumbers([]string{"-json", "-trunks", missing}) })
	if rc != 0 || !strings.Contains(out, `"trunks": []`) {
		t.Errorf("--json rc = %d:\n%s", rc, out)
	}
}

func TestRunNumbersRefusesBadConfig(t *testing.T) {
	dir := policyDir(t, map[string]string{
		"trunks.toml": "[[trunks]]\nid = \"x\"\n",
	})
	var rc int
	captureStderr(t, func() { rc = runNumbers([]string{"-trunks", filepath.Join(dir, "trunks.toml")}) })
	if rc != numbersExitUsage {
		t.Errorf("invalid trunks.toml: exit = %d, want %d", rc, numbersExitUsage)
	}

	dir = policyDir(t, map[string]string{
		"trunks.toml": balanceTrunk,
		"policy.toml": "[line]\ntrunk = \"nope\"\n",
	})
	captureStderr(t, func() {
		rc = runNumbers([]string{"-trunks", filepath.Join(dir, "trunks.toml"),
			"-handsets", filepath.Join(dir, "handsets.toml"), filepath.Join(dir, "policy.toml")})
	})
	if rc != numbersExitUsage {
		t.Errorf("a policy naming an unknown trunk: exit = %d, want %d", rc, numbersExitUsage)
	}
}

// With no policy file there are no claims, and every number on the account
// would read as unclaimed with exit 0 — a green from a typo'd path. It is a
// usage error instead, decided before any provider is asked.
func TestRunNumbersRefusesAMissingPolicyFile(t *testing.T) {
	dir := policyDir(t, map[string]string{"trunks.toml": telnyxNumbersTrunk})
	missing := filepath.Join(dir, "polcy.toml")
	var rc int
	errOut := captureStderr(t, func() {
		rc = runNumbers([]string{"-trunks", filepath.Join(dir, "trunks.toml"),
			"-handsets", filepath.Join(dir, "handsets.toml"), missing})
	})
	if rc != numbersExitUsage || !strings.Contains(errOut, missing) {
		t.Errorf("missing policy: exit = %d, want %d naming the path; stderr:\n%s", rc, numbersExitUsage, errOut)
	}
}
