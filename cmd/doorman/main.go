// doorman is the Call Me Maybe daemon: it connects to Asterisk over ARI and
// decides who gets connected. It is also its own operations tool:
//
//	doorman                  run the service
//	doorman init             interview and write a working configuration
//	doorman check [path]     validate policy.toml and print what it resolves to
//	doorman schema [name]    print the config surface as JSON Schema
//	doorman calls            read the call log
//	doorman pack             build and check prompt packs
//	doorman rotate [flags] [label ...]
//	                         rotate extension PINs (all, or by label)
//	doorman render [flags]   generate per-handset Asterisk config
//	doorman lsp              language server for policy.toml and handsets.toml
//	doorman e164 <number>    show how a raw caller ID normalises
//	doorman version
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"callmemaybe/internal/ari"
	"callmemaybe/internal/calls"
	"callmemaybe/internal/config"
	"callmemaybe/internal/lobby"
	"callmemaybe/internal/lsp"
	"callmemaybe/internal/notify"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/render"
	"callmemaybe/internal/schema"
)

// version is the release identity. A release build stamps it from the git tag
// with -ldflags "-X main.version=..."; this default is what a plain
// `go build` reports. It must stay a var — the linker cannot rewrite a const.
var version = "0.4.1"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			os.Exit(runInit(os.Args[2:]))
		case "template":
			os.Exit(runTemplate(os.Args[2:]))
		case "check":
			os.Exit(runCheck(os.Args[2:]))
		case "rotate":
			os.Exit(runRotate(os.Args[2:]))
		case "render":
			os.Exit(runRender(os.Args[2:]))
		case "lsp":
			os.Exit(runLsp())
		case "e164":
			os.Exit(runE164(os.Args[2:]))
		case "schema":
			os.Exit(runSchema(os.Args[2:]))
		case "calls":
			os.Exit(runCalls(os.Args[2:]))
		case "pack":
			os.Exit(runPack(os.Args[2:]))
		case "version", "-v", "--version":
			fmt.Println("doorman", version)
			return
		case "help", "-h", "--help":
			fmt.Print(usage)
			return
		}
	}
	runService()
}

const usage = `doorman — the Call Me Maybe lobby daemon

  doorman                       run the service (configuration via env, see examples/.env.example)
  doorman init [flags]          interview, generate every secret with crypto/rand,
                                and write .env, policy.toml, and handsets.toml.
                                The shipped examples carry placeholder PINs that
                                cannot work on purpose; this is what replaces them
      -rooms "A,B,C"            skip the interview
      -dry-run                  show what would be written
      -force                    replace existing config, backing it up first
  doorman check [flags] [path]  validate policy.toml and handsets.toml, and
                                report what they add up to — every extension
                                with every setting, including the defaults it
                                fell back to. A key the schema does not know
                                is an error here (with a "did you mean"); the
                                daemon only warns about the same key, because
                                a bad edit must never take the phone down.
                                On a box answering several numbers it lists
                                every line it found — policy.<line>.toml
                                beside policy.toml — and what each resolves to,
                                including what each presents on an outbound
                                call and which phones default to it
      -handsets path            inventory file (default $HANDSETS_PATH or ./handsets.toml)
      -allow-placeholders       accept the example sentinels; for CI, not operators
  doorman pack <cmd> <dir>      build and check prompt packs. "check" validates,
                                "build" renders audio through piper, ElevenLabs,
                                OpenAI or Polly, "voices" lists what a backend
                                offers. Rendering is content-addressed, so
                                editing one line re-renders one clip.
  doorman calls                 read the call log: who called, what happened,
                                and why. Caller IDs redacted unless
                                --no-redact. Needs CALL_LOG_PATH set.
  doorman schema [name]         print the configuration surface as JSON Schema:
                                every key, type, default, and cross-file
                                reference for policy.toml, handsets.toml, and
                                the environment. name is policy, handsets, or
                                env; all three when omitted. Written to be
                                read by tooling and by LLMs — see llms.txt
  doorman template <cmd>        list, show, and fill in policy templates:
                                a template declares questions and the structures
                                it emits, and this writes ordinary policy TOML
  doorman rotate [flags] [label ...]
                                rotate extension PINs; all extensions when no labels given
      -policy path              policy file (default $POLICY_PATH or ./policy.toml)
  doorman render [flags]        generate per-handset Asterisk config from
                                handsets.toml, including the outbound caller ID
                                each phone presents — read from every line's
                                [line] outbound_cid and outbound_handsets,
                                because a handset that picks up and dials never
                                reaches doorman at all
      -handsets path            inventory file (default $HANDSETS_PATH or ./handsets.toml)
      -policy path              policy file, for outbound caller ID (default $POLICY_PATH or ./policy.toml)
      -out dir                  output directory (default ./asterisk/generated)
      -env path                 secrets file for handset passwords (default ./.env)
  doorman e164 <number>         show how a raw caller ID normalises
  doorman lsp                   language server (stdio) for policy.toml and
                                handsets.toml — diagnostics from the same
                                validator that guards the daemon, plus
                                completions for handset/group/schedule ids
  doorman version

The three config interfaces: .env (secrets and tuning), handsets.toml (the
hardware — what exists), policy.toml (the rules — who gets in, what rings,
when). render makes handsets.toml authoritative over the Asterisk side.

https://callmemaybe.cc — Apache 2.0; bundled audio CC BY-SA 4.0 (LICENSES.md)
`

func policyPathArg(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p := os.Getenv("POLICY_PATH"); p != "" {
		return p
	}
	return "./policy.toml"
}

func handsetsPathArg(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p := os.Getenv("HANDSETS_PATH"); p != "" {
		return p
	}
	return "./handsets.toml"
}

// loadDotEnv parses KEY=VALUE lines for subcommands that need secrets and
// run outside systemd (render, mostly). Real environment wins over the file.
func loadDotEnv(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		out[strings.TrimSpace(key)] = value
	}
	return out
}

// ── doorman check ────────────────────────────────────────────────────────

func runCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	handsetsFlag := fs.String("handsets", "", "handsets file (default $HANDSETS_PATH or ./handsets.toml)")
	// Structural validation of the shipped examples, whose PINs are the
	// placeholder sentinel. CI uses this; an operator never should, which is
	// what makes a freshly copied config fail loudly until `doorman init` runs.
	allowPlaceholders := fs.Bool("allow-placeholders", false, "accept example placeholder PINs (for CI, not operators)")
	_ = fs.Parse(args)
	path := ""
	if fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	path = policyPathArg(path)
	handsetsPath := handsetsPathArg(*handsetsFlag)

	// StrictUnknownKeys is set here and nowhere else that loads a policy. An
	// operator is reading this output and a typo is exactly what they came to
	// find; the daemon, which cannot afford to refuse a file, only warns.
	opts := policy.Options{
		AllowPlaceholders: *allowPlaceholders,
		StrictUnknownKeys: true,
	}

	files, ignored := policy.DiscoverLines(path)
	results := make([]checkedLine, 0, len(files))
	for _, lf := range files {
		p, err := policy.LoadSplitWith(lf.Path, handsetsPath, opts)
		results = append(results, checkedLine{LineFile: lf, pol: p, err: err})
	}

	// The default line is what answers when nothing else does, so a broken one
	// is the whole phone rather than one number. Report it exactly as this has
	// always reported an invalid config, and stop.
	if results[0].err != nil {
		fmt.Fprintf(os.Stderr, "✗ configuration is not valid\n\n%v\n", results[0].err)
		return 1
	}
	if !policy.SplitLayout(handsetsPath) {
		fmt.Println("note: single-file layout (no handsets.toml). Still supported —")
		fmt.Println("      but splitting the hardware inventory out lets `doorman render`")
		fmt.Println("      generate the Asterisk side. See RUNBOOK: Config interfaces.")
		fmt.Println()
	}
	for _, ig := range ignored {
		fmt.Printf("note: ignoring %s — %s\n", ig.Path, ig.Reason)
		fmt.Println()
	}

	// One line is the overwhelmingly common install, and it should never have
	// to read the word "line" to check its config. The summary and the
	// per-line headers appear only once there is something to disambiguate.
	multi := len(results) > 1
	if multi {
		printLineSummary(results)
	}

	rc := 0
	for i, r := range results {
		if multi {
			if i > 0 {
				fmt.Println()
			}
			fmt.Printf("── line: %s %s\n\n", r.Name, strings.Repeat("─", max(3, 62-len(r.Name))))
		}
		if r.err != nil {
			fmt.Printf("✗ %s is not valid\n\n%v\n\n", r.Path, r.err)
			fmt.Printf("  Calls arriving on line %q are served by the %s line until this loads.\n",
				r.Name, policy.DefaultLine)
			rc = 1
			continue
		}
		describeLine(r.Path, r.pol, *allowPlaceholders)
	}
	if multi {
		printSharedPINs(results)
	}
	if !printOutbound(results) {
		rc = 1
	}
	return rc
}

// printOutbound reports what each line presents on an outbound call and which
// phones default to it. Returns false when the configuration is ambiguous.
//
// Quiet unless there is something to say: an install with one number and no
// outbound_cid gets nothing here, because it has nothing to choose between and
// should never have to read the word "line" to check its config.
func printOutbound(results []checkedLine) bool {
	ids := make([]lineIdentity, 0, len(results))
	var handsetIDs []string
	for _, r := range results {
		if r.err != nil {
			continue
		}
		ids = append(ids, lineIdentity{Name: r.Name, LineIdentity: r.pol.Line()})
		if r.Name == policy.DefaultLine {
			// One shared inventory, so the default line's view of it is every
			// line's view of it. Pseudo-handsets are left out: a caller ID is
			// carried by a PJSIP endpoint, and a Local channel into the
			// conference has nowhere to put one, so listing it here would
			// claim something untrue about it.
			for _, id := range r.pol.HandsetIDs() {
				if strings.HasPrefix(r.pol.HandsetEndpoint(id), "PJSIP/") {
					handsetIDs = append(handsetIDs, id)
				}
			}
		}
	}
	plan := newOutboundPlan(ids)
	if !plan.anyCID() && !plan.claims() {
		return true
	}

	fmt.Println()
	fmt.Print(plan.describe(handsetIDs))

	if len(plan.Conflicts) == 0 {
		return true
	}
	// An error rather than a note. A phone presents one number, so two lines
	// claiming it means somebody's customers see somebody else's number — and
	// which of them depends on nothing an operator can see. The daemon keeps
	// answering with the primary line's claim; this is where it gets fixed.
	fmt.Println("\n✗ a handset is claimed by more than one line")
	for _, c := range plan.Conflicts {
		fmt.Printf("    %s is in [line] outbound_handsets on both %s and %s\n",
			c.Handset, c.Winner, c.Loser)
	}
	fmt.Printf("\n  A phone presents one number. Remove it from one of them.\n"+
		"  Until then the daemon uses %s and `doorman render` refuses to generate.\n",
		plan.Conflicts[0].Winner)
	return false
}

// checkedLine is one line and whatever loading its policy produced. Both
// fields matter: a line that will not load is reported and the others are
// still described, because that is exactly what the daemon does with it.
type checkedLine struct {
	policy.LineFile
	pol *policy.Policy
	err error
}

// printLineSummary answers the question a multi-line operator actually has
// before any of the detail: which numbers does this box answer, which file
// governs each, and where does a call go when the dialplan says something
// unexpected.
func printLineSummary(results []checkedLine) {
	width := 0
	for _, r := range results {
		if len(r.Name) > width {
			width = len(r.Name)
		}
	}
	fmt.Printf("Lines: %d\n\n", len(results))
	for _, r := range results {
		state := "✓ valid"
		if r.err != nil {
			state = "✗ will not load"
		}
		suffix := ""
		if r.Name == policy.DefaultLine {
			suffix = "   (the default line)"
		}
		// The [line] identity, when there is one: on a box answering several
		// numbers, "which of these is the business one" is the question this
		// block exists to answer, and a filename only answers it if whoever
		// named the file was careful.
		if r.pol != nil {
			if ident := strings.TrimSpace(r.pol.Line().Label + "  " + r.pol.Line().Number); ident != "" {
				suffix += "   " + ident
			}
		}
		fmt.Printf("  %-*s  %-28s %s%s\n", width, r.Name, r.Path, state, suffix)
	}
	fmt.Println()
	fmt.Println("  The dialplan chooses: Stasis(${DOORMAN_APP},line,<name>). A call with no")
	fmt.Println("  line argument gets the default line, and so does a call naming a line")
	fmt.Println("  that is not listed here — doorman cannot read the dialplan, so it can")
	fmt.Println("  never drop a call over a name it does not recognise.")
	fmt.Println()
}

// printSharedPINs flags the same PIN on two lines. Separate namespaces are the
// design and this is not an error — but a person dialling six digits and
// reaching different places depending which number they rang is worth saying
// out loud. Invariant 1 holds here as everywhere: the labels are named, the
// digits never are.
func printSharedPINs(results []checkedLine) {
	type owner struct{ line, label string }
	seen := map[string]owner{}
	var clashes []string
	for _, r := range results {
		if r.err != nil {
			continue
		}
		for _, e := range r.pol.Extensions() {
			if e.PIN == policy.PlaceholderPIN {
				continue // every example shares this one; that is the sentinel, not a clash
			}
			if prev, ok := seen[e.PIN]; ok {
				clashes = append(clashes, fmt.Sprintf("  %s %q and %s %q share a PIN",
					prev.line, prev.label, r.Name, e.Label))
				continue
			}
			seen[e.PIN] = owner{r.Name, e.Label}
		}
	}
	if len(clashes) == 0 {
		return
	}
	fmt.Println("\n  Worth a look — the same PIN on more than one line:")
	for _, c := range clashes {
		fmt.Println(c)
	}
	fmt.Println("\n  Valid: each line is its own namespace, so both work. But whoever hands")
	fmt.Println("  the number out has to remember which line it belongs to.")
}

// describeLine prints what one policy file resolves to.
func describeLine(path string, p *policy.Policy, allowPlaceholders bool) {
	pinLen := "mixed (inter-digit timeout decides)"
	if p.PinLength > 0 {
		pinLen = fmt.Sprintf("%d", p.PinLength)
	}
	fmt.Printf("✓ %s is valid\n\n", path)
	if allowPlaceholders {
		fmt.Println("  note: placeholder PINs were accepted. This config cannot answer a")
		fmt.Println("        call until `doorman init` replaces them.")
		fmt.Println()
	}
	line := p.Line()
	fmt.Printf("  line label           : %s\n", orDefault(line.Label, noLineLabel))
	fmt.Printf("  line number          : %s\n", orDefault(line.Number, noLineNumber))
	fmt.Printf("  outbound caller id   : %s\n", orDefault(line.OutboundCID, noOutboundCID))
	fmt.Printf("  prompt pack          : %s\n", orDefault(line.Prompts, noLinePrompts))
	fmt.Printf("  no-input disposition : %s\n", describeNoInput(line, p.HousePlan().Mailbox))
	fmt.Printf("  allow-listed numbers : %d\n", p.AllowListCount())
	fmt.Printf("  extensions           : %d\n", p.ExtensionCount())
	fmt.Printf("  pin length           : %s\n", pinLen)
	fmt.Printf("  caller id format     : %s\n", withDefault(
		p.CallerIDFormat(), p.CallerIDFormat() == policy.DefaultCallerIDFormat))
	fmt.Printf("  house ring group     : %s\n", strings.Join(p.HouseEndpoints(), ", "))
	fmt.Printf("  house voicemail      : %s\n", orDefault(p.HousePlan().Mailbox, noMailbox))

	// Every extension, every setting — including the ones nobody wrote down.
	// A default rendered as "(none — ...)" is what catches the mistake the
	// validator structurally cannot: a key that was silently misspelled reads
	// here as a feature you configured and did not get.
	exts := p.Extensions()
	if len(exts) > 0 {
		fmt.Println("\n  Extensions, with the defaults they fell back to:")
		now := time.Now()
		for _, e := range exts {
			fmt.Printf("\n  %s\n", e.Label)
			fmt.Printf("      pin          %s\n", maskedPIN(e.PIN))
			fmt.Printf("      rings        %s\n", describePlan(e.Plan))
			fmt.Printf("      voicemail    %s\n", orDefault(e.Plan.Mailbox, noMailbox))
			fmt.Printf("      afterhours   %s\n", describeAfterhours(e, now))
			if e.AfterhoursID != "" {
				fmt.Printf("      during it    %s\n", describeAfterhoursPlan(e))
			}
		}
	}
}

const (
	noMailbox     = "(none — an unanswered caller is dismissed)"
	noAfterhours  = "(none — this line rings at any hour)"
	noLineLabel   = "(none — set [line] label to give this number a name)"
	noLineNumber  = "(none — set [line] number to write down which DID this is)"
	noLinePrompts = "(none — PROMPT_MEDIA_PREFIX from the environment)"
)

// describeNoInput spells out what a caller who dials nothing gets, and says so
// even when nobody configured it — the default here is the whole difference
// between a doorman and a concierge, and it is exactly the kind of thing an
// operator assumes they changed.
//
// ring-house earns a warning of its own. It is a legitimate choice and a
// deliberate one, but it means the allow-list stops being the thing that
// decides who reaches the house, and nobody should discover that from a phone
// call.
func describeNoInput(line policy.LineIdentity, houseMailbox string) string {
	switch line.OnNoInput {
	case policy.NoInputVoicemail:
		return fmt.Sprintf("%s — they land in the %q mailbox", line.OnNoInput, houseMailbox)
	case policy.NoInputRingHouse:
		return fmt.Sprintf("%s — the house rings for a caller who dialled nothing.\n"+
			"                         Anyone patient enough to say nothing gets through, so on this\n"+
			"                         line the allow-list is a shortcut past the lobby, not a gate.",
			line.OnNoInput)
	default:
		return fmt.Sprintf("%s   (default) — the parting prompt, then a hangup", policy.NoInputDismiss)
	}
}

func orDefault(value, whenEmpty string) string {
	if value == "" {
		return whenEmpty
	}
	return value
}

func withDefault(value string, isDefault bool) string {
	if isDefault {
		return value + "   (default)"
	}
	return value
}

// maskedPIN shows that a PIN exists and how long it is, never what it is —
// invariant 1. `doorman rotate` remains the only thing that prints one.
func maskedPIN(pin string) string {
	if pin == policy.PlaceholderPIN {
		return "(placeholder — run `doorman init` to generate a real one)"
	}
	return fmt.Sprintf("%s   (%d digits)", strings.Repeat("•", len(pin)), len(pin))
}

// describePlan spells out each stage and where its duration came from, so a
// stage running on the configured default is visibly doing that rather than
// looking like a stage with no timing at all.
func describePlan(plan policy.RingPlan) string {
	if len(plan.Steps) == 0 {
		return "(nothing — this extension rings no handsets)"
	}
	stages := make([]string, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		where := strings.Join(s.Endpoints, " + ")
		switch {
		case s.Timeout > 0:
			where += fmt.Sprintf(" for %s", s.Timeout)
		case s.Rings > 0:
			where += fmt.Sprintf(" for %d rings", s.Rings)
		default:
			where += " (default ring time)"
		}
		stages = append(stages, where)
	}
	return strings.Join(stages, ", then ")
}

func describeAfterhours(e policy.ResolvedExtension, now time.Time) string {
	if e.AfterhoursID == "" {
		return noAfterhours
	}
	if e.Afterhours == nil {
		// The schedule exists but carries enabled = false. Identical to "no
		// schedule" at call time, and completely different to whoever is
		// reading the file wondering why quiet hours stopped working.
		return fmt.Sprintf("%s — switched off (enabled = false), so this line rings at any hour", e.AfterhoursID)
	}
	state := ""
	if e.Afterhours.Active(now) {
		state = "  ← ACTIVE NOW"
	}
	return fmt.Sprintf("%s — %s%s", e.AfterhoursID, e.Afterhours.Describe(), state)
}

func describeAfterhoursPlan(e policy.ResolvedExtension) string {
	if len(e.AfterhoursPlan.Steps) == 0 {
		return "(no afterhours_ring — callers in the window go straight to " +
			orDefault(e.Plan.Mailbox, "a polite dismissal, there being no mailbox") + ")"
	}
	return describePlan(e.AfterhoursPlan)
}

// ── doorman rotate ───────────────────────────────────────────────────────

func runRotate(args []string) int {
	fs := flag.NewFlagSet("rotate", flag.ExitOnError)
	pathFlag := fs.String("policy", "", "policy file (default $POLICY_PATH or ./policy.toml)")
	handsetsFlag := fs.String("handsets", "", "handsets file (default $HANDSETS_PATH or ./handsets.toml)")
	_ = fs.Parse(args)
	path := policyPathArg(*pathFlag)
	labels := fs.Args()

	rot, err := policy.RotatePinsSplit(path, handsetsPathArg(*handsetsFlag), labels)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}

	// New PINs go to stdout for the operator's eyes — this is the one place
	// they may ever be printed. They are never logged.
	fmt.Printf("✓ rotated %d extension(s) in %s\n\n", len(rot.Changes), rot.Path)
	width := 0
	for _, c := range rot.Changes {
		if len(c.Label) > width {
			width = len(c.Label)
		}
	}
	for _, c := range rot.Changes {
		fmt.Printf("  %-*s  →  %s\n", width, c.Label, c.NewPIN)
	}
	fmt.Println("\nIf doorman is running with POLICY_WATCH=true (the default), the change")
	fmt.Println("is live within a second — no restart. The old PINs are dead now, so hand")
	fmt.Println("the new ones out before anyone needs them.")
	return 0
}

// ── doorman render ───────────────────────────────────────────────────────

func runRender(args []string) int {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	handsetsFlag := fs.String("handsets", "", "inventory file (default $HANDSETS_PATH or ./handsets.toml)")
	policyFlag := fs.String("policy", "", "policy file, for outbound caller ID (default $POLICY_PATH or ./policy.toml)")
	outFlag := fs.String("out", "./asterisk/generated", "output directory")
	envFlag := fs.String("env", "./.env", "secrets file for handset passwords")
	_ = fs.Parse(args)

	handsetsPath := handsetsPathArg(*handsetsFlag)
	handsets, _, err := policy.LoadHandsets(handsetsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}

	dotenv := loadDotEnv(*envFlag)
	env := func(key string) (string, bool) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v, true
		}
		v, ok := dotenv[key]
		return v, ok && v != ""
	}

	// Outbound identity is the one thing render needs from the *rules* rather
	// than the inventory: [line] outbound_cid says what a line presents, and
	// [line] outbound_handsets says which phones call as it. It has to be
	// baked into the endpoint here because a handset that picks up and dials
	// never reaches doorman — which is precisely why outbound calling survives
	// doorman being down.
	plan, err := renderOutbound(policyPathArg(*policyFlag), handsetsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	if len(plan.Conflicts) > 0 {
		// Refused rather than resolved: this file decides what every customer
		// sees, and a guess baked into it is invisible from this end.
		fmt.Fprintln(os.Stderr, "✗ a handset is claimed by more than one line")
		for _, c := range plan.Conflicts {
			fmt.Fprintf(os.Stderr, "  %s is in [line] outbound_handsets on both %s and %s\n",
				c.Handset, c.Winner, c.Loser)
		}
		fmt.Fprintln(os.Stderr, "\n  A phone presents one number. Remove it from one of them.")
		return 1
	}

	ids := make([]string, 0, len(handsets))
	for _, h := range handsets {
		ids = append(ids, h.ID)
	}
	frags, err := render.Build(handsets, env, plan.callerIDs(ids))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}

	if err := os.MkdirAll(*outFlag, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	pjsipPath := filepath.Join(*outFlag, "pjsip_handsets.conf")
	planPath := filepath.Join(*outFlag, "extensions_handsets.conf")
	// 0640: the PJSIP fragment contains real SIP passwords.
	if err := os.WriteFile(pjsipPath, []byte(frags.PJSIP), 0o640); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	if err := os.WriteFile(planPath, []byte(frags.Dialplan), 0o640); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}

	fmt.Printf("✓ rendered %d handset(s) from %s\n\n", frags.Generated, handsetsPath)
	fmt.Printf("  %s\n  %s\n\n", pjsipPath, planPath)
	fmt.Println("Install on the Pi:")
	fmt.Println("  sudo cp " + pjsipPath + " " + planPath + " /etc/asterisk/")
	fmt.Println("  sudo chown asterisk:asterisk /etc/asterisk/*_handsets.conf")
	fmt.Println("  sudo chmod 640 /etc/asterisk/*_handsets.conf")
	fmt.Println("  sudo asterisk -rx 'pjsip reload' && sudo asterisk -rx 'dialplan reload'")
	fmt.Println("\nThe PJSIP fragment contains real passwords — never commit it.")
	return 0
}

// ── doorman lsp ──────────────────────────────────────────────────────────

func runLsp() int {
	// LSP owns stdout — a single stray print corrupts the protocol stream,
	// so logging goes to stderr, always.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	server := lsp.NewServer(os.Stdin, os.Stdout, log)
	if err := server.Run(); err != nil {
		log.Error("lsp terminated", "err", err)
		return 1
	}
	return 0
}

// ── doorman e164 ─────────────────────────────────────────────────────────

func runE164(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: doorman e164 <number>")
		return 2
	}
	cc := os.Getenv("DEFAULT_COUNTRY_CODE")
	if cc == "" {
		cc = "1"
	}
	n := policy.NormaliseCallerID(args[0], cc)
	switch n.Kind {
	case policy.KindE164:
		fmt.Printf("%s  →  %s  (usable for the allow-list)\n", args[0], n.Value)
	case policy.KindAnonymous:
		fmt.Printf("%s  →  anonymous (always meets the bouncer)\n", args[0])
	case policy.KindUnparseable:
		fmt.Printf("%s  →  unparseable (never allow-listed, meets the bouncer)\n", args[0])
	}
	return 0
}

// ── doorman schema ───────────────────────────────────────────────────────

// runSchema publishes the configuration surface as JSON Schema. The audience
// is tooling and language models: policy.toml is the real interface to this
// system, and until this existed the only machine-readable knowledge of its
// shape was the validator itself — which will tell you whether a file is
// valid, but not what a valid file looks like.
func runSchema(args []string) int {
	fs := flag.NewFlagSet("schema", flag.ExitOnError)
	_ = fs.Parse(args)

	var (
		out []byte
		err error
	)
	switch fs.NArg() {
	case 0:
		out, err = schema.Marshal(schema.All(version))
	case 1:
		s, gerr := schema.Get(fs.Arg(0))
		if gerr != nil {
			fmt.Fprintf(os.Stderr, "%v\n", gerr)
			return 2
		}
		out, err = schema.Marshal(s)
	default:
		fmt.Fprintf(os.Stderr, "usage: doorman schema [%s]\n", strings.Join(schema.Names, "|"))
		return 2
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not render schema: %v\n", err)
		return 1
	}
	os.Stdout.Write(out)
	return 0
}

// ── the service ──────────────────────────────────────────────────────────

func runService() {
	cfg, err := config.Load()
	if err != nil {
		// A stack trace helps nobody here — the fix is always in .env.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "pretty" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	log := slog.New(handler)

	// The lines this box answers: the default line, whose file is POLICY_PATH
	// itself, plus one per sibling policy.<name>.toml. Discovered rather than
	// declared, because doorman cannot read the dialplan and so can never know
	// the authoritative set anyway. An install with one number finds one line
	// and behaves exactly as it always has.
	lineFiles, ignored := policy.DiscoverLines(cfg.PolicyPath)
	for _, ig := range ignored {
		log.Warn("ignoring a file that looks like a line policy but is not one",
			"path", ig.Path, "reason", ig.Reason)
	}

	opened, err := openLines(lineFiles, cfg.HandsetsPath, cfg.PolicyWatch, log)
	if err != nil {
		log.Error("cannot load policy", "path", cfg.PolicyPath, "err", err)
		os.Exit(1)
	}
	defer func() {
		for _, o := range opened {
			o.store.Close()
		}
	}()

	client := ari.New(ari.Options{
		BaseURL:      cfg.ARIBaseURL,
		Username:     cfg.ARIUsername,
		Password:     cfg.ARIPassword,
		App:          cfg.ARIApp,
		ReconnectMin: cfg.ReconnectMin,
		ReconnectMax: cfg.ReconnectMax,
		Log:          log,
	})

	// Fail fast on bad credentials rather than looping on a 401 forever.
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	astVersion, err := client.Ping(pingCtx)
	cancel()
	if err != nil {
		log.Error("cannot reach ARI — check ARI_* settings and asterisk/ari.conf",
			"baseUrl", cfg.ARIBaseURL, "err", err)
		os.Exit(1)
	}
	log.Info("connected to asterisk", "version", astVersion)

	prompts := lobby.NewPrompts(cfg.PromptMediaPrefix)
	reg := newRegistry()

	// The call log is off unless a path is configured. A failure to open it
	// is fatal at startup rather than silent: an operator who asked for a
	// call log and did not get one should find out now, not in a month when
	// they go looking for a call.
	var callLog *calls.Writer
	if cfg.CallLogPath != "" {
		var err error
		callLog, err = calls.Open(cfg.CallLogPath, cfg.CallLogMaxBytes)
		if err != nil {
			log.Error("could not open the call log", "err", err)
			os.Exit(1)
		}
		defer func() { _ = callLog.Close() }()
		log.Info("call log open", "path", cfg.CallLogPath)
	}

	// The event webhook, same shape and same reasoning: off unless a URL is
	// configured, and a URL that cannot work is fatal now rather than a warn
	// line after the first call nobody heard announced. Only the host is
	// logged — the URL is a credential.
	var hook *notify.Webhook
	if cfg.WebhookURL != "" {
		var err error
		hook, err = notify.Open(notify.Options{
			URL:              cfg.WebhookURL,
			Token:            cfg.WebhookToken,
			Timeout:          cfg.WebhookTimeout,
			SendFullCallerID: !cfg.WebhookRedactCallerID,
			Log:              log,
		})
		if err != nil {
			log.Error("could not start the event webhook", "err", err)
			os.Exit(1)
		}
		defer func() { _ = hook.Close() }()
		log.Info("event webhook enabled", "host", hook.Host(), "redacted", cfg.WebhookRedactCallerID)
	}

	// Everything a line does not own is shared: one ARI client, one prompt
	// pack, one registry, one call log, one webhook, one concurrency cap. What
	// a line owns is its policy and its rate-limit budget.
	lines := newLineSet()
	var limiters []*lobby.RateLimiter
	for _, o := range opened {
		limiter := lobby.NewRateLimiter(cfg.RateLimitEnabled, cfg.RateLimitMaxFailures, cfg.RateLimitWindow)
		limiters = append(limiters, limiter)

		deps := lobby.Deps{
			ARI:     ariAdapter{client},
			Policy:  o.store.Current,
			Limiter: limiter,
			Prompts: prompts,
			Log:     o.log,
			Cfg: lobby.Config{
				DefaultCountryCode: cfg.DefaultCountryCode,
				ExtensionLength:    cfg.ExtensionLength,
				FirstDigitTimeout:  cfg.FirstDigitTimeout,
				InterDigitTimeout:  cfg.InterDigitTimeout,
				RingTimeout:        cfg.RingTimeout,
				RingCycle:          6 * time.Second, // standard US cadence
				MaxPinAttempts:     cfg.MaxPinAttempts,
				RedactCallerID:     cfg.RedactCallerID,
				MaxConcurrentCalls: cfg.MaxConcurrentCalls,
			},
			OnLegCreated: reg.addLeg,
			OnFinished:   reg.remove,
		}
		// A typed nil in an interface is not nil, so these fields are only set
		// when there is a real sink — the state machine's nil checks depend on it.
		if callLog != nil {
			deps.Calls = callLog
		}
		if hook != nil {
			deps.Notify = hook
		}
		lines.add(o.name, deps)
	}

	go func() {
		for range time.Tick(5 * time.Minute) {
			now := time.Now()
			for _, limiter := range limiters {
				limiter.Sweep(now)
			}
		}
	}()

	announceOutbound(lines, log)

	client.Connect(func(ev ari.Event) { route(ev, reg, lines, client, log) })
	log.Info("doorman is on duty", "app", cfg.ARIApp, "version", version)
	if len(lines.byName) > 1 {
		log.Info("serving several lines", "lines", strings.Join(lines.names(), ", "),
			"default", policy.DefaultLine)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	// Live calls are left alone: bridged channels keep their audio path in
	// Asterisk and fall out of Stasis when the humans finish talking.
	log.Info("shutting down", "signal", s.String(), "activeCalls", reg.callerCount())
	client.Close()
}

// route dispatches ARI events to sessions. It runs on the websocket read
// goroutine, so everything here must hand off fast — sessions consume through
// buffered channels and never block this path.
//
// The Stasis application arguments are the dialplan talking to the router.
// There are three things it can say and they are all a first argument: "leg"
// (an originated handset leg answered, correlation id follows), "line" (this
// call arrived on the named line), and "console" (a handset dialled *4 and
// wants to choose which line to call as). Saying nothing is the fourth case
// and the one every existing install uses.
func route(ev ari.Event, reg *registry, lines *lineSet, client *ari.Client, log *slog.Logger) {
	switch ev.Type {
	case "StasisStart":
		if ev.Channel == nil {
			return
		}
		// An outbound ring-group leg entering Stasis means the handset
		// answered.
		if len(ev.Args) > 0 && ev.Args[0] == "leg" {
			if s := reg.byChannel(ev.Channel.ID); s != nil {
				s.LegAnswered(ev.Channel.ID)
				return
			}
			log.Warn("orphan leg answered, hanging up", "channel", ev.Channel.ID)
			go func(id string) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = client.Hangup(ctx, id)
			}(ev.Channel.ID)
			return
		}

		// A handset at the outbound console. Deliberately outside the
		// concurrent-call ceiling: that limit exists to stop a flood of
		// strangers ringing every phone in the house, and refusing the
		// household its own phone during one would be the wrong way round.
		if len(ev.Args) > 0 && ev.Args[0] == "console" {
			c := lobby.NewConsole(ev.Channel.ID, lobby.ConsoleDeps{
				ARI: ariAdapter{client},
				Log: log,
				// The timeouts, and nothing line-specific: the console belongs
				// to no line, which is the whole reason it exists.
				Cfg: lines.deflt.Cfg,
				// Called on the console's own goroutine, so reading every
				// line's current policy never happens on this one.
				Lines:      func() []lobby.ConsoleLine { return lines.outbound().consoleLines() },
				OnFinished: reg.removeConsole,
			})
			reg.addConsole(c)
			go c.Run()
			return
		}

		deps := lines.forCall(ev.Args, log)
		s := lobby.NewSession(ev.Channel.ID, ev.Channel.Caller.Number, deps)
		if !reg.admit(s, deps.Cfg.MaxConcurrentCalls) {
			// The rate limiter counts PIN failures per caller; it has nothing
			// to say about a flood from many different numbers. Without a
			// ceiling, every handset in the house rings until the spammer
			// stops. Refusing here costs the attacker the ring and costs the
			// household nothing.
			//
			// The session never started, so cancel the context it allocated
			// and hang up without originating a single handset leg.
			s.CallerGone()
			log.Warn("refused: at concurrent call limit",
				"limit", deps.Cfg.MaxConcurrentCalls, "active", reg.callerCount())
			go func(id string) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = client.Hangup(ctx, id)
			}(ev.Channel.ID)
			return
		}
		go s.Run()

	case "ChannelDtmfReceived":
		if ev.Channel == nil {
			return
		}
		if s := reg.caller(ev.Channel.ID); s != nil {
			s.Dtmf(ev.Digit)
			return
		}
		if c := reg.console(ev.Channel.ID); c != nil {
			c.Dtmf(ev.Digit)
		}

	case "PlaybackFinished":
		if ev.Playback == nil {
			return
		}
		channelID := strings.TrimPrefix(ev.Playback.TargetURI, "channel:")
		if s := reg.caller(channelID); s != nil {
			s.PlaybackFinished(ev.Playback.ID)
			return
		}
		if c := reg.console(channelID); c != nil {
			c.PlaybackFinished(ev.Playback.ID)
		}

	case "StasisEnd", "ChannelDestroyed":
		if ev.Channel == nil {
			return
		}
		// Both events fire for most hangups — sessions treat the inputs as
		// idempotent, so double delivery is harmless. The distinction that
		// matters: StasisEnd means the caller LEFT OUR APP, possibly alive
		// (a handset transferred them); ChannelDestroyed means dead. Only
		// the dead may be hung up by cleanup.
		if s := reg.byChannel(ev.Channel.ID); s != nil {
			switch {
			case s.ChannelID != ev.Channel.ID:
				s.LegGone(ev.Channel.ID)
			case ev.Type == "StasisEnd":
				s.CallerLeft()
			default:
				s.CallerGone()
			}
			return
		}
		// The same distinction decides the same thing for a console, and it
		// matters more here than anywhere: StasisEnd on a console channel is
		// the *successful* ending — the handset is in [outbound-console]
		// dialling a trunk — and treating it as a death would hang up every
		// outbound call at the moment it was placed.
		if c := reg.console(ev.Channel.ID); c != nil {
			if ev.Type == "StasisEnd" {
				c.CallerLeft()
			} else {
				c.CallerGone()
			}
		}
	}
}

// registry maps channel IDs — inbound callers and originated legs alike — to
// their owning session, plus the handsets currently at the outbound console.
//
// Consoles get their own map rather than an interface both types satisfy.
// They overlap in four methods and in nothing else: a console has no legs, no
// bridge, and no ring plan, and the one thing the router must never get wrong
// is which of the two it is talking to.
type registry struct {
	mu       sync.Mutex
	channels map[string]*lobby.Session
	callers  map[string]*lobby.Session
	consoles map[string]*lobby.Console
}

func newRegistry() *registry {
	return &registry{
		channels: make(map[string]*lobby.Session),
		callers:  make(map[string]*lobby.Session),
		consoles: make(map[string]*lobby.Console),
	}
}

func (r *registry) addCaller(s *lobby.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels[s.ChannelID] = s
	r.callers[s.ChannelID] = s
}

// admit registers a caller only if the house is below max concurrent calls.
// The count and the insert happen under one lock on purpose: checking
// callerCount() and then calling addCaller() is a race that lets a burst of
// simultaneous INVITEs all pass a capacity test none of them should have.
//
// max <= 0 means no limit.
func (r *registry) admit(s *lobby.Session, max int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if max > 0 && len(r.callers) >= max {
		return false
	}
	r.channels[s.ChannelID] = s
	r.callers[s.ChannelID] = s
	return true
}

func (r *registry) addLeg(legChannelID string, s *lobby.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels[legChannelID] = s
}

func (r *registry) byChannel(id string) *lobby.Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.channels[id]
}

func (r *registry) caller(id string) *lobby.Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callers[id]
}

func (r *registry) remove(s *lobby.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.callers, s.ChannelID)
	for id, owner := range r.channels {
		if owner == s {
			delete(r.channels, id)
		}
	}
}

func (r *registry) addConsole(c *lobby.Console) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.consoles[c.ChannelID] = c
}

func (r *registry) console(id string) *lobby.Console {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.consoles[id]
}

func (r *registry) removeConsole(c *lobby.Console) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.consoles, c.ChannelID)
}

func (r *registry) callerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.callers)
}

// ariAdapter satisfies lobby.ARI with the concrete client. lobby deliberately
// does not import the ari package — its OriginateParams is a mirror — so this
// one method translates between the two. Every other method matches by
// signature through embedding.
type ariAdapter struct{ *ari.Client }

var _ lobby.ARI = ariAdapter{}

func (a ariAdapter) Originate(ctx context.Context, p lobby.OriginateParams) (string, error) {
	return a.Client.Originate(ctx, ari.OriginateParams{
		Endpoint:   p.Endpoint,
		AppArgs:    p.AppArgs,
		CallerID:   p.CallerID,
		Timeout:    p.Timeout,
		Originator: p.Originator,
	})
}
