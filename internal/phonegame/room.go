package phonegame

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"callmemaybe/internal/ari"
	"go.starlark.net/starlark"
)

// API owns only the preview application's channels and bridges.
type API interface {
	Answer(context.Context, string) error
	ChannelVar(context.Context, string, string) (string, error)
	CreateBridge(context.Context) (string, error)
	AddToBridge(context.Context, string, string) error
	DestroyBridge(context.Context, string) error
	Play(context.Context, string, string, string) error
	PlayBridge(context.Context, string, string, string) error
	StopPlayback(context.Context, string) error
	MuteInput(context.Context, string, bool) error
	Originate(context.Context, ari.OriginateParams) (string, error)
	Hangup(context.Context, string) error
	Endpoint(context.Context, string, string) (ari.EndpointState, error)
}
type Handset struct {
	ID, Endpoint string
	Number       int
}
type Manager struct {
	Observe         func(string)
	previousLayouts map[string]map[string]string
	onWindow        func(string, string)
	closed          bool
	api             API
	pack            *Pack
	handsets        []Handset
	mu              sync.Mutex
	rooms           map[string]*room
	seats           map[string]*room
	ctx             context.Context
	wg              sync.WaitGroup
}

func New(ctx context.Context, a API, p *Pack, handsets []Handset) (*Manager, error) {
	if len(handsets) == 0 || len(handsets) > 8 {
		return nil, errors.New("preview needs 1–8 selected handsets")
	}
	seen := map[string]bool{}
	for _, h := range handsets {
		if !name.MatchString(h.ID) || h.Endpoint != "PJSIP/"+h.ID || h.Number < 101 || h.Number > 199 || seen[h.ID] {
			return nil, errors.New("only configured direct PJSIP handsets can join")
		}
		seen[h.ID] = true
	}
	return &Manager{api: a, pack: p, handsets: handsets, rooms: map[string]*room{}, previousLayouts: map[string]map[string]string{}, seats: map[string]*room{}, ctx: ctx}, nil
}

// Event never blocks Asterisk's WebSocket reader. An overflow cancels the room.
func (m *Manager) Event(e ari.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	var r *room
	if e.Channel != nil {
		r = m.seats[e.Channel.ID]
	}
	if e.Type == "StasisStart" && e.Channel != nil {
		if len(e.Args) == 3 && e.Args[0] == "invite" {
			r = m.rooms[e.Args[1]]
			if r != nil && r.inviteSlot(e.Args[2]) {
				m.seats[e.Channel.ID] = r
			} else {
				r = nil
			}
		}
		if r == nil && len(e.Args) == 1 && e.Args[0] == "new" && len(m.rooms) < 1 {
			ctx, cancel := context.WithTimeout(m.ctx, 20*time.Minute)
			r = &room{m: m, id: token(), host: e.Channel.ID, ctx: ctx, cancel: cancel, events: make(chan ari.Event, 128), members: map[string]string{}, owned: map[string]bool{}, detached: map[string]bool{}, started: map[string]bool{}, gone: map[string]bool{}}
			m.rooms[r.id] = r
			m.seats[r.host] = r
			m.wg.Add(1)
			go func() { defer m.wg.Done(); r.run() }()
			return
		}
	}
	if r != nil {
		select {
		case r.events <- e:
		default:
			r.cancel()
		}
		return
	}
	// Unknown starts get no membership and no media; never block the event reader.
	if e.Type == "StasisStart" && e.Channel != nil {
		id := e.Channel.ID
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
			defer c()
			_ = m.api.Hangup(ctx, id)
		}()
	}
	// Playback events identify their room by a random prefix, not a caller-supplied ID.
	if e.Playback != nil {
		for _, candidate := range m.rooms {
			if strings.HasPrefix(e.Playback.ID, candidate.id+"-") {
				select {
				case candidate.events <- e:
				default:
					candidate.cancel()
				}
			}
		}
	}
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for _, r := range m.rooms {
		r.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
func token() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	s := make([]byte, 24)
	for i := range s {
		s[i] = alphabet[pick(len(alphabet))]
	}
	return string(s)
}

type room struct {
	layouts                    map[string]map[string]string
	m                          *Manager
	id, host, bridge, inviting string
	ctx                        context.Context
	cancel                     context.CancelFunc
	events                     chan ari.Event
	members                    map[string]string // channel -> configured handset id
	owned, detached            map[string]bool
	started, gone              map[string]bool
	sequence                   int
}

func (r *room) inviteSlot(id string) bool { return id != "" && id == r.inviting }
func (r *room) cleanup() {
	r.cancel()
	ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
	defer c()
	r.m.mu.Lock()
	delete(r.m.rooms, r.id)
	for id, room := range r.m.seats {
		if room == r {
			delete(r.m.seats, id)
		}
	}
	r.m.mu.Unlock()
	for id := range r.owned {
		if !r.detached[id] {
			_ = r.m.api.Hangup(ctx, id)
		}
	}
	if r.bridge != "" {
		_ = r.m.api.DestroyBridge(ctx, r.bridge)
	}
}
func (r *room) run() {
	defer r.cleanup()
	r.owned[r.host] = true
	endpoint, err := r.m.api.ChannelVar(r.ctx, r.host, "CHANNEL(endpoint)")
	if err != nil {
		return
	}
	known := false
	for _, h := range r.m.handsets {
		if endpoint == h.ID {
			r.members[r.host] = h.ID
			known = true
			break
		}
	}
	if !known {
		return
	}
	if r.m.api.Answer(r.ctx, r.host) != nil {
		return
	}
	r.bridge, err = r.m.api.CreateBridge(r.ctx)
	if err != nil {
		return
	}
	if r.m.api.AddToBridge(r.ctx, r.bridge, r.host) != nil {
		return
	}
	if err = r.lobby(); err != nil {
		return
	}
	r.prepareLayouts()
	reply, err := r.m.pack.Start(r.host)
	if err != nil {
		return
	}
	if err = r.mode(); err != nil {
		return
	}
	for {
		if err = r.syncRoom(reply); err != nil {
			return
		}
		commands, e := r.display(reply)
		if e != nil {
			return
		}
		var ask Command
		for _, cmd := range commands {
			switch cmd.Op {
			case "say":
				err = r.say(cmd.Line)
			case "pause":
				err = r.wait(time.Duration(cmd.Pause) * time.Millisecond)
			case "ask_digit":
				ask = cmd
				if cmd.Player != r.host {
					return
				}
			}
			if err != nil {
				return
			}
		}
		if reply.Done {
			return
		}
		var digit *string
		remaining := time.Duration(ask.Timeout) * time.Second
		for {
			started := time.Now()
			digit, err = r.digit(r.host, ask.Keys+"9", remaining)
			remaining -= time.Since(started)
			if err != nil {
				return
			}
			if digit != nil && *digit == "9" {
				if err = r.social(); err != nil {
					return
				}
				if err = r.say("room.back"); err != nil {
					return
				}
				if remaining <= 0 {
					digit = nil
					break
				}
				continue
			}
			break
		}
		if err = r.syncRoom(reply); err != nil {
			return
		}
		reply, err = r.m.pack.Step(reply, r.host, r.canonical(reply, digit), false)
		if err != nil {
			return
		}
	}
}
func (r *room) mode() error {
	if len(r.members) > 1 {
		return r.say("room.group")
	}
	return r.say("room.solo")
}
func (r *room) lobby() error {
	for {
		if err := r.say("room.lobby"); err != nil {
			return err
		}
		d, err := r.digit(r.host, "120", 30*time.Second)
		if err != nil {
			return err
		}
		if d == nil || *d == "0" {
			return context.Canceled
		}
		if *d == "1" {
			return nil
		}
		if err = r.invite(); err != nil {
			return err
		}
	}
}
func (r *room) social() error {
	if err := r.say("room.social"); err != nil {
		return err
	}
	d, err := r.digit(r.host, "120", 15*time.Second)
	if err != nil {
		return err
	}
	if d == nil || *d == "0" {
		return nil
	}
	if *d == "1" {
		return r.invite()
	}
	if len(r.members) < 2 {
		return r.say("room.solo")
	}
	if err = r.say("room.talk"); err != nil {
		return err
	}
	for id := range r.members {
		if id != r.host {
			if err = r.m.api.MuteInput(r.ctx, id, false); err != nil {
				return err
			}
		}
	}
	_, err = r.digit(r.host, "0", 20*time.Second)
	for id := range r.members {
		if id != r.host {
			if e := r.m.api.MuteInput(r.ctx, id, true); e != nil {
				return e
			}
		}
	}
	return err
}
func (r *room) invite() error {
	if len(r.members) >= 8 {
		return r.say("room.full")
	}
	if err := r.say("room.pick"); err != nil {
		return err
	}
	options := map[string]Handset{}
	keys := "0"
	for i, h := range r.m.handsets {
		present := false
		for _, id := range r.members {
			if id == h.ID {
				present = true
			}
		}
		if present {
			continue
		}
		slot := fmt.Sprint(i + 1)
		options[slot] = h
		keys += slot
		if err := r.say("room.slot-" + slot); err != nil {
			return err
		}
		for _, n := range fmt.Sprint(h.Number) {
			if err := r.media("digits/"+string(n), ""); err != nil {
				return err
			}
		}
	}
	if len(options) == 0 {
		return r.say("room.unavailable")
	}
	d, err := r.digit(r.host, keys, 20*time.Second)
	if err != nil || d == nil || *d == "0" {
		return err
	}
	h := options[*d]
	// Do not interrupt a handset that is already on another call.
	endpoint, checkErr := r.m.api.Endpoint(r.ctx, "PJSIP", h.ID)
	if checkErr != nil || endpoint.State == "offline" || len(endpoint.ChannelIDs) > 0 {
		return r.say("room.unavailable")
	}
	r.m.mu.Lock()
	r.inviting = h.ID
	r.m.mu.Unlock()
	defer func() { r.m.mu.Lock(); r.inviting = ""; r.m.mu.Unlock() }()
	id, err := r.m.api.Originate(r.ctx, ari.OriginateParams{Endpoint: h.Endpoint, AppArgs: "invite," + r.id + "," + h.ID, Timeout: 20, Originator: r.host, CallerID: "Game invitation"})
	if err != nil {
		return r.say("room.unavailable")
	}
	r.owned[id] = true
	defer func() {
		if _, joined := r.members[id]; !joined {
			if !r.detached[id] {
				_ = r.m.api.Hangup(r.ctx, id)
			}
			r.m.mu.Lock()
			delete(r.m.seats, id)
			r.m.mu.Unlock()
		}
	}()
	r.m.mu.Lock()
	r.m.seats[id] = r
	r.m.mu.Unlock()
	timer := time.NewTimer(22 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-timer.C:
			return r.say("room.unavailable")
		case e := <-r.events:
			if e.Channel != nil && e.Channel.ID == id && e.Type == "StasisStart" {
				endpoint, e := r.m.api.ChannelVar(r.ctx, id, "CHANNEL(endpoint)")
				if e != nil || endpoint != h.ID {
					return r.say("room.unavailable")
				}
				if e = r.media(r.m.pack.MediaPrefix+"/room.invite", id); e != nil {
					if r.ctx.Err() != nil {
						return r.ctx.Err()
					}
					return r.say("room.unavailable")
				}
				answer, e := r.digit(id, "10", 15*time.Second)
				if e != nil {
					return e
				}
				if answer == nil || *answer != "1" {
					return r.say("room.declined")
				}
				if e = r.m.api.MuteInput(r.ctx, id, true); e != nil {
					return e
				}
				if e = r.m.api.AddToBridge(r.ctx, r.bridge, id); e != nil {
					return e
				}
				r.members[id] = h.ID
				if e = r.say("room.joined"); e != nil {
					return e
				}
				return r.mode()
			}
			if err = r.observe(e); err != nil {
				return err
			}
			if r.gone[id] {
				return r.say("room.unavailable")
			}
		}
	}
}
func (r *room) observe(e ari.Event) error {
	if e.Channel == nil {
		return nil
	}
	id := e.Channel.ID
	if e.Type == "StasisStart" {
		r.started[id] = true
	}
	if e.Type == "StasisEnd" || e.Type == "ChannelDestroyed" {
		if e.Type == "StasisEnd" {
			r.detached[id] = true
		}
		r.gone[id] = true
		delete(r.members, id)
		if id == r.host {
			return context.Canceled
		}
	}
	return nil
}
func (r *room) say(line string) error {
	clip, ok := r.m.pack.Clips[line]
	if !ok {
		return errors.New("missing audio")
	}
	if err := r.media(r.m.pack.MediaPrefix+"/"+strings.TrimSuffix(clip.File, ".wav"), ""); err != nil {
		return err
	}
	return r.wait(time.Duration(clip.PauseMS) * time.Millisecond)
}
func (r *room) media(media, channel string) error {
	r.sequence++
	id := fmt.Sprintf("%s-%d", r.id, r.sequence)
	var err error
	if channel == "" {
		err = r.m.api.PlayBridge(r.ctx, r.bridge, "sound:"+media, id)
	} else {
		err = r.m.api.Play(r.ctx, channel, "sound:"+media, id)
	}
	if err != nil {
		r.observeStage("media-command-failed")
		return err
	}
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-timer.C:
			r.observeStage("media-playback-timeout")
			_ = r.m.api.StopPlayback(r.ctx, id)
			return errors.New("playback timeout")
		case e := <-r.events:
			if e.Type == "PlaybackFinished" && e.Playback != nil && e.Playback.ID == id {
				if e.Playback.State == "failed" {
					r.observeStage("media-playback-failed")
					return errors.New("Asterisk could not play the declared media")
				}
				return nil
			}
			if err = r.observe(e); err != nil {
				return err
			}
			if channel != "" && r.gone[channel] {
				return errors.New("invited handset left")
			}
		}
	}
}
func (r *room) digit(channel, keys string, timeout time.Duration) (*string, error) {
	// Clear digits received during playback; they never answer a new prompt.
	for draining := true; draining; {
		select {
		case event := <-r.events:
			if err := r.observe(event); err != nil {
				return nil, err
			}
		default:
			draining = false
		}
	}
	if r.gone[channel] {
		return nil, nil
	}
	opened := time.Now()
	if r.m.onWindow != nil {
		r.m.onWindow(channel, keys)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return nil, r.ctx.Err()
		case <-timer.C:
			return nil, nil
		case e := <-r.events:
			if err := r.observe(e); err != nil {
				return nil, err
			}
			if channel != r.host && r.gone[channel] {
				return nil, nil
			}
			if stamp, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil && stamp.Before(opened) {
				continue
			}
			if e.Type == "ChannelDtmfReceived" && e.Channel != nil && e.Channel.ID == channel && len(e.Digit) == 1 && strings.Contains(keys, e.Digit) {
				d := e.Digit
				return &d, nil
			}
		}
	}
}
func (r *room) wait(timeout time.Duration) error {
	if timeout <= 0 {
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-timer.C:
			return nil
		case e := <-r.events:
			if err := r.observe(e); err != nil {
				return err
			}
		}
	}
}

func (r *room) syncRoom(reply *Reply) error {
	fn, ok := r.m.pack.globals["room_changed"]
	if !ok {
		return nil
	}
	players := []starlark.Value{}
	for id := range r.members {
		players = append(players, starlark.String(id))
	}
	t := &starlark.Thread{Name: "room-update"}
	t.SetMaxExecutionSteps(10000)
	v, err := starlark.Call(t, fn, starlark.Tuple{reply.State, starlark.NewList(players)}, nil)
	if err != nil {
		return errors.New("room update failed")
	}
	state, ok := v.(*starlark.Dict)
	if !ok {
		return errors.New("invalid room update")
	}
	state.Freeze()
	reply.State = state
	return nil
}

func (r *room) observeStage(stage string) {
	if r.m.Observe != nil {
		r.m.Observe(stage)
	}
}
