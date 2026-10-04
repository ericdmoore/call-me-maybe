package reminder

import (
	"errors"
	"testing"
	"time"
)

func chicago(t *testing.T) *time.Location {
	t.Helper()
	l, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestClockEntry(t *testing.T) {
	now := time.Date(2026, 10, 3, 6, 0, 0, 0, chicago(t))
	for _, tc := range []struct {
		digits, ampm string
		tomorrow     bool
		want         string
		err          error
	}{
		{"730", "1", false, "2026-10-03 07:30", nil},
		{"0730", "2", false, "2026-10-03 19:30", nil},
		{"1200", "2", false, "2026-10-03 12:00", nil},
		{"1200", "1", true, "2026-10-04 00:00", nil},
		{"600", "1", false, "", ErrPast},
		{"530", "1", false, "", ErrPast},
		{"1260", "1", false, "", ErrTime},
		{"1300", "1", false, "", ErrTime},
		{"0000", "1", false, "", ErrTime},
		{"73", "1", false, "", ErrTime},
		{"12345", "1", false, "", ErrTime},
		{"1x30", "1", false, "", ErrTime},
		{"730", "3", false, "", ErrTime},
	} {
		t.Run(tc.digits+tc.ampm+tc.want, func(t *testing.T) {
			got, err := At(now, tc.tomorrow, tc.ampm, tc.digits)
			if !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
			if err == nil && got.Format("2006-01-02 15:04") != tc.want {
				t.Errorf("time = %s", got)
			}
		})
	}
}

func TestCalendarDayAndDST(t *testing.T) {
	l := chicago(t)
	before := time.Date(2026, 3, 7, 23, 0, 0, 0, l)
	if _, err := At(before, true, "1", "230"); !errors.Is(err, ErrTime) {
		t.Fatalf("spring gap: %v", err)
	}
	got, err := At(before, true, "1", "730")
	if err != nil || got.Day() != 8 || got.Hour() != 7 || got.Sub(before) != 7*time.Hour+30*time.Minute {
		t.Fatalf("spring calendar: %s %v", got, err)
	}
	first, _ := time.Parse(time.RFC3339, "2026-11-01T01:00:00-05:00")
	got, err = At(first.In(l), false, "1", "130")
	if err != nil || got.Sub(first) != 30*time.Minute {
		t.Fatalf("first fold: %s %v", got, err)
	}
	after := first.Add(45 * time.Minute)
	got, err = At(after.In(l), false, "1", "130")
	if err != nil || got.Sub(after) != 45*time.Minute {
		t.Fatalf("second fold: %s %v", got, err)
	}
	end := time.Date(2026, 12, 31, 23, 0, 0, 0, l)
	got, err = At(end, true, "1", "730")
	if err != nil || got.Year() != 2027 || got.Month() != 1 || got.Day() != 1 {
		t.Fatalf("year boundary: %s %v", got, err)
	}
}

func TestRelativeMinutesOverflowAndBounds(t *testing.T) {
	for _, tc := range []struct {
		h, m string
		want time.Duration
	}{
		{"0", "1", time.Minute}, {"1", "99", 2*time.Hour + 39*time.Minute}, {"9", "99", 10*time.Hour + 39*time.Minute},
		{"9", "00", 9 * time.Hour}, {"0", "0", 0}, {"10", "0", 0}, {"-1", "1", 0}, {"1", "100", 0}, {"1", "", 0}, {"x", "1", 0},
	} {
		got, err := Delay(tc.h, tc.m)
		if got != tc.want || (err != nil) != (tc.want == 0) {
			t.Errorf("delay %s/%s: %s %v", tc.h, tc.m, got, err)
		}
	}
}
