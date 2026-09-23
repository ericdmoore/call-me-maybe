package provision

import (
	"encoding/xml"
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

func samplePhone() Phone {
	m, _ := Lookup("grandstream-wp826")
	return Phone{
		ID: "kitchen", Label: "Kitchen & Co <3", MAC: "ec:74:d7:88:a2:54", Model: m, Number: 101, Page: true,
		Mailbox: "kitchen", Books: []string{"house", "people"},
		Address: Address{Host: "192.168.7.133", Port: 8443}, Timezone: "America/Chicago",
		SIPPassword: "sip-secret", AdminPassword: "Admin1secret", ProvisionPassword: "prov-secret",
	}
}

type gsFile struct {
	XMLName xml.Name `xml:"gs_provision"`
	MAC     string   `xml:"mac"`
	Config  struct {
		Items []struct {
			XMLName xml.Name
			Value   string `xml:",chardata"`
		} `xml:",any"`
	} `xml:"config"`
}

func parse(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var f gsFile
	if err := xml.Unmarshal(body, &f); err != nil {
		t.Fatalf("rendered file is not well-formed XML: %v\n%s", err, body)
	}
	if f.MAC != "ec74d788a254" {
		t.Errorf("mac element %q", f.MAC)
	}
	out := map[string]string{}
	for _, it := range f.Config.Items {
		out[it.XMLName.Local] = it.Value
	}
	return out
}

// The file is the phone-side half of the invariants; each pinned value has
// a reason in the table and this test is what keeps the reasons true.
func TestGrandstreamFilePinsTheInvariants(t *testing.T) {
	body, err := Render(samplePhone())
	if err != nil {
		t.Fatal(err)
	}
	v := parse(t, body)
	want := map[string]string{
		"P271": "1", "P270": "Kitchen & Co <3-101", "P47": "192.168.7.133", "P35": "kitchen", "P36": "kitchen",
		"P34": "sip-secret", "P3": "Kitchen & Co <3", "P33": "*97", "P2": "Admin1secret",
		"P73": "0", "P74": "1", "P75": "0", // RFC 2833 only — invariant 9's other half
		"P57": "0", "P58": "9", // ulaw then g722
		"P237": "192.168.7.133:8443/prov", "P212": "2", "P1359": "kitchen", "P1360": "prov-secret", "P194": "0",
		"P330": "192.168.7.133:8444/prov/kitchen", "P331": "3", "P332": "60",
		"P246": "CST6CDT,M3.2.0,M11.1.0",
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %q, want %q", k, v[k], w)
		}
	}
	if strings.Contains(string(body), "&amp;amp;") || !strings.Contains(string(body), "Kitchen &amp; Co &lt;3") {
		t.Error("labels must be XML-escaped exactly once")
	}
}

func TestFileNameIsTheMACAsFirmwareRequestsIt(t *testing.T) {
	if got := samplePhone().FileName(); got != "cfgec74d788a254.xml" {
		t.Errorf("FileName = %q", got)
	}
}

func TestUnknownTimezoneLeavesThePhoneAlone(t *testing.T) {
	p := samplePhone()
	p.Timezone = "Mars/Olympus_Mons"
	body, _ := Render(p)
	if _, has := parse(t, body)["P246"]; has {
		t.Error("an unknown zone must not guess a POSIX string")
	}
}

func TestParseAddressRefusesTheAddressesPhonesCannotUse(t *testing.T) {
	for _, bad := range []string{"", "localhost", "127.0.0.1:8443", "jepsen.local"} {
		if _, err := ParseAddress(bad); err == nil {
			t.Errorf("ParseAddress(%q) accepted", bad)
		}
	}
	a, err := ParseAddress("192.168.7.133")
	if err != nil || a.Port != DefaultPort || a.BaseURL() != "https://192.168.7.133:8443/prov/" {
		t.Errorf("default port: %+v %v", a, err)
	}
	if a, _ := ParseAddress("192.168.7.133:9443"); a.Port != 9443 {
		t.Error("explicit port")
	}
}

func envOf(m map[string]string) Env {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestBuildAllRendersOnlyProvisionableHandsetsAndNamesEveryMissingSecret(t *testing.T) {
	handsets := []policy.Handset{
		{ID: "kitchen", Label: "Kitchen", Endpoint: "PJSIP/kitchen", Number: 101, PasswordEnv: "HANDSET_KITCHEN_PASSWORD", MAC: "EC74D788A254", Model: "grandstream-wp826"},
		{ID: "hall", Label: "Hall", Endpoint: "PJSIP/hall", Number: 102, PasswordEnv: "HANDSET_HALL_PASSWORD"},                                                           // by hand
		{ID: "porch", Label: "Porch", Endpoint: "PJSIP/porch", Number: 103, PasswordEnv: "HANDSET_PORCH_PASSWORD", MAC: "00:0b:82:00:00:03", Model: "grandstream-dp752"}, // no template yet
	}
	full := envOf(map[string]string{
		"PROVISION_ADDRESS": "192.168.7.133", "HANDSET_KITCHEN_PASSWORD": "s", "HANDSET_KITCHEN_ADMIN_PASSWORD": "a", "HANDSET_KITCHEN_PROVISION_PASSWORD": "p",
	})
	b, err := BuildAll(handsets, full, "America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Files) != 1 || b.Files["cfgec74d788a254.xml"] == nil || len(b.Phones) != 1 {
		t.Fatalf("expected exactly the kitchen file, got %v", b.Files)
	}
	if len(b.Notes) != 1 || !strings.Contains(b.Notes[0], "porch") {
		t.Errorf("the untemplated model should be a note, got %v", b.Notes)
	}

	missing := envOf(map[string]string{"PROVISION_ADDRESS": "192.168.7.133", "HANDSET_KITCHEN_PASSWORD": "s"})
	_, err = BuildAll(handsets, missing, "")
	if err == nil || !strings.Contains(err.Error(), "HANDSET_KITCHEN_ADMIN_PASSWORD") || !strings.Contains(err.Error(), "HANDSET_KITCHEN_PROVISION_PASSWORD") {
		t.Errorf("both missing secrets must be named at once, got %v", err)
	}
	if _, err := BuildAll(handsets, envOf(map[string]string{}), ""); err == nil || !strings.Contains(err.Error(), "PROVISION_ADDRESS") {
		t.Errorf("no address must be an error naming the variable, got %v", err)
	}
	// Nothing provisionable → nothing needed, not even the address.
	if b, err := BuildAll(handsets[1:2], envOf(map[string]string{}), ""); err != nil || len(b.Files) != 0 {
		t.Errorf("a by-hand inventory must build empty, got %v %v", b, err)
	}
}
