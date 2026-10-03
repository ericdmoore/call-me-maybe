// Package host prepares the machine doorman runs on: the systemd units,
// the directories each process owns, and which units the configuration
// calls for. It is s09 M2 — the part of `doorman init` that touches the
// host rather than the config files — and it is shaped so that a dry run
// and a real run are the same plan: every change is a command line the
// operator can read before it happens, and the only thing that differs is
// whether Exec runs it.
//
// The units ship inside the binary (go:embed), so a box needs the file and
// nothing else. A unit this package installed carries a header saying so
// and is replaced on every run — a rerun is the upgrade. A unit without the
// header is somebody's own and is left alone, and said so.
//
// Nothing here is reachable from a call: cmd/doorman names it from the
// init command only, asserted by test.
package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed units/*.service units/*.timer
var unitFS embed.FS

// managedMarker is the first line of every unit this package writes. Its
// presence is what makes a file ours to replace.
const managedMarker = "# Managed by `doorman init services` — a rerun replaces this file; edit the embedded copy, not this one.\n"

// Exec runs a command on the host. The real one is System; tests record.
type Exec interface {
	// Run executes argv and returns its combined output.
	Run(ctx context.Context, argv ...string) (string, error)
}

// Layout is where things go. The zero value is the default install; tests
// point every path at a temp dir.
type Layout struct {
	UnitDir  string // /etc/systemd/system
	StateDir string // /var/lib/doorman
	Spool    string // /var/spool/call-me-maybe — asterisk writes, doorman reads
	// Users and groups the directories are owned by.
	DoormanUser, DoormanGroup, AsteriskUser string
}

// DefaultLayout is the install every unit assumes.
func DefaultLayout() Layout {
	return Layout{
		UnitDir: "/etc/systemd/system", StateDir: "/var/lib/doorman", Spool: "/var/spool/call-me-maybe",
		DoormanUser: "doorman", DoormanGroup: "doorman", AsteriskUser: "asterisk",
	}
}

// Wants is which optional units the configuration calls for. The daemon
// and the *88 timer are always wanted; cmd/doorman decides the rest from
// .env and the files, and this package never reads either.
type Wants struct {
	Directory bool // PROVISION_ADDRESS and a phone with mac+model
	Inbox     bool // INBOX_URL and messages.toml
	Digest    bool // MAIL_HOOK and MAIL_TO
	Balance   bool // trunks.toml
	// Why records the reason for each decision, by unit, for the plan.
	Why map[string]string
}

// Unit is one embedded unit and the rule for enabling it.
type Unit struct {
	Name    string // doorman-inbox.service
	Timer   bool
	Always  bool               // enabled on every box
	Wanted  func(w Wants) bool // otherwise, when the config calls for it
	Content []byte             // as embedded, without the marker
}

// Units lists every unit the binary ships, in install order.
func Units() ([]Unit, error) {
	rule := map[string]struct {
		always bool
		wanted func(Wants) bool
	}{
		"doorman.service":           {always: true},
		"doorman-phonebook.service": {},
		"doorman-phonebook.timer":   {always: true},
		"doorman-directory.service": {wanted: func(w Wants) bool { return w.Directory }},
		"doorman-inbox.service":     {wanted: func(w Wants) bool { return w.Inbox }},
		"doorman-digest.service":    {},
		"doorman-digest.timer":      {wanted: func(w Wants) bool { return w.Digest }},
		"doorman-balance.service":   {},
		"doorman-balance.timer":     {wanted: func(w Wants) bool { return w.Balance }},
	}
	entries, err := fs.ReadDir(unitFS, "units")
	if err != nil {
		return nil, err
	}
	var out []Unit
	for _, e := range entries {
		r, known := rule[e.Name()]
		if !known {
			return nil, fmt.Errorf("host: embedded unit %s has no enabling rule", e.Name())
		}
		content, err := unitFS.ReadFile("units/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, Unit{Name: e.Name(), Timer: strings.HasSuffix(e.Name(), ".timer"),
			Always: r.always, Wanted: r.wanted, Content: content})
	}
	// Services before their timers, the daemon first.
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i].Name), rank(out[j].Name)
		if ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func rank(name string) int {
	switch {
	case name == "doorman.service":
		return 0
	case strings.HasSuffix(name, ".service"):
		return 1
	default:
		return 2
	}
}

// ShouldEnable is the one rule: always, or wanted by the config. A
// service a timer runs is never enabled itself.
func (u Unit) ShouldEnable(w Wants) bool {
	if u.Always {
		return true
	}
	return u.Wanted != nil && u.Wanted(w)
}

// Step is one change, as the command that makes it. Dry runs print these;
// real runs execute them in order. Describe is what the operator reads.
type Step struct {
	Describe string
	Argv     []string
	// write, when set, is a file to put in place before Argv runs (the unit
	// text, via a temp file so the install is atomic and owned by root).
	write *pendingWrite
}

type pendingWrite struct {
	target  string
	content []byte
}

// Plan is what a run would do, and why some units are left alone.
type Plan struct {
	Steps []Step
	// Foreign lists units found in UnitDir without the managed marker:
	// somebody's own, left untouched.
	Foreign []string
	// Enable and Disable are the units the config calls for and the ones it
	// no longer does, after the install steps.
	Enable, Disable []string
}

// Build works out the plan from the layout, the wants and what is on disk.
// It reads the host (unit files, user existence via Exec) and changes
// nothing.
func Build(ctx context.Context, x Exec, lay Layout, w Wants) (*Plan, error) {
	units, err := Units()
	if err != nil {
		return nil, err
	}
	p := &Plan{}

	// Directories, each with its owner. The spool is the one place asterisk
	// and doorman meet: asterisk-owned, doorman group, setgid so what
	// asterisk writes is doorman's to remove. If there is no asterisk user
	// (a box without Asterisk yet) the spool waits for the next run.
	for _, d := range []string{lay.StateDir, filepath.Join(lay.StateDir, "journal"), filepath.Join(lay.StateDir, "provision"),
		filepath.Join(lay.StateDir, "inbox"), filepath.Join(lay.StateDir, "phonebook"), filepath.Join(lay.StateDir, "phonebook", "own")} {
		p.Steps = append(p.Steps, Step{Describe: "directory " + d + " (" + lay.DoormanUser + ", 0700)",
			Argv: []string{"install", "-d", "-o", lay.DoormanUser, "-g", lay.DoormanGroup, "-m", "0700", d}})
	}
	if userExists(ctx, x, lay.AsteriskUser) {
		p.Steps = append(p.Steps, Step{Describe: "directory " + lay.Spool + " (" + lay.AsteriskUser + ":" + lay.DoormanGroup + ", 2770 — the *88 spool)",
			Argv: []string{"install", "-d", "-o", lay.AsteriskUser, "-g", lay.DoormanGroup, "-m", "2770", lay.Spool}})
	} else {
		p.Steps = append(p.Steps, Step{Describe: "skip " + lay.Spool + ": no user " + lay.AsteriskUser + " yet (install Asterisk, rerun)"})
	}

	// Units: ours are replaced, foreign ones reported, identical ones skipped.
	for _, u := range units {
		target := filepath.Join(lay.UnitDir, u.Name)
		want := append([]byte(managedMarker), u.Content...)
		have, err := os.ReadFile(target)
		switch {
		case err == nil && bytes.Equal(have, want):
			p.Steps = append(p.Steps, Step{Describe: "unit " + u.Name + " is current"})
		case err == nil && !ours(u.Name, have):
			p.Foreign = append(p.Foreign, u.Name)
			p.Steps = append(p.Steps, Step{Describe: "leave " + target + ": not written by doorman (no marker); yours to keep or remove"})
			continue
		case err == nil:
			p.Steps = append(p.Steps, Step{Describe: "replace unit " + u.Name + " (ours, out of date)",
				Argv: []string{"install", "-o", "root", "-g", "root", "-m", "0644", "{unit}", target}, write: &pendingWrite{target, want}})
		case errors.Is(err, fs.ErrNotExist):
			p.Steps = append(p.Steps, Step{Describe: "install unit " + u.Name,
				Argv: []string{"install", "-o", "root", "-g", "root", "-m", "0644", "{unit}", target}, write: &pendingWrite{target, want}})
		default:
			return nil, fmt.Errorf("host: %s: %w", target, err)
		}
	}
	p.Steps = append(p.Steps, Step{Describe: "systemctl daemon-reload", Argv: []string{"systemctl", "daemon-reload"}})

	foreign := map[string]bool{}
	for _, f := range p.Foreign {
		foreign[f] = true
	}
	for _, u := range units {
		if foreign[u.Name] {
			continue
		}
		if u.ShouldEnable(w) {
			why := w.Why[u.Name]
			if u.Always {
				why = "always"
			}
			p.Enable = append(p.Enable, u.Name)
			p.Steps = append(p.Steps, Step{Describe: "enable --now " + u.Name + " (" + why + ")", Argv: []string{"systemctl", "enable", "--now", u.Name}})
		} else if u.Always || u.Wanted != nil {
			p.Disable = append(p.Disable, u.Name)
			p.Steps = append(p.Steps, Step{Describe: "disable --now " + u.Name + " (" + w.Why[u.Name] + ")", Argv: []string{"systemctl", "disable", "--now", u.Name}})
		}
	}
	return p, nil
}

// Apply runs the plan. Each step's command is run as given; a step with a
// unit to write puts it in a temp file first and substitutes the path.
func Apply(ctx context.Context, x Exec, p *Plan) error {
	for _, s := range p.Steps {
		if len(s.Argv) == 0 {
			continue
		}
		argv := append([]string(nil), s.Argv...)
		if s.write != nil {
			tmp, err := os.CreateTemp("", "doorman-unit-*")
			if err != nil {
				return err
			}
			if _, err := tmp.Write(s.write.content); err != nil {
				return err
			}
			_ = tmp.Close()
			defer os.Remove(tmp.Name())
			for i := range argv {
				if argv[i] == "{unit}" {
					argv[i] = tmp.Name()
				}
			}
		}
		if out, err := x.Run(ctx, argv...); err != nil {
			return fmt.Errorf("%s: %w%s", s.Describe, err, trailing(out))
		}
	}
	return nil
}

func trailing(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	return ": " + out
}

// Status is one unit's state on the host against what the config wants.
type Status struct {
	Name            string
	Want            bool
	Enabled, Active bool
	Present         bool // the unit file exists
	Foreign         bool
}

// Inspect reports every unit's state for `doorman check`. It runs
// `systemctl is-enabled` / `is-active`, which need no root.
func Inspect(ctx context.Context, x Exec, lay Layout, w Wants) ([]Status, error) {
	units, err := Units()
	if err != nil {
		return nil, err
	}
	var out []Status
	for _, u := range units {
		if !u.Always && u.Wanted == nil {
			continue // a service a timer runs: the timer is the status
		}
		st := Status{Name: u.Name, Want: u.ShouldEnable(w)}
		if have, err := os.ReadFile(filepath.Join(lay.UnitDir, u.Name)); err == nil {
			st.Present = true
			st.Foreign = !ours(u.Name, have)
		}
		if o, err := x.Run(ctx, "systemctl", "is-enabled", u.Name); err == nil && strings.TrimSpace(o) == "enabled" {
			st.Enabled = true
		}
		if o, err := x.Run(ctx, "systemctl", "is-active", u.Name); err == nil && strings.TrimSpace(o) == "active" {
			st.Active = true
		}
		out = append(out, st)
	}
	return out, nil
}

// ours reports whether a unit file is doorman's to replace: one this
// package wrote (the marker), or one the shell installer copied from the
// repository before the marker existed — recognised only by matching, byte
// for byte, a unit some release actually shipped. A unit under our name
// with any other content is somebody's own and stays theirs: an operator
// who changed the user, the directory or the schedule did so on purpose.
func ours(name string, content []byte) bool {
	if bytes.HasPrefix(content, []byte(managedMarker)) {
		return true
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	for _, h := range legacyUnits[name] {
		if h == hash {
			return true
		}
	}
	return false
}

// legacyUnits is the sha256 of every unit file the repository shipped
// under scripts/ at any tag before the units moved into the binary — what
// an installer-era box has on disk. Regenerate with:
//
//	for ref in $(git tag) main; do for f in $(git ls-tree -r --name-only $ref -- scripts | grep -E 'doorman.*\.(service|timer)$'); do git show "$ref:$f" | shasum -a 256 | awk -v n="$(basename $f)" '{print $1, n}'; done; done | sort -u
var legacyUnits = map[string][]string{
	"doorman-balance.service": {
		"a11655c5ce9129be5b2682219f9d4f50c9b70026ce9bbc27544b292f64304be4",
	},
	"doorman-balance.timer": {
		"6e00ca2f7fe3c99b4f27ec96a9024a18aaee00550d541d26ee2b02a3eaafa240",
	},
	"doorman-digest.service": {
		"0fc451920b7b7280cfaaec1c8a5d7e0263bd8b027ebd676100ca49d5ff933571",
	},
	"doorman-digest.timer": {
		"a72b9fb4b93c5687b8be936ede71155fb64ae1912324eaa458bba8f427eb2bad",
	},
	"doorman-directory.service": {
		"06e88c2dda5624e9c1f6b9ad91d48901bc3a8f534d05e39256f738bbbe6fa69c",
	},
	"doorman-inbox.service": {
		"8ada18278d86b5150adefa1cf035b763fcb77a91592048fac899214a471948c5",
		"aecccf066e779f3ee4c20fb2430540e262a9102694e850d6ca96f0a59706625a",
	},
	"doorman-phonebook.service": {
		"e71336a96c3cc9c52f20c207139ae9dfe9c148d1f830aec5a334fd628387eee7",
	},
	"doorman-phonebook.timer": {
		"f5e83d962e839176576ee6a25fbe9b1592ca5edf6695ef7a1391883a3d45a4ab",
	},
	"doorman.service": {
		"13176c69da0e05fe5816c7715ba1ed7afc6a00e3e7a0a09be70fd713a7c49ca1",
		"a0f974e4898cc9680b77e13111b390ebd4990afeb463495453d75830aabd62b2",
		"e56d8724b08955831dca5230c332b15dd5411c713f9e8fcde0464ada981f94c6",
	},
}

func userExists(ctx context.Context, x Exec, name string) bool {
	_, err := x.Run(ctx, "id", "-u", name)
	return err == nil
}
