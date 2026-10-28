package books

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const MaxAudiobookBytes = 256 << 20

var ErrAudiobookArchive = errors.New("books: invalid or unsupported audiobook archive")
var compressedSegment = regexp.MustCompile(`^segment-[0-9]{6}\.opus$`)

type recordedManifest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Audiobook struct {
		Work struct {
			Language string `json:"language"`
		} `json:"work"`
		Recording struct {
			DurationMS int64 `json:"duration_ms"`
		} `json:"recording"`
		Audio struct {
			Codec      string `json:"codec"`
			Container  string `json:"container"`
			SampleRate int    `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"audio"`
		Chapters []struct {
			Number     int    `json:"number"`
			Title      string `json:"title"`
			DurationMS int64  `json:"duration_ms"`
			Segments   []struct {
				File       string `json:"file"`
				StartMS    int64  `json:"start_ms"`
				DurationMS int64  `json:"duration_ms"`
			} `json:"segments"`
		} `json:"chapters"`
	} `json:"audiobook"`
}

// ImportAudiobook verifies a bounded on-disk archive, retains compressed segments,
// and publishes only after checksums, decoding and offline prompts are ready.
// It never expands the novel to PCM or downloads content on the call path.
func ImportAudiobook(ctx context.Context, archive, hash, root, menus string) error {
	if !revisionPattern.MatchString(hash) {
		return ErrAudiobookArchive
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	input, err := os.Open(archive)
	if err != nil {
		return ErrAudiobookArchive
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxAudiobookBytes {
		return ErrAudiobookArchive
	}
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(input, MaxAudiobookBytes+1))
	if err != nil || n != info.Size() || hex.EncodeToString(digest.Sum(nil)) != hash {
		return ErrAudiobookArchive
	}
	if !boundedZipDirectory(input, info.Size()) {
		return ErrAudiobookArchive
	}
	z, err := zip.NewReader(input, info.Size())
	if err != nil || len(z.File) < 3 || len(z.File) > 4096 {
		return ErrAudiobookArchive
	}
	files := map[string]*zip.File{}
	var expanded uint64
	for _, f := range z.File {
		name := f.Name
		limit := uint64(1 << 20)
		if name == "pack.json" {
			limit = maxJSONBytes
		}
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00\r\n") || !f.Mode().IsRegular() || files[name] != nil || f.UncompressedSize64 > limit {
			return ErrAudiobookArchive
		}
		expanded += f.UncompressedSize64
		if expanded > 512<<20 {
			return ErrAudiobookArchive
		}
		files[name] = f
	}
	read := func(name string) ([]byte, error) {
		f := files[name]
		if f == nil {
			return nil, ErrAudiobookArchive
		}
		r, err := f.Open()
		if err != nil {
			return nil, ErrAudiobookArchive
		}
		defer r.Close()
		data, err := io.ReadAll(io.LimitReader(r, int64(f.UncompressedSize64)+1))
		if err != nil || uint64(len(data)) != f.UncompressedSize64 {
			return nil, ErrAudiobookArchive
		}
		return data, nil
	}
	sums, err := read("SHA256SUMS")
	if err != nil {
		return err
	}
	expected := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || !revisionPattern.MatchString(sum) || files[name] == nil || name == "SHA256SUMS" || expected[name] != "" {
			return ErrAudiobookArchive
		}
		expected[name] = sum
	}
	if len(expected) != len(files)-1 {
		return ErrAudiobookArchive
	}
	manifest, err := read("pack.json")
	if err != nil {
		return err
	}
	var m recordedManifest
	if json.Unmarshal(manifest, &m) != nil || m.Kind != "audiobook" || !archiveID.MatchString(m.ID) || !validText(m.Name) || !idPattern.MatchString(m.Audiobook.Work.Language) || m.Audiobook.Audio.Codec != "opus" || m.Audiobook.Audio.Container != "ogg" || m.Audiobook.Audio.SampleRate != 16000 || m.Audiobook.Audio.Channels != 1 || len(m.Audiobook.Chapters) == 0 || len(m.Audiobook.Chapters) > 999 || m.Audiobook.Recording.DurationMS <= 0 || m.Audiobook.Recording.DurationMS > 48*3600_000 {
		return ErrAudiobookArchive
	}
	seen := map[string]bool{}
	var duration int64
	for i, ch := range m.Audiobook.Chapters {
		if ch.Number != i+1 || !validText(ch.Title) || ch.DurationMS <= 0 || ch.DurationMS > 86400_000 || len(ch.Segments) == 0 || len(ch.Segments) > 4094 {
			return ErrAudiobookArchive
		}
		var end, sum int64
		for _, seg := range ch.Segments {
			// Source timestamps are rounded independently to milliseconds. Permit
			// only that quantisation error at each boundary, never an actual gap.
			if seen[seg.File] || files[seg.File] == nil || !strings.HasPrefix(seg.File, "audio/") || !strings.HasSuffix(seg.File, ".opus") || abs64(seg.StartMS-end) > 1 || seg.DurationMS <= 0 || seg.DurationMS > 45000 {
				return ErrAudiobookArchive
			}
			seen[seg.File] = true
			sum += seg.DurationMS
			end = seg.StartMS + seg.DurationMS
		}
		if abs64(end-ch.DurationMS) > 1 || abs64(sum-ch.DurationMS) > 120 {
			return ErrAudiobookArchive
		}
		duration += ch.DurationMS
	}
	if duration != m.Audiobook.Recording.DurationMS || len(seen) != len(files)-2 {
		return ErrAudiobookArchive
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	unlock, err := lockLibrary(root)
	if err != nil {
		return err
	}
	defer unlock()
	catalog, err := Load(root)
	if os.IsNotExist(err) {
		catalog = Catalog{Schema: 1}
	} else if err != nil {
		return err
	}
	id := m.ID
	if len(id) > 64 {
		sum := sha256.Sum256([]byte(id))
		id = "recorded-" + hex.EncodeToString(sum[:16])
	}
	b := Book{ID: id, Title: m.Name, Language: m.Audiobook.Work.Language, Origin: "store", Storage: "opus", Revision: hash}
	stage, err := os.MkdirTemp(root, ".audiobook-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for name, f := range files {
		if name == "SHA256SUMS" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := f.Open()
		if err != nil {
			return ErrAudiobookArchive
		}
		h := sha256.New()
		_, err = io.Copy(h, io.LimitReader(r, int64(f.UncompressedSize64)+1))
		r.Close()
		if err != nil || hex.EncodeToString(h.Sum(nil)) != expected[name] {
			return ErrAudiobookArchive
		}
	}
	segment := 0
	for i, ch := range m.Audiobook.Chapters {
		var playlist strings.Builder
		playlist.WriteString("ffconcat version 1.0\n")
		for _, seg := range ch.Segments {
			name := fmt.Sprintf("segment-%06d.opus", segment)
			segment++
			data, err := read(seg.File)
			if err != nil {
				return err
			}
			file := filepath.Join(stage, name)
			if err := os.WriteFile(file, data, 0644); err != nil {
				return err
			}
			job, cancel := context.WithTimeout(ctx, 30*time.Second)
			cmd := exec.CommandContext(job, "ffmpeg", "-nostdin", "-v", "error", "-protocol_whitelist", "file", "-f", "ogg", "-i", file, "-map", "0:a:0", "-vn", "-t", "46", "-ar", "16000", "-ac", "1", "-f", "s16le", "pipe:1")
			bounded := &previewPCM{dst: io.Discard, limit: 46 * sampleRate * 2}
			cmd.Stdout = bounded
			err = cmd.Run()
			cancel()
			ms := bounded.n * 1000 / (sampleRate * 2)
			if err != nil || bounded.n%2 != 0 || abs64(ms-seg.DurationMS) > 120 {
				return ErrAudiobookArchive
			}
			fmt.Fprintf(&playlist, "file %s\nduration %.6f\n", name, float64(seg.DurationMS)/1000)
		}
		name := chapterName(i) + ".ffconcat"
		if err := os.WriteFile(filepath.Join(stage, name), []byte(playlist.String()), 0644); err != nil {
			return err
		}
		if err := speak(ctx, filepath.Join(stage, chapterName(i)+"-title.sln16"), ch.Title); err != nil {
			return err
		}
		b.Chapters = append(b.Chapters, Chapter{Title: ch.Title, Audio: name, Seconds: float64(ch.DurationMS) / 1000})
	}
	if err := speak(ctx, filepath.Join(stage, "title.sln16"), b.Title); err != nil {
		return err
	}
	if err := os.Chmod(stage, 0755); err != nil {
		return err
	}
	return publishImported(ctx, root, b, stage, menus, catalog)
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// archive/zip reads central headers until EOF and compares the count modulo
// 65536. Bound the real directory before it allocates File objects; a lying
// directory count must not turn a bounded archive into unbounded metadata.
// These packs fit ordinary single-disk ZIP; ZIP64/self-extracting forms are not needed.
func boundedZipDirectory(reader io.ReaderAt, size int64) bool {
	if size < 22 {
		return false
	}
	n := int64(65557)
	if size < n {
		n = size
	}
	tail := make([]byte, n)
	if _, err := reader.ReadAt(tail, size-n); err != nil {
		return false
	}
	pos := -1
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:i+4]) == 0x06054b50 && i+22+int(binary.LittleEndian.Uint16(tail[i+20:i+22])) == len(tail) {
			pos = i
			break
		}
	}
	if pos < 0 {
		return false
	}
	end := tail[pos:]
	count := int(binary.LittleEndian.Uint16(end[10:12]))
	length := int64(binary.LittleEndian.Uint32(end[12:16]))
	offset := int64(binary.LittleEndian.Uint32(end[16:20]))
	if binary.LittleEndian.Uint16(end[4:6]) != 0 || binary.LittleEndian.Uint16(end[6:8]) != 0 || int(binary.LittleEndian.Uint16(end[8:10])) != count || count < 3 || count > 4096 || length > 8<<20 || offset+length != size-n+int64(pos) {
		return false
	}
	cursor := offset
	for i := 0; i < count; i++ {
		var header [46]byte
		if cursor+46 > offset+length {
			return false
		}
		if _, err := reader.ReadAt(header[:], cursor); err != nil || binary.LittleEndian.Uint32(header[:4]) != 0x02014b50 {
			return false
		}
		cursor += 46 + int64(binary.LittleEndian.Uint16(header[28:30])) + int64(binary.LittleEndian.Uint16(header[30:32])) + int64(binary.LittleEndian.Uint16(header[32:34]))
		if cursor > offset+length {
			return false
		}
	}
	return cursor == offset+length
}

func compressedChapterReady(dir string, ch Chapter) bool {
	file, err := os.Open(filepath.Join(dir, ch.Audio))
	if err != nil {
		return false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil || len(data) == 1<<20 {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) < 3 || lines[0] != "ffconcat version 1.0" || len(lines)%2 != 1 {
		return false
	}
	var seconds float64
	for i := 1; i < len(lines); i += 2 {
		name, ok := strings.CutPrefix(lines[i], "file ")
		if !ok || !compressedSegment.MatchString(name) {
			return false
		}
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 1<<20 {
			return false
		}
		duration, ok := strings.CutPrefix(lines[i+1], "duration ")
		if !ok {
			return false
		}
		value, err := strconv.ParseFloat(duration, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 45 {
			return false
		}
		seconds += value
	}
	return math.Abs(seconds-ch.Seconds) <= 0.150
}
