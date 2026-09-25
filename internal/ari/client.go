// Package ari is a deliberately thin client for the Asterisk REST Interface,
// covering exactly the surface doorman uses and nothing more. Asterisk owns
// SIP, RTP, and DTMF; this package owns the plumbing to ask it questions and
// hear its answers. Everything above it — who gets in — lives in lobby.
package ari

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Event is the union of every ARI event field doorman cares about. Unmodelled
// event types simply arrive with their Type set and no subscribers; unmodelled
// fields are dropped by encoding/json, which is exactly what we want.
type Event struct {
	Type        string    `json:"type"`
	Application string    `json:"application"`
	Timestamp   string    `json:"timestamp"`
	Args        []string  `json:"args"`
	Digit       string    `json:"digit"`
	Cause       int       `json:"cause"`
	Channel     *Channel  `json:"channel"`
	Playback    *Playback `json:"playback"`
}

type Channel struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	State  string   `json:"state"`
	Caller CallerID `json:"caller"`
}

type CallerID struct {
	Name   string `json:"name"`
	Number string `json:"number"`
}

type Playback struct {
	ID        string `json:"id"`
	MediaURI  string `json:"media_uri"`
	TargetURI string `json:"target_uri"`
}

// OriginateParams creates an outbound channel that lands back in our Stasis
// app when answered.
type OriginateParams struct {
	Endpoint string
	AppArgs  string
	CallerID string
	// Timeout is how long Asterisk lets the endpoint ring, in seconds.
	Timeout int
	// Originator copies codec/language from the inbound leg, avoiding a
	// pointless transcode on the Pi.
	Originator string
}

// Error is a non-2xx ARI response.
type Error struct {
	Status int
	Path   string
	Body   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("ari: %d on %s: %s", e.Status, e.Path, e.Body)
}

// IsNotFound reports whether err is a 404 — the normal result of hanging up a
// channel that already went away, and safe to ignore during teardown.
func IsNotFound(err error) bool {
	var ae *Error
	ok := asError(err, &ae)
	return ok && ae.Status == http.StatusNotFound
}

func asError(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Options configures a Client.
type Options struct {
	// BaseURL is e.g. http://127.0.0.1:8088 — no trailing /ari.
	BaseURL  string
	Username string
	Password string
	// App must match Stasis(...) in extensions.conf.
	App          string
	ReconnectMin time.Duration
	ReconnectMax time.Duration
	Log          *slog.Logger
	OnConnection func(bool) // nonblocking observation callback, no error text
}

// Client speaks ARI: REST for commands, a WebSocket for events.
type Client struct {
	baseURL string
	user    string
	pass    string
	app     string

	httpc        *http.Client
	reconnectMin time.Duration
	reconnectMax time.Duration
	log          *slog.Logger
	onConnection func(bool)

	closing    atomic.Bool
	socketMu   sync.Mutex
	socket     *websocket.Conn
	loop       sync.WaitGroup
	stop       chan struct{}
	stopOnce   sync.Once
	dialCtx    context.Context
	cancelDial context.CancelFunc
}

func New(o Options) *Client {
	dialCtx, cancelDial := context.WithCancel(context.Background())
	if o.ReconnectMin <= 0 {
		o.ReconnectMin = 500 * time.Millisecond
	}
	if o.ReconnectMax <= 0 {
		o.ReconnectMax = 30 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Client{stop: make(chan struct{}), dialCtx: dialCtx, cancelDial: cancelDial,
		baseURL:      strings.TrimRight(o.BaseURL, "/"),
		user:         o.Username,
		pass:         o.Password,
		app:          o.App,
		httpc:        &http.Client{Timeout: 10 * time.Second},
		reconnectMin: o.ReconnectMin,
		reconnectMax: o.ReconnectMax,
		log:          o.Log,
		onConnection: o.OnConnection,
	}
}

// AppName is the Stasis application name this client serves.
func (c *Client) AppName() string { return c.app }

// ── REST ─────────────────────────────────────────────────────────────────

func (c *Client) do(ctx context.Context, method, path string, q url.Values, out any) error {
	u := c.baseURL + "/ari" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.user, c.pass)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &Error{Status: resp.StatusCode, Path: path, Body: strings.TrimSpace(string(body))}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Ping fails fast on bad credentials or an unreachable Asterisk.
func (c *Client) Ping(ctx context.Context) (version string, err error) {
	var info struct {
		System struct {
			Version string `json:"version"`
		} `json:"system"`
	}
	if err := c.do(ctx, http.MethodGet, "/asterisk/info", nil, &info); err != nil {
		return "", err
	}
	return info.System.Version, nil
}

// Play starts a playback of an ARI media URI (e.g. "sound:call-me-maybe/good-day")
// on a channel. Completion arrives as a PlaybackFinished event carrying playbackID.
func (c *Client) Play(ctx context.Context, channelID, media, playbackID string) error {
	q := url.Values{"media": {media}, "playbackId": {playbackID}}
	return c.do(ctx, http.MethodPost, "/channels/"+channelID+"/play", q, nil)
}

func (c *Client) StopPlayback(ctx context.Context, playbackID string) error {
	return c.do(ctx, http.MethodDelete, "/playbacks/"+playbackID, nil, nil)
}

// Ring sends ringing indication to the caller. Once a call is answered the
// carrier stops generating ringback, so without this a caller waiting on the
// house would sit in silence.
func (c *Client) Ring(ctx context.Context, channelID string) error {
	return c.do(ctx, http.MethodPost, "/channels/"+channelID+"/ring", nil, nil)
}

func (c *Client) RingStop(ctx context.Context, channelID string) error {
	return c.do(ctx, http.MethodDelete, "/channels/"+channelID+"/ring", nil, nil)
}

func (c *Client) Hangup(ctx context.Context, channelID string) error {
	return c.do(ctx, http.MethodDelete, "/channels/"+channelID, nil, nil)
}

// Channel reads one channel's current state — GET /channels/{id}. A channel
// that has ended answers 404 (IsNotFound). Read by `doorman balance` to watch
// an announcement it originated ring, answer and end; never by the call path,
// which learns what happened to a channel from the event stream.
func (c *Client) Channel(ctx context.Context, channelID string) (Channel, error) {
	var ch Channel
	err := c.do(ctx, http.MethodGet, "/channels/"+channelID, nil, &ch)
	return ch, err
}

// SetChannelVar sets a channel variable, visible to the dialplan after a
// ContinueToDialplan — how doorman tells voicemail-drop which mailbox.
func (c *Client) SetChannelVar(ctx context.Context, channelID, name, value string) error {
	q := url.Values{"variable": {name}, "value": {value}}
	return c.do(ctx, http.MethodPost, "/channels/"+channelID+"/variable", q, nil)
}

// EndpointState is what ARI knows about one endpoint's registration: online
// when at least one contact is registered, offline when none is, unknown when
// the technology cannot say. Read by `doorman provision` to watch a phone
// arrive; never by the call path, which learns about phones by ringing them.
type EndpointState struct {
	Technology string   `json:"technology"`
	Resource   string   `json:"resource"`
	State      string   `json:"state"`
	ChannelIDs []string `json:"channel_ids"`
}

// Endpoint reads one endpoint's state — GET /endpoints/{tech}/{resource}.
func (c *Client) Endpoint(ctx context.Context, tech, resource string) (EndpointState, error) {
	var out EndpointState
	err := c.do(ctx, http.MethodGet, "/endpoints/"+url.PathEscape(tech)+"/"+url.PathEscape(resource), nil, &out)
	return out, err
}

// Endpoints lists every endpoint Asterisk has — GET /endpoints.
func (c *Client) Endpoints(ctx context.Context) ([]EndpointState, error) {
	var out []EndpointState
	err := c.do(ctx, http.MethodGet, "/endpoints", nil, &out)
	return out, err
}

// ContinueToDialplan releases a channel from Stasis into the dialplan at the
// given location. The channel is alive but no longer ours; a StasisEnd
// follows, and nothing may hang the channel up after this call.
func (c *Client) ContinueToDialplan(ctx context.Context, channelID, dpContext, extension string, priority int) error {
	q := url.Values{
		"context":   {dpContext},
		"extension": {extension},
		"priority":  {strconv.Itoa(priority)},
	}
	return c.do(ctx, http.MethodPost, "/channels/"+channelID+"/continue", q, nil)
}

// Originate dials an endpoint into our Stasis app and returns the new
// channel's ID. The channel entering Stasis later means it answered.
func (c *Client) Originate(ctx context.Context, p OriginateParams) (string, error) {
	q := url.Values{
		"endpoint": {p.Endpoint},
		"app":      {c.app},
	}
	if p.AppArgs != "" {
		q.Set("appArgs", p.AppArgs)
	}
	if p.CallerID != "" {
		q.Set("callerId", p.CallerID)
	}
	if p.Timeout > 0 {
		q.Set("timeout", strconv.Itoa(p.Timeout))
	}
	if p.Originator != "" {
		q.Set("originator", p.Originator)
	}
	var ch Channel
	if err := c.do(ctx, http.MethodPost, "/channels", q, &ch); err != nil {
		return "", err
	}
	return ch.ID, nil
}

func (c *Client) CreateBridge(ctx context.Context) (string, error) {
	var b struct {
		ID string `json:"id"`
	}
	q := url.Values{"type": {"mixing"}}
	if err := c.do(ctx, http.MethodPost, "/bridges", q, &b); err != nil {
		return "", err
	}
	return b.ID, nil
}

func (c *Client) AddToBridge(ctx context.Context, bridgeID, channelID string) error {
	q := url.Values{"channel": {channelID}}
	return c.do(ctx, http.MethodPost, "/bridges/"+bridgeID+"/addChannel", q, nil)
}

func (c *Client) DestroyBridge(ctx context.Context, bridgeID string) error {
	return c.do(ctx, http.MethodDelete, "/bridges/"+bridgeID, nil, nil)
}

// ── Events ───────────────────────────────────────────────────────────────

// Connect starts the event loop in a goroutine. handler is called on the
// read goroutine for every event; it must hand off quickly (sessions consume
// via buffered channels, so this holds).
//
// The connection reconnects forever with jittered exponential backoff so a
// router reboot does not turn into a reconnect storm against Asterisk.
func (c *Client) Connect(handler func(Event)) {
	c.loop.Add(1)
	go func() {
		defer c.loop.Done()
		attempt := 0
		for !c.closing.Load() {
			if err := c.runSocket(handler); err != nil && !c.closing.Load() {
				c.log.Warn("ari event stream down", "err", err)
			}
			if c.closing.Load() {
				return
			}
			delay := c.backoff(attempt)
			attempt++
			select {
			case <-c.stop:
				return
			case <-time.After(delay):
			}
		}
	}()
}

func (c *Client) runSocket(handler func(Event)) error {
	wsURL := strings.Replace(c.baseURL, "http", "ws", 1) +
		"/ari/events?" + url.Values{
		"app":          {c.app},
		"subscribeAll": {"false"},
		"api_key":      {c.user + ":" + c.pass},
	}.Encode()

	conn, resp, err := websocket.DefaultDialer.DialContext(c.dialCtx, wsURL, nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial: %w (http %d)", err, resp.StatusCode)
		}
		return fmt.Errorf("dial: %w", err)
	}
	c.socketMu.Lock()
	if c.closing.Load() {
		c.socketMu.Unlock()
		_ = conn.Close()
		return nil
	}
	c.socket = conn
	c.socketMu.Unlock()
	defer func() { c.socketMu.Lock(); c.socket = nil; c.socketMu.Unlock(); _ = conn.Close() }()
	c.log.Info("ari event stream up", "app", c.app)
	if c.onConnection != nil {
		c.onConnection(true)
		defer c.onConnection(false)
	}

	for {
		if c.closing.Load() {
			return nil
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var ev Event
		if err := json.Unmarshal(data, &ev); err != nil {
			c.log.Warn("undecodable ari event", "err", err)
			continue
		}
		handler(ev)
	}
}

func (c *Client) backoff(attempt int) time.Duration {
	d := c.reconnectMin << min(attempt, 12)
	if d > c.reconnectMax {
		d = c.reconnectMax
	}
	// 50–100% jitter.
	return time.Duration(float64(d) * (0.5 + rand.Float64()*0.5))
}

// Close stops reconnecting. In-flight reads end when the socket drops.
func (c *Client) Close() {
	c.stopOnce.Do(func() { c.closing.Store(true); close(c.stop); c.cancelDial() })
	c.socketMu.Lock()
	if c.socket != nil {
		_ = c.socket.Close()
	}
	c.socketMu.Unlock()
	c.loop.Wait()
}
