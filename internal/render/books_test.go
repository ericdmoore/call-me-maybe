package render

import (
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

func TestBooksAreLocalAndRespectCurfew(t *testing.T) {
	f, err := Build(fixture(), env(secrets), nil, map[string][]policy.CurfewWindow{"kids-room": {{Window: &policy.Afterhours{StartMin: 21 * 60, EndMin: 7 * 60, Days: [7]bool{true, true, true, true, true, true, true}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.BooksPJSIP, "[kitchen](+type=endpoint)\nset_var=CMM_BOOKS_HANDSET=kitchen\n") || strings.Contains(f.BooksPJSIP, "CMM_BOOKS_HANDSET=conference") {
		t.Fatal("books marker must identify real handsets only")
	}
	menu := f.BooksDialplan
	if !strings.Contains(menu, "[handsets-internal](+)") {
		t.Fatal("books must append to local context")
	}
	for _, want := range []string{"exten => *2665,1,Answer()", "Set(AGISIGHUP=no)", "AGI(/opt/call-me-maybe/bin/doorman,books,menu)"} {
		if !strings.Contains(menu, want) {
			t.Fatal("missing", want)
		}
	}
	curfew := f.Dialplan[strings.Index(f.Dialplan, "[curfew-kids-room]"):]
	if strings.Contains(curfew, "exten => *2665") || strings.Contains(menu, "cmm-reminder") || strings.Contains(menu, "CALLERID") {
		t.Fatal("books bypasses curfew or trusts caller ID")
	}
}
