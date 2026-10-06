package inbox

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const contactQuiet = 15 * time.Second
const contactChoice = 2 * time.Minute
const shelfLimit = 64

// ContactShelf holds split MMS conversations off the call path. Files contain
// contact data, so they have the same private permissions as the inbox state.
// Reader owns the shelf and serialises its updates with incoming messages.
type ContactShelf struct {
	lock    *os.File
	dir     string
	batches map[string]contactBatch
}

type contactBatch struct {
	Message    Message   `json:"message"`
	IDs        []string  `json:"ids"`
	QuietUntil time.Time `json:"quiet_until"`
	Deadline   time.Time `json:"deadline"`
	NoticeAt   time.Time `json:"notice_at"`
	// Words and Targets are pinned before writing any books. After a crash a
	// partially completed explicit selection must never revert to House.
	Words       []string `json:"words,omitempty"`
	Targets     []string `json:"targets,omitempty"`
	DefaultWord string   `json:"default_word,omitempty"`
	NoDefault   bool     `json:"no_default,omitempty"`
}

func OpenContactShelf(dir string) (*ContactShelf, error) {
	s := &ContactShelf{dir: filepath.Join(dir, "contacts-pending"), batches: map[string]contactBatch{}}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return nil, errors.New("cannot create pending contact directory")
	}
	lock, err := os.OpenFile(filepath.Join(s.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("cannot lock pending contacts")
	}
	s.lock = lock
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lock.Close()
		return nil, errors.New("another inbox owns pending contacts")
	}
	success := false
	defer func() {
		if !success {
			s.Close()
		}
	}()
	paths, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) > shelfLimit {
		return nil, errors.New("too many pending contact batches")
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.Size() > 1<<20 {
			return nil, errors.New("cannot read pending contacts")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("cannot read pending contacts")
		}
		var b contactBatch
		if json.Unmarshal(data, &b) != nil || b.Message.ID == "" || len(b.IDs) == 0 || len(b.IDs) > 100 || len(b.Message.Contacts) > 100 || b.Message.MediaCount < 0 || b.Message.MediaCount > 3 || len(b.Words) != len(b.Targets) || b.Deadline.IsZero() {
			return nil, errors.New("invalid pending contact state")
		}
		key := shelfKey(b.Message.From, b.Message.To)
		if filepath.Base(path) != key+".json" {
			return nil, errors.New("invalid pending contact state")
		}
		s.batches[key] = b
	}
	success = true
	return s, nil
}

func shelfKey(from, to string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(from+"\x00"+to)))
}

func (s *ContactShelf) put(key string, b contactBatch) error {
	if _, ok := s.batches[key]; !ok && len(s.batches) >= shelfLimit {
		return errors.New("pending contact shelf is full")
	}
	data, err := json.Marshal(b)
	if err != nil {
		return errors.New("cannot encode pending contacts")
	}
	tmp, err := os.CreateTemp(s.dir, ".pending-*")
	if err != nil {
		return errors.New("cannot save pending contacts")
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(s.dir, key+".json"))
	}
	if err != nil {
		return errors.New("cannot save pending contacts")
	}
	s.batches[key] = b
	return nil
}

func (s *ContactShelf) remove(key string) error {
	err := os.Remove(filepath.Join(s.dir, key+".json"))
	if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot remove pending contacts")
	}
	delete(s.batches, key)
	return nil
}

// forget drops a batch from memory without touching its file, for the one
// case where the file could not be removed after its work was done.
func (s *ContactShelf) forget(key string) {
	delete(s.batches, key)
}

func (s *ContactShelf) keys() []string {
	keys := make([]string, 0, len(s.batches))
	for k := range s.batches {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Close releases the single inbox owner's lock.
func (s *ContactShelf) Close() error {
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	s.lock = nil
	return err
}
