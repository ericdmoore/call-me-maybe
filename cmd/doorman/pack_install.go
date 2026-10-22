package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"callmemaybe/internal/pack"
	"callmemaybe/internal/voice"
)

const defaultSoundsDir = "/var/lib/asterisk/sounds"

type packRunner interface {
	Run(context.Context, ...string) (string, error)
}
type packSystem struct{}

func (packSystem) Run(ctx context.Context, argv ...string) (string, error) {
	output, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	return string(output), err
}

type packCLI struct {
	out      io.Writer
	err      io.Writer
	exec     packRunner
	renderer func(string) (voice.Renderer, error)
	prepare  func()
}

func runPackLocal(args []string) int {
	c := packCLI{out: os.Stdout, err: os.Stderr, exec: packSystem{}, prepare: func() {
		// A slow offline render is preferable to stealing CPU from the phone.
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 10)
	}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return c.run(ctx, args)
}

func (c packCLI) run(ctx context.Context, args []string) int {
	command := args[0]
	rest := args[1:]
	target := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		target, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("pack "+command, flag.ContinueOnError)
	fs.SetOutput(c.err)
	sounds := fs.String("sounds-dir", defaultSoundsDir, "Asterisk sounds root")
	envFile := fs.String("env", ".env", "operator environment file")
	revision := fs.String("renderer-revision", "doorman-"+version, "render cache revision; change when local models or renderer settings change")
	dry := fs.Bool("dry-run", false, "plan without rendering or installing")
	jsonOut := fs.Bool("json", false, "list as JSON")
	noRestart := fs.Bool("no-restart", false, "configure only; restart the service yourself")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if target == "" && fs.NArg() == 1 {
		target = fs.Arg(0)
	} else if fs.NArg() != 0 {
		fmt.Fprintln(c.err, "unexpected arguments")
		return 2
	}
	if (command == "list" && target != "") || (command != "list" && target == "") {
		fmt.Fprintln(c.err, "provide one file/id for lint, install or use; list takes none")
		return 2
	}
	if (*dry && command != "install") || (*jsonOut && command != "list") || (*noRestart && command != "use") {
		fmt.Fprintln(c.err, "--dry-run is for install, --json for list, --no-restart for use")
		return 2
	}
	store := pack.Store{SoundsDir: *sounds}
	fail := func(err error) int { fmt.Fprintf(c.err, "✗ %v\n", err); return 1 }
	if command == "list" {
		entries, err := store.List()
		if err != nil {
			return fail(err)
		}
		prefix := configuredPack(*envFile)
		if *jsonOut {
			return encodePackList(c.out, prefix, entries)
		}
		fmt.Fprintf(c.out, "Default voice (configured): %s\n", prefix)
		if len(entries) == 0 {
			fmt.Fprintln(c.out, "No managed packs installed.")
		}
		for _, entry := range entries {
			fmt.Fprintf(c.out, "%s", entry.ID)
			if entry.Installed != nil {
				i := entry.Installed
				fmt.Fprintf(c.out, "  %s  version %q  installed", i.Kind, i.Version)
				if i.Prefix == prefix {
					fmt.Fprint(c.out, "  active selection")
				} else if strings.HasPrefix(prefix, "doorman-packs/"+i.ID+"/versions/") {
					fmt.Fprint(c.out, "  active selection uses an earlier version")
				}
			}
			if entry.Attempt != nil {
				p := entry.Attempt
				fmt.Fprintf(c.out, "  last attempt: %s (%d/%d)", p.Status, p.Completed, p.Total)
				if p.Problem != "" {
					fmt.Fprintf(c.out, "  %s", p.Problem)
				}
			}
			fmt.Fprintln(c.out)
		}
		return 0
	}
	if command == "use" {
		var reload func() error
		if !*noRestart {
			// Refuse to restart a service which reads a different environment file.
			// `use --no-restart` supports another supervisor or an offline setup.
			probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			output, err := c.exec.Run(probeCtx, "systemctl", "show", "doorman.service", "--property=EnvironmentFiles", "--value")
			if err != nil {
				return fail(errors.New("cannot inspect doorman.service; use --no-restart only for an offline/manual activation"))
			}
			absolute, err := filepath.Abs(*envFile)
			if err != nil {
				return fail(err)
			}
			if !strings.Contains(output, absolute+" (ignore_errors=") {
				return fail(errors.New("doorman.service does not read this --env file; choose its EnvironmentFile or use --no-restart"))
			}
			reload = func() error {
				reloadCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer stop()
				_, err := c.exec.Run(reloadCtx, "systemctl", "try-restart", "doorman.service")
				return err
			}
		}
		info, err := store.Use(target, *envFile, reload)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(c.out, "✓ selected %s (%s)\n", info.ID, info.Prefix)
		if *noRestart {
			fmt.Fprintln(c.out, "Configuration saved only. Restart doorman to activate; per-line prompts still override this default.")
		} else {
			fmt.Fprintln(c.out, "Running doorman service restarted; if stopped, this selection takes effect on its next start. Per-line prompts still override this default.")
		}
		return 0
	}
	artifact, err := pack.OpenArchive(target)
	if err != nil {
		return fail(err)
	}
	defer artifact.Close()
	fmt.Fprintf(c.out, "%s — %q v%q (%s), %d clips\n", artifact.Pack.Manifest.ID, artifact.Pack.Manifest.Name, artifact.Pack.Manifest.Version, artifact.Pack.Manifest.Kind, len(artifact.Pack.Items))
	for _, warning := range artifact.Warnings {
		fmt.Fprintf(c.out, "  ! %s\n", warning)
	}
	if command == "lint" {
		fmt.Fprintln(c.out, "✓ archive, source and bundled audio are valid (missing audio renders at install)")
		return 0
	}
	plan := store.Plan(artifact, *revision)
	fmt.Fprintf(c.out, "%d bundled, %d cached, %d to render\n", plan.Bundled, plan.Cached, len(plan.Render))
	toRender := *artifact.Pack
	toRender.Items = plan.Render
	estimates := toRender.Estimate(func(backend string) voice.Config { return packVoiceConfig(*envFile, backend) })
	backends := make([]string, 0, len(estimates))
	for b := range estimates {
		backends = append(backends, b)
	}
	sort.Strings(backends)
	for _, b := range backends {
		estimate := estimates[b]
		fmt.Fprintf(c.out, "  %s: %d characters in %d requests; %s\n", b, estimate.Characters, estimate.Requests, estimate.Note)
	}
	if *dry {
		fmt.Fprintln(c.out, "Dry run: nothing rendered or installed.")
		return 0
	}
	renderer := c.renderer
	if renderer == nil {
		renderer = func(backend string) (voice.Renderer, error) {
			return voice.New(backend, packVoiceConfig(*envFile, backend))
		}
	}
	if len(plan.Render) > 0 && c.prepare != nil {
		c.prepare()
	}
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	info, err := store.Install(ctx, artifact, pack.InstallOptions{Renderer: renderer, RendererRevision: *revision, Progress: func(p pack.Progress) { fmt.Fprintf(c.out, "  %s %d/%d %s\n", p.Status, p.Completed, p.Total, p.Clip) }})
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(c.out, "✓ installed %s at %s\n", info.ID, info.Prefix)
	if info.Kind == pack.KindVoice {
		fmt.Fprintf(c.out, "Activate separately: doorman pack use %s --sounds-dir %q --env %q\n", info.ID, *sounds, *envFile)
	} else {
		fmt.Fprintln(c.out, "Installed alongside voice packs. The story handset player is separate work (#69).")
	}
	return 0
}

func configuredPack(envPath string) string {
	value, ok := secretLookup(envPath)("PROMPT_MEDIA_PREFIX")
	if !ok {
		return "call-me-maybe"
	}
	return value
}

func encodePackList(out io.Writer, prefix string, entries []pack.Entry) int {
	if err := json.NewEncoder(out).Encode(struct {
		Active  string       `json:"configured_active_prefix"`
		Entries []pack.Entry `json:"packs"`
	}{prefix, entries}); err != nil {
		return 1
	}
	return 0
}
