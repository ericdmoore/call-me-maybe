package lsp

import (
	"strings"
	"testing"
)

const trunksDoc = `[[trunks]]
id = "voipms"
provider = "voip.ms"
host = "chicago.voip.ms"
username = "123456_home"
password_env = "TRUNK_VOIPMS_PASSWORD"
`

// A trunks.toml is a different root struct, so classifying it as policy would
// report every key in it as unknown — and with no policy.toml open, that is
// what the pairing did before it had a kind of its own.
func TestKindOfRecognisesTheThirdFile(t *testing.T) {
	for path, want := range map[string]DocKind{
		"/etc/call-me-maybe/trunks.toml":     KindTrunks,
		"/etc/call-me-maybe/handsets.toml":   KindHandsets,
		"/etc/call-me-maybe/policy.toml":     KindPolicy,
		"/etc/call-me-maybe/policy.biz.toml": KindPolicy,
	} {
		if got := KindOf(path); got != want {
			t.Errorf("KindOf(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestTrunksAreLintedOnTheirOwn(t *testing.T) {
	if d := AnalyseTrunks(trunksDoc); len(d) != 0 {
		t.Fatalf("diagnostics on a valid trunks.toml: %v", d)
	}

	broken := strings.Replace(trunksDoc, "host =", "hostname =", 1)
	d := AnalyseTrunks(broken)
	if len(d) == 0 {
		t.Fatal("a misspelled key should be reported")
	}
	if !strings.Contains(d[0].Message, "hostname") {
		t.Errorf("diagnostic = %q", d[0].Message)
	}
	// The squiggle goes on the typo, which is the only place it helps.
	if line := strings.Split(broken, "\n")[d[0].Range.Start.Line]; !strings.Contains(line, "hostname") {
		t.Errorf("diagnostic placed on %q", line)
	}
}

// The reference that crosses files runs from policy.toml into trunks.toml, so
// it is checked where the policy document is — and only when there is an
// inventory to check against.
func TestLineTrunkIsCheckedInTheEditorWhenAnInventoryIsOpen(t *testing.T) {
	h, tr := handsetsDoc, trunksDoc
	withTrunk := "[line]\ntrunk = \"telnyx\"\n\n" + policyDoc

	pDiags, _ := AnalyseWithTrunks(withTrunk, &h, &tr)
	if len(pDiags) == 0 || !strings.Contains(pDiags[0].Message, "telnyx") {
		t.Fatalf("an unknown trunk should squiggle: %v", pDiags)
	}

	// And with no trunks.toml anywhere, nothing is claimed either way: a
	// single-provider install has no inventory and no reference to check.
	if p, _ := AnalyseWithTrunks(withTrunk, &h, nil); len(p) != 0 {
		t.Errorf("diagnostics with no trunks.toml: %v", p)
	}
}

func TestTrunkCompletion(t *testing.T) {
	h, tr := handsetsDoc, trunksDoc
	m := BuildModelWith(policyDoc, &h, &tr)

	for _, line := range []string{`trunk = "`, `emergency_trunk = "`} {
		got := Complete(m, line, len(line))
		if len(got) != 1 || got[0].Label != "voipms" {
			t.Errorf("%q completed to %v, want the declared trunk", line, got)
		}
	}
	// `handsets = [` must not start offering trunks.
	if got := Complete(m, `handsets = ["`, 13); contains(labelsOf(got), "voipms") {
		t.Error("a trunk id was offered where a handset id belongs")
	}
}

func labelsOf(items []CompletionItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Label)
	}
	return out
}
