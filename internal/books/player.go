package books

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Position belongs to an authenticated handset and one recording edition.
// Speed is stored in tenths, avoiding accumulated floating-point key presses.
type Position struct {
	Chapter int     `json:"chapter"`
	Seconds float64 `json:"seconds"`
	Speed   int     `json:"speed"`
	Started bool    `json:"started"`
}

func (m *menu) progressPath(b Book) string {
	h := sha256.Sum256([]byte(m.handset + "\x00" + b.ID + "\x00" + b.Language + "\x00" + b.Revision))
	return filepath.Join(m.state, hex.EncodeToString(h[:])+".json")
}

func (m *menu) loadProgress(b Book) Position {
	if m.public {
		if p, ok := m.memory[b.ID]; ok {
			return p
		}
		return Position{Speed: 10}
	}
	p := Position{Speed: 10}
	if readJSON(m.progressPath(b), &p) != nil || p.Chapter < 0 || p.Chapter >= len(b.Chapters) || math.IsNaN(p.Seconds) || math.IsInf(p.Seconds, 0) || p.Seconds < 0 || p.Seconds > b.Chapters[p.Chapter].Seconds || p.Speed < 5 || p.Speed > 20 {
		return Position{Speed: 10}
	}
	return p
}

// saveProgress keeps a position: on disk for a handset's bookmark, in memory
// for an outside caller, whose place is forgotten when the call ends.
func (m *menu) saveProgress(b Book, p Position) error {
	if m.public {
		if m.memory == nil {
			m.memory = map[string]Position{}
		}
		m.memory[b.ID] = p
		return nil
	}
	return writeJSON(m.progressPath(b), p)
}

type window struct {
	path    string
	samples int64
	err     error
}

// A single FFmpeg process keeps atempo's signal state across playback windows.
// An unbuffered handoff and pipe backpressure bound this to the playing window
// plus one prefetched window (30 seconds each), at any speed/chapter length.
// Asterisk owns all SIP/RTP/DTMF. No persistent transcoder or speed copies.
type stream struct {
	windows chan window
	cancel  context.CancelFunc
	done    chan struct{}
	dir     string
	once    sync.Once
}

func startStream(ctx context.Context, state, input string, seconds float64, speed int) (*stream, error) {
	dir, err := os.MkdirTemp(state, "play-")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &stream{windows: make(chan window), cancel: cancel, done: make(chan struct{}), dir: dir}
	args := []string{"-nostdin", "-v", "error", "-protocol_whitelist", "file,pipe"}
	if filepath.Ext(input) != ".opus" {
		args = append(args, "-f", "s16le", "-ar", "16000", "-ac", "1")
	}
	args = append(args, "-ss", fmt.Sprintf("%.6f", seconds), "-i", input, "-af", fmt.Sprintf("atempo=%.1f", float64(speed)/10), "-f", "s16le", "-ar", "16000", "-ac", "1", "pipe:1")
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	pipe, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return nil, errors.New("playback transcoder unavailable")
	}
	go func() {
		defer close(s.done)
		defer close(s.windows)
		defer pipe.Close()
		send := func(w window) bool {
			select {
			case s.windows <- w:
				return true
			case <-ctx.Done():
				return false
			}
		}
		buf := make([]byte, 30*sampleRate*2)
		for n := 0; ; n++ {
			count, readErr := io.ReadFull(pipe, buf)
			if count > 0 {
				path := filepath.Join(dir, fmt.Sprintf("%06d.sln16", n))
				if err := os.WriteFile(path, buf[:count], 0o600); err != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					send(window{err: errors.New("playback buffer write failed")})
					return
				}
				if !send(window{path: path, samples: int64(count / 2)}) {
					_ = cmd.Wait()
					return
				}
			}
			if readErr != nil {
				err := cmd.Wait()
				if ctx.Err() == nil && (err != nil || (readErr != io.EOF && readErr != io.ErrUnexpectedEOF)) {
					send(window{err: errors.New("playback conversion failed")})
				}
				return
			}
		}
	}()
	return s, nil
}

func (s *stream) close() { s.once.Do(func() { s.cancel(); <-s.done; _ = os.RemoveAll(s.dir) }) }

func (m *menu) listen(b Book, p *Position) error {
	p.Started = true
	if err := m.saveProgress(b, *p); err != nil {
		return err
	}
	// Help is skippable. A key pressed during it is applied before playback.
	// A story of the day starts with the story.
	var key int
	var err error
	if !m.story {
		intro := "controls"
		if m.clips["tip"] != "" {
			intro = "tip"
		}
		if key, err = m.prompt(intro); err != nil {
			return err
		}
	}
	paused := false
	var current *stream
	stop := func() {
		if current != nil {
			current.close()
			current = nil
		}
	}
	defer stop()
	defer func() { _ = m.saveProgress(b, *p) }()
	for {
		if m.allowed != nil && !m.allowed(m.handset) {
			return errors.New("podcast subscription ended")
		}
		if err := m.ctx.Err(); err != nil {
			return err
		}
		if key != 0 {
			stop()
			before := p.Speed
			switch key {
			case '0':
				return nil
			case '1':
				if p.Chapter > 0 {
					p.Chapter--
				}
				p.Seconds = 0
			case '3':
				if p.Chapter+1 < len(b.Chapters) {
					p.Chapter++
					p.Seconds = 0
				}
			case '2':
				if p.Speed < 20 {
					p.Speed++
				}
			case '8':
				if p.Speed > 5 {
					p.Speed--
				}
			case '4':
				p.Seconds = math.Max(0, p.Seconds-10)
			case '6':
				p.Seconds = math.Min(b.Chapters[p.Chapter].Seconds, p.Seconds+10)
			case '5':
				paused = !paused
			case '*':
				key, err = m.prompt("controls")
				if err != nil {
					return err
				}
				continue
			}
			key = 0
			if before != p.Speed {
				key, err = m.saySpeed(p.Speed)
				if err != nil {
					return err
				}
			}
			if err := m.saveProgress(b, *p); err != nil {
				return err
			}
			if key != 0 {
				continue
			}
		}
		if paused {
			key, err = m.prompt("paused")
			if err != nil {
				return err
			}
			for tries := 0; key == 0 && tries < 6; tries++ {
				key, err = m.a.wait()
				if err != nil {
					return err
				}
			}
			if key == 0 {
				return nil
			}
			continue
		}
		if p.Seconds >= b.Chapters[p.Chapter].Seconds-0.001 {
			stop()
			if p.Chapter+1 >= len(b.Chapters) {
				_, err = m.prompt("finished")
				return err
			}
			p.Chapter++
			p.Seconds = 0
			if err := m.saveProgress(b, *p); err != nil {
				return err
			}
		}
		if current == nil {
			current, err = startStream(m.ctx, m.state, filepath.Join(bookDir(m.root, b), b.Chapters[p.Chapter].Audio), p.Seconds, p.Speed)
			if err != nil {
				return err
			}
		}
		var w window
		var ok bool
		timer := time.NewTimer(30 * time.Second)
		select {
		case w, ok = <-current.windows:
			timer.Stop()
		case <-m.ctx.Done():
			timer.Stop()
			return m.ctx.Err()
		case <-timer.C:
			return errors.New("playback conversion timed out")
		}
		if !ok {
			stop()
			p.Seconds = b.Chapters[p.Chapter].Seconds
			continue
		}
		if w.err != nil {
			return w.err
		}
		r, playErr := m.playWindow(w)
		_ = os.Remove(w.path)
		played := r.samples
		if played < 0 {
			played = 0
		}
		if played > w.samples {
			played = w.samples
		}
		p.Seconds = math.Min(b.Chapters[p.Chapter].Seconds, p.Seconds+float64(played)/sampleRate*float64(p.Speed)/10)
		if err := m.saveProgress(b, *p); err != nil {
			return err
		}
		if playErr != nil {
			return playErr
		}
		key = r.key
	}
}

func (m *menu) saySpeed(speed int) (int, error) {
	if clip := m.clips[fmt.Sprintf("speed-%d", speed)]; clip != "" {
		return m.a.play(clip)
	}
	steps := []func() (int, error){func() (int, error) { return m.prompt("speed") }, func() (int, error) { return m.a.number(speed / 10) }, func() (int, error) { return m.prompt("point") }, func() (int, error) { return m.a.number(speed % 10) }}
	for _, step := range steps {
		key, err := step()
		if key != 0 || err != nil {
			return key, err
		}
	}
	return 0, nil
}
