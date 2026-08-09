package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

func loadTrunks(t *testing.T, body string) *policy.Trunks {
	t.Helper()
	tr, err := policy.TrunksFromTOML([]byte(body))
	if err != nil {
		t.Fatalf("TrunksFromTOML: %v", err)
	}
	return tr
}

const cliTrunks = `
emergency_trunk = "voipms"

[[trunks]]
id = "voipms"
provider = "voip.ms"
host = "chicago.voip.ms"
username = "123456_home"
password_env = "TRUNK_VOIPMS_PASSWORD"
e911 = true

[[trunks]]
id = "telnyx"
provider = "telnyx"
host = "sip.telnyx.com"
username = "cmm-home"
password_env = "TRUNK_TELNYX_PASSWORD"
`

// trunkPolicy is a primary line that names a provider and the number it
// answers — the pair a DID route needs.
func trunkPolicy(trunk string) string {
	return `
[line]
label  = "Home"
number = "+15125550100"
trunk  = "` + trunk + `"

[house]
handsets = ["kitchen"]
`
}

func lineResult(name, trunk, number string) checkedLine {
	pol, err := policy.FromTOML([]byte(`
[line]
trunk = "` + trunk + `"
number = "` + number + `"

[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
`))
	if err != nil {
		panic(err)
	}
	return checkedLine{LineFile: policy.LineFile{Name: name, Path: "./policy.toml"}, pol: pol}
}

// The compatibility gate, stated as a test: with no trunks.toml, `doorman
// check` says nothing about trunks at all. A box with one provider should
// never have to read the word.
func TestCheckSaysNothingAboutTrunksWithoutAnInventory(t *testing.T) {
	absent := &policy.Trunks{}
	results := []checkedLine{lineResult(policy.DefaultLine, "", "")}
	out := capture(t, func() {
		printTrunks(absent, results)
		printEmergency(absent, results)
	})
	if out != "" {
		t.Errorf("check spoke about trunks with no trunks.toml:\n%s", out)
	}
}

func TestCheckListsTheTrunksAndWhichLinesLandOnThem(t *testing.T) {
	trunks := loadTrunks(t, cliTrunks)
	results := []checkedLine{
		lineResult(policy.DefaultLine, "voipms", "+15125550100"),
		lineResult("biz", "telnyx", "+15125550142"),
	}
	out := capture(t, func() { printTrunks(trunks, results) })

	for _, want := range []string{
		"Trunks: 2", "voipms", "voip.ms", "chicago.voip.ms", "default",
		"telnyx", "sip.telnyx.com", "biz",
		// The failure mode that has no runtime symptom is worth naming where
		// somebody is already reading about trunks.
		"line=yes", "endpoint=<id>",
		// Both generated files are outputs, and one holds passwords.
		"pjsip_trunks.conf", "never commit", "extensions_trunks.conf",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the trunk block should mention %q:\n%s", want, out)
		}
	}
}

// A route needs [line] trunk and [line] number together. Half of it is a real
// state and a silent one — the registration works, the calls arrive, and they
// all reach the wrong greeting.
func TestCheckNamesLinesThatWillGetNoDIDRoute(t *testing.T) {
	out := capture(t, func() {
		printTrunks(loadTrunks(t, cliTrunks), []checkedLine{
			lineResult(policy.DefaultLine, "voipms", "+15125550100"),
			lineResult("biz", "telnyx", ""),
		})
	})
	if !strings.Contains(out, "No DID route generated for: biz") {
		t.Errorf("check should name a line with a trunk and no number:\n%s", out)
	}
}

// The plan asks for this by name: which trunk, and whether it was chosen or
// inferred. Defaulting without announcing produces a surprise; requiring
// without defaulting produces a state where somebody forgot.
func TestCheckSaysWhetherTheEmergencyTrunkWasChosenOrInferred(t *testing.T) {
	results := []checkedLine{lineResult(policy.DefaultLine, "voipms", "+15125550100")}

	chosen := capture(t, func() { printEmergency(loadTrunks(t, cliTrunks), results) })
	if !strings.Contains(chosen, "voipms") || !strings.Contains(chosen, "chosen") {
		t.Errorf("a designated trunk should be reported as chosen:\n%s", chosen)
	}
	if !strings.Contains(chosen, "declared (e911 = true)") {
		t.Errorf("check should say whether an address is registered:\n%s", chosen)
	}

	// Inferred from a line pointing at the trunk whose address nobody declared,
	// so both halves of the report are exercised at once.
	noDesignation := strings.Replace(cliTrunks, `emergency_trunk = "voipms"`, "", 1)
	inferred := capture(t, func() {
		printEmergency(loadTrunks(t, noDesignation),
			[]checkedLine{lineResult(policy.DefaultLine, "telnyx", "+15125550100")})
	})
	if !strings.Contains(inferred, "inferred") || !strings.Contains(inferred, "primary line") {
		t.Errorf("an undesignated trunk should be reported as inferred from policy.toml:\n%s", inferred)
	}
	if !strings.Contains(inferred, "unknown") {
		t.Errorf("an undeclared e911 should read as unknown, not as either answer:\n%s", inferred)
	}

	// Nothing set anywhere is a real state, and inventing an answer from
	// declaration order is exactly what the plan rejects.
	none := capture(t, func() {
		printEmergency(loadTrunks(t, noDesignation), []checkedLine{lineResult(policy.DefaultLine, "", "")})
	})
	if !strings.Contains(none, "undecided") {
		t.Errorf("with nothing set, check must say so:\n%s", none)
	}
	if strings.Contains(none, "voipms") {
		t.Errorf("check inferred a trunk from declaration order:\n%s", none)
	}
}

// Honesty about today: the key settles where 911 will go, and nothing routes
// on it yet. Claiming otherwise would be the worst kind of wrong in this file.
func TestCheckDoesNotClaim911AlreadyRoutesByTrunk(t *testing.T) {
	out := capture(t, func() {
		printEmergency(loadTrunks(t, cliTrunks), []checkedLine{lineResult(policy.DefaultLine, "voipms", "")})
	})
	for _, want := range []string{"not built", "supplementary phone", "street address"} {
		if !strings.Contains(out, want) {
			t.Errorf("the emergency block should say %q:\n%s", want, out)
		}
	}
}

func TestAnnounceEmergencyTrunkIsSilentWithoutAnInventory(t *testing.T) {
	var b strings.Builder
	log := slog.New(slog.NewTextHandler(&b, nil))
	announceEmergencyTrunk(&policy.Trunks{}, "", log)
	if b.Len() != 0 {
		t.Errorf("startup spoke about trunks with no trunks.toml: %s", b.String())
	}

	announceEmergencyTrunk(loadTrunks(t, cliTrunks), "", log)
	if !strings.Contains(b.String(), "voipms") || !strings.Contains(b.String(), "chosen") {
		t.Errorf("startup should name the emergency trunk and how: %s", b.String())
	}
}

// ── `doorman render` ─────────────────────────────────────────────────────

// The other half of the compatibility gate: with no trunks.toml, render
// writes exactly the two files it always wrote.
func TestRenderWithoutATrunkInventoryWritesOnlyTheHandsetFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "handsets.toml", renderHandsets)
	writeFile(t, dir, "policy.toml", renderPolicy)
	writeFile(t, dir, ".env", "HANDSET_KITCHEN_PASSWORD=k\nHANDSET_OFFICE_PASSWORD=o\n")
	out := filepath.Join(dir, "generated")

	rc := 1
	capture(t, func() {
		rc = runRender([]string{
			"-handsets", filepath.Join(dir, "handsets.toml"),
			"-policy", filepath.Join(dir, "policy.toml"),
			"-trunks", filepath.Join(dir, "trunks.toml"), // deliberately absent
			"-env", filepath.Join(dir, ".env"),
			"-out", out,
		})
	})
	if rc != 0 {
		t.Fatalf("render exited %d", rc)
	}

	got := generatedFiles(t, out)
	want := "extensions_handsets.conf pjsip_handsets.conf"
	if got != want {
		t.Errorf("generated %q, want %q — no trunks.toml must generate no trunk files", got, want)
	}
}

func TestRenderWritesTheTrunkFilesWhenThereIsAnInventory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "handsets.toml", renderHandsets)
	writeFile(t, dir, "policy.toml", trunkPolicy("voipms"))
	writeFile(t, dir, "trunks.toml", cliTrunks)
	writeFile(t, dir, ".env", "HANDSET_KITCHEN_PASSWORD=k\nHANDSET_OFFICE_PASSWORD=o\n"+
		"TRUNK_VOIPMS_PASSWORD=v\nTRUNK_TELNYX_PASSWORD=t\n")
	out := filepath.Join(dir, "generated")

	rc := 1
	capture(t, func() {
		rc = runRender([]string{
			"-handsets", filepath.Join(dir, "handsets.toml"),
			"-policy", filepath.Join(dir, "policy.toml"),
			"-trunks", filepath.Join(dir, "trunks.toml"),
			"-env", filepath.Join(dir, ".env"),
			"-out", out,
		})
	})
	if rc != 0 {
		t.Fatalf("render exited %d", rc)
	}

	got := generatedFiles(t, out)
	want := "extensions_handsets.conf extensions_trunks.conf pjsip_handsets.conf pjsip_trunks.conf"
	if got != want {
		t.Errorf("generated %q, want %q", got, want)
	}

	pjsip, err := os.ReadFile(filepath.Join(out, "pjsip_trunks.conf"))
	if err != nil {
		t.Fatal(err)
	}
	// The secret came from .env and nowhere else: trunks.toml named it.
	if !strings.Contains(string(pjsip), "password=v") {
		t.Error("the trunk password was not substituted from the environment")
	}
	if strings.Contains(string(cliTrunks), "password=") {
		t.Error("trunks.toml must never carry a password of its own")
	}

	plan, err := os.ReadFile(filepath.Join(out, "extensions_trunks.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan), "exten => 5125550100,1,Goto(cmm-line-default") {
		t.Errorf("the primary line's DID was not routed:\n%s", plan)
	}
}

// A line naming a provider that does not exist must fail where a person is
// standing, not produce a route in a context nothing generated.
func TestRenderRefusesALineNamingAnUnknownTrunk(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "handsets.toml", renderHandsets)
	writeFile(t, dir, "policy.toml", trunkPolicy("nope"))
	writeFile(t, dir, "trunks.toml", cliTrunks)
	writeFile(t, dir, ".env", "HANDSET_KITCHEN_PASSWORD=k\nHANDSET_OFFICE_PASSWORD=o\n"+
		"TRUNK_VOIPMS_PASSWORD=v\nTRUNK_TELNYX_PASSWORD=t\n")

	rc := 0
	capture(t, func() {
		rc = runRender([]string{
			"-handsets", filepath.Join(dir, "handsets.toml"),
			"-policy", filepath.Join(dir, "policy.toml"),
			"-trunks", filepath.Join(dir, "trunks.toml"),
			"-env", filepath.Join(dir, ".env"),
			"-out", filepath.Join(dir, "generated"),
		})
	})
	if rc == 0 {
		t.Fatal("render accepted a line naming a trunk that does not exist")
	}
}

func generatedFiles(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, " ")
}
