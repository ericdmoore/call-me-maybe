package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"callmemaybe/internal/host"
	"callmemaybe/internal/policy"
)

// runInitServices is `doorman init services`: the units from inside the
// binary onto the host, the directories each process owns, and `enable
// --now` for exactly the units the configuration calls for. Idempotent — a
// rerun is the upgrade — and `--dry-run` prints the same plan a real run
// executes. Root for the real thing; without it the plan is printed and
// the sudo line with it.
func runInitServices(args []string) int {
	fs := flag.NewFlagSet("init services", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "print the plan and change nothing")
	envPath := fs.String("env", "/opt/call-me-maybe/.env", "secrets file the units read (decides which are enabled)")
	handsetsFlag := fs.String("handsets", "", "inventory file (default $HANDSETS_PATH or ./handsets.toml)")
	trunksFlag := fs.String("trunks", "", "provider inventory, optional (default $TRUNKS_PATH or ./trunks.toml)")
	messagesFlag := fs.String("messages", "", "words file, optional (default $MESSAGES_PATH or ./messages.toml)")
	_ = fs.Parse(args)

	env := secretLookup(*envPath)
	base := filepath.Dir(*envPath) // the units' WorkingDirectory: where .env lives
	wants := decideWants(env, servicePath(*handsetsFlag, "HANDSETS_PATH", "handsets.toml", env, base),
		servicePath(*trunksFlag, "TRUNKS_PATH", "trunks.toml", env, base), servicePath(*messagesFlag, "MESSAGES_PATH", "messages.toml", env, base))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	x := host.System{}
	plan, err := host.Build(ctx, x, host.DefaultLayout(), wants)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	printPlan(plan, wants)
	if *dryRun {
		return 0
	}
	if os.Geteuid() != 0 {
		fmt.Println("\nThat is the plan. Run it as root:\n\n  sudo doorman init services")
		return 2
	}
	if err := host.Apply(ctx, x, plan); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	fmt.Printf("\n✓ services installed: %d enabled, %d left disabled", len(plan.Enable), len(plan.Disable))
	if len(plan.Foreign) > 0 {
		fmt.Printf(", %d of yours untouched", len(plan.Foreign))
	}
	fmt.Println(". `doorman check` shows their state.")
	return 0
}

// servicePath resolves an inventory the way the units will see it: an
// explicit flag wins; then the variable from the process environment or the
// chosen .env (the units read .env as EnvironmentFile); then the default
// name beside .env, which is the units' WorkingDirectory — never the
// operator's current directory, which is wherever they typed sudo.
func servicePath(explicit, key, name string, env func(string) (string, bool), base string) string {
	if explicit != "" {
		return explicit
	}
	if v, ok := env(key); ok && strings.TrimSpace(v) != "" {
		v = strings.TrimSpace(v)
		if filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(base, v)
	}
	return filepath.Join(base, name)
}

// decideWants is the one place the enabling rules read the config. Each
// answer carries its reason, printed beside the unit, so "why is the inbox
// off" is answered by the plan rather than by the runbook.
func decideWants(env func(string) (string, bool), handsetsPath, trunksPath, messagesPath string) host.Wants {
	w := host.Wants{Why: map[string]string{}}
	set := func(k string) bool { v, ok := env(k); return ok && strings.TrimSpace(v) != "" }
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	switch {
	case !set("PROVISION_ADDRESS"):
		w.Why["doorman-directory.service"] = "PROVISION_ADDRESS is not set"
	default:
		templated := false
		if handsets, _, err := policy.LoadHandsets(handsetsPath); err == nil {
			for _, h := range handsets {
				if h.MAC != "" && h.Model != "" {
					templated = true
					break
				}
			}
		}
		if templated {
			w.Directory, w.Why["doorman-directory.service"] = true, "PROVISION_ADDRESS is set and a handset has mac + model"
		} else {
			w.Why["doorman-directory.service"] = "no handset has mac + model, so no phone polls a directory"
		}
	}
	switch {
	case !set("INBOX_URL"):
		w.Why["doorman-inbox.service"] = "INBOX_URL is not set"
	case !exists(messagesPath):
		w.Why["doorman-inbox.service"] = "no " + messagesPath
	default:
		w.Inbox, w.Why["doorman-inbox.service"] = true, "INBOX_URL and "+messagesPath
	}
	if set("MAIL_HOOK") && set("MAIL_TO") {
		w.Digest, w.Why["doorman-digest.timer"] = true, "MAIL_HOOK and MAIL_TO are set"
	} else {
		w.Why["doorman-digest.timer"] = "MAIL_HOOK and MAIL_TO are not both set"
	}
	if exists(trunksPath) {
		w.Balance, w.Why["doorman-balance.timer"] = true, trunksPath+" exists"
	} else {
		w.Why["doorman-balance.timer"] = "no " + trunksPath + " (one provider: nothing to check)"
	}
	return w
}

func printPlan(p *host.Plan, w host.Wants) {
	fmt.Println("Plan:")
	for _, s := range p.Steps {
		fmt.Printf("  %s\n", s.Describe)
	}
}

// printServices is `doorman check`'s "Services" section: each unit the
// config calls for against what the host says. Silent where there is no
// systemd to ask (a workstation), so check stays useful everywhere.
func printServices(ctx context.Context, x host.Exec, lay host.Layout, w host.Wants) bool {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return true
	}
	statuses, err := host.Inspect(ctx, x, lay, w)
	if err != nil {
		fmt.Printf("\nServices: %v\n", err)
		return false
	}
	fmt.Println("\nServices (what the config calls for, and what the host says):")
	ok := true
	for _, s := range statuses {
		state := "disabled"
		switch {
		case s.Enabled && s.Active:
			state = "enabled, active"
		case s.Enabled:
			state = "enabled, not active"
		case s.Active:
			state = "active, not enabled"
		}
		if !s.Present {
			state = "not installed"
		}
		mark := "  "
		switch {
		case s.Foreign:
			mark = "  !"
			state += " (not doorman's unit; left alone)"
		case s.Want && !(s.Enabled && s.Active):
			mark = "  ✗"
			ok = false
		case !s.Want && s.Enabled:
			mark = "  !"
		}
		want := "wanted"
		if !s.Want {
			want = "not wanted"
		}
		why := w.Why[s.Name]
		if why == "" && s.Want {
			why = "always"
		}
		fmt.Printf("%s %-28s %-12s %s — %s\n", mark, s.Name, want, state, why)
	}
	if !ok {
		fmt.Println("    run `sudo doorman init services` to install and enable what the config calls for")
	}
	return ok
}
