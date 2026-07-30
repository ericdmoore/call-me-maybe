package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/setup"
	"callmemaybe/internal/tmpl"
)

// runTemplate handles `doorman template <list|show|apply>`.
//
// A template is data: it declares questions and the structures it emits, and
// this fills in the answers and serialises ordinary policy TOML. What lands in
// policy.toml is plain configuration — editable, deletable, and readable
// without knowing a template produced it.
func runTemplate(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, templateUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return runTemplateList(args[1:])
	case "show":
		return runTemplateShow(args[1:])
	case "apply":
		return runTemplateApply(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown: doorman template %s\n\n%s", args[0], templateUsage)
		return 2
	}
}

const templateUsage = `doorman template — fill in a policy template

  doorman template list                  templates on this machine
  doorman template show <id>             its questions and what it emits
  doorman template apply <id> [flags]    answer the questions, print the TOML
      -apply                             append to policy.toml instead of printing
      -answers k=v,k=v                   skip the interview

Templates are searched in ./templates, then $XDG_CONFIG_HOME/doorman/templates
(or ~/.config/doorman/templates). A template may emit extensions and schedules
and nothing else — one that could write [[people]] could add its own author to
your allow-list.
`

// searchPaths is where templates live, nearest first.
func searchPaths() []string {
	var out []string
	out = append(out, "templates")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		out = append(out, filepath.Join(x, "doorman", "templates"))
	} else if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".config", "doorman", "templates"))
	}
	return out
}

type found struct {
	path string
	tpl  *tmpl.Template
	err  error
}

func discover() []found {
	var out []found
	seen := map[string]bool{}
	for _, dir := range searchPaths() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !(strings.HasSuffix(name, ".toml") || strings.HasSuffix(name, ".json")) {
				continue
			}
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				out = append(out, found{path: path, err: err})
				continue
			}
			t, err := tmpl.Parse(data)
			if err != nil {
				out = append(out, found{path: path, err: err})
				continue
			}
			if seen[t.Meta.ID] {
				continue // nearer path wins
			}
			seen[t.Meta.ID] = true
			out = append(out, found{path: path, tpl: t})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func byID(id string) (*tmpl.Template, error) {
	for _, f := range discover() {
		if f.tpl != nil && f.tpl.Meta.ID == id {
			return f.tpl, nil
		}
	}
	return nil, fmt.Errorf("no template %q — try `doorman template list`", id)
}

func runTemplateList(_ []string) int {
	all := discover()
	if len(all) == 0 {
		fmt.Println("No templates found. Looked in:")
		for _, d := range searchPaths() {
			fmt.Printf("  %s\n", d)
		}
		return 0
	}
	for _, f := range all {
		if f.err != nil {
			fmt.Printf("  %-18s %s\n", "(invalid)", f.path)
			fmt.Printf("      %s\n", strings.ReplaceAll(f.err.Error(), "\n", "\n      "))
			continue
		}
		fmt.Printf("  %-18s %s\n", f.tpl.Meta.ID, f.tpl.Meta.Name)
		if f.tpl.Meta.Description != "" {
			fmt.Printf("  %-18s %s\n", "", f.tpl.Meta.Description)
		}
	}
	return 0
}

func runTemplateShow(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: doorman template show <id>")
		return 2
	}
	t, err := byID(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	fmt.Printf("%s — %s (v%s)\n", t.Meta.ID, t.Meta.Name, t.Meta.Version)
	if t.Meta.Description != "" {
		fmt.Printf("\n%s\n", t.Meta.Description)
	}
	fmt.Printf("\nAsks:\n")
	for _, q := range t.Questions {
		fmt.Printf("  %-18s %-14s %s\n", q.ID, "("+q.Type+")", q.Prompt)
	}
	fmt.Printf("\nEmits: %d extension(s), %d schedule(s)\n",
		len(t.Emit.Extensions), len(t.Emit.Schedules))
	fmt.Printf("\nTemplates may emit extensions and schedules only — never [[people]].\n")
	return 0
}

func runTemplateApply(args []string) int {
	fs := flag.NewFlagSet("template apply", flag.ExitOnError)
	doApply := fs.Bool("apply", false, "append to policy.toml instead of printing")
	answerFlag := fs.String("answers", "", "k=v,k=v to skip the interview")
	policyFlag := fs.String("policy", "", "policy file (default $POLICY_PATH or ./policy.toml)")
	handsetsFlag := fs.String("handsets", "", "handsets file (default $HANDSETS_PATH or ./handsets.toml)")
	// The template id comes first, then flags. Go's flag package stops parsing
	// at the first positional argument, so `apply kids-line --answers=…` would
	// silently ignore every flag and drop into an interactive prompt. Pull the
	// id off the front and parse what remains.
	var id string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	_ = fs.Parse(args)
	if id == "" && fs.NArg() > 0 {
		id = fs.Arg(0)
	}
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: doorman template apply <id> [flags]")
		return 2
	}
	t, err := byID(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}

	polPath := policyPathArg(*policyFlag)
	hsPath := handsetsPathArg(*handsetsFlag)

	// The current policy supplies what the template cannot know: which
	// handsets exist, and which schedule ids and PINs are already taken.
	pol, err := policy.LoadSplitWith(polPath, hsPath, policy.Options{AllowPlaceholders: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ cannot read the current configuration: %v\n", err)
		fmt.Fprintf(os.Stderr, "  A template extends an existing policy — run `doorman init` first.\n")
		return 1
	}

	opts := tmpl.Options{
		HandsetLabel:     pol.HandsetLabel,
		TakenScheduleIDs: pol.ScheduleIDs(),
		TakenPINs:        pol.PINs(),
	}

	answers, err := gatherAnswers(t, *answerFlag, pol)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}

	out, err := t.Render(answers, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}

	// Validate the combined result before it can reach anyone.
	current, err := os.ReadFile(polPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ reading %s: %v\n", polPath, err)
		return 1
	}
	handsetsData, _ := os.ReadFile(hsPath)
	combined := string(current) + "\n" + out
	if problems := policy.LintSplitWith([]byte(combined), handsetsData,
		policy.Options{AllowPlaceholders: true}); len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "✗ the result would not be a valid policy:\n")
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "    %s\n", p)
		}
		return 1
	}

	if !*doApply {
		fmt.Print(out)
		fmt.Fprintf(os.Stderr, "\n# Printed, not applied. Re-run with --apply to append to %s.\n", polPath)
		return 0
	}

	if bak, err := setup.Backup(polPath); err != nil {
		fmt.Fprintf(os.Stderr, "✗ backing up %s: %v\n", polPath, err)
		return 1
	} else if bak != "" {
		fmt.Printf("  backed up %s\n", bak)
	}
	if err := setup.WriteFile(polPath, combined); err != nil {
		fmt.Fprintf(os.Stderr, "✗ writing %s: %v\n", polPath, err)
		return 1
	}
	fmt.Printf("  appended to %s\n", polPath)

	pins := t.PINs(out)
	if len(pins) > 0 {
		fmt.Println()
		fmt.Println("─────────────────────────────────────────────────────────────")
		fmt.Println(" New extension PINs — shown once.")
		fmt.Println("─────────────────────────────────────────────────────────────")
		labels := make([]string, 0, len(pins))
		for l := range pins {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		for _, l := range labels {
			fmt.Printf("  %-22s %s\n", l, pins[l])
		}
		fmt.Println("─────────────────────────────────────────────────────────────")
	}
	return 0
}

// gatherAnswers uses --answers, or asks. Plain prompts: this runs over SSH.
func gatherAnswers(t *tmpl.Template, flagValue string, pol *policy.Policy) (tmpl.Answers, error) {
	preset := map[string]string{}
	for _, kv := range strings.Split(flagValue, ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
			preset[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	interactive := len(preset) == 0
	if interactive {
		stat, err := os.Stdin.Stat()
		if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
			return nil, fmt.Errorf("stdin is not a terminal — pass --answers for a non-interactive run")
		}
		fmt.Printf("%s — %s\n\n", t.Meta.Name, t.Meta.Description)
	}

	in := bufio.NewScanner(os.Stdin)
	ask := func(q tmpl.Question, hint string) string {
		if v, ok := preset[q.ID]; ok {
			return v
		}
		def := ""
		if s, ok := q.Default.(string); ok && s != "" {
			def = s
		}
		for {
			if def != "" {
				fmt.Printf("  %s [%s]\n  > ", q.Prompt, def)
			} else {
				fmt.Printf("  %s%s\n  > ", q.Prompt, hint)
			}
			if !in.Scan() {
				return def
			}
			line := strings.TrimSpace(in.Text())
			if line == "" && def != "" {
				return def
			}
			if line != "" {
				return line
			}
		}
	}

	answers := tmpl.Answers{}
	for _, q := range t.Questions {
		switch q.Type {
		case tmpl.TypeHandset:
			hint := ""
			if ids := pol.HandsetIDs(); len(ids) > 0 && preset[q.ID] == "" {
				hint = "\n    (" + strings.Join(ids, ", ") + ")"
			}
			answers[q.ID] = ask(q, hint)

		case tmpl.TypeHandsetGroup:
			hint := ""
			if ids := pol.RingTargetIDs(); len(ids) > 0 && preset[q.ID] == "" {
				hint = "\n    (" + strings.Join(ids, ", ") + ")"
			}
			raw := ask(q, hint)
			var list []string
			for _, part := range strings.Split(raw, " ") {
				for _, p := range strings.Split(part, ",") {
					if p = strings.TrimSpace(p); p != "" {
						list = append(list, p)
					}
				}
			}
			answers[q.ID] = list

		case tmpl.TypeMailbox, tmpl.TypeText:
			answers[q.ID] = ask(q, "")

		case tmpl.TypeYesNo:
			raw := strings.ToLower(ask(q, " (y/n)"))
			answers[q.ID] = raw == "y" || raw == "yes" || raw == "true"

		case tmpl.TypeTimeWindow:
			w := tmpl.Window{}
			if d, ok := q.Default.(map[string]any); ok {
				w.Start, _ = d["start"].(string)
				w.End, _ = d["end"].(string)
				if days, ok := d["days"].([]any); ok {
					for _, x := range days {
						if s, ok := x.(string); ok {
							w.Days = append(w.Days, s)
						}
					}
				}
			}
			if v, ok := preset[q.ID]; ok {
				// start-end[:DAYS] — 20:30-07:00:SU|MO|TU
				main, days, _ := strings.Cut(v, ":D")
				if s, e, ok := strings.Cut(main, "-"); ok {
					w.Start, w.End = s, e
				}
				if days != "" {
					w.Days = strings.Split(days, "|")
				}
			} else {
				fmt.Printf("  %s [%s to %s, %s]\n  > (enter to accept) ",
					q.Prompt, w.Start, w.End, strings.Join(w.Days, " "))
				if in.Scan() {
					if line := strings.TrimSpace(in.Text()); line != "" {
						if s, e, ok := strings.Cut(line, "-"); ok {
							w.Start, w.End = strings.TrimSpace(s), strings.TrimSpace(e)
						}
					}
				}
			}
			answers[q.ID] = w
		}
	}
	if interactive {
		fmt.Println()
	}
	return answers, nil
}
