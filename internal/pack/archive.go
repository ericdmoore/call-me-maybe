package pack

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"callmemaybe/internal/voice"
)

const (
	maxArchiveBytes = 256 << 20
	maxEntryBytes   = 64 << 20
	maxSourceBytes  = 1 << 20
	maxEntries      = 4096
)

var component = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var packID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func safeName(name string) bool {
	if name == "" || len(name) > 240 || strings.Contains(name, "\\") || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if !component.MatchString(part) {
			return false
		}
	}
	return true
}

func (p *Pack) validateAudioSources() error {
	items := map[string]Item{}
	for _, item := range p.Items {
		if !safeName(item.Name) {
			return errors.New("pack: unsafe clip name")
		}
		if _, exists := items[item.Name]; exists {
			return errors.New("pack: duplicate clip name")
		}
		items[item.Name] = item
	}
	for name, source := range p.Manifest.Audio {
		item, ok := items[name]
		if !ok {
			return fmt.Errorf("pack: audio names an unknown clip %q", name)
		}
		if source.Origin != "human" && source.Origin != "rendered" {
			return fmt.Errorf("pack: %s audio origin must be human or rendered", name)
		}
		if item.Text != "" && source.SourceHash != item.hash() {
			return fmt.Errorf("pack: bundled audio for %s does not match its script/voice; rebuild or remove the audio declaration", name)
		}
		if item.Text == "" && source.SourceHash != "" {
			return fmt.Errorf("pack: %s has a source hash without a script", name)
		}
	}
	return nil
}

// SourceHash is the binding an author records when bundling a recording with
// its script. It attests which source was read, not what was actually spoken.
func (i Item) SourceHash() string { return i.hash() }

// Artifact owns a private extraction directory. No downloaded file is ever
// extracted into the live sounds directory.
type Artifact struct {
	Pack     *Pack
	Digest   string
	Warnings []string
	dir      string
}

func (a *Artifact) Close() error { return os.RemoveAll(a.dir) }

// OpenArchive checks sizes, names, checksums and every supplied audio file
// before returning an artifact which the installer may stage.
func OpenArchive(filename string) (_ *Artifact, err error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > maxArchiveBytes {
		return nil, errors.New("pack: archive must be a regular file of at most 256 MiB")
	}
	z, err := zip.NewReader(f, st.Size())
	if err != nil {
		return nil, fmt.Errorf("pack: invalid ZIP: %w", err)
	}
	if len(z.File) == 0 || len(z.File) > maxEntries {
		return nil, errors.New("pack: archive entry limit exceeded")
	}
	files := map[string][]byte{}
	folded := map[string]bool{}
	var total uint64
	for _, entry := range z.File {
		name := entry.Name
		if !safeName(name) || !entry.Mode().IsRegular() || folded[strings.ToLower(name)] {
			return nil, errors.New("pack: unsafe, duplicate or non-regular archive entry")
		}
		folded[strings.ToLower(name)] = true
		limit := uint64(maxEntryBytes)
		if name == "pack.json" || name == "SHA256SUMS" || strings.HasSuffix(name, ".md") {
			limit = maxSourceBytes
		}
		total += entry.UncompressedSize64
		if entry.UncompressedSize64 > limit || total > maxArchiveBytes {
			return nil, errors.New("pack: uncompressed archive size limit exceeded")
		}
		r, err := entry.Open()
		if err != nil {
			return nil, err
		}
		b, readErr := io.ReadAll(io.LimitReader(r, int64(limit)+1))
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || uint64(len(b)) != entry.UncompressedSize64 {
			return nil, errors.New("pack: corrupt or oversized archive entry")
		}
		files[name] = b
	}
	sums, ok := files["SHA256SUMS"]
	if !ok {
		return nil, errors.New("pack: missing SHA256SUMS")
	}
	if err := verifySums(files, sums); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "doorman-pack-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	for name, data := range files {
		dest := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return nil, err
		}
	}
	p, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if p.Manifest.ArchiveVersion != 1 {
		return nil, errors.New("pack: archive_version must be 1")
	}
	if err := distributable(p); err != nil {
		return nil, err
	}
	allowed := map[string]bool{"pack.json": true, "SHA256SUMS": true}
	if p.Story != nil {
		allowed[storyFilename(p.Manifest)] = true
	}
	var warnings []string
	for _, item := range p.Items {
		_, bundled := p.Manifest.Audio[item.Name]
		if !bundled {
			if item.Text == "" {
				return nil, fmt.Errorf("pack: %s needs bundled audio", item.Name)
			}
			continue
		}
		for _, rate := range []int{8000, 16000} {
			name := item.Name + ext(rate)
			allowed[name] = true
			data, ok := files[name]
			if !ok {
				return nil, fmt.Errorf("pack: missing %s", name)
			}
			warning, err := validateWAV(data, rate)
			if err != nil {
				return nil, fmt.Errorf("pack: %s: %w", name, err)
			}
			if warning != "" {
				warnings = append(warnings, name+": "+warning)
			}
		}
	}
	for name := range files {
		if !allowed[name] {
			return nil, fmt.Errorf("pack: undeclared payload %q", name)
		}
	}
	h := sha256.Sum256(sums)
	return &Artifact{Pack: p, Digest: hex.EncodeToString(h[:]), Warnings: warnings, dir: dir}, nil
}

func distributable(p *Pack) error {
	m := p.Manifest
	if !packID.MatchString(m.ID) {
		return errors.New("pack: id must be 1–64 lowercase letters, digits or hyphens, starting with a letter or digit")
	}
	if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Version) == "" || strings.TrimSpace(m.License) == "" {
		return errors.New("pack: name, version and license are required")
	}
	if p.Story != nil && !strings.HasSuffix(storyFilename(m), ".md") {
		return errors.New("pack: archived story source must have a .md filename")
	}
	for _, item := range p.Items {
		if _, bundled := m.Audio[item.Name]; bundled || item.Text == "" {
			continue
		}
		if !slicesContains(voice.Backends(), item.Voice.Backend) {
			return fmt.Errorf("pack: unknown renderer %q", item.Voice.Backend)
		}
		// Downloaded packs may select a locally installed model by name, never
		// an arbitrary filesystem path. Source-directory builds retain paths.
		if item.Voice.Backend == "piper" && item.Voice.ID != "" && (!component.MatchString(item.Voice.ID) || strings.HasSuffix(item.Voice.ID, ".onnx")) {
			return errors.New("pack: archived piper voices must be model names without paths or .onnx extensions")
		}
		if rate, ok := item.Voice.Settings["sample_rate"]; ok {
			n, err := strconv.Atoi(rate)
			if err != nil || n < 8000 || n > 192000 {
				return errors.New("pack: sample_rate must be between 8000 and 192000")
			}
		}
	}
	return nil
}

func storyFilename(m Manifest) string {
	if m.Story != "" {
		return m.Story
	}
	return "story.md"
}

func verifySums(files map[string][]byte, raw []byte) error {
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		hash, name, ok := strings.Cut(line, "  ")
		if !ok || len(hash) != 64 || !safeName(name) || name == "SHA256SUMS" || seen[name] {
			return errors.New("pack: malformed SHA256SUMS")
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return errors.New("pack: malformed checksum")
		}
		b, exists := files[name]
		got := sha256.Sum256(b)
		if !exists || hex.EncodeToString(got[:]) != hash {
			return fmt.Errorf("pack: checksum mismatch for %s", name)
		}
		seen[name] = true
	}
	if len(seen) != len(files)-1 {
		return errors.New("pack: every payload file must be checksummed")
	}
	return nil
}

// WriteArchive packages only declared source and audio, never the directory's
// other files (.env, private models, caches). audioDir optionally supplies a
// completed Build; empty means package source and explicitly bundled recordings.
func (p *Pack) WriteArchive(filename, audioDir string) error {
	if err := distributable(p); err != nil {
		return err
	}
	m := p.Manifest
	m.ArchiveVersion = 1
	m.Audio = map[string]AudioSource{}
	files := map[string][]byte{}
	if p.Story != nil {
		name := storyFilename(m)
		b, err := readRegular(p.Dir, name, maxSourceBytes)
		if err != nil {
			return err
		}
		files[name] = b
	}
	for _, item := range p.Items {
		source, bundled := p.Manifest.Audio[item.Name]
		dir := p.Dir
		if !bundled && item.Text == "" {
			source, bundled = AudioSource{Origin: "human"}, true
		}
		if !bundled && audioDir != "" {
			hash, err := readRegular(audioDir, item.Name+".hash", 256)
			if err != nil || string(hash) != item.hash() {
				return fmt.Errorf("pack: %s build is missing or stale", item.Name)
			}
			source = AudioSource{Origin: "rendered"}
			if item.Text != "" {
				source.SourceHash = item.hash()
			}
			bundled, dir = true, audioDir
		}
		if !bundled {
			continue
		}
		m.Audio[item.Name] = source
		for _, rate := range []int{8000, 16000} {
			name := item.Name + ext(rate)
			b, err := readRegular(dir, name, maxEntryBytes)
			if err != nil {
				return err
			}
			if _, err := validateWAV(b, rate); err != nil {
				return fmt.Errorf("pack: %s: %w", name, err)
			}
			files[name] = b
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if len(b)+1 > maxSourceBytes {
		return errors.New("pack: manifest size limit exceeded")
	}
	files["pack.json"] = append(b, '\n')
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var sums bytes.Buffer
	var total int
	for _, name := range names {
		total += len(files[name])
		h := sha256.Sum256(files[name])
		fmt.Fprintf(&sums, "%x  %s\n", h, name)
	}
	if total+sums.Len() > maxArchiveBytes || len(names)+1 > maxEntries {
		return errors.New("pack: archive size limit exceeded")
	}
	files["SHA256SUMS"] = sums.Bytes()
	names = append(names, "SHA256SUMS")
	sort.Strings(names)
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, name := range names {
		entry, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		if _, err = entry.Write(files[name]); err != nil {
			return err
		}
	}
	if err := z.Close(); err != nil {
		return err
	}
	if buf.Len() > maxArchiveBytes {
		return errors.New("pack: compressed archive size limit exceeded")
	}
	return atomicFile(filename, buf.Bytes(), 0o644)
}

func readRegular(dir, name string, limit int64) ([]byte, error) {
	if !safeName(name) {
		return nil, errors.New("pack: unsafe filename")
	}
	// Root prevents a local author's symlink from exporting files outside the
	// source/build directory. Archive entries themselves cannot be symlinks.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return nil, fmt.Errorf("pack: missing or inaccessible %s: %w", name, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > limit {
		return nil, errors.New("pack: file is not regular or exceeds its size limit")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("pack: file size limit exceeded")
	}
	return b, nil
}

func copyAudio(src, dst, name string) error {
	for _, rate := range []int{8000, 16000} {
		b, err := readRegular(src, name+ext(rate), maxEntryBytes)
		if err != nil {
			return err
		}
		if _, err := validateWAV(b, rate); err != nil {
			return fmt.Errorf("pack: %s: %w", name, err)
		}
		dest := filepath.Join(dst, filepath.FromSlash(name)+ext(rate))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func atomicFile(dest string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(dest), ".pack-write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), dest)
}
