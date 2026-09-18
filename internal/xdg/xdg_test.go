package xdg

import (
	"fmt"
	"testing"
)

func TestDir(t *testing.T) {
	home := func() (string, error) { return "/home/operator", nil }
	empty := func(string) string { return "" }
	if got := Dir("CONFIG", empty, home); got != "/home/operator/.config" {
		t.Fatal(got)
	}
	if got := Dir("STATE", empty, home); got != "/home/operator/.local/state" {
		t.Fatal(got)
	}
	if got := Dir("STATE", func(k string) string {
		if k == "XDG_STATE_HOME" {
			return "/state"
		}
		return ""
	}, home); got != "/state" {
		t.Fatal(got)
	}
	if got := Dir("STATE", empty, func() (string, error) { return "", fmt.Errorf("no home") }); got != "" {
		t.Fatal(got)
	}
}
