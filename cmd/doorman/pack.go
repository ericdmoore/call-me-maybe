package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"callmemaybe/internal/pack"
	"callmemaybe/internal/voice"
)

var packUsage = `doorman pack — build, install and select local packs

Usage:
  doorman pack check  <dir>            validate a pack without rendering
  doorman pack build  <dir> [flags]    render it to audio
  doorman pack voices --backend <name> list the voices a backend offers
  doorman pack lint <file.cmmpack>      validate without rendering
  doorman pack install <file.cmmpack>   stage, render missing clips and install
  doorman pack list                    installed versions and latest attempts
  doorman pack use <id>                select a complete voice and restart doorman

Install/list/use flags (after the file or id):
  --sounds-dir <dir>   Asterisk sounds root (default /var/lib/asterisk/sounds)
  --env <file>         operator environment (default ./.env)
  --renderer-revision <id>  cache revision (default this doorman version)
  --dry-run           install plan and estimate, without rendering or installing
  --json              list as JSON (includes the configured active prefix)
  --no-restart        use: configure only; restart doorman yourself to activate

Build flags:
  -o <dir>        output directory (default <dir>/build)
  --dry-run       report what would be rendered and what it would cost
  --backend <n>   override the backend named in the pack (directory output only)
  --archive <f>   also write a .cmmpack
  --source-only   package scripts and declared recordings; requires --archive

Check flag:
  --hashes        print clip source hashes for binding recorded audio to scripts

Backends: ` + backendList + `

Rendering is content-addressed, so editing one line re-renders one clip. Run
build twice and the second is free.

Nothing here runs on a call. Audio may render on the box during installation;
only complete installed audio can be activated. Local packs need no account.
`

var backendList = strings.Join(voice.Backends(), ", ")

func runPack(args []string) int {
	if len(args) == 0 {
		fmt.Print(packUsage)
		return 2
	}
	switch args[0] {
	case "lint", "install", "list", "use":
		return runPackLocal(args)
	case "check":
		return runPackCheck(args[1:])
	case "build":
		return runPackBuild(args[1:])
	case "voices":
		return runPackVoices(args[1:])
	case "help", "-h", "--help":
		fmt.Print(packUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "doorman pack: unknown command %q\n\n%s", args[0], packUsage)
		return 2
	}
}

// voiceConfig builds the backend config from the environment. Credentials are
// never taken from a pack: a pack is a file you downloaded.
func voiceConfig(backend string) voice.Config {
	return packVoiceConfig(".env", backend)
}

func packVoiceConfig(envPath, backend string) voice.Config {
	lookup := secretLookup(envPath)
	env := func(key string) string { v, _ := lookup(key); return v }
	cfg := voice.Config{Region: env("AWS_REGION"), Env: env}
	switch backend {
	case "piper":
		cfg.Command = env("PIPER_CMD")
	case "exec":
		cfg.Command = env("VOICE_EXEC_CMD")
		cfg.Args = strings.Fields(env("VOICE_EXEC_ARGS"))
	}
	return cfg
}

func packDir(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return ".", args
}

func runPackCheck(args []string) int {
	dir, rest := packDir(args)
	fs := flag.NewFlagSet("pack check", flag.ContinueOnError)
	hashes := fs.Bool("hashes", false, "print clip source hashes")
	if err := fs.Parse(rest); err != nil || fs.NArg() != 0 {
		return 2
	}

	p, err := pack.Load(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}

	fmt.Printf("%s — %s v%s (%s)\n", p.Manifest.ID, p.Manifest.Name, p.Manifest.Version, p.Manifest.Kind)
	if p.Manifest.License == "" {
		fmt.Fprintln(os.Stderr, "\n✗ no licence in pack.json — see LICENSES.md")
		return 1
	}
	if *hashes {
		for _, item := range p.Items {
			if item.Text != "" {
				fmt.Printf("%s  %s\n", item.SourceHash(), item.Name)
			}
		}
		return 0
	}

	if p.Story != nil {
		_, warnings := p.Story.Validate()
		fmt.Printf("  %d nodes, %d clips to render\n", len(p.Story.Nodes), len(p.Items))
		for _, w := range warnings {
			fmt.Printf("  ! %s\n", w)
		}
	} else {
		fmt.Printf("  %d prompts\n", len(p.Items))
	}

	for _, b := range p.Backends() {
		if _, err := voice.New(b, voiceConfig(b)); errors.Is(err, voice.ErrNoCredentials) {
			fmt.Printf("  ! %s: %v\n", b, err)
		} else if err != nil {
			fmt.Printf("  ! %s: %v\n", b, err)
		} else {
			fmt.Printf("  ✓ %s ready\n", b)
		}
	}

	fmt.Println("\n✓ pack is valid")
	return 0
}

func runPackBuild(args []string) int {
	dir, rest := packDir(args)

	fs := flag.NewFlagSet("pack build", flag.ExitOnError)
	fs.Usage = func() { fmt.Print(packUsage) }
	out := fs.String("o", "", "output directory")
	dryRun := fs.Bool("dry-run", false, "report cost without rendering")
	override := fs.String("backend", "", "override the pack's backend")
	archive := fs.String("archive", "", "write a .cmmpack archive")
	sourceOnly := fs.Bool("source-only", false, "package source and declared recordings without synthesis (requires --archive)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	_ = loadDotEnv(".env")

	p, err := pack.Load(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	if (*sourceOnly && *archive == "") || (*archive != "" && *override != "") {
		fmt.Fprintln(os.Stderr, "✗ --source-only requires --archive; --archive uses the manifest voice (edit it instead of --backend)")
		return 2
	}
	if *sourceOnly {
		if *dryRun {
			fmt.Println("Source-only archive: no audio will be rendered.")
			return 0
		}
		if err := p.WriteArchive(*archive, ""); err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return 1
		}
		fmt.Printf("✓ packaged source into %s\n", *archive)
		return 0
	}
	if *override != "" {
		for i := range p.Items {
			p.Items[i].Voice.Backend = *override
		}
	}
	if *out == "" {
		*out = filepath.Join(dir, "build")
	}

	// Always show the cost before spending it, whether or not this is a dry
	// run. A build that quietly bills is a build people stop trusting.
	fmt.Printf("%s — %d clips\n", p.Manifest.Name, len(p.Items))
	for _, b := range p.Backends() {
		c := p.Estimate(voiceConfig)[b]
		switch {
		case c.Free:
			fmt.Printf("  %-12s %6d characters   free — %s\n", b, c.Characters, c.Note)
		default:
			fmt.Printf("  %-12s %6d characters in %d requests\n", b, c.Characters, c.Requests)
			if c.Note != "" {
				fmt.Printf("  %-12s %s\n", "", c.Note)
			}
		}
	}

	if *dryRun {
		fmt.Println("\n(dry run — nothing rendered, nothing spent)")
		return 0
	}

	// Check every backend before rendering anything, so a story using three
	// vendors fails on the missing key rather than half way through.
	for _, b := range p.Backends() {
		if _, err := voice.New(b, voiceConfig(b)); err != nil {
			fmt.Fprintf(os.Stderr, "\n✗ %v\n", err)
			return 1
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Println()
	rep, err := p.Build(ctx, *out, func(b string) (voice.Renderer, error) {
		return voice.New(b, voiceConfig(b))
	})
	for _, n := range rep.Rendered {
		fmt.Printf("  → %s\n", n)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ %v\n", err)
		return 1
	}
	if len(rep.Cached) > 0 {
		fmt.Printf("  %d unchanged\n", len(rep.Cached))
	}

	if *archive != "" {
		if err := p.WriteArchive(*archive, rep.OutDir); err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return 1
		}
		fmt.Printf("\n✓ packaged into %s\nInstall: doorman pack install %s\n", *archive, *archive)
		return 0
	}
	fmt.Printf("\n✓ built into %s\n", rep.OutDir)
	fmt.Printf("\nInstall on the Pi:\n"+
		"  sudo mkdir -p /var/lib/asterisk/sounds/%s\n"+
		"  rsync -av %s/ pi@raspberrypi:/tmp/pack/\n"+
		"  sudo cp -r /tmp/pack/* /var/lib/asterisk/sounds/%s/\n"+
		"  sudo chown -R asterisk:asterisk /var/lib/asterisk/sounds/%s\n",
		p.Manifest.ID, rep.OutDir, p.Manifest.ID, p.Manifest.ID)
	if p.Story == nil {
		fmt.Printf("\nThen set PROMPT_MEDIA_PREFIX=%s and restart.\n", p.Manifest.ID)
	}
	return 0
}

func runPackVoices(args []string) int {
	fs := flag.NewFlagSet("pack voices", flag.ExitOnError)
	fs.Usage = func() { fmt.Print(packUsage) }
	backend := fs.String("backend", "piper", "which backend to ask")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = loadDotEnv(".env")

	r, err := voice.New(*backend, voiceConfig(*backend))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	l, ok := r.(voice.Lister)
	if !ok {
		fmt.Fprintf(os.Stderr, "✗ %s cannot list its voices\n", *backend)
		return 1
	}

	vs, err := l.Voices(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].ID < vs[j].ID })
	for _, v := range vs {
		fmt.Printf("%-28s %-18s %s %s\n", v.ID, v.Name, v.Language, v.Note)
	}
	return 0
}
