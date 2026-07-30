package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/setup"
)

// runInit interviews the operator and writes a working configuration.
//
// The examples deliberately cannot work — their PINs are the placeholder
// sentinel — so this is the supported way to get from a fresh checkout to
// something `doorman check` accepts. Every secret is generated with
// crypto/rand; PINs are printed to stdout exactly once and never logged, which
// is the rule `doorman rotate` already follows.
func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	rooms := fs.String("rooms", "", "comma-separated room names, e.g. \"Kitchen,Office,Kids Room\" (skips the interview)")
	force := fs.Bool("force", false, "overwrite existing config, backing it up first")
	dryRun := fs.Bool("dry-run", false, "print what would be written and exit")
	envPath := fs.String("env", ".env", "path to write .env")
	policyPath := fs.String("policy", "", "path to write policy.toml (default $POLICY_PATH or ./policy.toml)")
	handsetsPath := fs.String("handsets", "", "path to write handsets.toml (default $HANDSETS_PATH or ./handsets.toml)")
	examplesDir := fs.String("examples", "examples", "directory holding .env.example")
	_ = fs.Parse(args)

	paths := setup.Paths{
		Env:      *envPath,
		Policy:   policyPathArg(*policyPath),
		Handsets: handsetsPathArg(*handsetsPath),
	}

	// Refuse to clobber before asking anything: nobody wants to answer six
	// questions and then be told it was pointless.
	if !*force && !*dryRun {
		var existing []string
		for _, p := range []string{paths.Env, paths.Policy, paths.Handsets} {
			if _, err := os.Stat(p); err == nil {
				existing = append(existing, p)
			}
		}
		if len(existing) > 0 {
			fmt.Fprintf(os.Stderr, "✗ these already exist:\n")
			for _, p := range existing {
				fmt.Fprintf(os.Stderr, "    %s\n", p)
			}
			fmt.Fprintf(os.Stderr, "\nRe-run with --force to replace them (each is backed up first),\n")
			fmt.Fprintf(os.Stderr, "or --dry-run to see what init would write.\n")
			return 1
		}
	}

	roomList, err := gatherRooms(*rooms)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}

	plan, err := setup.BuildPlan(roomList, paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}

	// Validate before writing anything. A configuration this tool generates
	// must pass the same validator that guards the daemon — if it does not,
	// that is a bug here and the operator should not inherit it.
	if problems := policy.LintSplit([]byte(plan.PolicyTOML()), []byte(plan.HandsetsTOML())); len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "✗ internal error: generated config does not validate\n")
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "    %s\n", p)
		}
		return 1
	}

	envBase, err := os.ReadFile(*examplesDir + "/.env.example")
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ cannot read %s/.env.example: %v\n", *examplesDir, err)
		fmt.Fprintf(os.Stderr, "  Run init from the repository root, or pass --examples.\n")
		return 1
	}
	envOut := plan.EnvFile(string(envBase))

	if *dryRun {
		fmt.Printf("Would write:\n  %s\n  %s\n  %s\n\n",
			paths.Handsets, paths.Policy, paths.Env)
		fmt.Printf("Handsets: ")
		for i, h := range plan.Handsets {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Printf("%s (%d)", h.ID, h.Number)
		}
		fmt.Printf("\n\nSecrets are generated fresh on every run; nothing was written.\n")
		return 0
	}

	for _, w := range []struct {
		path, content string
	}{
		{paths.Handsets, plan.HandsetsTOML()},
		{paths.Policy, plan.PolicyTOML()},
		{paths.Env, envOut},
	} {
		if bak, err := setup.Backup(w.path); err != nil {
			fmt.Fprintf(os.Stderr, "✗ backing up %s: %v\n", w.path, err)
			return 1
		} else if bak != "" {
			fmt.Printf("  backed up %s\n", bak)
		}
		if err := setup.WriteFile(w.path, w.content); err != nil {
			fmt.Fprintf(os.Stderr, "✗ writing %s: %v\n", w.path, err)
			return 1
		}
		fmt.Printf("  wrote %s\n", w.path)
	}

	printSecrets(plan)
	return 0
}

// gatherRooms takes the --rooms flag, or interviews the operator. Plain
// prompts, no TUI: this runs over SSH on a Pi.
func gatherRooms(flagValue string) ([]string, error) {
	if strings.TrimSpace(flagValue) != "" {
		return splitRooms(flagValue), nil
	}

	stat, err := os.Stdin.Stat()
	if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
		return nil, fmt.Errorf("stdin is not a terminal — pass --rooms \"Kitchen,Office\" for a non-interactive run")
	}

	fmt.Println("Setting up Call Me Maybe.")
	fmt.Println()
	fmt.Println("Which rooms get a phone? One per line, blank line when done.")
	fmt.Println("Names become handset ids, so \"Kids Room\" becomes kids-room.")
	fmt.Println()

	var rooms []string
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Printf("  room %d> ", len(rooms)+1)
		if !in.Scan() {
			break
		}
		line := strings.TrimSpace(in.Text())
		if line == "" {
			break
		}
		rooms = append(rooms, line)
	}
	if len(rooms) == 0 {
		return nil, fmt.Errorf("no rooms given")
	}
	fmt.Println()
	return rooms, nil
}

func splitRooms(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// printSecrets writes the generated PINs to stdout, once. This is the only
// place they are ever shown: they are not logged, and policy.toml is 0600.
func printSecrets(plan *setup.Plan) {
	fmt.Println()
	fmt.Println("─────────────────────────────────────────────────────────────")
	fmt.Println(" Extension PINs — shown once. Write them down now.")
	fmt.Println("─────────────────────────────────────────────────────────────")
	fmt.Printf("  %-22s %s\n", "Whole house", plan.HousePIN)
	for _, h := range plan.Handsets {
		fmt.Printf("  %-22s %s\n", h.Label, plan.ExtensionPINs[h.ID])
	}
	if pin, ok := plan.VoicemailPINs[plan.Mailbox]; ok {
		fmt.Println()
		fmt.Printf("  %-22s %s   (set this in voicemail.conf)\n", "Voicemail "+plan.Mailbox, pin)
	}
	fmt.Println("─────────────────────────────────────────────────────────────")
	fmt.Println()
	fmt.Println("These are credentials on the public phone network. Do not put them")
	fmt.Println("anywhere public. Rotate any that leak: doorman rotate \"<label>\"")
	fmt.Println()
	fmt.Println("Next:")
	fmt.Println("  doorman check                 confirm it all resolves")
	fmt.Println("  doorman render                generate the Asterisk handset config")
	fmt.Println("  docs/RUNBOOK.md               the rest of provisioning")
}
