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

// [cmm-listen] (#155) is reached only by doorman's release: nothing includes
// it, no handset dials into it, and an unset title list or limit refuses
// rather than meaning "everything" or "forever".
func TestListenContextIsReachableOnlyByRelease(t *testing.T) {
	f, err := Build(fixture(), env(secrets), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := f.BooksDialplan
	at := strings.Index(plan, "["+ListenContext+"]\n")
	if at < 0 {
		t.Fatalf("no [%s] context:\n%s", ListenContext, plan)
	}
	handsets, listen := plan[:at], plan[at:]
	if strings.Contains(handsets, "books,listen") || strings.Contains(listen, "books,menu") {
		t.Fatal("the handset menu and the outside listener share an entry point")
	}
	for _, generated := range []string{f.Dialplan, f.BooksDialplan} {
		if strings.Contains(generated, "include => "+ListenContext) || strings.Contains(generated, "Goto("+ListenContext) {
			t.Fatalf("something leads into [%s] besides doorman", ListenContext)
		}
	}
	order := []string{
		`GotoIf($["${CMM_LISTEN_TITLES}" = "" | "${CMM_LISTEN_SECONDS}" = "" | "${CMM_LISTEN_MAX}" = "" | "${CMM_LISTEN_GROUP}" = ""]?refused)`,
		"Set(GROUP(" + ListenGroupCategory + ")=${CMM_LISTEN_GROUP})",
		"GotoIf($[${GROUP_COUNT(${CMM_LISTEN_GROUP}@" + ListenGroupCategory + ")} > ${CMM_LISTEN_MAX}]?busy)",
		"Set(TIMEOUT(absolute)=${CMM_LISTEN_SECONDS})",
		"AGI(/opt/call-me-maybe/bin/doorman,books,listen)",
		"n(busy),Set(GROUP(" + ListenGroupCategory + ")=)",
		"Playback(call-me-maybe/system/listen-busy)",
		"n(refused),Hangup()",
	}
	last := -1
	for _, want := range order {
		i := strings.Index(listen, want)
		if i < 0 {
			t.Fatalf("[%s] lacks %q:\n%s", ListenContext, want, listen)
		}
		if i < last {
			t.Fatalf("%q is out of order: the group is joined before it is counted, and the limit set before the AGI", want)
		}
		last = i
	}
	if strings.Contains(listen, "CALLERID") || strings.Contains(listen, "PJSIP_HEADER") {
		t.Fatal("the listener context reads something the caller controls")
	}
}
