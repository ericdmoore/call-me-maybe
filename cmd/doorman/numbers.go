package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/provider"
)

// `doorman numbers` — which phone numbers each trunk's account actually holds,
// weighed against the numbers the policy files claim for that trunk.
//
// The failure it removes: a number is copied by hand from a provider's portal
// into [line] number, and a typo there is a line that silently never rings. The
// same silence comes from a number bought and never assigned to a connection,
// or assigned to the wrong one. Nothing on the box can see any of those — the
// registration is up, the dialplan is right, and the calls go somewhere else.
// The provider's API can see all three.
//
// A command of its own rather than a section of `doorman check`, for the same
// reason `doorman balance` is one. check runs offline, on the Pi, from the same
// file as the daemon, and must never need a provider API key — a key that can
// buy and release numbers is far more privilege than the SIP password, and the
// daemon never reads it (the fence tests in balance_test.go). This is a CLI,
// best run wherever that key already lives, against a copy of the config.
//
// The numbers printed are the household's own DIDs, and they are printed in
// full, as `doorman check` prints them. Caller-ID redaction (invariant 1) is
// about logs and about other people's numbers; a report whose whole point is
// "this exact number is not on the account" is useless with the digits masked.
//
// Exit codes, the same shape as `doorman balance`:
//
//	0  every number the policy claims is on its trunk's account and, if a line
//	   answers it, pointed at the connection that trunk registers as
//	1  at least one claimed number is not — the one to act on
//	2  the command line or the configuration was wrong
//	3  nothing was wrong, but at least one trunk could not be checked
//
// A provider doorman cannot ask (VoIP.ms, today) is reported and never fails
// the run: that is a gap in this tool, not a fault in the account.

const (
	numbersExitProblem   = 1
	numbersExitUsage     = 2
	numbersExitUnchecked = 3
)

// numbersState is what happened to one trunk, the same vocabulary as balance.
type numbersState string

const (
	numbersChecked      numbersState = "checked"
	numbersNoCapability numbersState = "no_capability"
	numbersNoClient     numbersState = "no_client"
	numbersUnconfigured numbersState = "unconfigured"
	numbersError        numbersState = "error"
)

// numberVerdict is what one number turned out to be.
type numberVerdict string

const (
	// numberRouted is on the account and pointed at the connection this trunk
	// registers as — or, for a number a line only presents, simply held.
	numberRouted numberVerdict = "ok"
	// numberUnverified is on the account and assigned to a connection the
	// provider does not name a SIP username for, so whether it is this trunk's
	// cannot be told from here.
	numberUnverified numberVerdict = "connection_unknown"
	numberMissing    numberVerdict = "not_on_account"
	numberInactive   numberVerdict = "inactive"
	numberNoConn     numberVerdict = "no_connection"
	numberOtherConn  numberVerdict = "other_connection"
	// numberUnclaimed is on the account and named by no policy file. Not a
	// problem — a number held for later is ordinary — and listed because the
	// one you meant to type is often in this list.
	numberUnclaimed numberVerdict = "unclaimed"
	// numberOtherTrunk is on this account and claimed for a different trunk
	// that shares it, whose own section weighs the claim. Not a problem here.
	numberOtherTrunk numberVerdict = "other_trunk"
	// numberUnchecked is a claim on a trunk that could not be asked.
	numberUnchecked numberVerdict = "unchecked"
)

// numberClaim is one number the policy files name for one trunk.
type numberClaim struct {
	E164 string
	// Lines are the lines that name it, primary first.
	Lines []string
	// Answers is [line] number: inbound calls to it must reach this trunk.
	// Presents is [line] outbound_cid: calls leave showing it.
	Answers, Presents bool
}

func (c numberClaim) role() string {
	who := "line " + strings.Join(c.Lines, ", line ")
	switch {
	case c.Answers && c.Presents:
		return who + " answers and presents it"
	case c.Answers:
		return who + " answers it"
	default:
		return who + " presents it"
	}
}

// numberRow is one number in the report, and the JSON shape --json publishes.
type numberRow struct {
	Number   string        `json:"number"`
	Verdict  numberVerdict `json:"verdict"`
	Problem  bool          `json:"problem"`
	Lines    []string      `json:"lines,omitempty"`
	Answers  bool          `json:"answers,omitempty"`
	Presents bool          `json:"presents,omitempty"`
	Status   string        `json:"status,omitempty"`
	// Connection is where the provider sends the number's calls, empty when
	// nowhere; ConnectionUser is the SIP login that registers as it, when the
	// provider says.
	Connection     string `json:"connection,omitempty"`
	ConnectionID   string `json:"connection_id,omitempty"`
	ConnectionUser string `json:"connection_user,omitempty"`
	Emergency      *bool  `json:"emergency_enabled,omitempty"`
	Detail         string `json:"detail"`
}

type trunkNumbers struct {
	Trunk    string       `json:"trunk"`
	Provider string       `json:"provider,omitempty"`
	Username string       `json:"registers_as"`
	State    numbersState `json:"state"`
	Detail   string       `json:"detail,omitempty"`
	Numbers  []numberRow  `json:"numbers"`
	// Warnings are true and worth reading but decide nothing — the e911
	// declaration contradicting what the provider says, today.
	Warnings []string `json:"warnings,omitempty"`
}

type numbersReport struct {
	CheckedAt string         `json:"checked_at"`
	Where     string         `json:"trunks_file"`
	Trunks    []trunkNumbers `json:"trunks"`
	// Unattributed are numbers a line names without naming a trunk, which
	// leave by the dialplan's DEFAULT_TRUNK — a value nothing here can read.
	Unattributed []numberRow `json:"unattributed,omitempty"`
	Problems     int         `json:"problems"`
	Unchecked    int         `json:"unchecked"`
	Exit         int         `json:"exit"`
}

func runNumbers(args []string) int {
	fs := flag.NewFlagSet("numbers", flag.ExitOnError)
	handsetsFlag := fs.String("handsets", "", "handsets file (default $HANDSETS_PATH or ./handsets.toml)")
	trunksFlag := fs.String("trunks", "", "provider inventory (default $TRUNKS_PATH or ./trunks.toml)")
	envFlag := fs.String("env", "./.env", "secrets file holding the provider API keys")
	jsonFlag := fs.Bool("json", false, "machine-readable output")
	timeoutFlag := fs.Duration("timeout", 30*time.Second, "how long to wait on one provider")
	_ = fs.Parse(args)

	trunksPath := trunksPathArg(*trunksFlag)
	trunks, err := policy.LoadTrunks(trunksPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %s is not valid\n\n%v\n", trunksPath, err)
		return numbersExitUsage
	}
	if !trunks.Present() {
		if *jsonFlag {
			out, _ := json.MarshalIndent(numbersReport{
				CheckedAt: time.Now().UTC().Format(time.RFC3339), Where: trunksPath, Trunks: []trunkNumbers{},
			}, "", "  ")
			fmt.Println(string(out))
			return 0
		}
		fmt.Printf("No trunks.toml at %s, so there is no account to ask.\n\n", trunksPath)
		fmt.Println("  The numbers an account holds belong to a provider, and trunks.toml is")
		fmt.Println("  what says which providers this box has and how to reach their APIs.")
		fmt.Println("  See examples/trunks.example.toml. Nothing is wrong.")
		return 0
	}

	path := ""
	if fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	policyPath := policyPathArg(path)
	if _, err := os.Stat(policyPath); errors.Is(err, os.ErrNotExist) {
		// renderLines reads a missing policy as no lines, which is right for
		// render and wrong here: the claims are the whole input, and with none
		// every number reads as unclaimed and the run exits 0 — a green from a
		// typo'd path, or a copy of trunks.toml without the policy beside it.
		fmt.Fprintf(os.Stderr, "✗ no policy file at %s, so there are no claimed numbers to check.\n"+
			"  Pass the path of policy.toml (its policy.<line>.toml files are found beside it).\n", policyPath)
		return numbersExitUsage
	}
	lines, _, err := renderLines(policyPath, handsetsPathArg(*handsetsFlag), trunks)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return numbersExitUsage
	}

	claims, unattributed := numberClaims(lines)
	report := checkNumbers(context.Background(), numbersCheck{
		trunks:  trunks.All(),
		claims:  claims,
		secret:  secretLookup(*envFlag),
		timeout: *timeoutFlag,
	})
	report.Where = trunks.Where()
	report.Unattributed = unattributed
	report.Exit = numbersExit(report)

	if *jsonFlag {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return numbersExitUsage
		}
		fmt.Println(string(out))
		return report.Exit
	}
	printNumbers(report)
	return report.Exit
}

// numberClaims is every number the policy files name, grouped by the trunk
// each is declared on. One number named by several lines on one trunk is one
// claim, so the report has one row per number.
//
// Two keys name a DID: [line] number (inbound, so the number must be routed to
// this trunk) and [line] outbound_cid (outbound, so the account must hold it).
// The failover ladder presents a number some line already declares on that
// trunk, so it adds no number of its own. A number on a line that names no
// trunk is returned separately — it leaves by DEFAULT_TRUNK, which nothing
// here can read, and saying so beats guessing.
func numberClaims(lines []lineIdentity) (byTrunk map[string][]numberClaim, unattributed []numberRow) {
	byTrunk = map[string][]numberClaim{}
	index := map[string]int{} // trunk + " " + number → position in byTrunk[trunk]
	loose := map[string]int{}

	add := func(trunk, number, line string, answers bool) {
		if number == "" {
			return
		}
		if trunk == "" {
			i, ok := loose[number]
			if !ok {
				i = len(unattributed)
				loose[number] = i
				unattributed = append(unattributed, numberRow{Number: number, Verdict: numberUnchecked})
			}
			r := &unattributed[i]
			r.Lines = appendOnce(r.Lines, line)
			r.Answers = r.Answers || answers
			r.Presents = r.Presents || !answers
			return
		}
		key := trunk + " " + number
		i, ok := index[key]
		if !ok {
			i = len(byTrunk[trunk])
			index[key] = i
			byTrunk[trunk] = append(byTrunk[trunk], numberClaim{E164: number})
		}
		c := &byTrunk[trunk][i]
		c.Lines = appendOnce(c.Lines, line)
		c.Answers = c.Answers || answers
		c.Presents = c.Presents || !answers
	}
	for _, l := range lines {
		add(l.Trunk, l.Number, l.Name, true)
		add(l.Trunk, l.OutboundCID, l.Name, false)
	}
	for i := range unattributed {
		r := &unattributed[i]
		r.Detail = numberClaim{Lines: r.Lines, Answers: r.Answers, Presents: r.Presents}.role() +
			" but names no [line] trunk, so it rides the dialplan's DEFAULT_TRUNK — which nothing here can read"
	}
	return byTrunk, unattributed
}

func appendOnce(list []string, s string) []string {
	for _, have := range list {
		if have == s {
			return list
		}
	}
	return append(list, s)
}

// numbersCheck is everything one run needs, as a struct so the tests can drive
// it against an httptest server with no account anywhere.
type numbersCheck struct {
	trunks  []policy.Trunk
	claims  map[string][]numberClaim
	secret  func(string) (string, bool)
	timeout time.Duration
	// endpoint repoints every provider API, for tests only — never a flag,
	// for the reason balanceCheck.endpoint is not one.
	endpoint string
}

func checkNumbers(ctx context.Context, c numbersCheck) numbersReport {
	report := numbersReport{CheckedAt: time.Now().UTC().Format(time.RFC3339), Trunks: []trunkNumbers{}}
	for _, tr := range c.trunks {
		report.Trunks = append(report.Trunks, checkTrunkNumbers(ctx, c, tr))
	}
	for _, t := range report.Trunks {
		if t.State == numbersError {
			report.Unchecked++
		}
		for _, n := range t.Numbers {
			if n.Problem {
				report.Problems++
			}
		}
	}
	return report
}

func numbersExit(r numbersReport) int {
	switch {
	case r.Problems > 0:
		return numbersExitProblem
	case r.Unchecked > 0:
		return numbersExitUnchecked
	}
	return 0
}

// checkTrunkNumbers answers for one trunk. Every path returns a result with
// every claim in it: a claim silently missing from the report is the same
// silence this command exists to break.
func checkTrunkNumbers(ctx context.Context, c numbersCheck, tr policy.Trunk) trunkNumbers {
	out := trunkNumbers{Trunk: tr.ID, Provider: tr.Provider, Username: tr.Username, State: numbersNoClient}
	claims := c.claims[tr.ID]
	unchecked := func() trunkNumbers {
		for _, cl := range claims {
			out.Numbers = append(out.Numbers, numberRow{Number: cl.E164, Verdict: numberUnchecked,
				Lines: cl.Lines, Answers: cl.Answers, Presents: cl.Presents,
				Detail: cl.role() + " — not checked"})
		}
		return out
	}

	switch {
	case tr.Provider == "":
		out.Detail = "this trunk declares no provider, so nothing knows which API to ask"
		return unchecked()
	case provider.Canonical(tr.Provider) == "":
		out.Detail = fmt.Sprintf("no client for %q — doorman can list the numbers of: %s",
			tr.Provider, strings.Join(provider.NumberBackends(), ", "))
		return unchecked()
	}

	// The capability first: a provider this command cannot ask is that answer
	// whether or not credentials were configured for it. Telling somebody to
	// set a variable that would then be used for nothing — or failing the run
	// because the balance check's secret is not in this shell — is worse than
	// saying so.
	if !canListNumbers(tr.Provider) {
		out.State = numbersNoCapability
		out.Detail = fmt.Sprintf("doorman cannot ask %s which numbers an account holds yet — it can for: %s",
			provider.Canonical(tr.Provider), strings.Join(provider.NumberBackends(), ", "))
		return unchecked()
	}
	cfg, unset := trunkAPIConfig(tr, c.secret, c.endpoint)
	if unset != "" {
		out.State = numbersError
		out.Detail = fmt.Sprintf("%s is not set in the environment or the secrets file", unset)
		return unchecked()
	}
	p, err := provider.New(tr.Provider, cfg)
	switch {
	case errors.Is(err, provider.ErrNoCredentials):
		if mismatch := trunkCredentialMismatch(tr); mismatch != "" {
			// Credentials were declared and cannot be used: a mistake, so the
			// run says it could not check rather than that nothing was asked.
			out.State = numbersError
			out.Detail = mismatch
			return unchecked()
		}
		out.State = numbersUnconfigured
		out.Detail = "no API key on this trunk, so its numbers cannot be listed — " +
			"add api_key_env to it in trunks.toml and the variable it names to the environment"
		return unchecked()
	case err != nil:
		out.State = numbersError
		out.Detail = err.Error()
		return unchecked()
	}
	lister, ok := p.(provider.NumberLister)
	if !ok {
		// canListNumbers said yes; a client that disagrees is a bug here, and
		// it is reported as one rather than as an account with no numbers.
		out.State = numbersError
		out.Detail = "the " + p.Name() + " client does not list numbers after all"
		return unchecked()
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	held, err := lister.Numbers(ctx)
	if err != nil {
		out.State = numbersError
		out.Detail = err.Error()
		return unchecked()
	}
	out.State = numbersChecked
	out.Detail = fmt.Sprintf("%d %s on the account", len(held), plural(len(held), "number"))

	account := make(map[string]provider.Number, len(held))
	for _, n := range held {
		account[normaliseAccountNumber(n.E164)] = n
	}
	claimed := map[string]bool{}
	for _, cl := range claims {
		claimed[cl.E164] = true
		n, ok := account[cl.E164]
		row := weighClaim(tr, cl, n, ok)
		if tr.E911 != nil && *tr.E911 && cl.Answers && ok && n.Emergency != nil && !*n.Emergency {
			out.Warnings = append(out.Warnings, fmt.Sprintf("trunks.toml declares e911 = true for %s, "+
				"but %s says emergency calling is not enabled on %s — check the address on file "+
				"before relying on the declaration", tr.ID, provider.Canonical(tr.Provider), cl.E164))
		}
		out.Numbers = append(out.Numbers, row)
	}

	// Two trunks can share one account — two credential connections behind
	// one API key — and each then sees the other's numbers. Those are claimed,
	// just not here, and saying "claimed by no policy file" about them invites
	// somebody tidying the account to release a number in use.
	elsewhere := map[string][]string{}
	for id, cls := range c.claims {
		if id == tr.ID {
			continue
		}
		for _, cl := range cls {
			elsewhere[cl.E164] = appendOnce(elsewhere[cl.E164], id)
		}
	}

	var spare []string
	for e164 := range account {
		if !claimed[e164] {
			spare = append(spare, e164)
		}
	}
	sort.Strings(spare)
	for _, e164 := range spare {
		n := account[e164]
		if others := elsewhere[e164]; len(others) > 0 {
			sort.Strings(others)
			row := describeHeld(numberRow{Number: e164, Verdict: numberOtherTrunk}, n)
			row.Detail = fmt.Sprintf("on this account, claimed for %s %s and checked there — %s",
				plural(len(others), "trunk"), strings.Join(others, ", "), connectionPhrase(n.Connection))
			out.Numbers = append(out.Numbers, row)
			continue
		}
		row := describeHeld(numberRow{Number: e164, Verdict: numberUnclaimed}, n)
		row.Detail = "on this account, claimed by no policy file — " + connectionPhrase(n.Connection)
		out.Numbers = append(out.Numbers, row)
	}
	return out
}

func canListNumbers(name string) bool {
	canonical := provider.Canonical(name)
	for _, have := range provider.NumberBackends() {
		if have == canonical {
			return true
		}
	}
	return false
}

// weighClaim is the verdict on one claimed number.
//
// What counts as a problem follows from what the line does with the number. A
// number a line ANSWERS must be on the account, active, and pointed at the
// connection this trunk registers as, or its calls ring nowhere or somewhere
// else. A number a line only PRESENTS must be on the account and active: a
// provider will not carry a caller ID its account does not hold, but which
// connection the number's inbound calls go to is not this call's business.
func weighClaim(tr policy.Trunk, cl numberClaim, n provider.Number, held bool) numberRow {
	row := numberRow{Number: cl.E164, Lines: cl.Lines, Answers: cl.Answers, Presents: cl.Presents}
	role := cl.role()
	if !held {
		row.Verdict, row.Problem = numberMissing, true
		row.Detail = role + " — NOT on this account. A typo here is a line that never rings; " +
			"copy the number from the list below rather than from memory"
		return row
	}
	row = describeHeld(row, n)
	conn := n.Connection

	switch {
	case !n.Active:
		row.Verdict, row.Problem = numberInactive, true
		row.Detail = fmt.Sprintf("%s — on this account, but its status is %q, and calls will not "+
			"route to or from it until it is active", role, orDefault(n.Status, "unknown"))
	case !cl.Answers:
		row.Verdict = numberRouted
		row.Detail = role + " — on this account, which is what presenting it needs"
	case !conn.Assigned():
		row.Verdict, row.Problem = numberNoConn, true
		row.Detail = role + " — on this account but assigned to no connection, so the provider " +
			"has nowhere to send its calls. Assign it to the connection that registers as " + tr.Username
	case conn.Username == "":
		row.Verdict = numberUnverified
		row.Detail = fmt.Sprintf("%s — on this account, %s. The provider does not say which SIP "+
			"login registers as that connection, so whether it is this trunk's (%s) cannot be told "+
			"from here", role, connectionPhrase(conn), tr.Username)
	case conn.Username != tr.Username:
		row.Verdict, row.Problem = numberOtherConn, true
		row.Detail = fmt.Sprintf("%s — on this account, %s, which registers as %s and not as this "+
			"trunk's %s: its calls go there", role, connectionPhrase(conn), conn.Username, tr.Username)
	default:
		row.Verdict = numberRouted
		row.Detail = fmt.Sprintf("%s — on this account, %s, the one this trunk registers as",
			role, connectionPhrase(conn))
	}
	return row
}

func describeHeld(row numberRow, n provider.Number) numberRow {
	row.Status = n.Status
	row.Connection = n.Connection.Name
	row.ConnectionID = n.Connection.ID
	row.ConnectionUser = n.Connection.Username
	row.Emergency = n.Emergency
	return row
}

func connectionPhrase(c provider.Connection) string {
	switch {
	case !c.Assigned():
		return "assigned to no connection"
	case c.Name == "":
		return "assigned to connection " + c.ID
	case c.ID == "" || c.ID == c.Name:
		return "assigned to connection " + c.Name
	}
	return fmt.Sprintf("assigned to connection %s (%s)", c.Name, c.ID)
}

// normaliseAccountNumber puts a provider's number in the form policy files
// compile to, so "+15125550142" and "15125550142" are the same number. A
// value that will not normalise is kept as given: it then matches nothing,
// and shows up in the unclaimed list where somebody can see it.
func normaliseAccountNumber(raw string) string {
	if n := policy.NormaliseCallerID(raw, "1"); n.Kind == policy.KindE164 {
		return n.Value
	}
	return raw
}

func printNumbers(r numbersReport) {
	fmt.Printf("\nNumbers: %d %s   (%s)\n", len(r.Trunks), plural(len(r.Trunks), "trunk"), r.Where)
	for _, t := range r.Trunks {
		fmt.Printf("\n%s (%s), registers as %s — %s\n", t.Trunk, orDefault(t.Provider, "no provider"),
			t.Username, numbersStateLine(t))
		for _, n := range t.Numbers {
			fmt.Printf("  %s %s  %s\n", numberMark(n), n.Number, n.Detail)
		}
		if len(t.Numbers) == 0 && t.State == numbersChecked {
			fmt.Println("    the account holds no numbers and no policy file claims one here")
		}
		for _, w := range t.Warnings {
			fmt.Printf("  ! %s\n", w)
		}
	}
	if len(r.Unattributed) > 0 {
		fmt.Println("\nNumbers no trunk is named for:")
		for _, n := range r.Unattributed {
			fmt.Printf("  · %s  %s\n", n.Number, n.Detail)
		}
	}

	fmt.Println()
	switch {
	case r.Problems > 0:
		fmt.Printf("✗ %d %s the policy files claim %s not where they say — exit %d\n",
			r.Problems, plural(r.Problems, "number"), pluralVerb(r.Problems), numbersExitProblem)
	case r.Unchecked > 0:
		fmt.Printf("✗ %d %s could not be checked — exit %d\n",
			r.Unchecked, plural(r.Unchecked, "trunk"), numbersExitUnchecked)
	default:
		fmt.Println("✓ every number that could be checked is where the policy files say — exit 0")
	}
	fmt.Println()
	fmt.Println("  The API key this uses can buy and release numbers; the daemon never reads")
	fmt.Println("  it. Run this wherever that key lives, against a copy of the config.")
}

func numbersStateLine(t trunkNumbers) string {
	switch t.State {
	case numbersChecked:
		return t.Detail
	case numbersError:
		return "✗ could not be checked: " + t.Detail
	default:
		return t.Detail
	}
}

func numberMark(n numberRow) string {
	switch {
	case n.Problem:
		return "✗"
	case n.Verdict == numberRouted:
		return "✓"
	case n.Verdict == numberUnverified:
		return "?"
	default:
		return "·"
	}
}

func pluralVerb(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
