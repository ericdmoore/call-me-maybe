package lsp

import (
	"strings"
	"testing"
)

const handsetsDoc = `[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
number = 101
mailbox = "adults"
password_env = "HANDSET_KITCHEN_PASSWORD"

[[handsets]]
id = "kids-room"
endpoint = "PJSIP/kids-room"
number = 105
password_env = "HANDSET_KIDS_ROOM_PASSWORD"

[[groups]]
id = "adults"
handsets = ["kitchen"]
`

const policyDoc = `[house]
handsets = ["adults"]
voicemail = "family"

[[schedules]]
id = "school-night"
start = "20:30"
end = "07:00"
days = ["SU", "MO"]

[[extensions]]
pin = "428917"
label = "Kids"
handsets = ["kids-room"]
voicemail = "kids"
afterhours = "school-night"
`

func TestAnalyseCleanPairHasNoDiagnostics(t *testing.T) {
	h := handsetsDoc
	p, hd := Analyse(policyDoc, &h)
	if len(p) != 0 || len(hd) != 0 {
		t.Fatalf("diagnostics on a valid pair: policy=%v handsets=%v", p, hd)
	}
}

func TestUnknownHandsetIsPlacedOnItsLine(t *testing.T) {
	broken := strings.Replace(policyDoc, `handsets = ["kids-room"]`, `handsets = ["kids-rom"]`, 1)
	h := handsetsDoc
	pDiags, _ := Analyse(broken, &h)
	if len(pDiags) != 1 {
		t.Fatalf("diagnostics = %v", pDiags)
	}
	d := pDiags[0]
	if !strings.Contains(d.Message, "kids-rom") {
		t.Errorf("message = %q", d.Message)
	}
	wantLine := 0
	for i, line := range strings.Split(broken, "\n") {
		if strings.Contains(line, "kids-rom") {
			wantLine = i
			break
		}
	}
	if d.Range.Start.Line != wantLine {
		t.Errorf("diagnostic on line %d, want %d", d.Range.Start.Line, wantLine)
	}
}

func TestSyntaxErrorGetsDecoderPosition(t *testing.T) {
	broken := policyDoc + "\nthis is not toml\n"
	h := handsetsDoc
	pDiags, _ := Analyse(broken, &h)
	if len(pDiags) != 1 {
		t.Fatalf("diagnostics = %v", pDiags)
	}
	if pDiags[0].Range.Start.Line == 0 {
		t.Errorf("syntax diagnostic not positioned: %+v", pDiags[0])
	}
}

func TestInventoryProblemLandsInHandsetsDoc(t *testing.T) {
	brokenH := strings.Replace(handsetsDoc, "number = 105", "number = 101", 1)
	pDiags, hDiags := Analyse(policyDoc, &brokenH)
	if len(hDiags) != 1 || len(pDiags) != 0 {
		t.Fatalf("policy=%v handsets=%v — duplicate number belongs to handsets doc", pDiags, hDiags)
	}
	if !strings.Contains(hDiags[0].Message, "share number") {
		t.Errorf("message = %q", hDiags[0].Message)
	}
}

func TestCompletionContexts(t *testing.T) {
	h := handsetsDoc
	m := BuildModel(policyDoc, &h)

	labels := func(items []CompletionItem) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.Label)
		}
		return out
	}

	line := `handsets = ["`
	got := labels(Complete(m, line, len(line)))
	want := []string{"kids-room", "kitchen", "adults"}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("handsets context missing %q in %v", w, got)
		}
	}

	line = `afterhours = "`
	got = labels(Complete(m, line, len(line)))
	if !contains(got, "school-night") {
		t.Errorf("afterhours context = %v", got)
	}

	line = `  days = ["SU", "`
	got = labels(Complete(m, line, len(line)))
	if !contains(got, "MO") || !contains(got, "SA") {
		t.Errorf("days context = %v", got)
	}

	line = `voicemail = "`
	got = labels(Complete(m, line, len(line)))
	for _, w := range []string{"adults", "family", "kids"} {
		if !contains(got, w) {
			t.Errorf("mailbox context missing %q in %v", w, got)
		}
	}

	if items := Complete(m, `pin = "4289`, 11); len(items) != 0 {
		t.Errorf("no completions expected inside a pin, got %v", items)
	}
}

func TestModelSurvivesHalfTypedFiles(t *testing.T) {
	h := handsetsDoc
	m := BuildModel(policyDoc+"\n[[exten", &h)
	if len(m.HandsetIDs) == 0 {
		t.Error("a half-typed policy must not empty the handset vocabulary")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
