package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// `doorman balance --prom <file>` — the gauge.
//
// The phone call answers "act now"; a gauge answers "what has it been doing",
// and it goes to whatever alerting the operator already runs rather than
// making doorman grow thresholds, SMTP and retry logic of its own. The daemon
// cannot serve this on a metrics endpoint — it never checks a balance,
// deliberately (M1) — so the CLI writes the Prometheus text format to a file
// for node_exporter's textfile collector, which is exactly the mechanism that
// exists for numbers a cron job produces. Written atomically, because the
// collector reads the directory whenever it is scraped.
//
// No account identifiers in labels. A trunk id and a provider name are
// inventory; an API username is a credential-shaped string, and labels are
// forever in a time series database.

func promText(results []balanceResult, now time.Time) string {
	var b strings.Builder
	sorted := append([]balanceResult(nil), results...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Trunk < sorted[j].Trunk })

	b.WriteString("# HELP doorman_trunk_balance Prepaid credit left on the trunk's provider account, in the account's currency.\n")
	b.WriteString("# TYPE doorman_trunk_balance gauge\n")
	for _, r := range sorted {
		if r.Balance == nil {
			continue
		}
		fmt.Fprintf(&b, "doorman_trunk_balance{trunk=%s,provider=%s,currency=%s} %s\n",
			promLabel(r.Trunk), promLabel(r.Provider), promLabel(r.Currency), promFloat(*r.Balance))
	}

	b.WriteString("# HELP doorman_trunk_balance_threshold The balance below which `doorman balance` exits 1 for this trunk.\n")
	b.WriteString("# TYPE doorman_trunk_balance_threshold gauge\n")
	for _, r := range sorted {
		if r.Threshold == nil {
			continue
		}
		fmt.Fprintf(&b, "doorman_trunk_balance_threshold{trunk=%s,provider=%s} %s\n",
			promLabel(r.Trunk), promLabel(r.Provider), promFloat(*r.Threshold))
	}

	b.WriteString("# HELP doorman_trunk_balance_known 1 when the last check read a number for this trunk, 0 when it could not — an unset key, a provider error, or a provider that reports no balance.\n")
	b.WriteString("# TYPE doorman_trunk_balance_known gauge\n")
	for _, r := range sorted {
		known := 0
		if r.Balance != nil {
			known = 1
		}
		fmt.Fprintf(&b, "doorman_trunk_balance_known{trunk=%s,provider=%s} %d\n",
			promLabel(r.Trunk), promLabel(r.Provider), known)
	}

	b.WriteString("# HELP doorman_balance_last_check_timestamp_seconds When `doorman balance` last wrote this file.\n")
	b.WriteString("# TYPE doorman_balance_last_check_timestamp_seconds gauge\n")
	fmt.Fprintf(&b, "doorman_balance_last_check_timestamp_seconds %d\n", now.Unix())
	return b.String()
}

// promLabel quotes a label value the way the exposition format requires:
// backslash, double quote and newline escaped, everything else as is.
func promLabel(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}

func promFloat(f float64) string {
	return fmt.Sprintf("%g", f)
}

func writeProm(path string, results []balanceResult, now time.Time) error {
	return writeFileAtomic(path, []byte(promText(results, now)), 0o644)
}
