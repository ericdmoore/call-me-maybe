package lsp

import (
	"strings"
	"testing"
)

// [[menus]] in the editor: the ids a choice may name complete, and a choice
// naming an extension that does not exist squiggles where it is written.

const menuPolicyDoc = `[line]
menu = "front"

[house]
handsets = ["kitchen"]

[[extensions]]
id = "orders"
label = "Orders"
handsets = ["kitchen"]

[[menus]]
id = "front"

  [[menus.choices]]
  digit = "1"
  extension = "orders"

  [[menus.choices]]
  digit = "2"
  voicemail = "general"

  [[menus.choices]]
  digit = "3"
  extension = "ghost"
`

func TestMenuCompletionsOfferExtensionAndMenuIDs(t *testing.T) {
	h := handsetsDoc
	m := BuildModel(menuPolicyDoc, &h)
	if !strings.Contains(m.Describe(), "extensions=1 menus=1") {
		t.Fatalf("model = %s", m.Describe())
	}
	for line, want := range map[string]string{
		`  extension = "`: "orders",
		`  menu = "`:      "front",
		`menu = "fr`:      "front",
		`  voicemail = "`: "general",
	} {
		var got []string
		for _, it := range Complete(m, line, len(line)) {
			got = append(got, it.Label)
		}
		if !contains(got, want) {
			t.Errorf("%q completes %v, want %q among them", line, got, want)
		}
	}
	// on_no_input is not a menu context.
	if items := Complete(m, `on_no_input = "`, len(`on_no_input = "`)); len(items) != 0 {
		t.Errorf("on_no_input completed %v", items)
	}
}

func TestMenuChoiceNamingAnUnknownExtensionIsPlacedOnItsLine(t *testing.T) {
	h := handsetsDoc
	diags, _ := Analyse(menuPolicyDoc, &h)
	for _, d := range diags {
		if strings.Contains(d.Message, `"ghost"`) {
			if line := strings.Split(menuPolicyDoc, "\n")[d.Range.Start.Line]; !strings.Contains(line, "ghost") {
				t.Fatalf("placed on %q", line)
			}
			return
		}
	}
	t.Fatalf("no diagnostic for the unknown extension: %+v", diags)
}
