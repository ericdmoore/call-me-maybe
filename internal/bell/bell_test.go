package bell

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var (
	errNetwork = errors.New("account: service connection failed")
	gone       = fmt.Errorf("%w: account: this box was removed", ErrGone)
)

// fakeService is the hosted side of the bell, as small as the contract: it
// answers "ping" with "pong" unless told not to, and hands each accepted
// socket to the test through conns.
type fakeService struct {
	t      *testing.T
	server *httptest.Server
	conns  chan *serverConn
	silent atomic.Bool // stop answering pings
	pings  atomic.Int32
	other  chan string // anything the box sent that was not "ping"
	closes chan int    // close codes the box sent
}

func newFakeService(t *testing.T) *fakeService {
	f := &fakeService{t: t, conns: make(chan *serverConn, 16), other: make(chan string, 16), closes: make(chan int, 16)}
	upgrader := websocket.Upgrader{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		sc := &serverConn{conn: conn}
		go func() {
			for {
				_, data, err := conn.ReadMessage()
				if err != nil {
					var closed *websocket.CloseError
					if errors.As(err, &closed) {
						f.closes <- closed.Code
					}
					return
				}
				if string(data) != "ping" {
					f.other <- string(data)
					continue
				}
				f.pings.Add(1)
				if !f.silent.Load() {
					_ = sc.WriteMessage(websocket.TextMessage, []byte("pong"))
				}
			}
		}()
		f.conns <- sc
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeService) dial(ctx context.Context) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(f.server.URL, "http"), nil)
	if err != nil {
		return nil, errNetwork
	}
	return conn, nil
}

// serverConn serialises the service's writes: gorilla allows one writer, and
// both the pong goroutine and the test write.
type serverConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *serverConn) WriteMessage(kind int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(kind, data)
}

func (c *serverConn) WriteControl(kind int, data []byte, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteControl(kind, data, deadline)
}

func (c *serverConn) Close() error { return c.conn.Close() }

func (f *fakeService) next(t *testing.T) *serverConn {
	t.Helper()
	select {
	case c := <-f.conns:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("the box never connected")
		return nil
	}
}

// recorder collects what a Bell reports to its caller.
type recorder struct {
	rings   chan struct{}
	health  chan bool
	mu      sync.Mutex
	lines   []string
	tag     atomic.Value
	credMu  sync.Mutex
	credErr error
}

func (r *recorder) setCredErr(err error) {
	r.credMu.Lock()
	defer r.credMu.Unlock()
	r.credErr = err
}

func newRecorder() *recorder {
	r := &recorder{rings: make(chan struct{}, 64), health: make(chan bool, 64)}
	r.tag.Store("first")
	return r
}

func (r *recorder) log(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
}

func (r *recorder) logged() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

func (r *recorder) credential(context.Context) (string, error) {
	r.credMu.Lock()
	err := r.credErr
	r.credMu.Unlock()
	if err != nil {
		return "", err
	}
	return r.tag.Load().(string), nil
}

func (r *recorder) bell(dial func(context.Context) (*websocket.Conn, error)) *Bell {
	return &Bell{Dial: dial, Credential: r.credential,
		Ring:    func() { r.rings <- struct{}{} },
		Healthy: func(up bool) { r.health <- up },
		Log:     r.log,
		// Real but short timings, as the lobby tests use.
		Heartbeat: 40 * time.Millisecond, PongWait: 60 * time.Millisecond,
		MinBackoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
		Stable: time.Hour, RefusedCheck: 10 * time.Millisecond,
	}
}

func run(t *testing.T, b *Bell) (cancel func(), done chan error) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return stop, done
}

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func drain[T any](ch <-chan T) int {
	n := 0
	for {
		select {
		case <-ch:
			n++
		default:
			return n
		}
	}
}

func TestARingAsksForASyncAndEverythingElseIsIgnored(t *testing.T) {
	f := newFakeService(t)
	r := newRecorder()
	run(t, r.bell(f.dial))
	conn := f.next(t)
	if !waitFor(t, r.health, "healthy") {
		t.Fatal("first transition was not up")
	}
	// Connecting rings once: anything queued while the bell was down rang
	// nobody.
	waitFor(t, r.rings, "catch-up ring")

	for _, junk := range []string{`{"t":"reboot"}`, `{"t":"sync"`, `sync`, `PONG`, `{"t":1}`, `[]`} {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(junk))
	}
	_ = conn.WriteMessage(websocket.BinaryMessage, []byte(`{"t":"sync"}`))
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"sync"}`))
	waitFor(t, r.rings, "ring")
	time.Sleep(50 * time.Millisecond)
	if n := drain(r.rings); n != 0 {
		t.Fatalf("%d rings from messages that were not a sync signal", n)
	}
	select {
	case got := <-f.other:
		t.Fatalf("the box sent something other than ping: %q", got)
	default:
	}
	for _, line := range r.logged() {
		if strings.Contains(line, "reboot") || strings.Contains(line, "sync\"") {
			t.Fatalf("a payload reached the log: %q", line)
		}
	}
}

func TestHeartbeatPingsAndANoPongReconnects(t *testing.T) {
	f := newFakeService(t)
	r := newRecorder()
	run(t, r.bell(f.dial))
	f.next(t)
	waitFor(t, r.health, "healthy")
	eventually(t, "regular pings", func() bool { return f.pings.Load() >= 3 })

	f.silent.Store(true)
	if waitFor(t, r.health, "unhealthy") {
		t.Fatal("expected the bell to go down without pongs")
	}
	f.next(t) // and it dials again
	eventually(t, "disconnect logged", func() bool {
		for _, l := range r.logged() {
			if l == "bell: disconnected (no heartbeat answer); polling as usual" {
				return true
			}
		}
		return false
	})
	// A socket that never answers is never healthy.
	select {
	case up := <-r.health:
		t.Fatalf("reported healthy=%t without a pong", up)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestServerClosesReconnectAndRepeatedReplacementSaysSoOnce(t *testing.T) {
	f := newFakeService(t)
	r := newRecorder()
	run(t, r.bell(f.dial))
	closeWith := func(code int) {
		conn := f.next(t)
		waitFor(t, r.health, "healthy")
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, "bye"), time.Now().Add(time.Second))
		waitFor(t, r.health, "unhealthy")
	}
	closeWith(CloseHeartbeatTimeout)
	for range replacedWarnAfter + 2 {
		closeWith(CloseReplaced)
	}
	closeWith(websocket.ClosePolicyViolation)
	closeWith(websocket.CloseGoingAway)
	f.next(t)
	lines := strings.Join(r.logged(), "\n")
	for _, want := range []string{"(service heartbeat timeout)", "(replaced)", "(protocol)", "(closed by the service)"} {
		if !strings.Contains(lines, want) {
			t.Errorf("no %s line in:\n%s", want, lines)
		}
	}
	if n := strings.Count(lines, "second `doorman sync run`"); n != 1 {
		t.Errorf("replacement warning said %d times:\n%s", n, lines)
	}
	if n := strings.Count(lines, "(replaced)"); n != 1 {
		t.Errorf("an unstable bell repeated its replaced line %d times", n)
	}
	if strings.Contains(lines, "bye") {
		t.Error("a close reason from the service reached the log")
	}
}

func TestAnOversizedFrameDropsTheSocket(t *testing.T) {
	f := newFakeService(t)
	r := newRecorder()
	run(t, r.bell(f.dial))
	conn := f.next(t)
	waitFor(t, r.health, "healthy")
	_ = conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", maxFrame+1)))
	waitFor(t, r.health, "unhealthy")
	eventually(t, "protocol line", func() bool {
		return strings.Contains(strings.Join(r.logged(), "\n"), "disconnected (protocol)")
	})
}

func TestRefusalWaitsForANewCredentialInsteadOfRetrying(t *testing.T) {
	for _, refusal := range []error{
		&Refused{Reason: "401 unauthorized"},
		&Refused{Reason: "403 sync_required"},
		&Refused{Reason: "sign in with doorman login"},
	} {
		r := newRecorder()
		var dials atomic.Int32
		b := r.bell(func(context.Context) (*websocket.Conn, error) {
			dials.Add(1)
			return nil, refusal
		})
		cancel, _ := run(t, b)
		eventually(t, "first dial", func() bool { return dials.Load() == 1 })
		time.Sleep(100 * time.Millisecond) // ten credential checks, no dial
		if n := dials.Load(); n != 1 {
			t.Fatalf("%v: retried a refused credential %d times", refusal, n-1)
		}
		r.tag.Store("second")
		eventually(t, "dial with the new credential", func() bool { return dials.Load() == 2 })
		cancel()
		lines := r.logged()
		if len(lines) == 0 || !strings.Contains(lines[0], "refused for this credential") {
			t.Fatalf("%v: refusal not reported: %q", refusal, lines)
		}
	}
}

func TestACredentialThatCannotBeReadIsARefusalToo(t *testing.T) {
	r := newRecorder()
	r.setCredErr(&Refused{Reason: "sign in with doorman login"})
	var dials atomic.Int32
	run(t, r.bell(func(context.Context) (*websocket.Conn, error) { dials.Add(1); return nil, errNetwork }))
	time.Sleep(60 * time.Millisecond)
	if dials.Load() != 0 {
		t.Fatal("dialled without a credential")
	}
	r.setCredErr(nil)
	eventually(t, "dial after login", func() bool { return dials.Load() > 0 })
}

func TestRemovalRingsOnceAndStops(t *testing.T) {
	r := newRecorder()
	_, done := run(t, r.bell(func(context.Context) (*websocket.Conn, error) { return nil, gone }))
	if err := waitFor(t, done, "Run to return"); !errors.Is(err, gone) {
		t.Fatalf("Run returned %v", err)
	}
	waitFor(t, r.rings, "the ring that lets sync see the removal")
	done <- nil // let cleanup find a result

	// Removed while waiting out a refusal.
	r = newRecorder()
	_, done = run(t, r.bell(func(context.Context) (*websocket.Conn, error) {
		return nil, &Refused{Reason: "401 unauthorized"}
	}))
	eventually(t, "the refusal", func() bool { return len(r.logged()) > 0 })
	r.setCredErr(gone)
	if err := waitFor(t, done, "Run to return"); !errors.Is(err, gone) {
		t.Fatalf("Run returned %v", err)
	}
	done <- nil
}

func TestUnavailableKeepsTryingWithBackoffAndLogsOnce(t *testing.T) {
	for _, unavailable := range []error{
		errors.New("account: service refused request (404, service_error)"),
		errors.New("account: service refused request (426, upgrade_required)"),
		errNetwork,
	} {
		r := newRecorder()
		var mu sync.Mutex
		var at []time.Time
		b := r.bell(func(context.Context) (*websocket.Conn, error) {
			mu.Lock()
			at = append(at, time.Now())
			mu.Unlock()
			return nil, unavailable
		})
		cancel, _ := run(t, b)
		eventually(t, "several attempts", func() bool { mu.Lock(); defer mu.Unlock(); return len(at) >= 6 })
		cancel()
		if lines := r.logged(); len(lines) != 1 || !strings.Contains(lines[0], "bell: unavailable (") {
			t.Fatalf("%v: want one line for a repeated failure, got %q", unavailable, lines)
		}
		if drain(r.health) != 0 || drain(r.rings) != 0 {
			t.Fatalf("%v: an unavailable bell reported health or rang", unavailable)
		}
	}
}

func TestBackoffGrowsWithBoundedJitterAndHonoursRetryAfter(t *testing.T) {
	b := &Bell{}
	b.defaults()
	b.Jitter = func(d time.Duration) time.Duration { return d }
	want := []time.Duration{1, 2, 4, 8, 16, 32, 64, 128, 256, 300, 300}
	for i, w := range want {
		if got := b.backoff(i, nil); got != w*time.Second {
			t.Fatalf("failure %d: %v, want %v", i, got, w*time.Second)
		}
	}
	if got := b.backoff(1000, nil); got != DefaultMaxBackoff {
		t.Fatalf("a long outage overflowed the cap: %v", got)
	}
	b.Jitter = func(time.Duration) time.Duration { return 0 }
	if got := b.backoff(3, nil); got != 4*time.Second {
		t.Fatalf("jitter floor is not half: %v", got)
	}
	if got := b.backoff(0, &Later{Err: errNetwork, After: time.Minute}); got != time.Minute {
		t.Fatalf("Retry-After ignored: %v", got)
	}
	b.Jitter = nil
	b.defaults()
	for i := range 200 {
		d := b.backoff(i%12, nil)
		ceiling := min(DefaultMinBackoff<<(i%12), DefaultMaxBackoff)
		if d < ceiling/2 || d > ceiling {
			t.Fatalf("random jitter out of [%v, %v]: %v", ceiling/2, ceiling, d)
		}
	}
}

func TestAStableConnectionResetsBackoffAndLogMemory(t *testing.T) {
	f := newFakeService(t)
	r := newRecorder()
	b := r.bell(f.dial)
	b.Stable = 30 * time.Millisecond
	b.MinBackoff, b.MaxBackoff = time.Millisecond, 10*time.Second
	run(t, b)
	for range 3 {
		conn := f.next(t)
		waitFor(t, r.health, "healthy")
		time.Sleep(50 * time.Millisecond) // longer than Stable
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(CloseHeartbeatTimeout, ""), time.Now().Add(time.Second))
		waitFor(t, r.health, "unhealthy")
	}
	f.next(t) // quickly: backoff restarted at a millisecond every time
	lines := strings.Join(r.logged(), "\n")
	if n := strings.Count(lines, "bell: connected"); n != 3 {
		t.Fatalf("stable sessions should each log their transitions, got %d:\n%s", n, lines)
	}
}

func TestShutdownClosesTheSocketCleanly(t *testing.T) {
	f := newFakeService(t)
	r := newRecorder()
	cancel, done := run(t, r.bell(f.dial))
	f.next(t)
	waitFor(t, r.health, "healthy")
	cancel()
	if err := waitFor(t, done, "Run"); err != nil {
		t.Fatalf("Run returned %v after cancel", err)
	}
	done <- nil
	select {
	case code := <-f.closes:
		if code != websocket.CloseNormalClosure {
			t.Fatalf("server saw close %d, want a normal close", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no close frame")
	}
}

func TestCancelWhileDiallingOrWaiting(t *testing.T) {
	r := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	f := newFakeService(t)
	b := r.bell(func(c context.Context) (*websocket.Conn, error) {
		conn, err := f.dial(c)
		cancel() // the context ends while the socket is being opened
		return conn, err
	})
	if err := b.Run(ctx); err != nil {
		t.Fatal(err)
	}
	// Waiting out a backoff, and waiting for a new credential.
	for _, err := range []error{errNetwork, &Refused{Reason: "401 unauthorized"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		b := r.bell(func(context.Context) (*websocket.Conn, error) { return nil, err })
		b.MinBackoff, b.MaxBackoff, b.RefusedCheck = time.Hour, time.Hour, time.Hour
		if got := b.Run(ctx); got != nil {
			t.Fatal(got)
		}
		cancel()
	}
	// Waiting out the backoff after a disconnect.
	f = newFakeService(t)
	ctx, cancel = context.WithCancel(context.Background())
	b = r.bell(f.dial)
	b.MinBackoff, b.MaxBackoff = time.Hour, time.Hour
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	conn := f.next(t)
	<-r.health
	_ = conn.Close()
	<-r.health
	cancel()
	if err := waitFor(t, done, "Run"); err != nil {
		t.Fatal(err)
	}
}

func TestQuietLinesAndOptionalCallbacks(t *testing.T) {
	b := &Bell{Quiet: time.Hour}
	b.defaults()
	b.say("nobody listening") // no Log: nothing to do
	b.setHealthy(true)        // no Healthy: nothing to call
	var got []string
	b.Log = func(l string) { got = append(got, l) }
	b.say("a")
	b.say("a")
	b.say("b")
	b.Quiet = time.Nanosecond
	time.Sleep(time.Millisecond)
	b.say("a")
	if strings.Join(got, ",") != "a,b,a" {
		t.Fatalf("rate limit: %q", got)
	}
	if closeClass(errors.New("read tcp: connection reset")) != "network" {
		t.Fatal("a dropped connection is not a network failure")
	}
	if l := (&Later{Err: errNetwork}); l.Error() != errNetwork.Error() || !errors.Is(l, errNetwork) {
		t.Fatal("Later hides its cause")
	}
}
