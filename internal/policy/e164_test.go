package policy

import "testing"

func TestNormaliseFoldsNANPVariants(t *testing.T) {
	variants := []string{"5125550100", "15125550100", "+15125550100", "(512) 555-0100", "512-555-0100"}
	for _, v := range variants {
		if got := E164OrEmpty(v, "1"); got != "+15125550100" {
			t.Errorf("E164OrEmpty(%q) = %q, want +15125550100", v, got)
		}
	}
}

func TestNormalisePassesThroughInternational(t *testing.T) {
	if got := E164OrEmpty("+442071838750", "1"); got != "+442071838750" {
		t.Errorf("got %q", got)
	}
}

func TestNormaliseStrips011Prefix(t *testing.T) {
	if got := E164OrEmpty("011442071838750", "1"); got != "+442071838750" {
		t.Errorf("got %q", got)
	}
}

func TestNormaliseRecognisesWithheldCallerID(t *testing.T) {
	for _, v := range []string{"", "anonymous", "Unknown", "restricted", "  "} {
		if n := NormaliseCallerID(v, "1"); n.Kind != KindAnonymous {
			t.Errorf("NormaliseCallerID(%q).Kind = %v, want anonymous", v, n.Kind)
		}
	}
}

func TestNormaliseRefusesShortCodesAndJunk(t *testing.T) {
	for _, v := range []string{"611", "12345"} {
		if n := NormaliseCallerID(v, "1"); n.Kind != KindUnparseable {
			t.Errorf("NormaliseCallerID(%q).Kind = %v, want unparseable", v, n.Kind)
		}
	}
	if got := E164OrEmpty("611", "1"); got != "" {
		t.Errorf("E164OrEmpty(611) = %q, want empty", got)
	}
}

func TestNormaliseHonoursNonNANPCountryCode(t *testing.T) {
	if got := E164OrEmpty("2071838750", "44"); got != "+442071838750" {
		t.Errorf("got %q", got)
	}
}

func TestRedactKeepsIdentifiableTail(t *testing.T) {
	if got := Redact("+15125550100"); got != "+1512•••0100" {
		t.Errorf("got %q", got)
	}
}
