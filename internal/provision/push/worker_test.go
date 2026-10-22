package push

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"callmemaybe/internal/provision"
)

func TestDiscoveryUsesCompletePrivateUniqueMACMappings(t *testing.T) {
	got := parseNeighbours(strings.NewReader(`IP address HW type Flags HW address Mask Device
192.168.7.54 0x1 0x2 ec:74:d7:88:a2:54 * eth0
192.168.7.55 0x1 0x0 00:11:22:33:44:55 * eth0
8.8.8.8 0x1 0x2 00:11:22:33:44:66 * eth0
192.168.7.60 0x1 0x2 00:11:22:33:44:77 * eth0
192.168.7.61 0x1 0x2 00:11:22:33:44:77 * eth0
192.168.7.62 0x1 0x2 00:00:00:00:00:00 * eth0
192.168.7.63 0x1 0x2 invalid * eth0
malformed
`))
	if len(got) != 1 || got[testPhone().MAC].String() != "192.168.7.54" {
		t.Fatalf("unsafe discovery: %v", got)
	}
	_ = Neighbours() // Non-Linux platforms deliberately have no ARP table.
}

func TestWorkerCoalescesEditsAndDoesNotBlockOtherPhones(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	first := testPhone()
	second := first
	second.ID, second.MAC = "hall", "00:11:22:33:44:55"
	body := "first"
	results := make(chan string, 10)
	block := make(chan struct{})
	worker := Worker{
		Interval: time.Millisecond,
		Documents: func() []Document {
			mu.Lock()
			defer mu.Unlock()
			return []Document{{Phone: first, XML: []byte(body)}, {Phone: second, XML: []byte("hall")}}
		},
		Neighbours: func() map[string]netip.Addr {
			return map[string]netip.Addr{first.MAC: netip.MustParseAddr("192.168.7.54"), second.MAC: netip.MustParseAddr("192.168.7.55")}
		},
		Upload: func(ctx context.Context, p provision.Phone, _ netip.Addr, b []byte) error {
			results <- p.ID + ":" + string(b)
			if p.ID == first.ID && string(b) == "first" {
				select {
				case <-block:
				case <-ctx.Done():
				}
			}
			return nil
		},
	}
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	seen := map[string]bool{}
	for range 2 {
		select {
		case r := <-results:
			seen[r] = true
		case <-time.After(time.Second):
			t.Fatal("one handset blocked another")
		}
	}
	if !seen["kitchen:first"] || !seen["hall:hall"] {
		t.Fatal(seen)
	}
	mu.Lock()
	body = "latest"
	mu.Unlock()
	close(block)
	select {
	case r := <-results:
		if r != "kitchen:latest" {
			t.Fatal(r)
		}
	case <-time.After(time.Second):
		t.Fatal("lost an edit arriving during delivery")
	}
	select {
	case r := <-results:
		t.Fatalf("unchanged phonebook re-uploaded: %s", r)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	<-done
}

func TestWorkerRetriesFailureAndWaitsForDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := testPhone()
	var mu sync.Mutex
	discovered := false
	attempts := make(chan struct{}, 5)
	results := make(chan Result, 5)
	n := 0
	worker := Worker{
		Interval:  5 * time.Millisecond,
		Documents: func() []Document { return []Document{{Phone: p, XML: []byte("book")}} },
		Neighbours: func() map[string]netip.Addr {
			mu.Lock()
			defer mu.Unlock()
			if !discovered {
				return nil
			}
			return map[string]netip.Addr{p.MAC: netip.MustParseAddr("192.168.7.54")}
		},
		Upload: func(context.Context, provision.Phone, netip.Addr, []byte) error {
			n++
			attempts <- struct{}{}
			if n == 1 {
				return ErrUnavailable
			}
			return nil
		},
		OnResult: func(r Result) { results <- r },
	}
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	select {
	case <-attempts:
		t.Fatal("uploaded without a discovered handset")
	case <-time.After(20 * time.Millisecond):
	}
	mu.Lock()
	discovered = true
	mu.Unlock()
	for i := range 2 {
		select {
		case r := <-results:
			if (r.Err == nil) != (i == 1) {
				t.Fatalf("unexpected retry outcome: %v", r)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("did not retry a pending phonebook")
		}
	}
	cancel()
	<-done
}

func TestWorkerHoldsRefusalsUntilTheBookOrPhoneChanges(t *testing.T) {
	for _, refusal := range []error{ErrImport, ErrIdentity, ErrAuth} {
		t.Run(refusal.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mu sync.Mutex
			p := testPhone()
			body := "book"
			attempts := make(chan string, 20)
			worker := Worker{
				Interval: time.Millisecond,
				Documents: func() []Document {
					mu.Lock()
					defer mu.Unlock()
					return []Document{{Phone: p, XML: []byte(body)}}
				},
				Neighbours: func() map[string]netip.Addr {
					return map[string]netip.Addr{p.MAC: netip.MustParseAddr("192.168.7.54")}
				},
				Upload: func(_ context.Context, _ provision.Phone, _ netip.Addr, b []byte) error {
					attempts <- string(b)
					return refusal
				},
			}
			done := make(chan struct{})
			go func() { defer close(done); worker.Run(ctx) }()
			<-attempts
			select {
			case <-attempts:
				t.Fatal("retried a refusal on the short backoff")
			case <-time.After(60 * time.Millisecond):
			}
			mu.Lock()
			body = "edited"
			mu.Unlock()
			select {
			case got := <-attempts:
				if got != "edited" {
					t.Fatal(got)
				}
			case <-time.After(300 * time.Millisecond):
				t.Fatal("a changed book stayed held")
			}
			mu.Lock()
			p.AdminPassword = "corrected-fixture"
			mu.Unlock()
			select {
			case <-attempts:
			case <-time.After(300 * time.Millisecond):
				t.Fatal("a corrected inventory stayed held")
			}
			cancel()
			<-done
		})
	}
}

func TestRefusalsWaitAsLongAsAFailedLogin(t *testing.T) {
	for _, err := range []error{ErrImport, ErrIdentity, ErrAuth} {
		if backoff(err, 1) != holdBackoff {
			t.Fatalf("%v retries an admin login too soon", err)
		}
	}
	if backoff(ErrUnavailable, 1) != time.Second || backoff(ErrUnavailable, 9) != 30*time.Second {
		t.Fatal("an unreachable phone lost its short bounded retry")
	}
}
