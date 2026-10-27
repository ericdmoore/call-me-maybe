// Package phonegame runs opt-in local Starlark packs on internal handsets.
// It is independent of the ordinary lobby and has no provider or TTS client.
package phonegame

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.starlark.net/starlark"
)

type Clip struct {
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	SHA25616 string `json:"sha256_16"`
	PauseMS  int    `json:"pause_ms"`
}
type Question struct {
	Prefix string            `json:"prefix"`
	Pins   map[string]string `json:"pins,omitempty"`
	ID     string            `json:"id"`
	Tier   int               `json:"tier"`
	Answer string            `json:"answer"`
}
type Pack struct {
	ShuffleAnswers bool              `json:"shuffle_answers"`
	Schema         int               `json:"schema_version"`
	ID             string            `json:"id"`
	Entry          string            `json:"entry"`
	Sources        map[string]string `json:"sources"`
	Clips          map[string]Clip   `json:"clips"`
	MediaPrefix    string            `json:"media_prefix"`
	Questions      []Question        `json:"questions"`
	Openings       []string          `json:"openings"`
	InitialArgs    []string          `json:"initial_args"`
	Tiers          int               `json:"tiers"`
	PerTier        int               `json:"per_tier"`
	root           string
	globals        starlark.StringDict
}

var name = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,119}$`)
var sourceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,119}\.star$`)

func Load(dir string) (*Pack, error) {
	raw, err := readBounded(filepath.Join(dir, "game.json"), 2<<20)
	if err != nil {
		return nil, errors.New("cannot read game manifest")
	}
	var p Pack
	if err = json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("invalid game manifest")
	}
	p.root = dir
	if p.Schema != 1 || !name.MatchString(p.ID) || p.MediaPrefix != "games/"+p.ID || !sourceName.MatchString(p.Entry) || len(p.Sources) > 8 || len(p.Sources) == 0 || len(p.Clips) > 2500 || len(p.Clips) == 0 || p.Tiers < 1 || p.Tiers > 6 || p.PerTier < 1 || p.PerTier > 2 || len(p.Openings) == 0 || len(p.Openings) > 20 {
		return nil, errors.New("invalid pack bounds or identity")
	}
	for file, hash := range p.Sources {
		if !sourceName.MatchString(file) {
			return nil, errors.New("invalid source name")
		}
		if err = checkFile(dir, file, hash, 256<<10); err != nil {
			return nil, err
		}
	}
	for id, c := range p.Clips {
		if !name.MatchString(id) || c.File != id+".wav" || c.PauseMS < 0 || c.PauseMS > 10000 {
			return nil, errors.New("invalid clip")
		}
		if err = checkFile(dir, c.File, c.SHA256, 8<<20); err != nil {
			return nil, err
		}
		if err = checkFile(dir, strings.TrimSuffix(c.File, ".wav")+".wav16", c.SHA25616, 16<<20); err != nil {
			return nil, err
		}
		for file, rate := range map[string]uint32{c.File: 8000, strings.TrimSuffix(c.File, ".wav") + ".wav16": 16000} {
			if err = checkWAV(filepath.Join(dir, file), rate); err != nil {
				return nil, err
			}
		}
	}
	for _, id := range p.Openings {
		if _, ok := p.Clips[id]; !ok {
			return nil, errors.New("missing opening")
		}
	}
	for _, id := range []string{"room.lobby", "room.pick", "room.invite", "room.waiting", "room.joined", "room.declined", "room.unavailable", "room.full", "room.solo", "room.group", "room.social", "room.talk", "room.back", "room.left"} {
		if _, ok := p.Clips[id]; !ok {
			return nil, fmt.Errorf("missing room prompt %s", id)
		}
	}
	for i := 1; i <= 8; i++ {
		if _, ok := p.Clips[fmt.Sprintf("room.slot-%d", i)]; !ok {
			return nil, errors.New("missing room slot prompt")
		}
	}
	counts := make([]int, p.Tiers)
	seen := map[string]bool{}
	for _, q := range p.Questions {
		if !name.MatchString(q.ID) || seen[q.ID] || q.Tier < 1 || q.Tier > p.Tiers || !strings.Contains("1234", q.Answer) || len(q.Answer) != 1 {
			return nil, errors.New("invalid question pool")
		}
		if err = validateLayout(&p, q); err != nil {
			return nil, err
		}
		seen[q.ID] = true
		counts[q.Tier-1]++
	}
	for _, n := range counts {
		if n < p.PerTier || n > 100 {
			return nil, errors.New("invalid tier size")
		}
	}
	if len(p.InitialArgs) != 4 || strings.Join(p.InitialArgs, ",") != "$player,$deck,$keep_wrong,$opening" {
		return nil, errors.New("unsupported start argument contract")
	}
	cache := map[string]starlark.StringDict{}
	loading := map[string]bool{}
	var load func(*starlark.Thread, string) (starlark.StringDict, error)
	load = func(_ *starlark.Thread, file string) (starlark.StringDict, error) {
		if v, ok := cache[file]; ok {
			return v, nil
		}
		if _, ok := p.Sources[file]; !ok || loading[file] {
			return nil, errors.New("undeclared or cyclic source load")
		}
		loading[file] = true
		raw, err := readBounded(filepath.Join(dir, file), 256<<10)
		if err != nil {
			return nil, err
		}
		thread := &starlark.Thread{Name: "pack-load", Load: load}
		thread.Print = func(*starlark.Thread, string) {}
		thread.SetMaxExecutionSteps(200000)
		v, err := starlark.ExecFile(thread, file, raw, nil)
		delete(loading, file)
		if err != nil {
			return nil, errors.New("cannot initialize game rules")
		}
		v.Freeze()
		cache[file] = v
		return v, nil
	}
	p.globals, err = load(nil, p.Entry)
	if err != nil {
		return nil, err
	}
	for _, f := range []string{"start", "step"} {
		if _, ok := p.globals[f].(starlark.Callable); !ok {
			return nil, errors.New("missing game entry point")
		}
	}
	return &p, nil
}
func readBounded(path string, n int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, n+1))
	if int64(len(b)) > n {
		return nil, errors.New("file too large")
	}
	return b, e
}
func checkFile(dir, file, hash string, n int64) error {
	info, e := os.Lstat(filepath.Join(dir, file))
	if e != nil || !info.Mode().IsRegular() {
		return errors.New("missing or nonregular pack file")
	}
	b, e := readBounded(filepath.Join(dir, file), n)
	if e != nil {
		return e
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != hash {
		return errors.New("pack checksum mismatch")
	}
	return nil
}
func pick(n int) int {
	v, e := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if e != nil {
		panic("entropy unavailable")
	}
	return int(v.Int64())
}
func (p *Pack) Start(player string) (*Reply, error) {
	deck := []starlark.Value{}
	keep := starlark.NewDict(0)
	for tier := 1; tier <= p.Tiers; tier++ {
		pool := []Question{}
		for _, q := range p.Questions {
			if q.Tier == tier {
				pool = append(pool, q)
			}
		}
		for range p.PerTier {
			j := pick(len(pool))
			q := pool[j]
			pool = append(pool[:j], pool[j+1:]...)
			deck = append(deck, starlark.String(q.ID))
			wrong := []string{}
			for _, key := range []string{"1", "2", "3", "4"} {
				if key != q.Answer {
					wrong = append(wrong, key)
				}
			}
			_ = keep.SetKey(starlark.String(q.ID), starlark.String(wrong[pick(len(wrong))]))
		}
	}
	return p.call("start", starlark.Tuple{starlark.String(player), starlark.NewList(deck), keep, starlark.String(p.Openings[pick(len(p.Openings))])})
}

type Command struct {
	Op, Line, Keys, Player string
	Timeout, Pause         int
}
type Reply struct {
	State    *starlark.Dict
	Commands []Command
	Done     bool
}

func (p *Pack) call(f string, args starlark.Tuple) (*Reply, error) {
	t := &starlark.Thread{Name: "game"}
	t.Print = func(*starlark.Thread, string) {}
	t.SetMaxExecutionSteps(200000)
	v, err := starlark.Call(t, p.globals[f], args, nil)
	if err != nil {
		return nil, errors.New("game evaluation failed")
	}
	d, ok := v.(*starlark.Dict)
	if !ok {
		return nil, errors.New("invalid game result")
	}
	state, _, _ := d.Get(starlark.String("state"))
	s, ok := state.(*starlark.Dict)
	if !ok {
		return nil, errors.New("invalid game state")
	}
	request, _, _ := s.Get(starlark.String("request"))
	var requestID int
	if request == nil || starlark.AsInt(request, &requestID) != nil || requestID < 0 || requestID > 10000 || getString(s, "phase") == "" {
		return nil, errors.New("invalid state correlation")
	}
	s.Freeze()
	raw, _, _ := d.Get(starlark.String("commands"))
	list, ok := raw.(*starlark.List)
	if !ok || list.Len() > 64 {
		return nil, errors.New("invalid command batch")
	}
	r := &Reply{State: s, Done: getString(s, "phase") == "done"}
	for i := 0; i < list.Len(); i++ {
		c, ok := list.Index(i).(*starlark.Dict)
		if !ok {
			return nil, errors.New("invalid command")
		}
		cmd := Command{Op: getString(c, "op"), Line: getString(c, "line"), Keys: getString(c, "keys"), Player: getString(c, "player"), Timeout: getInt(c, "timeout_s"), Pause: getInt(c, "milliseconds")}
		switch cmd.Op {
		case "say":
			if to := getString(c, "to"); to != "" && to != "all" {
				return nil, errors.New("private say routing is not supported by this preview")
			}
			if _, ok := p.Clips[cmd.Line]; !ok {
				return nil, errors.New("undeclared audio line")
			}
		case "ask_digit":
			if i != list.Len()-1 || cmd.Timeout < 1 || cmd.Timeout > 120 || cmd.Keys == "" || strings.Contains(cmd.Keys, "9") || strings.Trim(cmd.Keys, "0123456789*#") != "" {
				return nil, errors.New("invalid keypad window")
			}
		case "pause":
			if cmd.Pause < 0 || cmd.Pause > 10000 {
				return nil, errors.New("invalid pause")
			}
		default:
			return nil, errors.New("unsupported effect")
		}
		r.Commands = append(r.Commands, cmd)
	}
	if !r.Done && (len(r.Commands) == 0 || r.Commands[len(r.Commands)-1].Op != "ask_digit") {
		return nil, errors.New("game has no input window")
	}
	return r, nil
}
func (p *Pack) Step(r *Reply, player string, key *string, deadline bool) (*Reply, error) {
	e := starlark.NewDict(5)
	req, _, _ := r.State.Get(starlark.String("request"))
	for k, v := range map[string]starlark.Value{"request": req, "player": starlark.String(player), "kind": starlark.String("digit"), "value": starlark.None} {
		_ = e.SetKey(starlark.String(k), v)
	}
	if key != nil {
		_ = e.SetKey(starlark.String("value"), starlark.String(*key))
	}
	if deadline {
		_ = e.SetKey(starlark.String("cause"), starlark.String("session_deadline"))
	}
	return p.call("step", starlark.Tuple{r.State, e})
}
func getString(d *starlark.Dict, k string) string {
	v, _, _ := d.Get(starlark.String(k))
	s, _ := starlark.AsString(v)
	return s
}
func getInt(d *starlark.Dict, k string) int {
	v, _, _ := d.Get(starlark.String(k))
	n := 0
	if v != nil {
		_ = starlark.AsInt(v, &n)
	}
	return n
}

func checkWAV(path string, rate uint32) error {
	b, e := readBounded(path, 16<<20)
	if e != nil {
		return e
	}
	if len(b) < 44 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return errors.New("invalid WAV")
	}
	format, data := false, false
	for off := 12; off+8 <= len(b); {
		n := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		start := off + 8
		if n < 0 || n > len(b)-start {
			return errors.New("truncated WAV")
		}
		switch string(b[off : off+4]) {
		case "fmt ":
			if n < 16 || binary.LittleEndian.Uint16(b[start:]) != 1 || binary.LittleEndian.Uint16(b[start+2:]) != 1 || binary.LittleEndian.Uint32(b[start+4:]) != rate || binary.LittleEndian.Uint16(b[start+14:]) != 16 {
				return errors.New("expected mono PCM16 audio")
			}
			format = true
		case "data":
			data = n > 0
		}
		off = start + n + (n % 2)
	}
	if !format || !data {
		return errors.New("missing WAV audio")
	}
	return nil
}
