package phonegame

import (
	"testing"

	"go.starlark.net/starlark"
)

func TestShufflePreservesNumericPinsAndConfirmation(t *testing.T) {
	p := fixture(t)
	p.ShuffleAnswers = true
	p.Questions[0].Prefix = "test.q1"
	p.Questions[0].Pins = map[string]string{"1": "3"}
	for _, c := range []string{"1", "2", "3", "4"} {
		for _, k := range []string{"1", "2", "3", "4"} {
			p.Clips["test.q1.choice"+c+".key"+k] = Clip{}
		}
	}
	m := &Manager{pack: p, previousLayouts: map[string]map[string]string{}}
	r := &room{m: m}
	r.prepareLayouts()
	first := r.layouts["q1"]
	if first["1"] != "3" {
		t.Fatal("numeric answer moved off matching button")
	}
	r.prepareLayouts()
	different := false
	for key, value := range first {
		if r.layouts["q1"][key] != value {
			different = true
		}
	}
	if !different {
		t.Fatal("new game retained an avoidable identical shuffle")
	}
	reply, e := p.Start("host")
	if e != nil {
		t.Fatal(e)
	}
	reply.Commands = []Command{{Op: "say", Line: "test.q1.choice1"}, {Op: "say", Line: "test.q1.choice2"}, {Op: "say", Line: "test.q1.choice3"}, {Op: "say", Line: "test.q1.choice4"}, {Op: "ask_digit", Keys: "12340", Player: "host", Timeout: 20}}
	commands, e := r.display(reply)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 4; i++ {
		if commands[i].Line[len(commands[i].Line)-1] != byte('1'+i) {
			t.Fatal("answer readings are not in button order")
		}
	}
	key := "3"
	if *r.canonical(reply, &key) != "1" {
		t.Fatal("display answer not mapped back")
	}
	state := starlark.NewDict(1)
	_ = state.SetKey(starlark.String("phase"), starlark.String("confirm"))
	reply.State = state
	if *r.canonical(reply, &key) != "3" {
		t.Fatal("confirmation keys were remapped")
	}
}
