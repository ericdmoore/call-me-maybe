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
