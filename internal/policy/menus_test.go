package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// [[menus]], extension ids, and PIN-less extensions (#150 M2).

const menuHouse = `
[house]
handsets = ["kitchen"]
voicemail = "family"

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[extensions]]
id = "orders"
label = "Orders"
handsets = ["kitchen"]
`

const frontMenu = `
[[menus]]
id = "front"
say = "For orders, press 1."

  [[menus.choices]]
  digit = "1"
  extension = "orders"
`

func menuPolicy(t *testing.T, src string) *Policy {
	t.Helper()
	p, err := FromTOML([]byte(src))
	if err != nil {
		t.Fatalf("FromTOML: %v", err)
	}
	return p
}

func menuError(t *testing.T, src string) string {
	t.Helper()
	_, err := FromTOML([]byte(src))
	if err == nil {
		t.Fatalf("expected a refusal:\n%s", src)
	}
	// Every load path refuses the same file: the daemon's, check's, and the
	// lint path the sync observer and the language server share.
	if problems := LintSplit([]byte(src), nil); len(problems) == 0 {
		t.Fatalf("the lint path accepted what the load path refused: %v", err)
	}
	return err.Error()
}

func TestMenuCompilesAndAPinlessExtensionIsReachableOnlyByID(t *testing.T) {
	p := menuPolicy(t, "[line]\nmenu = \"front\"\n"+menuHouse+frontMenu)
	if p.Line().Menu != "front" {
		t.Fatalf("[line] menu = %q", p.Line().Menu)
	}
	m, ok := p.Menu("front")
	if !ok || m.Repeat != 1 || m.Say == "" || len(m.Choices) != 1 {
		t.Fatalf("menu = %+v", m)
	}
	c, ok := m.Choice("1")
	if !ok || c.Kind != ChoiceExtension || c.Target != "orders" || c.Challenge {
		t.Fatalf("choice = %+v", c)
	}
	if _, ok := m.Choice("2"); ok {
		t.Fatal("a key with no choice found one")
	}
	ext, ok := p.LookupExtensionID("orders")
	if !ok || ext.PIN != "" || ext.Label != "Orders" {
		t.Fatalf("by id: %+v", ext)
	}
	// Never under the empty key a bare # submits, never in PINs().
	if _, ok := p.LookupExtension(""); ok {
		t.Fatal("a PIN-less extension is in the PIN map")
	}
	if len(p.PINs()) != 0 || p.PinLength != 0 || p.ExtensionCount() != 1 {
		t.Fatalf("PINs %v, length %d, count %d", p.PINs(), p.PinLength, p.ExtensionCount())
	}
	if _, ok := p.LookupExtensionID(""); ok {
		t.Fatal("the empty id finds an extension")
	}
	if p.MenuDepth("front") != 1 {
		t.Fatalf("depth = %d", p.MenuDepth("front"))
	}
}

// An extension with both a PIN and an id keeps working in the lobby on a line
// without a menu, so a file can move to a menu without rewriting extensions.
func TestExtensionWithPinAndIDWorksBothWays(t *testing.T) {
	src := strings.Replace(menuHouse, `label = "Orders"`, "label = \"Orders\"\npin = \"428917\"", 1)
	p := menuPolicy(t, src)
	if _, ok := p.LookupExtension("428917"); !ok {
		t.Fatal("lost the PIN")
	}
	if e, ok := p.LookupExtensionID("orders"); !ok || e.PIN != "428917" {
		t.Fatal("lost the id")
	}
	if len(p.Warnings()) != 0 {
		t.Fatalf("warnings on a lobby line: %v", p.Warnings())
	}
	// On a menu line the PIN still loads, and check is told it guards nothing.
	p = menuPolicy(t, "[line]\nmenu = \"front\"\n"+src+frontMenu)
	if w := strings.Join(p.Warnings(), "\n"); !strings.Contains(w, "never asks for one") {
		t.Fatalf("warnings = %q", w)
	}
}

func TestMenuRefusals(t *testing.T) {
	line := "[line]\nmenu = \"front\"\n"
	sub := func(id, body string) string { return "\n[[menus]]\nid = \"" + id + "\"\n" + body }
	choice := func(fields string) string { return "\n  [[menus.choices]]\n" + fields + "\n" }

	for name, c := range map[string]struct{ src, want string }{
		"unknown [line] menu": {"[line]\nmenu = \"nope\"\n" + menuHouse + frontMenu, `[line] menu "nope" is not a [[menus]] id`},
		"unknown extension": {line + menuHouse + frontMenu + choice("digit = \"2\"\nextension = \"ghost\""),
			`names extension "ghost"`},
		"unknown menu": {line + menuHouse + frontMenu + choice("digit = \"2\"\nmenu = \"ghost\""),
			`names menu "ghost"`},
		"a menu naming itself": {line + menuHouse + frontMenu + choice("digit = \"2\"\nmenu = \"front\""),
			"already repeats a menu"},
		"duplicate digit": {line + menuHouse + frontMenu + choice("digit = \"1\"\nrecording = \"hours\""),
			`offers digit "1" twice`},
		"reserved star": {line + menuHouse + frontMenu + choice("digit = \"*\"\nrecording = \"hours\""),
			"reserved"},
		"reserved hash": {line + menuHouse + frontMenu + choice("digit = \"#\"\nrecording = \"hours\""),
			"reserved"},
		"two keys": {line + menuHouse + frontMenu + choice("digit = \"12\"\nrecording = \"hours\""),
			"must be one key"},
		"two targets": {line + menuHouse + frontMenu + choice("digit = \"2\"\nrecording = \"hours\"\nvoicemail = \"family\""),
			"exactly one of"},
		"no target": {line + menuHouse + frontMenu + choice("digit = \"2\""),
			"exactly one of"},
		"then without a recording": {line + menuHouse + frontMenu + choice("digit = \"2\"\nvoicemail = \"family\"\nthen = \"menu\""),
			"only follows a recording"},
		"bad then": {line + menuHouse + frontMenu + choice("digit = \"2\"\nrecording = \"hours\"\nthen = \"later\""),
			`then "later"`},
		"bad recording id": {line + menuHouse + frontMenu + choice("digit = \"2\"\nrecording = \"../x\""),
			"recording"},
		"no choices": {line + menuHouse + frontMenu + sub("empty", ""), `menu "empty" has no choices`},
		"repeat out of range": {line + menuHouse + strings.Replace(frontMenu, `id = "front"`, "id = \"front\"\nrepeat = 9", 1),
			"repeat 9"},
		"duplicate menu id": {line + menuHouse + frontMenu + frontMenu, `duplicate menu id "front"`},
		"depth three": {line + menuHouse + frontMenu + choice("digit = \"2\"\nmenu = \"two\"") +
			sub("two", choice("digit = \"1\"\nmenu = \"three\"")) +
			sub("three", choice("digit = \"1\"\nextension = \"orders\"")),
			`menu "three" is 3 menus deep`},
		"pin-less and named by nothing": {menuHouse, `has no pin and no menu reachable from [line] menu names "orders"`},
		"pin-less and named only by an unreachable menu": {line + strings.Replace(menuHouse, `id = "orders"`, `id = "orders"`, 1) +
			"\n[[extensions]]\nid = \"side\"\nlabel = \"Side door\"\nhandsets = [\"kitchen\"]\n" + frontMenu +
			sub("orphan", choice("digit = \"1\"\nextension = \"side\"")),
			`names "side"`},
		"pin-less with no id": {strings.Replace(menuHouse, "id = \"orders\"\n", "", 1), "has no pin — an extension is dialled by its PIN"},
		"duplicate extension id": {line + menuHouse + "\n[[extensions]]\nid = \"orders\"\nlabel = \"Again\"\npin = \"428917\"\nhandsets = [\"kitchen\"]\n" + frontMenu,
			`id "orders" is already another extension's`},
	} {
		t.Run(name, func(t *testing.T) {
			if msg := menuError(t, c.src); !strings.Contains(msg, c.want) {
				t.Errorf("error = %q, want it to say %q", msg, c.want)
			}
		})
	}
}

// Two levels, and back to the front from the second, is the shape the
// depth limit exists to allow.
func TestSubmenuBackToMainIsNotALoopError(t *testing.T) {
	src := "[line]\nmenu = \"front\"\n" + menuHouse + frontMenu + `
  [[menus.choices]]
  digit = "4"
  menu = "more"

[[menus]]
id = "more"
repeat = 0

  [[menus.choices]]
  digit = "0"
  menu = "front"

  [[menus.choices]]
  digit = "2"
  recording = "hours"
  then = "goodbye"
`
	p := menuPolicy(t, src)
	if p.MenuDepth("more") != 2 || len(p.Menus()) != 2 {
		t.Fatalf("depth %d, menus %d", p.MenuDepth("more"), len(p.Menus()))
	}
	more, _ := p.Menu("more")
	if c, _ := more.Choice("2"); c.Kind != ChoiceRecording || c.Then != ThenGoodbye {
		t.Fatalf("choice = %+v", c)
	}
	front, _ := p.Menu("front")
	if c, _ := front.Choice("1"); c.Then != "" {
		t.Fatalf("an extension choice has a then: %+v", c)
	}
}

func TestUnreachableMenusAreWarnedNotRefused(t *testing.T) {
	withPIN := strings.Replace(menuHouse, `label = "Orders"`, "label = \"Orders\"\npin = \"428917\"", 1)
	p := menuPolicy(t, withPIN+frontMenu)
	if w := strings.Join(p.Warnings(), "\n"); !strings.Contains(w, "[line] menu names none") {
		t.Fatalf("warnings = %q", w)
	}
	p = menuPolicy(t, "[line]\nmenu = \"front\"\n"+menuHouse+frontMenu+"\n[[menus]]\nid = \"orphan\"\n  [[menus.choices]]\n  digit = \"1\"\n  recording = \"hours\"\n")
	if w := strings.Join(p.Warnings(), "\n"); !strings.Contains(w, `menu "orphan" is not reachable`) {
		t.Fatalf("warnings = %q", w)
	}
	if p.MenuDepth("orphan") != 0 {
		t.Fatal("an orphan has a depth")
	}
}

// A disabled extension keeps its id: switching it off must not invalidate the
// menu that names it. The walker treats its choice as inert.
func TestDisabledExtensionStillSatisfiesAMenuReference(t *testing.T) {
	src := "[line]\nmenu = \"front\"\n" + strings.Replace(menuHouse, `label = "Orders"`, "label = \"Orders\"\nenabled = false", 1) + frontMenu
	p := menuPolicy(t, src)
	if _, ok := p.LookupExtensionID("orders"); ok {
		t.Fatal("a disabled extension is reachable")
	}
}

// Each file owns its sections.
func TestMenusInTheWrongFileAreRefused(t *testing.T) {
	handsets := "[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n" + frontMenu
	_, err := fromSplitTOML([]byte("[house]\nhandsets = [\"kitchen\"]\n"), []byte(handsets), Options{})
	if err == nil || !strings.Contains(err.Error(), "menus belong in policy.toml") {
		t.Fatalf("err = %v", err)
	}
	for _, file := range []string{"trunks", "contacts"} {
		if hint := misplaced[file+".menus"]; !strings.Contains(hint, "policy.toml") {
			t.Errorf("%s.toml has no hint for a stray [[menus]]: %q", file, hint)
		}
	}
}

// Rotation never gives a menu-only extension a PIN, and leaves its lines
// untouched byte for byte.
func TestRotateSkipsPinlessExtensions(t *testing.T) {
	src := "[line]\nmenu = \"front\"\n" + menuHouse + `
[[extensions]]
label = "Back office"
pin = "428917"
handsets = ["kitchen"]
` + frontMenu
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	rot, err := RotatePins(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rot.Changes) != 1 || rot.Changes[0].Label != "Back office" {
		t.Fatalf("changes = %+v", rot.Changes)
	}
	after, _ := os.ReadFile(path)
	ordersBlock := "[[extensions]]\nid = \"orders\"\nlabel = \"Orders\"\nhandsets = [\"kitchen\"]\n"
	if !strings.Contains(string(after), ordersBlock) {
		t.Fatalf("the PIN-less extension changed:\n%s", after)
	}
	if _, err := RotatePins(path, []string{"Orders"}); err == nil || !strings.Contains(err.Error(), "no PIN to rotate") {
		t.Fatalf("rotating a PIN-less extension by name: %v", err)
	}
}
