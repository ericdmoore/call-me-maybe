package main

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/host"
)

func TestDecideWantsReadsTheConfigAndSaysWhy(t *testing.T) {
	dir := t.TempDir()
	handsets := filepath.Join(dir, "handsets.toml")
	os.WriteFile(handsets, []byte("[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\nnumber = 101\npassword_env = \"HANDSET_KITCHEN_PASSWORD\"\nmac = \"00:0b:82:12:34:56\"\nmodel = \"grandstream-wp826\"\n"), 0o600)
	messages := filepath.Join(dir, "messages.toml")
	os.WriteFile(messages, []byte("[[words]]\nword = \"ping\"\nreply = \"pong\"\n"), 0o600)
	env := secretsFrom(map[string]string{"PROVISION_ADDRESS": "192.168.7.133", "INBOX_URL": "https://edge.example", "MAIL_HOOK": "/x", "MAIL_TO": "a@b"})
	w := decideWants(env, handsets, filepath.Join(dir, "trunks.toml"), messages)
	if !w.Directory || !w.Inbox || !w.Digest || w.Balance {
		t.Errorf("wants = %+v", w)
	}
	if !strings.Contains(w.Why["doorman-balance.timer"], "one provider") {
		t.Errorf("balance reason = %q", w.Why["doorman-balance.timer"])
	}

	// Nothing set: only the always-on units, and every reason says what is missing.
	none := decideWants(secretsFrom(map[string]string{}), handsets, filepath.Join(dir, "trunks.toml"), filepath.Join(dir, "nope.toml"))
	if none.Directory || none.Inbox || none.Digest || none.Balance {
		t.Errorf("nothing configured but wants = %+v", none)
	}
	for _, u := range []string{"doorman-directory.service", "doorman-inbox.service", "doorman-digest.timer", "doorman-balance.timer"} {
		if none.Why[u] == "" {
			t.Errorf("no reason for %s", u)
		}
	}
	// An address but no provisionable phone: the directory would serve nothing.
	half := decideWants(secretsFrom(map[string]string{"PROVISION_ADDRESS": "x"}), filepath.Join(dir, "missing.toml"), "", "")
	if half.Directory {
		t.Error("directory wanted with no provisionable handset")
	}
}

type recordingExec struct{ ran [][]string }

func (r *recordingExec) Run(_ context.Context, argv ...string) (string, error) {
	r.ran = append(r.ran, argv)
	if argv[0] == "systemctl" {
		return "disabled\n", os.ErrNotExist
	}
	return "", nil
}

func TestCheckServicesSectionNamesWhatIsMissing(t *testing.T) {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("no systemd on this host; the section is silent by design")
	}
	lay := host.Layout{UnitDir: t.TempDir(), StateDir: t.TempDir(), Spool: t.TempDir(), DoormanUser: "doorman", DoormanGroup: "doorman", AsteriskUser: "asterisk"}
	out := capture(t, func() {
		if printServices(context.Background(), &recordingExec{}, lay, host.Wants{Why: map[string]string{}}) {
			t.Error("nothing installed must fail the check")
		}
	})
	if !strings.Contains(out, "doorman.service") || !strings.Contains(out, "not installed") || !strings.Contains(out, "init services") {
		t.Errorf("output:\n%s", out)
	}
}

// The host package runs commands as root; nothing on a call path may
// reach it. Same fence as provider, inbox, stt.
func TestTheHostPackageIsReachableFromInitOnly(t *testing.T) {
	fset := token.NewFileSet()
	root := filepath.Join("..", "..", "internal")
	dirs, _ := os.ReadDir(root)
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "host" {
			continue
		}
		pkgs, err := parser.ParseDir(fset, filepath.Join(root, d.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, pkg := range pkgs {
			for path, f := range pkg.Files {
				for _, imp := range f.Imports {
					if strings.Contains(imp.Path.Value, "internal/host") {
						t.Errorf("%s imports internal/host", path)
					}
				}
			}
		}
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "init_services.go" || f == "main.go" {
			continue
		}
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "internal/host") {
			t.Errorf("%s names internal/host; only init_services.go (and check's one call in main.go) may", f)
		}
	}
}

// The dry run goes through the real command: reads the config, builds the
// plan against this host (no systemd here, so every unit is "install"),
// prints it, changes nothing, and exits 0 — then without --dry-run and
// without root, prints the sudo line and exits 2.
func TestInitServicesDryRunPrintsThePlanAndTouchesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root would apply the plan")
	}
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	os.WriteFile(env, []byte("INBOX_URL=https://edge.example\n"), 0o600)
	messages := filepath.Join(dir, "messages.toml")
	os.WriteFile(messages, []byte("[[words]]\nword = \"ping\"\nreply = \"pong\"\n"), 0o600)
	args := []string{"-env", env, "-handsets", filepath.Join(dir, "none.toml"), "-trunks", filepath.Join(dir, "none-trunks.toml"), "-messages", messages}
	out := capture(t, func() {
		if rc := runInitServices(append([]string{"-dry-run"}, args...)); rc != 0 {
			t.Errorf("dry run rc = %d", rc)
		}
	})
	for _, want := range []string{"Plan:", "doorman.service", "doorman-inbox.service", "INBOX_URL and", "daemon-reload"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sudo doorman init services") {
		t.Error("a dry run should not nag about root")
	}
	out = capture(t, func() {
		if rc := runInitServices(args); rc != 2 {
			t.Errorf("non-root apply rc = %d, want 2", rc)
		}
	})
	if !strings.Contains(out, "sudo doorman init services") {
		t.Errorf("non-root run should print the sudo line:\n%s", out)
	}
}

// The inventories are resolved as the units see them: from .env's own
// variables and beside .env, never from wherever sudo was typed.
func TestServicePathsComeFromTheEnvFileNotTheCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	elsewhere := filepath.Join(t.TempDir(), "words.toml")
	os.WriteFile(elsewhere, []byte("[[words]]\nword = \"ping\"\nreply = \"pong\"\n"), 0o600)
	os.WriteFile(env, []byte("INBOX_URL=https://edge.example\nMESSAGES_PATH="+elsewhere+"\n"), 0o600)
	lookup := secretLookup(env)
	base := filepath.Dir(env)
	if got := servicePath("", "MESSAGES_PATH", "messages.toml", lookup, base); got != elsewhere {
		t.Errorf("an absolute MESSAGES_PATH in .env must be honoured, got %s", got)
	}
	if got := servicePath("", "HANDSETS_PATH", "handsets.toml", lookup, base); got != filepath.Join(dir, "handsets.toml") {
		t.Errorf("the default must sit beside .env, got %s", got)
	}
	if got := servicePath("/explicit.toml", "HANDSETS_PATH", "handsets.toml", lookup, base); got != "/explicit.toml" {
		t.Errorf("an explicit flag wins, got %s", got)
	}
	w := decideWants(lookup, filepath.Join(dir, "handsets.toml"), filepath.Join(dir, "trunks.toml"), servicePath("", "MESSAGES_PATH", "messages.toml", lookup, base))
	if !w.Inbox {
		t.Errorf("INBOX_URL plus a MESSAGES_PATH named in .env must want the inbox: %+v", w.Why["doorman-inbox.service"])
	}
}

// --policy-only validates files anywhere — CI, a workstation, a box with no
// units installed yet — so the host's services are not consulted.
func TestCheckPolicyOnlyDoesNotConsultTheHostServices(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.toml")
	handsets := filepath.Join(dir, "handsets.toml")
	os.WriteFile(handsets, []byte("[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\nnumber = 101\npassword_env = \"HANDSET_KITCHEN_PASSWORD\"\n"), 0o600)
	os.WriteFile(policy, []byte("[house]\nhandsets = [\"kitchen\"]\n\n[[people]]\nname = \"Grandma\"\nnumbers = [\"512-555-0100\"]\n\n[[extensions]]\npin = \"428917\"\nlabel = \"Kitchen\"\nhandsets = [\"kitchen\"]\n"), 0o600)
	out := capture(t, func() {
		if rc := runCheck([]string{"--policy-only", "--handsets", handsets, "--env", filepath.Join(dir, ".env"), policy}); rc != 0 {
			t.Errorf("policy-only check rc = %d", rc)
		}
	})
	if strings.Contains(out, "Services (") {
		t.Errorf("policy-only must not print the host's services:\n%s", out)
	}
}
