package lsp

import (
	"strings"
	"testing"
)

// [[shelves]] in the editor (#155): a choice's shelf and story_of_the_day
// complete the shelf ids, and a choice naming a shelf that does not exist
// squiggles where it is written.

const shelfPolicyDoc = `[line]
menu = "front"

[house]
handsets = ["kitchen"]

[[shelves]]
id = "stories"
titles = ["little-red-hen"]

[[menus]]
id = "front"

  [[menus.choices]]
  digit = "1"
  shelf = "stories"

  [[menus.choices]]
  digit = "2"
  story_of_the_day = "ghost"
`

func TestShelfCompletionsOfferShelfIDs(t *testing.T) {
	h := handsetsDoc
	m := BuildModel(shelfPolicyDoc, &h)
	if !strings.Contains(m.Describe(), "shelves=1") {
		t.Fatalf("model = %s", m.Describe())
	}
	for _, line := range []string{`  shelf = "`, `  story_of_the_day = "st`} {
		var got []string
		for _, it := range Complete(m, line, len(line)) {
			got = append(got, it.Label)
		}
		if !contains(got, "stories") {
			t.Errorf("%q completes %v, want the shelf id", line, got)
		}
	}
}

func TestChoiceNamingAnUnknownShelfIsPlacedOnItsLine(t *testing.T) {
	h := handsetsDoc
	diags, _ := Analyse(shelfPolicyDoc, &h)
	for _, d := range diags {
		if strings.Contains(d.Message, `"ghost"`) {
			if line := strings.Split(shelfPolicyDoc, "\n")[d.Range.Start.Line]; !strings.Contains(line, "ghost") {
				t.Fatalf("placed on %q", line)
			}
			return
		}
	}
	t.Fatalf("no diagnostic for the unknown shelf: %+v", diags)
}
