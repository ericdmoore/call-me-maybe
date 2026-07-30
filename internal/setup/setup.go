// Package setup turns a fresh checkout into a working configuration.
//
// It exists because the shipped examples deliberately cannot work: their PINs
// are the placeholder sentinel, and copying them verbatim fails `doorman check`
// on purpose. Something has to replace those sentinels with real, generated
// values, or the operator is merely stuck instead of merely insecure.
//
// Every secret here comes from crypto/rand. None of them are ever logged —
// generated PINs go to stdout exactly once, which is the same rule
// `doorman rotate` follows and the only place a PIN may ever be printed.
package setup

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"callmemaybe/internal/policy"
)

// Handset is one phone the operator wants, as answered during the interview.
type Handset struct {
	ID      string
	Label   string
	Number  int    // internal extension, 101-199
	Mailbox string // may be empty
}

// Plan is everything init decided, before anything is written. Keeping it
// separate from the writing makes the whole thing testable without a terminal
// and lets --dry-run print exactly what would happen.
type Plan struct {
	Handsets []Handset
	// House lists the handset ids that ring for a known caller.
	House []string
	// Mailbox is the household mailbox name, or empty for none.
	Mailbox string

	// Generated secrets. Never logged.
	ARIPassword      string
	HandsetPasswords map[string]string // handset id -> password
	ExtensionPINs    map[string]string // handset id -> PIN
	HousePIN         string
	VoicemailPINs    map[string]string // mailbox -> PIN

	// Paths that will be written.
	EnvPath      string
	PolicyPath   string
	HandsetsPath string
}

// Paths locates the files init manages.
type Paths struct {
	Env      string
	Policy   string
	Handsets string
}

func DefaultPaths() Paths {
	return Paths{Env: ".env", Policy: "policy.toml", Handsets: "handsets.toml"}
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns "Kids' Room" into "kids-room" so a room name can be a handset id
// and a PJSIP endpoint without the operator thinking about it.
func Slug(name string) string {
	s := slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return ""
	}
	// handsetIDPattern requires a leading alphanumeric.
	if s[0] < 'a' || s[0] > 'z' {
		if s[0] < '0' || s[0] > '9' {
			s = "h" + s
		}
	}
	return s
}

// EnvVarFor is the .env variable name holding a handset's SIP password.
// handsets.toml stores this name, never the secret.
func EnvVarFor(id string) string {
	return "HANDSET_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_PASSWORD"
}

// BuildPlan turns answers into a complete, valid configuration — including
// every generated secret. It does not touch the filesystem.
func BuildPlan(rooms []string, paths Paths) (*Plan, error) {
	if len(rooms) == 0 {
		return nil, fmt.Errorf("no handsets: a phone system needs at least one phone")
	}

	p := &Plan{
		HandsetPasswords: map[string]string{},
		ExtensionPINs:    map[string]string{},
		VoicemailPINs:    map[string]string{},
		Mailbox:          "family",
		EnvPath:          paths.Env,
		PolicyPath:       paths.Policy,
		HandsetsPath:     paths.Handsets,
	}

	seen := map[string]bool{}
	number := 101
	for _, room := range rooms {
		id := Slug(room)
		if id == "" {
			return nil, fmt.Errorf("cannot make a handset id from %q", room)
		}
		if seen[id] {
			return nil, fmt.Errorf("two handsets would both be called %q — give them distinct names", id)
		}
		if number > 199 {
			return nil, fmt.Errorf("more than %d handsets: internal numbers only run 101-199", 199-101+1)
		}
		seen[id] = true

		p.Handsets = append(p.Handsets, Handset{
			ID:      id,
			Label:   strings.TrimSpace(room),
			Number:  number,
			Mailbox: p.Mailbox,
		})
		p.House = append(p.House, id)
		number++
	}

	// Secrets. A failure here must abort before anything is written — a
	// half-generated configuration is worse than none.
	var err error
	if p.ARIPassword, err = Secret(24); err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, h := range p.Handsets {
		if p.HandsetPasswords[h.ID], err = Secret(18); err != nil {
			return nil, err
		}
		if p.ExtensionPINs[h.ID], err = PIN(6, taken); err != nil {
			return nil, err
		}
	}
	if p.HousePIN, err = PIN(6, taken); err != nil {
		return nil, err
	}
	if p.VoicemailPINs[p.Mailbox], err = PIN(6, map[string]bool{}); err != nil {
		return nil, err
	}
	return p, nil
}

// Secret returns a URL-safe random string of roughly n bytes of entropy.
func Secret(n int) (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	max := big.NewInt(int64(len(alphabet)))
	var b strings.Builder
	for range n {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generating a secret: %w", err)
		}
		b.WriteByte(alphabet[v.Int64()])
	}
	return b.String(), nil
}

// PIN returns a random digit string not already in taken, and records it.
// crypto/rand, not math/rand: extensions are credentials exposed to the PSTN.
func PIN(length int, taken map[string]bool) (string, error) {
	if length < policy.MinPINLength {
		length = 6
	}
	ten := big.NewInt(10)
	for range 1000 {
		var b strings.Builder
		for range length {
			n, err := rand.Int(rand.Reader, ten)
			if err != nil {
				return "", fmt.Errorf("generating a PIN: %w", err)
			}
			b.WriteByte(byte('0' + n.Int64()))
		}
		pin := b.String()
		if !taken[pin] {
			taken[pin] = true
			return pin, nil
		}
	}
	return "", fmt.Errorf("could not find an unused %d-digit PIN", length)
}

// ── Rendering ────────────────────────────────────────────────────────────

func (p *Plan) HandsetsTOML() string {
	var b strings.Builder
	b.WriteString("# handsets.toml — the hardware inventory.\n")
	b.WriteString("# Generated by `doorman init`. Safe to hand-edit afterwards.\n")
	b.WriteString("#\n")
	b.WriteString("# `doorman render` generates the per-handset Asterisk config from this\n")
	b.WriteString("# file, so ids, endpoints, and dialplan cannot drift apart.\n")
	for _, h := range p.Handsets {
		fmt.Fprintf(&b, "\n[[handsets]]\nid = %q\nlabel = %q\nendpoint = \"PJSIP/%s\"\nnumber = %d\npage = true\n",
			h.ID, h.Label, h.ID, h.Number)
		if h.Mailbox != "" {
			fmt.Fprintf(&b, "mailbox = %q\n", h.Mailbox)
		}
		fmt.Fprintf(&b, "password_env = %q\n", EnvVarFor(h.ID))
	}
	return b.String()
}

func (p *Plan) PolicyTOML() string {
	var b strings.Builder
	b.WriteString("# policy.toml — who gets in, what rings, and when.\n")
	b.WriteString("# Generated by `doorman init`. This is the file that changes weekly;\n")
	b.WriteString("# it is safe to edit at 9pm, because a file that fails validation is\n")
	b.WriteString("# rejected on reload and the previous good policy stays in service.\n\n")

	b.WriteString("[house]\nhandsets = [")
	for i, id := range p.House {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", id)
	}
	b.WriteString("]\n")
	if p.Mailbox != "" {
		fmt.Fprintf(&b, "voicemail = %q\n", p.Mailbox)
	}
	b.WriteString("caller_id_format = \"Lobby: {name} <{number}>\"\n")

	b.WriteString("\n# ── The allow-list ───────────────────────────────────────────\n")
	b.WriteString("# These callers hear \"Welcome, I'll connect you\" and ring the whole\n")
	b.WriteString("# house. Numbers may be written any way you like — 512-555-0100,\n")
	b.WriteString("# (512) 555-0100, +15125550100 — they are normalised to E.164 at load,\n")
	b.WriteString("# and anything that is not a real number is a load error rather than a\n")
	b.WriteString("# silent never-match. Probe one with `doorman e164 \"...\"`.\n")
	b.WriteString("#\n")
	b.WriteString("# Left empty on purpose: until you add someone, every caller meets the\n")
	b.WriteString("# lobby, which is the safe default. Uncomment and edit to start.\n")
	b.WriteString("#\n")
	b.WriteString("# [[people]]\n")
	b.WriteString("# name = \"Grandma\"\n")
	b.WriteString("# numbers = [\"512-555-0100\"]\n")

	b.WriteString("\n# ── Extensions ───────────────────────────────────────────────\n")
	b.WriteString("# Unknown callers dial one of these in the lobby. The PIN *is* the\n")
	b.WriteString("# extension, so treat it like a password. These were generated with\n")
	b.WriteString("# crypto/rand; rotate any that leak with `doorman rotate <label>`.\n")

	fmt.Fprintf(&b, "\n[[extensions]]\npin = %q\nlabel = \"Whole house\"\nhandsets = [", p.HousePIN)
	for i, id := range p.House {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", id)
	}
	b.WriteString("]\n")
	if p.Mailbox != "" {
		fmt.Fprintf(&b, "voicemail = %q\n", p.Mailbox)
	}

	for _, h := range p.Handsets {
		fmt.Fprintf(&b, "\n[[extensions]]\npin = %q\nlabel = %q\nhandsets = [%q]\n",
			p.ExtensionPINs[h.ID], h.Label, h.ID)
	}
	return b.String()
}

// EnvFile renders .env. base is the shipped example, whose comments are worth
// keeping; the generated values are substituted into it.
func (p *Plan) EnvFile(base string) string {
	out := base
	set := func(key, value string) {
		re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=.*$`)
		line := key + "=" + value
		if re.MatchString(out) {
			out = re.ReplaceAllLiteralString(out, line)
		} else {
			out = strings.TrimRight(out, "\n") + "\n" + line + "\n"
		}
	}
	set("ARI_PASSWORD", p.ARIPassword)
	for id, pw := range p.HandsetPasswords {
		set(EnvVarFor(id), pw)
	}
	// Any handset variables the example shipped that this install has no
	// handset for would otherwise sit empty and confuse `render`.
	out = regexp.MustCompile(`(?m)^HANDSET_[A-Z0-9_]+_PASSWORD=$\n?`).ReplaceAllString(out, "")
	return out
}

// ── Writing ──────────────────────────────────────────────────────────────

// Backup renames an existing file out of the way, returning the new name.
func Backup(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", nil // nothing to back up
	}
	dst := fmt.Sprintf("%s.bak-%s", path, time.Now().Format("20060102-150405"))
	return dst, os.Rename(path, dst)
}

// WriteFile writes with restrictive permissions. policy.toml carries PINs and
// .env carries every password in the house; both are 0600 and smoke.sh checks
// it.
func WriteFile(path, content string) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(content), 0o600)
}
