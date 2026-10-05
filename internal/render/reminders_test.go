package render

import (
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

func TestReminderMenusUseAuthenticatedEndpointsAndBypassCurfew(t *testing.T) {
	f, err := Build(fixture(), env(secrets), nil, map[string][]policy.CurfewWindow{"kids-room": {{Window: &policy.Afterhours{StartMin: 21 * 60, EndMin: 7 * 60, Days: [7]bool{true, true, true, true, true, true, true}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"kitchen", "kids-room"} {
		if !strings.Contains(f.PJSIP, "set_var=CMM_REMINDER_HANDSET="+id+"\n") {
			t.Fatalf("no trusted marker for %s", id)
		}
	}
	if strings.Contains(f.PJSIP, "CMM_REMINDER_HANDSET=conference") {
		t.Fatal("pseudo endpoint gained access")
	}
	menu := f.Dialplan[strings.Index(f.Dialplan, "; *80:"):strings.Index(f.Dialplan, "; *88:")]
	for _, code := range []string{"80", "81", "82"} {
		if !strings.Contains(menu, "exten => *"+code+",1,Answer()") || !strings.Contains(menu, "AGI(/opt/call-me-maybe/bin/doorman,reminders,menu,"+code+")") {
			t.Fatalf("missing app %s", code)
		}
	}
	if strings.Contains(menu, "CALLERID") || strings.Contains(menu, "DND_") || strings.Contains(menu, "Auto-Answer") {
		t.Fatal("menu trusts caller ID or overrides local DND")
	}
	if strings.Count(menu, "Set(AGISIGHUP=no)") != 3 || strings.Count(menu, "Set(CHANNEL(accountcode)=cmm-reminder)") != 3 {
		t.Fatal("missing hangup/curfew safety")
	}
	curfew := f.Dialplan[strings.Index(f.Dialplan, "[curfew-kids-room]"):]
	gate := strings.Index(curfew, "GotoIf")
	for _, code := range []string{"80", "81", "82"} {
		if at := strings.Index(curfew, "exten => *"+code+",1,Goto(internal,${EXTEN},1)"); at < 0 || at > gate {
			t.Fatalf("curfew hides app %s", code)
		}
	}
	if strings.Contains(f.Dialplan, "include => cmm-reminder-deliver") {
		t.Fatal("delivery accessible by dialing")
	}
}
