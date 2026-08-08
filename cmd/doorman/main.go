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
                                a bad edit must never take the phone down
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
  doorman render [flags]        generate per-handset Asterisk config from handsets.toml
      -handsets path            inventory file (default $HANDSETS_PATH or ./handsets.toml)
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
	p, err := policy.LoadSplitWith(path, handsetsPath, policy.Options{
		AllowPlaceholders: *allowPlaceholders,
		StrictUnknownKeys: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ configuration is not valid\n\n%v\n", err)
		return 1
	}
	if !policy.SplitLayout(handsetsPath) {
		fmt.Println("note: single-file layout (no handsets.toml). Still supported —")
		fmt.Println("      but splitting the hardware inventory out lets `doorman render`")
		fmt.Println("      generate the Asterisk side. See RUNBOOK: Config interfaces.")
		fmt.Println()
	}

	pinLen := "mixed (inter-digit timeout decides)"
	if p.PinLength > 0 {
		pinLen = fmt.Sprintf("%d", p.PinLength)
	}
	fmt.Printf("✓ %s is valid\n\n", path)
	if *allowPlaceholders {
		fmt.Println("  note: placeholder PINs were accepted. This config cannot answer a")
		fmt.Println("        call until `doorman init` replaces them.")
		fmt.Println()
	}
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
	return 0
}

const (
	noMailbox    = "(none — an unanswered caller is dismissed)"
	noAfterhours = "(none — this line rings at any hour)"
)

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

	frags, err := render.Build(handsets, env)
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

	// Unknown keys warn here and refuse in `doorman check`. The daemon must
	// keep answering the phone (invariant 4), so a key it does not recognise
	// is something to say loudly and then ignore — which is what the decoder
	// was already doing, minus the saying. Only names are logged; a key's
	// value never is.
	loadOpts := policy.Options{
		OnUnknownKey: func(u policy.UnknownKey) {
			log.Warn("unrecognised key in config, ignoring it",
				"file", u.File, "key", u.Path, "didYouMean", u.Suggest,
				"hint", "run `doorman check` for the full report")
		},
	}

	store, err := policy.OpenStoreWith(cfg.PolicyPath, cfg.HandsetsPath, cfg.PolicyWatch, loadOpts,
		func(p *policy.Policy) {
			log.Info("policy reloaded",
				"allowList", p.AllowListCount(), "extensions", p.ExtensionCount())
		},
		func(err error) {
			log.Error("policy reload failed, keeping previous version", "err", err)
		},
	)
	if err != nil {
		log.Error("cannot load policy", "path", cfg.PolicyPath, "err", err)
		os.Exit(1)
	}
	defer store.Close()

	pol := store.Current()
	log.Info("policy loaded", "path", cfg.PolicyPath,
		"allowList", pol.AllowListCount(), "extensions", pol.ExtensionCount(),
		"pinLength", pol.PinLength)

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

	limiter := lobby.NewRateLimiter(cfg.RateLimitEnabled, cfg.RateLimitMaxFailures, cfg.RateLimitWindow)
	go func() {
		for range time.Tick(5 * time.Minute) {
			limiter.Sweep(time.Now())
		}
	}()

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

	deps := lobby.Deps{
		ARI:     ariAdapter{client},
		Policy:  store.Current,
		Limiter: limiter,
		Prompts: prompts,
		Log:     log,
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

	client.Connect(func(ev ari.Event) { route(ev, reg, deps, client, log) })
	log.Info("doorman is on duty", "app", cfg.ARIApp, "version", version)

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
func route(ev ari.Event, reg *registry, deps lobby.Deps, client *ari.Client, log *slog.Logger) {
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
		}

	case "PlaybackFinished":
		if ev.Playback == nil {
			return
		}
		channelID := strings.TrimPrefix(ev.Playback.TargetURI, "channel:")
		if s := reg.caller(channelID); s != nil {
			s.PlaybackFinished(ev.Playback.ID)
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
		}
	}
}

// registry maps channel IDs — inbound callers and originated legs alike — to
// their owning session.
type registry struct {
	mu       sync.Mutex
	channels map[string]*lobby.Session
	callers  map[string]*lobby.Session
}

func newRegistry() *registry {
	return &registry{
		channels: make(map[string]*lobby.Session),
		callers:  make(map[string]*lobby.Session),
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
