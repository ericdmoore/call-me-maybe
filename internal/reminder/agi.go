package reminder

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

var ErrHangup = errors.New("channel ended")
var errRestart = errors.New("restart entry")

// AGI is the small, synchronous part of Asterisk's native protocol these
// menus need. stdout is protocol, not a log. In particular, responses may
// contain digits and must never be included in errors or diagnostics.
type AGI struct {
	in  *bufio.Scanner
	out io.Writer
}

func Connect(in io.Reader, out io.Writer) (*AGI, error) {
	a := &AGI{in: bufio.NewScanner(in), out: out}
	for n := 0; n < 100; n++ {
		if !a.in.Scan() {
			return nil, ErrHangup
		}
		if a.in.Text() == "" {
			return a, nil
		}
	}
	return nil, errors.New("invalid AGI handshake")
}

func quote(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }

func (a *AGI) command(command string) (int, string, error) {
	if strings.ContainsAny(command, "\r\n") {
		return 0, "", errors.New("invalid AGI command")
	}
	if _, err := fmt.Fprintln(a.out, command); err != nil {
		return 0, "", ErrHangup
	}
	if !a.in.Scan() || a.in.Text() == "HANGUP" {
		return 0, "", ErrHangup
	}
	r := a.in.Text()
	if !strings.HasPrefix(r, "200 result=") {
		return 0, "", errors.New("AGI command failed")
	}
	rest := strings.TrimPrefix(r, "200 result=")
	num, _, _ := strings.Cut(rest, " ")
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, "", errors.New("invalid AGI response")
	}
	if n < 0 {
		return n, "", ErrHangup
	}
	value := ""
	if start := strings.Index(rest, " ("); start >= 0 && strings.HasSuffix(rest, ")") {
		value = rest[start+2 : len(rest)-1]
	}
	return n, value, nil
}

func (a *AGI) variable(expr string) (string, error) {
	n, v, err := a.command("GET FULL VARIABLE " + quote(expr))
	if err == nil && n != 1 {
		err = errors.New("required Asterisk variable missing")
	}
	return v, err
}

func (a *AGI) play(media string) error {
	_, _, err := a.command("STREAM FILE " + quote(media) + ` ""`)
	return err
}

func (a *AGI) option(media, keys string) (string, error) {
	n, _, err := a.command("GET OPTION " + quote(media) + " " + quote(keys) + " 10000")
	if n == 0 || err != nil {
		return "", err
	}
	return string(rune(n)), nil
}

func (a *AGI) sayTime(t time.Time) error {
	// Asterisk and this process both use the appliance's local timezone.
	_, _, err := a.command(fmt.Sprintf(`SAY DATETIME %d "" "ABd 'digits/at' IMp"`, t.Unix()))
	return err
}

func (a *AGI) sayDigits(digits string) error {
	if !decimal(digits) {
		return nil
	}
	_, _, err := a.command("SAY DIGITS " + quote(digits) + ` ""`)
	return err
}

// collect requires #. A timeout never silently accepts a partial time. It
// keeps at most the first four digits for the caller's error readback, while
// still consuming overflow through #; raw input is never persisted or logged.
func (a *AGI) collect(prompt string, max int) (string, error) {
	key, err := a.option(prompt, "0123456789*#")
	var digits string
	overflow := false
	for err == nil {
		switch key {
		case "":
			return digits, ErrTime
		case "*":
			return "", errRestart
		case "#":
			if overflow || digits == "" {
				return digits, ErrTime
			}
			return digits, nil
		default:
			if decimal(key) {
				if len(digits) < 4 {
					digits += key
				} else {
					overflow = true
				}
				if len(digits) > max {
					overflow = true
				}
			}
		}
		n, _, e := a.command("WAIT FOR DIGIT 10000")
		err = e
		key = ""
		if n > 0 {
			key = string(rune(n))
		}
	}
	return "", err
}

type menu struct {
	a                    *AGI
	store                *Store
	handset, code, media string
	now                  func() time.Time
}

func (m *menu) play(name string) error { return m.a.play(m.media + "/reminder-" + name) }
func (m *menu) option(name, keys string) (string, error) {
	return m.a.option(m.media+"/reminder-"+name, keys)
}

// Run authenticates against two values provided by Asterisk itself: the PJSIP
// endpoint and its generated marker. Caller ID and DTMF never select a phone.
// No .env, inventory credentials, ARI password or journal is read by this mode.
func Run(a *AGI, mode, arg string, now func() time.Time) error {
	handset, err := a.variable("${CHANNEL(endpoint)}")
	if err != nil {
		return err
	}
	marker, err := a.variable("${CMM_REMINDER_HANDSET}")
	if err != nil {
		return err
	}
	if handset != marker || !handsetPattern.MatchString(handset) {
		return errors.New("reminders require a local handset")
	}
	media := ""
	if mode == "menu" && validCode(arg) {
		media, err = a.variable("${CMM_REMINDER_MEDIA}")
		if err != nil {
			return err
		}
		if !mediaPattern.MatchString(media) {
			return errors.New("invalid reminder media")
		}
	} else if mode != "deliver" {
		return errors.New("invalid reminder operation")
	}
	spool, err := a.variable("${ASTSPOOLDIR}")
	if err != nil {
		return err
	}
	s, err := Open(spool)
	if err != nil {
		if media != "" {
			_ = a.play(media + "/reminder-unavailable")
		}
		return err
	}
	if mode == "deliver" {
		j, err := s.Claim(handset, arg)
		if err != nil {
			return err // cancelled, already answered, wrong handset, or corrupt
		}
		defer removeIfPresent(s.audio(j.ID))
		if err := a.play(j.Media + "/reminder-call-" + j.Code); err != nil {
			return err
		}
		if j.Audio {
			return a.play(strings.TrimSuffix(s.audio(j.ID), ".wav"))
		}
		return nil
	}
	m := &menu{a: a, store: s, handset: handset, code: arg, media: media, now: now}
	if err := m.run(); err != nil {
		if !errors.Is(err, ErrHangup) {
			_ = m.play("unavailable")
		}
		return err
	}
	return nil
}

func (m *menu) run() error {
	if err := m.play("menu-" + m.code); err != nil {
		return err
	}
	jobs, err := m.store.Pending(m.handset)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		if err := m.play("none"); err != nil {
			return err
		}
	} else {
		if err := m.play("pending"); err != nil {
			return err
		}
		for _, j := range jobs {
			if err := m.play("kind-" + j.Code); err != nil {
				return err
			}
			if err := m.a.sayTime(j.Due); err != nil {
				return err
			}
			kind := "standard"
			if j.Audio {
				kind = "recorded"
			}
			if err := m.play(kind); err != nil {
				return err
			}
			if !j.Due.After(m.now()) {
				if j.Next.After(m.now()) {
					if err := m.play("retrying"); err != nil {
						return err
					}
					if err := m.a.sayTime(j.Next); err != nil {
						return err
					}
				} else if err := m.play("due"); err != nil {
					return err
				}
			}
			key, err := m.option("cancel", "1#")
			if err != nil {
				return err
			}
			if key == "1" {
				err := m.store.Cancel(m.handset, j.ID)
				if errors.Is(err, ErrMissing) {
					if err := m.play("changed"); err != nil {
						return err
					}
					continue
				}
				if err != nil {
					return err
				}
				if err := m.play("cancelled"); err != nil {
					return err
				}
			}
		}
		key, err := m.option("new", "1#")
		if err != nil {
			return err
		}
		if key != "1" {
			return nil
		}
	}
	for {
		jobs, err := m.store.Pending(m.handset)
		if err != nil {
			return err
		}
		if len(jobs) >= MaxPending {
			return m.play("full")
		}
		due, delay, err := m.when()
		if errors.Is(err, errRestart) {
			continue
		}
		if err != nil {
			return err
		}
		err = m.finish(due, delay)
		if errors.Is(err, errRestart) {
			continue
		}
		return err
	}
}

func (m *menu) invalid(digits string, why error) error {
	if errors.Is(why, ErrPast) {
		if err := m.play("past"); err != nil {
			return err
		}
	} else {
		if digits != "" {
			if err := m.play("entered"); err != nil {
				return err
			}
			if err := m.a.sayDigits(digits[:min(4, len(digits))]); err != nil {
				return err
			}
		}
		if err := m.play("invalid"); err != nil {
			return err
		}
	}
	return nil
}

// After an invalid entry the caller re-enters digits directly. Star restarts
// during the first entry and exits from the error prompt, as spoken there.
func (m *menu) input(prompt string, max int, validate func(string) error) error {
	retrying := false
	for {
		digits, err := m.a.collect(m.media+"/reminder-"+prompt, max)
		if errors.Is(err, errRestart) {
			if retrying {
				return ErrHangup
			}
			return errRestart
		}
		if err != nil && !errors.Is(err, ErrTime) {
			return err
		}
		if err == nil {
			err = validate(digits)
		}
		if err == nil {
			return nil
		}
		if err := m.invalid(digits, err); err != nil {
			return err
		}
		prompt, retrying = "try-again", true
	}
}

func (m *menu) when() (time.Time, time.Duration, error) {
	if m.code == "81" {
		h, err := m.option("hours", "0123456789*")
		if err != nil {
			return time.Time{}, 0, err
		}
		if h == "*" {
			return time.Time{}, 0, errRestart
		}
		if !decimal(h) {
			return time.Time{}, 0, ErrHangup
		}
		var delay time.Duration
		err = m.input("minutes", 2, func(digits string) error {
			var err error
			delay, err = Delay(h, digits)
			return err
		})
		return time.Time{}, delay, err
	}
	meridiem, err := m.option("ampm", "12*")
	if err != nil {
		return time.Time{}, 0, err
	}
	if meridiem == "*" {
		return time.Time{}, 0, errRestart
	}
	if meridiem != "1" && meridiem != "2" {
		return time.Time{}, 0, ErrHangup
	}
	var due time.Time
	err = m.input("time", 4, func(digits string) error {
		var err error
		due, err = At(m.now(), m.code == "82", meridiem, digits)
		return err
	})
	return due, 0, err
}

func (m *menu) finish(due time.Time, delay time.Duration) error {
	j, err := NewJob(m.handset, m.code, m.media, m.now())
	if err != nil {
		return err
	}
	saved := false
	defer func() {
		if !saved {
			_ = removeIfPresent(m.store.audio(j.ID))
		}
	}()
	key, err := m.option("message", "1#*")
	if err != nil {
		return err
	}
	if key == "*" {
		return errRestart
	}
	if key == "1" {
		if err := m.play("record"); err != nil {
			return err
		}
		n, _, err := m.a.command("RECORD FILE " + quote(strings.TrimSuffix(m.store.audio(j.ID), ".wav")) + ` wav "#*" 60000 0 BEEP s=5`)
		if err != nil {
			return err
		}
		if n == int('*') {
			return errRestart
		}
		j.Audio = true
	}
	// Hangup during a prompt/recording never schedules an unfinished request.
	status, _, err := m.a.command("CHANNEL STATUS")
	if err != nil {
		return err
	}
	if status != 6 {
		return ErrHangup
	}
	now := m.now()
	if delay > 0 {
		due = now.Add(delay)
	}
	j.Due = due
	if err := m.store.Commit(j, now); err != nil {
		if errors.Is(err, ErrPast) {
			if err := m.invalid("", ErrPast); err != nil {
				return err
			}
			return errRestart
		}
		if errors.Is(err, ErrFull) {
			return m.play("full")
		}
		return err
	}
	saved = true
	if err := m.play("saved"); err != nil {
		return err
	}
	return m.a.sayTime(due)
}
