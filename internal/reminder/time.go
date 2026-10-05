// Package reminder implements the handset's one-shot reminders. Asterisk owns
// the media, ringing, durable call queue and retries; no scheduler runs in the
// lobby daemon and no carrier or speech service is involved.
package reminder

import (
	"errors"
	"strconv"
	"time"
)

var (
	ErrTime = errors.New("invalid time")
	ErrPast = errors.New("time has already passed")
)

// At accepts HMM or HHMM after a separate AM/PM choice. Calendar arithmetic
// matters: tomorrow is not necessarily 24 hours away. Spring's missing hour
// is refused, rather than silently turned into a different appointment. In
// autumn's repeated hour, choose the first occurrence still in the future.
func At(now time.Time, tomorrow bool, meridiem, digits string) (time.Time, error) {
	if (meridiem != "1" && meridiem != "2") || (len(digits) != 3 && len(digits) != 4) || !decimal(digits) {
		return time.Time{}, ErrTime
	}
	h, _ := strconv.Atoi(digits[:len(digits)-2])
	m, _ := strconv.Atoi(digits[len(digits)-2:])
	if h < 1 || h > 12 || m > 59 {
		return time.Time{}, ErrTime
	}
	h %= 12
	if meridiem == "2" {
		h += 12
	}
	d := now
	if tomorrow {
		d = d.AddDate(0, 0, 1)
	}
	y, mo, day := d.Date()
	// Enumerating UTC minutes around this date handles both hour and half-hour
	// transitions, without depending on time.Date's unspecified fold choice.
	start := time.Date(y, mo, day, 0, 0, 0, 0, now.Location()).Add(-3 * time.Hour)
	found := false
	for t := start; t.Before(start.Add(30 * time.Hour)); t = t.Add(time.Minute) {
		ty, tm, td := t.Date()
		if ty == y && tm == mo && td == day && t.Hour() == h && t.Minute() == m {
			found = true
			if t.After(now) {
				return t, nil
			}
		}
	}
	if found {
		return time.Time{}, ErrPast
	}
	return time.Time{}, ErrTime
}

// Delay deliberately accepts 99 minutes: arithmetic carries the overflow.
func Delay(hours, minutes string) (time.Duration, error) {
	if len(hours) != 1 || len(minutes) < 1 || len(minutes) > 2 || !decimal(hours) || !decimal(minutes) {
		return 0, ErrTime
	}
	h, _ := strconv.Atoi(hours)
	m, _ := strconv.Atoi(minutes)
	d := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute
	if d == 0 {
		return 0, ErrTime
	}
	return d, nil
}

func decimal(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}
