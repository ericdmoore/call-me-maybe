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
