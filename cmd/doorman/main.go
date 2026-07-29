// doorman is the Call Me Maybe daemon: it connects to Asterisk over ARI and
// decides who gets connected. It is also its own operations tool:
//
//	doorman                  run the service
//	doorman check [path]     validate policy.toml and print what it resolves to
//	doorman rotate [flags] [label ...]
//	                         rotate extension PINs (all, or by label)
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
	"callmemaybe/internal/config"
	"callmemaybe/internal/lobby"
	"callmemaybe/internal/lsp"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/render"
)

const version = "0.4.1"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
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

  doorman                       run the service (configuration via env, see .env.example)
  doorman check [path]          validate a policy file
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
	_ = fs.Parse(args)
	path := ""
	if fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	path = policyPathArg(path)
	handsetsPath := handsetsPathArg(*handsetsFlag)

	p, err := policy.LoadSplit(path, handsetsPath)
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
	fmt.Printf("  allow-listed numbers : %d\n", p.AllowListCount())
	fmt.Printf("  extensions           : %d\n", p.ExtensionCount())
	fmt.Printf("  pin length           : %s\n", pinLen)
	fmt.Printf("  house ring group     : %s\n", strings.Join(p.HouseEndpoints(), ", "))
	if mb := p.HousePlan().Mailbox; mb != "" {
		fmt.Printf("  house voicemail      : %s\n", mb)
	}
	now := time.Now()
	for _, e := range p.Extensions() {
		var notes []string
		if n := len(e.Plan.Steps); n > 1 {
			notes = append(notes, fmt.Sprintf("%d-stage ladder", n))
		}
		if e.Plan.Mailbox != "" {
			notes = append(notes, "voicemail:"+e.Plan.Mailbox)
		}
		if e.Afterhours != nil {
			state := "afterhours configured"
			if e.Afterhours.Active(now) {
				state = "afterhours ACTIVE NOW — this line goes straight to voicemail"
			}
			notes = append(notes, state)
		}
		if len(notes) > 0 {
			fmt.Printf("  %-20s : %s\n", e.Label, strings.Join(notes, ", "))
		}
	}
	return 0
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

	store, err := policy.OpenStore(cfg.PolicyPath, cfg.HandsetsPath, cfg.PolicyWatch,
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
		},
		OnLegCreated: reg.addLeg,
		OnFinished:   reg.remove,
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
		reg.addCaller(s)
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
