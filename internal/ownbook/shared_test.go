package ownbook

import (
	"fmt"
	"sync"
	"testing"
)

func TestSharedImportsKeepConcurrentAdditionsAndSeparateHouseHandset(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := Import(dir, "house", []Entry{{Name: "Contact", E164: fmt.Sprintf("+151255501%02d", i)}}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	b, err := Load(dir, "house")
	if err != nil || len(b.Entries) != 10 {
		t.Fatalf("lost concurrent entries: %v %+v", err, b)
	}
	if n, err := Import(dir, "house", b.Entries); n != 0 || err != nil {
		t.Fatal("repeat import not idempotent")
	}
	a, _, _ := SharedLocation(dir, "house")
	c, _, _ := SharedLocation(dir, "handset:house")
	if a == c {
		t.Fatal("house and handset named house collide")
	}
	for _, target := range []string{"../escape", "handset:../../escape", "handset:"} {
		if _, err := Import(dir, target, nil); err == nil {
			t.Fatal("unsafe target accepted")
		}
	}
}
