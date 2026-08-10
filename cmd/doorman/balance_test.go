package main

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/policy"
)

// The API password used throughout. Fiction, like the 555-01xx numbers: no
// test here may ever need a real provider account.
const testTrunkAPIPassword = "not-a-real-api-key-0123456789"

// balanceServer answers like the VoIP.ms REST API: HTTP 200 with a status
// token, and the balance quoted as a string.
func balanceServer(t *testing.T, balance string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","balance":{"current_balance":"` + balance + `"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func trunksFor(t *testing.T, body string) []policy.Trunk {
	t.Helper()
	tr, err := policy.TrunksFromTOML([]byte(body))
	if err != nil {
		t.Fatalf("TrunksFromTOML: %v\n%s", err, body)
	}
	return tr.All()
}

func secretsFrom(pairs map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := pairs[key]
		return v, ok && v != ""
	}
}

const balanceTrunk = `
[[trunks]]
id = "voipms"
provider = "voip.ms"
host = "chicago.voip.ms"
username = "123456_home"
password_env = "TRUNK_VOIPMS_PASSWORD"
api_username = "owner@example.invalid"
api_password_env = "TRUNK_VOIPMS_API_PASSWORD"
balance_min = 25.0
`

func TestBalanceReportsATrunkAboveItsThreshold(t *testing.T) {
	srv := balanceServer(t, "112.50")
	results := checkBalances(context.Background(), balanceCheck{
		trunks:   trunksFor(t, balanceTrunk),
		secret:   secretsFrom(map[string]string{"TRUNK_VOIPMS_API_PASSWORD": testTrunkAPIPassword}),
		timeout:  5 * time.Second,
		endpoint: srv.URL,
	})
	if len(results) != 1 || results[0].State != balanceOK {
		t.Fatalf("want one ok result, got %+v", results)
	}
	if *results[0].Balance != 112.5 || *results[0].Threshold != 25 {
		t.Errorf("balance/threshold wrong: %+v", results[0])
	}
	if got := balanceExit(results); got != 0 {
		t.Errorf("exit = %d, want 0", got)
	}
}

// The contract cron depends on, and the reason nobody needs a wrapper script.
func TestBalanceExitsNonZeroBelowTheThreshold(t *testing.T) {
	srv := balanceServer(t, "4.20")
	results := checkBalances(context.Background(), balanceCheck{
		trunks:   trunksFor(t, balanceTrunk),
		secret:   secretsFrom(map[string]string{"TRUNK_VOIPMS_API_PASSWORD": testTrunkAPIPassword}),
		timeout:  5 * time.Second,
		endpoint: srv.URL,
	})
	if results[0].State != balanceLow {
		t.Fatalf("state = %q, want low: %+v", results[0].State, results[0])
	}
	if got := balanceExit(results); got != balanceExitLow {
		t.Errorf("exit = %d, want %d", got, balanceExitLow)
	}
}

// Several trunks means several balances, and the output has to say WHICH
// account is low — a message that does not is one somebody has to investigate.
func TestBalanceNamesTheAccountThatIsLow(t *testing.T) {
	low := balanceServer(t, "3.00")
	fine := balanceServer(t, "300.00")

	trunks := trunksFor(t, `
[[trunks]]
id = "voipms-home"
provider = "voip.ms"
host = "chicago.voip.ms"
username = "123456_home"
password_env = "TRUNK_HOME_PASSWORD"
api_username = "owner@example.invalid"
api_password_env = "HOME_API_PASSWORD"
balance_min = 20.0

[[trunks]]
id = "voipms-biz"
provider = "voip.ms"
host = "newyork.voip.ms"
username = "123456_biz"
password_env = "TRUNK_BIZ_PASSWORD"
api_username = "owner@example.invalid"
api_password_env = "BIZ_API_PASSWORD"
balance_min = 20.0
`)
	secrets := secretsFrom(map[string]string{
		"HOME_API_PASSWORD": testTrunkAPIPassword,
		"BIZ_API_PASSWORD":  testTrunkAPIPassword,
	})

	// Each trunk gets its own endpoint, which is what per-trunk means.
	first := checkTrunkBalance(context.Background(),
		balanceCheck{secret: secrets, timeout: 5 * time.Second, endpoint: low.URL}, trunks[0])
	second := checkTrunkBalance(context.Background(),
		balanceCheck{secret: secrets, timeout: 5 * time.Second, endpoint: fine.URL}, trunks[1])
	results := []balanceResult{first, second}

	out := capture(t, func() { printBalances(results, "./trunks.toml", 0) })
	if !strings.Contains(out, "voipms-home is low") {
		t.Errorf("the low account is not named:\n%s", out)
	}
	if strings.Contains(out, "voipms-biz is low") {
		t.Errorf("the healthy account is reported as low:\n%s", out)
	}
	if balanceExit(results) != balanceExitLow {
		t.Error("one low trunk out of two must still fail")
	}
}

// The distinction the whole capability exists for: an invoiced provider says
// so, rather than printing a zero that looks alarming or a blank that looks
// broken.
func TestAProviderWithNoBalanceSaysSo(t *testing.T) {
	results := checkBalances(context.Background(), balanceCheck{
		trunks: trunksFor(t, `
[[trunks]]
id = "fr"
provider = "Flowroute"
host = "sip.flowroute.com"
username = "cmm-home"
password_env = "TRUNK_FR_PASSWORD"
`),
		secret:  secretsFrom(nil),
		timeout: time.Second,
	})
	if results[0].State != balanceNoCapability {
		t.Fatalf("state = %q, want %q", results[0].State, balanceNoCapability)
	}
	if results[0].Billing != "postpaid" {
		t.Errorf("billing = %q, want postpaid", results[0].Billing)
	}
	if results[0].Balance != nil {
		t.Error("a postpaid provider must not report a number, least of all zero")
	}
	if got := balanceExit(results); got != 0 {
		t.Errorf("exit = %d — nothing is wrong with a postpaid account", got)
	}

	out := capture(t, func() { printBalances(results, "./trunks.toml", 0) })
	if !strings.Contains(out, "Flowroute is postpaid — no balance to report") {
		t.Errorf("the sentence this milestone turns on is missing:\n%s", out)
	}
}

// Reported, never skipped. A row that quietly disappears is the same silence
// the feature exists to break.
func TestAProviderWithNoClientIsReportedNotSkipped(t *testing.T) {
	results := checkBalances(context.Background(), balanceCheck{
		trunks: trunksFor(t, `
[[trunks]]
id = "cc"
provider = "callcentric"
host = "callcentric.com"
username = "17771234567"
password_env = "TRUNK_CC_PASSWORD"
`),
		secret:  secretsFrom(nil),
		timeout: time.Second,
	})
	if len(results) != 1 || results[0].State != balanceNoClient {
		t.Fatalf("want one no_client result, got %+v", results)
	}
	out := capture(t, func() { printBalances(results, "./trunks.toml", 0) })
	if !strings.Contains(out, "cc") || !strings.Contains(out, "voip.ms") {
		t.Errorf("should name the trunk and what doorman can ask:\n%s", out)
	}
	if balanceExit(results) != 0 {
		t.Error("a provider doorman has no client for is not a failure")
	}
}

func TestATrunkWithNoCredentialsIsUnconfiguredNotAnError(t *testing.T) {
	results := checkBalances(context.Background(), balanceCheck{
		trunks: trunksFor(t, `
[[trunks]]
id = "voipms"
provider = "voip.ms"
host = "chicago.voip.ms"
username = "123456_home"
password_env = "TRUNK_VOIPMS_PASSWORD"
`),
		secret:  secretsFrom(nil),
		timeout: time.Second,
	})
	if results[0].State != balanceUnconfigured {
		t.Fatalf("state = %q, want %q", results[0].State, balanceUnconfigured)
	}
	if balanceExit(results) != 0 {
		t.Error("never having set this up is not a failure")
	}
}

// An unset variable IS a mistake, and it exits differently from a low balance:
// conflating them makes an expired key look like an empty account.
func TestAnUnsetAPIPasswordIsUncheckedWithItsOwnExitCode(t *testing.T) {
	results := checkBalances(context.Background(), balanceCheck{
		trunks:  trunksFor(t, balanceTrunk),
		secret:  secretsFrom(nil),
		timeout: time.Second,
	})
	if results[0].State != balanceError {
		t.Fatalf("state = %q, want %q", results[0].State, balanceError)
	}
	if !strings.Contains(results[0].Detail, "TRUNK_VOIPMS_API_PASSWORD") {
		t.Errorf("the variable to set is not named: %q", results[0].Detail)
	}
	if got := balanceExit(results); got != balanceExitUnchecked {
		t.Errorf("exit = %d, want %d", got, balanceExitUnchecked)
	}
}

// A balance known to be low is more actionable than one that is unknown.
func TestLowBeatsUncheckedInTheExitCode(t *testing.T) {
	srv := balanceServer(t, "1.00")
	trunks := trunksFor(t, balanceTrunk)
	low := checkTrunkBalance(context.Background(), balanceCheck{
		secret:   secretsFrom(map[string]string{"TRUNK_VOIPMS_API_PASSWORD": testTrunkAPIPassword}),
		timeout:  5 * time.Second,
		endpoint: srv.URL,
	}, trunks[0])
	broken := balanceResult{Trunk: "other", State: balanceError, Detail: "nope"}

	if got := balanceExit([]balanceResult{broken, low}); got != balanceExitLow {
		t.Errorf("exit = %d, want %d", got, balanceExitLow)
	}
}

func TestMinFlagAppliesOnlyWhereTheTrunkDeclaresNone(t *testing.T) {
	srv := balanceServer(t, "10.00")
	c := balanceCheck{
		secret:   secretsFrom(map[string]string{"TRUNK_VOIPMS_API_PASSWORD": testTrunkAPIPassword}),
		timeout:  5 * time.Second,
		endpoint: srv.URL,
		min:      50,
	}
	declared := trunksFor(t, balanceTrunk)[0] // balance_min = 25
	if got := c.thresholdFor(declared); got != 25 {
		t.Errorf("threshold = %v, want the trunk's own 25", got)
	}
	silent := declared
	silent.BalanceMin = 0
	if got := c.thresholdFor(silent); got != 50 {
		t.Errorf("threshold = %v, want --min 50", got)
	}
}

// With no threshold anywhere, nothing can fail — and the output says so rather
// than letting somebody believe a green run means anything.
func TestNoThresholdReportsAndNeverFails(t *testing.T) {
	srv := balanceServer(t, "0.02")
	trunks := trunksFor(t, strings.Replace(balanceTrunk, "balance_min = 25.0", "", 1))
	results := checkBalances(context.Background(), balanceCheck{
		trunks:   trunks,
		secret:   secretsFrom(map[string]string{"TRUNK_VOIPMS_API_PASSWORD": testTrunkAPIPassword}),
		timeout:  5 * time.Second,
		endpoint: srv.URL,
	})
	if results[0].State != balanceOK || results[0].Threshold != nil {
		t.Fatalf("want ok with no threshold, got %+v", results[0])
	}
	if balanceExit(results) != 0 {
		t.Error("a balance nobody set a threshold for cannot be below one")
	}
	out := capture(t, func() { printBalances(results, "./trunks.toml", 0) })
	if !strings.Contains(out, "No threshold set") || !strings.Contains(out, "balance_min") {
		t.Errorf("should say a green run proves nothing yet:\n%s", out)
	}
}

func TestBalanceJSONCarriesEveryTrunkAndTheExitCode(t *testing.T) {
	srv := balanceServer(t, "4.00")
	results := checkBalances(context.Background(), balanceCheck{
		trunks:   trunksFor(t, balanceTrunk),
		secret:   secretsFrom(map[string]string{"TRUNK_VOIPMS_API_PASSWORD": testTrunkAPIPassword}),
		timeout:  5 * time.Second,
		endpoint: srv.URL,
	})
	out := capture(t, func() { _ = printBalanceJSON(results, "./trunks.toml", balanceExit(results)) })

	var report balanceReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("--json is not JSON: %v\n%s", err, out)
	}
	if report.Exit != balanceExitLow || report.Low != 1 || len(report.Trunks) != 1 {
		t.Errorf("report = %+v", report)
	}
	if report.Trunks[0].Trunk != "voipms" || report.Trunks[0].State != balanceLow {
		t.Errorf("trunk row = %+v", report.Trunks[0])
	}
	assertNoAPICredential(t, out)
}

// Nothing this command prints may carry the API password, on any path — and
// the error path is the one that would, because net/http wraps transport
// failures together with the URL the credentials travel in.
func TestNothingPrintedCarriesTheAPIPassword(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	endpoint := srv.URL
	srv.Close()

	results := checkBalances(context.Background(), balanceCheck{
		trunks:   trunksFor(t, balanceTrunk),
		secret:   secretsFrom(map[string]string{"TRUNK_VOIPMS_API_PASSWORD": testTrunkAPIPassword}),
		timeout:  5 * time.Second,
		endpoint: endpoint,
	})
	if results[0].State != balanceError {
		t.Fatalf("expected a transport failure, got %+v", results[0])
	}
	out := capture(t, func() { printBalances(results, "./trunks.toml", 0) })
	assertNoAPICredential(t, out)
	assertNoAPICredential(t, results[0].Detail)
}

func assertNoAPICredential(t *testing.T, text string) {
	t.Helper()
	for _, forbidden := range []string{testTrunkAPIPassword, "api_password=", "api_username="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("output leaks %q:\n%s", forbidden, text)
		}
	}
}

// ── the compatibility gate ───────────────────────────────────────────────

func TestBalanceWithoutATrunksTomlExplainsAndExitsCleanly(t *testing.T) {
	dir := t.TempDir()
	var code int
	out := capture(t, func() { code = runBalance([]string{"-trunks", filepath.Join(dir, "trunks.toml")}) })
	if code != 0 {
		t.Errorf("exit = %d, want 0 — no trunks.toml is the normal state, not a failure", code)
	}
	if !strings.Contains(out, "nothing to check") {
		t.Errorf("should say why there is nothing to do:\n%s", out)
	}
}

func TestBalanceWithoutATrunksTomlStillEmitsJSON(t *testing.T) {
	dir := t.TempDir()
	var code int
	out := capture(t, func() {
		code = runBalance([]string{"-json", "-trunks", filepath.Join(dir, "trunks.toml")})
	})
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	var report balanceReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("--json must stay JSON even with nothing to say: %v\n%s", err, out)
	}
	if len(report.Trunks) != 0 {
		t.Errorf("report = %+v", report)
	}
}

func TestBalanceRefusesAnInvalidTrunksFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trunks.toml")
	if err := os.WriteFile(path, []byte("[[trunks]]\nid = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runBalance([]string{"-trunks", path}); code != balanceExitUsage {
		t.Errorf("exit = %d, want %d", code, balanceExitUsage)
	}
}

// ── the daemon must not read these credentials ───────────────────────────

// M1 is CLI-only, and this is what makes that structural rather than a
// promise. A provider API key manages DIDs, sub-accounts and billing; keeping
// it out of the long-running process keeps it off the call path entirely.
func TestOnlyTheBalanceCommandTouchesProviderCredentials(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || name == "balance.go" || name == "balance_test.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"internal/provider", "APIPasswordEnv", "APIUsername"} {
			if strings.Contains(string(src), forbidden) {
				t.Errorf("%s mentions %s — the provider API credential is read by "+
					"`doorman balance` and by nothing else, deliberately", name, forbidden)
			}
		}
	}
}

// And the import direction, which is the same rule one level up: nothing the
// daemon links can reach the provider package at all.
func TestNothingOnTheCallPathImportsTheProviderPackage(t *testing.T) {
	fset := token.NewFileSet()
	root := filepath.Join("..", "..", "internal")
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "provider" {
			continue
		}
		pkgs, err := parser.ParseDir(fset, filepath.Join(root, d.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", d.Name(), err)
		}
		for _, pkg := range pkgs {
			for path, file := range pkg.Files {
				for _, imp := range file.Imports {
					if strings.Contains(imp.Path.Value, "internal/provider") {
						t.Errorf("%s imports internal/provider — balance checking is a CLI, "+
							"and the daemon must never hold a provider API key", path)
					}
				}
			}
		}
	}
}
