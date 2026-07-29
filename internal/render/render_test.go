package render

import (
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

func env(m map[string]string) Env {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func fixture() []policy.Handset {
	return []policy.Handset{
		{ID: "kitchen", Label: "Kitchen", Endpoint: "PJSIP/kitchen", Number: 101,
			Page: true, Mailbox: "adults", PasswordEnv: "HANDSET_KITCHEN_PASSWORD"},
		{ID: "kids-room", Label: "Kids' room", Endpoint: "PJSIP/kids-room", Number: 105,
			PasswordEnv: "HANDSET_KIDS_ROOM_PASSWORD"},
		// Pseudo-handset: valid policy target, nothing to generate.
		{ID: "conference", Label: "Conference", Endpoint: "Local/600@internal"},
	}
}

var secrets = map[string]string{
	"HANDSET_KITCHEN_PASSWORD":   "kitchen-secret",
	"HANDSET_KIDS_ROOM_PASSWORD": "kids-secret",
}

func TestRenderGeneratesAllThreePlacesFromOneFile(t *testing.T) {
	f, err := Build(fixture(), env(secrets))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if f.Generated != 2 {
		t.Errorf("generated %d, want 2 (Local pseudo-handset skipped)", f.Generated)
	}
	for _, want := range []string{
		"[kitchen]", "auth=kitchen-auth", "password=kitchen-secret",
		"mailboxes=adults@household", "dtmf_mode=rfc4733", "callerid=Kitchen <101>",
	} {
		if !strings.Contains(f.PJSIP, want) {
			t.Errorf("pjsip missing %q", want)
		}
	}
	for _, want := range []string{
		"exten => 101,1,Dial(PJSIP/kitchen,30)",
		"exten => 101,hint,PJSIP/kitchen",
		"exten => 100,1,Dial(PJSIP/kids-room&PJSIP/kitchen,30)",
		"exten => 500,1,Page(PJSIP/kitchen,ib(page-autoanswer^s^1),60)",
	} {
		if !strings.Contains(f.Dialplan, want) {
			t.Errorf("dialplan missing %q", want)
		}
	}
	if strings.Contains(f.PJSIP, "Local/") || strings.Contains(f.Dialplan, "conference") {
		t.Error("pseudo-handset leaked into generated config")
	}
}

func TestRenderRefusesMissingSecrets(t *testing.T) {
	_, err := Build(fixture(), env(map[string]string{
		"HANDSET_KITCHEN_PASSWORD": "x",
		// kids-room secret absent
	}))
	if err == nil || !strings.Contains(err.Error(), "HANDSET_KIDS_ROOM_PASSWORD") {
		t.Errorf("err = %v, want the missing variable named", err)
	}
}

func TestRenderRefusesEndpointIdMismatch(t *testing.T) {
	h := fixture()
	h[0].Endpoint = "PJSIP/cocina"
	_, err := Build(h, env(secrets))
	if err == nil || !strings.Contains(err.Error(), "PJSIP/kitchen") {
		t.Errorf("err = %v, want naming-rule violation", err)
	}
}

func TestGeneratedFilesCarryTheDoNotEditHeader(t *testing.T) {
	f, err := Build(fixture(), env(secrets))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"pjsip": f.PJSIP, "dialplan": f.Dialplan} {
		if !strings.Contains(body, "DO NOT EDIT") {
			t.Errorf("%s fragment missing the generated-file header", name)
		}
	}
}
