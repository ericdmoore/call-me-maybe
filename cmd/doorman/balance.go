package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/provider"
)

// `doorman balance` — how much credit is left, per trunk, and a non-zero exit
// when any of it is running out.
//
// The failure this exists to remove is silent by construction: a prepaid trunk
// that reaches zero does not error, inbound simply stops arriving, and "nobody
// called today" is indistinguishable from a quiet Tuesday. There is no log line
// to grep for and no symptom until somebody mentions they tried to reach you
// last week.
//
// It is a CLI and not a daemon feature, deliberately, and the reason is the
// credential. A provider's API key manages DIDs, sub-accounts and billing —
// considerably more privilege than the SIP sub-account password, which RUNBOOK
// §1 already says to keep off the Pi. Making this a CLI keeps that key off the
// call path and out of the long-running process entirely: the daemon does not
// read it, does not poll, and does not import internal/provider at all. Put the
// check wherever your alerting already lives, point it at a copy of
// trunks.toml, and the Pi never holds the key.
//
// The exit code is the whole interface for cron, so it is a contract:
//
//	0  every trunk that could be checked is above its threshold
//	1  at least one trunk is BELOW its threshold — the one to act on
//	2  the command line or the inventory was wrong
//	3  nothing was below a threshold, but at least one trunk could not be
//	   checked at all
//
// 1 and 3 are separated because conflating them makes an expired API key look
// like an empty account and an empty account look like a network blip. 1 wins
// when both happen: a balance known to be low is more actionable than one that
// is unknown.

// balanceState is what happened to one trunk. Every trunk gets one — a trunk
// whose provider cannot report a balance is reported as such, never skipped,
// because a row silently missing from this table is the same silence the
// feature exists to break.
type balanceState string

const (
	balanceOK           balanceState = "ok"
	balanceLow          balanceState = "low"
	balanceNoCapability balanceState = "no_capability"
	balanceNoClient     balanceState = "no_client"
	balanceUnconfigured balanceState = "unconfigured"
	balanceError        balanceState = "error"
)

const (
	balanceExitLow       = 1
	balanceExitUsage     = 2
	balanceExitUnchecked = 3
)

// balanceResult is one trunk's answer, and the JSON shape `--json` publishes.
// It is deliberately flat: this is read by jq in a cron job.
type balanceResult struct {
	Trunk     string       `json:"trunk"`
	Provider  string       `json:"provider,omitempty"`
	State     balanceState `json:"state"`
	Billing   string       `json:"billing,omitempty"`
	Balance   *float64     `json:"balance,omitempty"`
	Currency  string       `json:"currency,omitempty"`
	Threshold *float64     `json:"threshold,omitempty"`
	// Detail is why, for every state that is not a number: the error, the
	// variable that is not set, the providers doorman does have a client for.
	// Never a credential and never a URL — see provider.scrub.
	Detail string `json:"detail,omitempty"`
}

type balanceReport struct {
	CheckedAt string          `json:"checked_at"`
	Where     string          `json:"trunks_file"`
	Trunks    []balanceResult `json:"trunks"`
	Low       int             `json:"low"`
	Unchecked int             `json:"unchecked"`
	Exit      int             `json:"exit"`
}

func runBalance(args []string) int {
	fs := flag.NewFlagSet("balance", flag.ExitOnError)
	trunksFlag := fs.String("trunks", "", "provider inventory (default $TRUNKS_PATH or ./trunks.toml)")
	envFlag := fs.String("env", "./.env", "secrets file holding the provider API passwords")
	minFlag := fs.Float64("min", 0, "threshold for trunks that declare no balance_min")
	jsonFlag := fs.Bool("json", false, "machine-readable output for a cron job")
	timeoutFlag := fs.Duration("timeout", 20*time.Second, "how long to wait on one provider")
	_ = fs.Parse(args)

	trunksPath := trunksPathArg(*trunksFlag)
	trunks, err := policy.LoadTrunks(trunksPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %s is not valid\n\n%v\n", trunksPath, err)
		return balanceExitUsage
	}
	if !trunks.Present() {
		printNoTrunksToCheck(trunksPath, *jsonFlag)
		return 0
	}
	if *minFlag < 0 {
		fmt.Fprintln(os.Stderr, "✗ --min must not be negative")
		return balanceExitUsage
	}

	results := checkBalances(context.Background(), balanceCheck{
		trunks:  trunks.All(),
		secret:  secretLookup(*envFlag),
		min:     *minFlag,
		timeout: *timeoutFlag,
	})
	code := balanceExit(results)

	if *jsonFlag {
		return printBalanceJSON(results, trunks.Where(), code)
	}
	printBalances(results, trunks.Where(), *minFlag)
	return code
}

// printNoTrunksToCheck is the compatibility gate, spoken out loud. No
// trunks.toml is the normal state of every single-provider install, and it is
// not a mistake to be told off for: there is nothing to check, so this says so
// and exits cleanly rather than failing a cron job into a mailbox every hour.
func printNoTrunksToCheck(path string, asJSON bool) {
	if asJSON {
		report := balanceReport{
			CheckedAt: time.Now().UTC().Format(time.RFC3339),
			Where:     path,
			Trunks:    []balanceResult{},
		}
		out, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(out))
		return
	}
	fmt.Printf("No trunks.toml at %s, so there is nothing to check.\n\n", path)
	fmt.Println("  A balance belongs to a provider account, and the provider inventory is")
	fmt.Println("  what says which accounts this box has. Without one there is a single")
	fmt.Println("  hand-written trunk in asterisk/pjsip.conf, which names no provider and")
	fmt.Println("  carries no API credentials — see examples/trunks.example.toml.")
	fmt.Println()
	fmt.Println("  Nothing is wrong, and nothing else about this install changes.")
}

// balanceCheck is everything one run needs. It exists as a struct so the work
// is a function of its arguments and the tests can drive it against an httptest
// server without a real account anywhere.
type balanceCheck struct {
	trunks  []policy.Trunk
	secret  func(string) (string, bool)
	min     float64
	timeout time.Duration
	// endpoint repoints every provider API, for tests only. Deliberately not a
	// flag: a switch that aims a billing API at another host is a foot-gun,
	// and nothing an operator does should need it.
	endpoint string
}

func checkBalances(ctx context.Context, c balanceCheck) []balanceResult {
	out := make([]balanceResult, 0, len(c.trunks))
	for _, tr := range c.trunks {
		out = append(out, checkTrunkBalance(ctx, c, tr))
	}
	return out
}

// checkTrunkBalance answers for one trunk. Every path returns a result: there
// is no branch here that drops a trunk on the floor.
func checkTrunkBalance(ctx context.Context, c balanceCheck, tr policy.Trunk) balanceResult {
	r := balanceResult{Trunk: tr.ID, Provider: tr.Provider, State: balanceNoClient}

	switch {
	case tr.Provider == "":
		r.Detail = "this trunk declares no provider, so nothing knows which API to ask"
		return r
	case provider.Canonical(tr.Provider) == "":
		r.Detail = fmt.Sprintf("no client for %q — doorman can report a balance for: %s",
			tr.Provider, strings.Join(provider.BalanceBackends(), ", "))
		return r
	}

	cfg := provider.Config{Endpoint: c.endpoint, Username: tr.APIUsername}
	if tr.APIPasswordEnv != "" {
		secret, ok := c.secret(tr.APIPasswordEnv)
		if !ok || secret == "" {
			// An unset variable is a mistake somebody made, not a provider that
			// has nothing to report — so it counts as unchecked rather than as
			// unconfigured, and the exit code says so.
			r.State = balanceError
			r.Detail = fmt.Sprintf("%s is not set in the environment or the secrets file",
				tr.APIPasswordEnv)
			return r
		}
		cfg.Password = secret
	}

	p, err := provider.New(tr.Provider, cfg)
	switch {
	case errors.Is(err, provider.ErrNoCredentials):
		r.State = balanceUnconfigured
		r.Detail = "no API credentials, so this balance is not being watched"
		return r
	case err != nil:
		r.State = balanceError
		r.Detail = err.Error()
		return r
	}
	r.Billing = string(p.Billing())

	// The capability, asked for rather than assumed. A provider that invoices
	// does not implement it, and that is an answer.
	asker, ok := p.(provider.Balance)
	if !ok {
		r.State = balanceNoCapability
		return r
	}

	if min := c.thresholdFor(tr); min > 0 {
		r.Threshold = &min
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	amount, err := asker.Balance(ctx)
	if err != nil {
		r.State = balanceError
		r.Detail = err.Error()
		return r
	}
	value := amount.Value
	r.Balance, r.Currency = &value, amount.Currency
	r.State = balanceOK
	if r.Threshold != nil && value < *r.Threshold {
		r.State = balanceLow
	}
	return r
}

// thresholdFor is the trunk's own balance_min, or --min for a trunk that
// declares none. Zero means no threshold at all, which reports and never
// fails: a number nobody chose would fire at the wrong time, and this command
// is meant to run untended.
func (c balanceCheck) thresholdFor(tr policy.Trunk) float64 {
	if tr.BalanceMin > 0 {
		return tr.BalanceMin
	}
	return c.min
}

// balanceTally is the three numbers every summary and the exit code are
// derived from, counted once so the table, the JSON and the exit code cannot
// disagree about what just happened.
type balanceTally struct{ low, unchecked, checked int }

func tallyBalances(results []balanceResult) balanceTally {
	var t balanceTally
	for _, r := range results {
		switch r.State {
		case balanceLow:
			t.low++
			t.checked++
		case balanceOK:
			t.checked++
		case balanceError:
			t.unchecked++
		}
	}
	return t
}

func balanceExit(results []balanceResult) int {
	t := tallyBalances(results)
	switch {
	case t.low > 0:
		return balanceExitLow
	case t.unchecked > 0:
		return balanceExitUnchecked
	}
	return 0
}

func printBalanceJSON(results []balanceResult, where string, code int) int {
	t := tallyBalances(results)
	out, err := json.MarshalIndent(balanceReport{
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Where:     where,
		Trunks:    results,
		Low:       t.low,
		Unchecked: t.unchecked,
		Exit:      code,
	}, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return balanceExitUsage
	}
	fmt.Println(string(out))
	return code
}

func printBalances(results []balanceResult, where string, fallbackMin float64) {
	width := len("trunk")
	pwidth := len("provider")
	for _, r := range results {
		if len(r.Trunk) > width {
			width = len(r.Trunk)
		}
		if len(r.Provider) > pwidth {
			pwidth = len(r.Provider)
		}
	}

	fmt.Printf("\nBalances: %d %s   (%s)\n\n", len(results), plural(len(results), "trunk"), where)
	fmt.Printf("  %-*s  %-*s  %12s  %12s  %s\n", width, "trunk", pwidth, "provider",
		"balance", "threshold", "status")
	for _, r := range results {
		fmt.Printf("  %-*s  %-*s  %12s  %12s  %s\n", width, r.Trunk, pwidth,
			orDefault(r.Provider, "—"), balanceCell(r), thresholdCell(r), balanceVerdict(r))
	}

	printBalanceNotes(results, fallbackMin)
	printBalanceSummary(results)
}

func balanceCell(r balanceResult) string {
	if r.Balance == nil {
		return "—"
	}
	return provider.Amount{Value: *r.Balance, Currency: r.Currency}.String()
}

func thresholdCell(r balanceResult) string {
	if r.Threshold == nil {
		return "—"
	}
	return provider.Amount{Value: *r.Threshold, Currency: r.Currency}.String()
}

// balanceVerdict is the short word in the last column. LOW is the only one
// shouted, because it is the only one anybody has to do something about today.
func balanceVerdict(r balanceResult) string {
	switch r.State {
	case balanceOK:
		if r.Threshold == nil {
			return "ok   (no threshold)"
		}
		return "ok"
	case balanceLow:
		return "✗ LOW"
	case balanceNoCapability:
		return "no balance to report"
	case balanceNoClient:
		return "not checkable"
	case balanceUnconfigured:
		return "not configured"
	default:
		return "✗ could not check"
	}
}

// printBalanceNotes says which account and why, for every row that is not a
// plain number. Which account is the point: several trunks means several
// balances, and "the balance is low" without a name is a message somebody has
// to go and investigate.
func printBalanceNotes(results []balanceResult, fallbackMin float64) {
	for _, r := range results {
		switch r.State {
		case balanceLow:
			fmt.Printf("\n✗ %s is low: %s, under its threshold of %s.\n",
				r.Trunk, balanceCell(r), thresholdCell(r))
			fmt.Println("    Top it up. A prepaid account that reaches zero stops inbound calls")
			fmt.Println("    arriving with no error anywhere — the phone looks fine and nobody")
			fmt.Println("    can reach you.")
		case balanceNoCapability:
			fmt.Printf("\n  %s is %s — no balance to report.\n",
				orDefault(r.Provider, r.Trunk), orDefault(r.Billing, "invoiced"))
			fmt.Println("    Nothing to run out of and nothing to watch. Reported rather than")
			fmt.Println("    left out of the table, because a row that quietly disappears is the")
			fmt.Println("    same silence this command exists to break.")
		case balanceNoClient:
			fmt.Printf("\n  %s: %s\n", r.Trunk, r.Detail)
			fmt.Println("    Reported, not skipped. If this provider invoices you there is")
			fmt.Println("    nothing to check; if it is prepaid, its balance is the one thing")
			fmt.Println("    nobody here is watching.")
		case balanceUnconfigured:
			fmt.Printf("\n  %s: %s\n", r.Trunk, r.Detail)
			fmt.Println("    Add api_username and api_password_env to this trunk in trunks.toml,")
			fmt.Println("    and the variable it names to the environment. On VoIP.ms the API")
			fmt.Println("    password is a separate one set in Main Menu → Account Settings →")
			fmt.Println("    API, where API access also has to be switched on and the IP of the")
			fmt.Println("    machine running this allow-listed. It is not the portal login and")
			fmt.Println("    not the SIP sub-account password.")
		case balanceError:
			fmt.Printf("\n✗ %s could not be checked: %s\n", r.Trunk, r.Detail)
			fmt.Println("    Not the same as a balance of zero, and deliberately a different")
			fmt.Println("    exit code: an expired key and an empty account look identical from")
			fmt.Println("    here, and treating one as the other is how a real outage gets")
			fmt.Println("    dismissed as a broken cron job.")
		}
	}

	if noThreshold(results) {
		fmt.Println("\n  No threshold set, so nothing here can fail.")
		fmt.Println("    Set balance_min on a trunk in trunks.toml, or pass --min, and this")
		fmt.Println("    command exits 1 when that account drops below it — which is the whole")
		fmt.Println("    point of running it from cron. Choose a number that leaves time to")
		fmt.Println("    act: a warning at zero is a death rattle, not an alert.")
	}
	if fallbackMin > 0 {
		fmt.Printf("\n  --min %.2f applied to every trunk that declares no balance_min.\n", fallbackMin)
	}
}

func noThreshold(results []balanceResult) bool {
	for _, r := range results {
		if r.Threshold != nil {
			return false
		}
	}
	return true
}

func printBalanceSummary(results []balanceResult) {
	t := tallyBalances(results)

	fmt.Println()
	switch {
	case t.low > 0:
		fmt.Printf("✗ %d %s below threshold — exit %d\n", t.low, plural(t.low, "trunk"), balanceExitLow)
	case t.unchecked > 0:
		fmt.Printf("✗ %d %s could not be checked — exit %d\n",
			t.unchecked, plural(t.unchecked, "trunk"), balanceExitUnchecked)
	case t.checked > 0:
		fmt.Printf("✓ %d %s checked, none below threshold — exit 0\n", t.checked, plural(t.checked, "trunk"))
	default:
		fmt.Println("✓ nothing to check — exit 0")
	}

	fmt.Println()
	fmt.Println("  In cron, the exit code is the whole interface — no wrapper script:")
	fmt.Println("    0  everything checkable is above its threshold")
	fmt.Println("    1  a trunk is BELOW its threshold. This is the one to act on")
	fmt.Println("    2  the command line or trunks.toml was wrong")
	fmt.Println("    3  nothing was low, but something could not be checked at all")
	fmt.Println()
	fmt.Println("  Run it wherever your alerting already lives rather than on the Pi.")
	fmt.Println("  `doorman balance --json` needs no daemon and no phone system: a copy of")
	fmt.Println("  trunks.toml and the API password is the whole of it. That is deliberate —")
	fmt.Println("  a provider API key manages DIDs, sub-accounts and billing, which is far")
	fmt.Println("  more privilege than the SIP password, and the daemon never reads it.")
}

// balanceCheckable names the trunks `doorman balance` could ask about, for
// `doorman check`. Empty on every install that has not set this up, which is
// what keeps that command silent about a feature nobody opted into.
func balanceCheckable(trunks *policy.Trunks) []string {
	var out []string
	for _, tr := range trunks.All() {
		if tr.BalanceConfigured() {
			out = append(out, tr.ID)
		}
	}
	return out
}
