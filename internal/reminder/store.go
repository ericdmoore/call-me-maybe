package reminder

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	MaxPending    = 10
	Account       = "cmm-reminder"
	retryInterval = 3 * time.Minute
)

var (
	idPattern       = regexp.MustCompile(`^[a-f0-9]{32}$`)
	handsetPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	mediaPattern    = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	retryEndPattern = regexp.MustCompile(`^EndRetry: [1-9][0-9]* [1-9][0-9]* \(([0-9]+)\)$`)
	ErrFull         = errors.New("reminder limit reached")
	ErrMissing      = errors.New("reminder is no longer pending")
)

type Job struct {
	ID      string    `json:"id"`
	Handset string    `json:"handset"`
	Code    string    `json:"code"`
	Due     time.Time `json:"due"`
	Created time.Time `json:"created"`
	Media   string    `json:"media"`
	Audio   bool      `json:"audio"`
	State   string    `json:"state"`
	// Next is a verified queued-retry snapshot, not our own retry clock.
	// Active or uncertain attempts leave it zero: a future mtime alone is not a retry.
	Next time.Time `json:"-"`
}

// Store lives wholly in Asterisk's spool. Its private files contain handset
// IDs and times, never caller IDs, credentials or entered digit strings.
// A flock serialises short mutations across independent AGI processes; it is
// never held while a person is listening, typing or recording.
type Store struct{ Spool, root string }

func Open(spool string) (*Store, error) {
	if !filepath.IsAbs(spool) || !mediaPattern.MatchString(spool) {
		return nil, errors.New("invalid Asterisk spool path")
	}
	s := &Store{Spool: filepath.Clean(spool), root: filepath.Join(spool, "cmm-reminders")}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(s.root, 0o700); err != nil {
		return nil, err
	}
	// These are Asterisk's directories. Do not change their existing modes.
	for _, dir := range []string{"outgoing", "outgoing_done"} {
		if err := os.MkdirAll(filepath.Join(spool, dir), 0o750); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func NewJob(handset, code, media string, now time.Time) (Job, error) {
	if !handsetPattern.MatchString(handset) || !validCode(code) || !mediaPattern.MatchString(media) {
		return Job{}, errors.New("invalid reminder destination or media")
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Job{}, err
	}
	return Job{ID: hex.EncodeToString(b[:]), Handset: handset, Code: code, Media: media, Created: now, State: "pending"}, nil
}

func validCode(code string) bool        { return code == "80" || code == "81" || code == "82" }
func (s *Store) audio(id string) string { return filepath.Join(s.root, id+".wav") }
func (s *Store) meta(id string) string  { return filepath.Join(s.root, id+".json") }
func (s *Store) queue(dir, id string) string {
	return filepath.Join(s.Spool, dir, "cmm-reminder-"+id+".call")
}

func (s *Store) lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// atomic writes outside outgoing, syncs, and only then publishes. Asterisk
// must never observe half a call file. root and outgoing share a filesystem.
func (s *Store) atomic(path string, data []byte, due time.Time) error {
	f, err := os.CreateTemp(s.root, ".stage-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil && !due.IsZero() {
		err = os.Chtimes(f.Name(), due, due)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *Store) write(j Job) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return s.atomic(s.meta(j.ID), b, time.Time{})
}

func (s *Store) read(id string) (Job, error) {
	if !idPattern.MatchString(id) {
		return Job{}, ErrMissing
	}
	b, err := os.ReadFile(s.meta(id))
	if errors.Is(err, fs.ErrNotExist) {
		// A menu can outlive the job it announced: the housekeeping process
		// removes completed jobs without holding up an interactive prompt.
		return Job{}, ErrMissing
	}
	if err != nil {
		return Job{}, err
	}
	var j Job
	if json.Unmarshal(b, &j) != nil || j.ID != id || !handsetPattern.MatchString(j.Handset) || !validCode(j.Code) || !mediaPattern.MatchString(j.Media) {
		return Job{}, errors.New("invalid reminder state")
	}
	return j, nil
}

// Commit's rename into outgoing is the scheduling boundary. No successful
// acknowledgement is spoken until this returns. Draft recordings are harmless
// until then; a crashed draft is pruned later.
func (s *Store) Commit(j Job, now time.Time) error {
	if !idPattern.MatchString(j.ID) || !handsetPattern.MatchString(j.Handset) || !validCode(j.Code) || !mediaPattern.MatchString(j.Media) || j.State != "pending" {
		return errors.New("invalid reminder")
	}
	if !j.Due.After(now) {
		return ErrPast
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	jobs, err := s.pending(j.Handset)
	if err != nil {
		return err
	}
	if len(jobs) >= MaxPending {
		return ErrFull
	}
	if _, err := os.Stat(s.meta(j.ID)); !errors.Is(err, fs.ErrNotExist) {
		return errors.New("reminder already exists")
	}
	if j.Audio {
		fi, err := os.Stat(s.audio(j.ID))
		if err != nil || fi.Size() <= 44 {
			return errors.New("empty recording")
		}
		if err := os.Chmod(s.audio(j.ID), 0o600); err != nil {
			return err
		}
		f, err := os.Open(s.audio(j.ID))
		if err != nil {
			return err
		}
		err = f.Sync()
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	if err := s.write(j); err != nil {
		return err
	}
	// Direct PJSIP dial: no lobby, voicemail fallback, quiet-hour gate or
	// auto-answer header. The physical handset's DND remains authoritative.
	call := fmt.Sprintf("Channel: PJSIP/%s\nCallerid: Scheduled call <*%s>\nWaitTime: 30\nMaxRetries: 2\nRetryTime: %d\nAccount: %s\nContext: cmm-reminder-deliver\nExtension: %s\nPriority: 1\nArchive: yes\n", j.Handset, j.Code, int(retryInterval/time.Second), Account, j.ID)
	if err := s.atomic(s.queue("outgoing", j.ID), []byte(call), j.Due); err != nil {
		// A failure after rename still must not leave an unacknowledged job.
		_ = os.Remove(s.queue("outgoing", j.ID))
		_ = os.Remove(s.meta(j.ID))
		return err
	}
	return nil
}

func (s *Store) Pending(handset string) ([]Job, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return s.pending(handset)
}

func (s *Store) pending(handset string) ([]Job, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	var out []Job
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		j, err := s.read(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		if j.Handset != handset || j.State != "pending" {
			continue
		}
		next, err := queuedRetry(s.queue("outgoing", j.ID))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} // finished, or never published
		if err != nil {
			return nil, err
		}
		j.Next = next
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].Due.Equal(out[k].Due) {
			return out[i].ID < out[k].ID
		}
		return out[i].Due.Before(out[k].Due)
	})
	return out, nil
}

// Asterisk's pbx_spool.c also moves mtime into the future during StartRetry
// and DelayedRetry. Only a final EndRetry plus its matching mtime establishes
// a waiting retry. Appending the record and updating mtime are separate writes;
// a partial or inconsistent snapshot must not invent a future appointment.
func queuedRetry(path string) (time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	const maxQueueBytes = 64 << 10
	b, err := io.ReadAll(io.LimitReader(f, maxQueueBytes+1))
	if err != nil {
		return time.Time{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		return time.Time{}, err
	}
	if len(b) == 0 || len(b) > maxQueueBytes || fi.Size() != int64(len(b)) || b[len(b)-1] != '\n' {
		return time.Time{}, nil
	}
	last := string(b[:len(b)-1])
	last = last[strings.LastIndexByte(last, '\n')+1:]
	match := retryEndPattern.FindStringSubmatch(last)
	if match == nil {
		return time.Time{}, nil
	}
	ended, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || ended <= 0 || !fi.ModTime().Equal(time.Unix(ended, 0).Add(retryInterval)) {
		return time.Time{}, nil
	}
	return fi.ModTime(), nil
}

func (s *Store) Cancel(handset, id string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	j, err := s.read(id)
	if err != nil {
		return err
	}
	if j.Handset != handset || j.State != "pending" {
		return ErrMissing
	}
	// A tombstone wins against an answer racing cancellation. An attempt
	// already ringing may ring out, but cannot play a message or retry.
	j.State = "cancelled"
	if err := s.write(j); err != nil {
		return err
	}
	if err := os.Remove(s.queue("outgoing", id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return removeIfPresent(s.audio(id))
}

// Claim consumes a job exactly once, before playing anything. Asterisk regards
// an answer as success; hanging up during the message never schedules a retry.
func (s *Store) Claim(handset, id string) (Job, error) {
	unlock, err := s.lock()
	if err != nil {
		return Job{}, err
	}
	defer unlock()
	j, err := s.read(id)
	if err != nil {
		return Job{}, err
	}
	if j.Handset != handset || j.State != "pending" {
		return Job{}, ErrMissing
	}
	j.State = "answered"
	if err := s.write(j); err != nil {
		return Job{}, err
	}
	return j, nil
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Prune runs off the call path. Exhausted call files are archived by Asterisk;
// their recordings need deleting even if nobody ever opens another menu.
// A day also bounds crash leftovers, while never expiring a queued reminder.
func (s *Store) Prune(now time.Time) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == ".lock" {
			continue
		}
		fi, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // sibling metadata/audio removed above
		}
		if err != nil {
			return err
		}
		id := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if idPattern.MatchString(id) {
			if _, err := os.Stat(s.queue("outgoing", id)); err == nil {
				continue
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			_, archived := os.Stat(s.queue("outgoing_done", id))
			if archived != nil && !errors.Is(archived, fs.ErrNotExist) {
				return archived
			}
			if archived == nil || now.Sub(fi.ModTime()) > 24*time.Hour {
				for _, path := range []string{s.meta(id), s.audio(id)} {
					if err := removeIfPresent(path); err != nil {
						return err
					}
				}
			}
		} else if strings.HasPrefix(e.Name(), ".stage-") && now.Sub(fi.ModTime()) > 24*time.Hour {
			if err := removeIfPresent(filepath.Join(s.root, e.Name())); err != nil {
				return err
			}
		}
	}
	// Only our prefixed files. Other applications own the rest of this spool.
	archives, err := os.ReadDir(filepath.Join(s.Spool, "outgoing_done"))
	if err != nil {
		return err
	}
	for _, e := range archives {
		id := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "cmm-reminder-"), ".call")
		if idPattern.MatchString(id) && e.Name() == "cmm-reminder-"+id+".call" {
			if err := removeIfPresent(s.queue("outgoing_done", id)); err != nil {
				return err
			}
		}
	}
	return nil
}
