package syncwire

import (
	"strings"
	"testing"
)

func TestClosedWireAndRedactionProjection(t *testing.T) {
	for _, body := range []string{
		`{"id":"bedtime","start":"21:00","end":"07:00","enabled":true,"days":[],"pin":"123456"}`,
		`{"id":"bedtime","start":"21:00","end":"07:00","enabled":true,"days":["nonday"]}`,
		`{"id":"bedtime","start":"25:00","end":"07:00","enabled":true,"days":[]}`,
		`{"id":"bedtime","start":"21:00","end":"07:00","enabled":"true","days":[]}`,
		`{"id":"bedtime","start":"21:00","end":"07:00","enabled":true,"days":null}`,
	} {
		var schedule Schedule
		if Decode("Schedule", []byte(body), &schedule) == nil {
			t.Fatal("accepted invalid schedule")
		}
	}
	secret := map[string]any{"handsets": []any{map[string]any{"id": "kitchen", "endpoint": "PJSIP/kitchen", "password": "must-never-leave"}}, "unknown": []any{"sensitive"}}
	projected, err := Project("HandsetValues", secret)
	if err != nil {
		t.Fatal(err)
	}
	value := projected.(map[string]any)
	if len(value) != 1 || len(value["handsets"].([]any)[0].(map[string]any)) != 2 {
		t.Fatal("unknown values escaped the projection")
	}
	if _, err := Project("Unknown", secret); err == nil {
		t.Fatal("accepted unknown schema")
	}
	if Decode("Unknown", []byte("{}"), &value) == nil {
		t.Fatal("decoded unknown schema")
	}
	if Decode("Schedule", []byte(strings.Repeat(" ", 2<<20+1)), &value) == nil {
		t.Fatal("decoded oversized message")
	}
}

func TestProjectDropsRefusedZeroValuesAndExplainNamesThePath(t *testing.T) {
	raw := map[string]any{"house": map[string]any{"handsets": []any{"kitchen"}, "voicemail": ""},
		"extensions": []any{map[string]any{"label": "L", "pinSet": true, "handsets": []any{}, "steps": []any{map[string]any{"handsets": []any{"kitchen"}, "rings": 0.0, "seconds": 5.0}}}}}
	projected, err := Project("PolicyValues", raw)
	if err != nil {
		t.Fatalf("an empty optional value was refused: %v (%s)", err, Explain("PolicyValues", raw))
	}
	house := projected.(map[string]any)["house"].(map[string]any)
	if _, ok := house["voicemail"]; ok {
		t.Fatal("empty voicemail survived projection")
	}
	// A required value is never dropped, so an empty one still fails.
	if _, err := Project("HandsetValues", map[string]any{"handsets": []any{}}); err == nil {
		t.Fatal("empty required list accepted")
	}
	bad := map[string]any{"schedules": []any{map[string]any{"id": "ok", "start": "21:00", "end": "07:00"}, map[string]any{"id": "x", "start": "nope", "end": "07:00"}}}
	if got := Explain("PolicyValues", bad); got != "schedules[1].start" {
		t.Fatalf("Explain = %q", got)
	}
	if got := Explain("PolicyValues", map[string]any{"house": map[string]any{}}); got != "house.handsets (missing)" {
		t.Fatalf("Explain = %q", got)
	}
	if got := Explain("PolicyValues", map[string]any{"nope": 1}); got != "nope (not allowed)" {
		t.Fatalf("Explain = %q", got)
	}
	if Explain("PolicyValues", map[string]any{}) != "" || Explain("Unknown", nil) != "Unknown" {
		t.Fatal("Explain on a match or an unknown schema")
	}
}
