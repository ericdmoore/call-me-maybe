package books

import (
	"archive/zip"
	"context"
	"crypto/sha256"
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
	"strings"
	"time"
)

const MaxRecordedBytes int64 = 256 << 20

var ErrRecordedArchive = errors.New("books: invalid or unsupported recorded archive")
var segmentName = regexp.MustCompile(`^audio/[0-9]{2,4}/[0-9]{4}\.opus$`)

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

func concatChapter(ch Chapter) string {
	var out strings.Builder
	out.WriteString("ffconcat version 1.0\n")
	for _, seg := range ch.Segments {
		fmt.Fprintf(&out, "file '%s'\nduration %.6f\n", seg.Audio, seg.Seconds)
	}
	return out.String()
}

func validChapterAudio(ch Chapter, i int) bool {
	if len(ch.Segments) == 0 {
		return ch.Audio == chapterName(i)+".sln16"
	}
	if ch.Audio != chapterName(i)+".ffconcat" || len(ch.Segments) > 4094 {
		return false
	}
	total := 0.0
	seen := map[string]bool{}
	for _, seg := range ch.Segments {
		if !segmentName.MatchString(seg.Audio) || seen[seg.Audio] || seg.Bytes <= 0 || seg.Bytes > 2<<20 || !revisionPattern.MatchString(seg.SHA256) || math.IsNaN(seg.Seconds) || seg.Seconds <= 0 || seg.Seconds > 45 {
			return false
		}
		seen[seg.Audio] = true
		total += seg.Seconds
	}
	return math.Abs(total-ch.Seconds) < 0.001
}

// ImportRecordedWithMenus reads an already downloaded local archive. Both local
// and hosted installations use this path; neither requires an account at playback.
// Each compressed segment is copied and verified separately. At most 46 seconds
// of decoded PCM passes through a counting writer, and none is retained on disk.
func ImportRecordedWithMenus(ctx context.Context, archive, hash, root, menuSource string) error {
	return importRecorded(ctx, archive, hash, root, menuSource, "store")
}

// ImportLocalRecordedWithMenus uses identical verification without implying a
// hosted purchase. Local content remains fully playable without enrolment.
func ImportLocalRecordedWithMenus(ctx context.Context, archive, hash, root, menuSource string) error {
	return importRecorded(ctx, archive, hash, root, menuSource, "cli")
}

func importRecorded(ctx context.Context, archive, hash, root, menuSource, origin string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > MaxRecordedBytes {
		return ErrRecordedArchive
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.NewSectionReader(f, 0, st.Size())); err != nil || hex.EncodeToString(h.Sum(nil)) != hash {
		return ErrRecordedArchive
	}
	z, err := zip.NewReader(f, st.Size())
	if err != nil || len(z.File) < 3 || len(z.File) > 4096 {
		return ErrRecordedArchive
	}
	files := map[string]*zip.File{}
	totalBytes := uint64(0)
	for _, entry := range z.File {
		name := entry.Name
		if path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00\r\n") || !entry.Mode().IsRegular() || files[name] != nil || entry.UncompressedSize64 > 2<<20 {
			return ErrRecordedArchive
		}
		totalBytes += entry.UncompressedSize64
		if totalBytes > 300<<20 {
			return ErrRecordedArchive
		}
		files[name] = entry
	}
	read := func(name string) ([]byte, error) {
		entry := files[name]
		if entry == nil {
			return nil, ErrRecordedArchive
		}
		r, e := entry.Open()
		if e != nil {
			return nil, e
		}
		defer r.Close()
		return io.ReadAll(io.LimitReader(r, (2<<20)+1))
	}
	raw, err := read("pack.json")
	if err != nil {
		return ErrRecordedArchive
	}
	var m recordedManifest
	if json.Unmarshal(raw, &m) != nil || m.Kind != "audiobook" || !archiveID.MatchString(m.ID) || !validText(m.Name) || !idPattern.MatchString(m.Audiobook.Work.Language) || m.Audiobook.Audio.Codec != "opus" || m.Audiobook.Audio.Container != "ogg" || m.Audiobook.Audio.SampleRate != sampleRate || m.Audiobook.Audio.Channels != 1 || len(m.Audiobook.Chapters) == 0 || len(m.Audiobook.Chapters) > 999 || m.Audiobook.Recording.DurationMS <= 0 || m.Audiobook.Recording.DurationMS > 48*3600*1000 {
		return ErrRecordedArchive
	}
	sumData, err := read("SHA256SUMS")
	if err != nil {
		return ErrRecordedArchive
	}
	sums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(sumData)), "\n") {
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || !revisionPattern.MatchString(sum) || sums[name] != "" || files[name] == nil || name == "SHA256SUMS" {
			return ErrRecordedArchive
		}
		sums[name] = sum
	}
	if len(sums) != len(files)-1 {
		return ErrRecordedArchive
	}
	manifestSum := sha256.Sum256(raw)
	if fmt.Sprintf("%x", manifestSum) != sums["pack.json"] {
		return ErrRecordedArchive
	}
	seen := map[string]bool{}
	duration := int64(0)
	for i, ch := range m.Audiobook.Chapters {
		if ch.Number != i+1 || !validText(ch.Title) || ch.DurationMS <= 0 || ch.DurationMS > 86400000 || len(ch.Segments) == 0 {
			return ErrRecordedArchive
		}
		chapterMS := int64(0)
		for _, seg := range ch.Segments {
			if !segmentName.MatchString(seg.File) || seen[seg.File] || files[seg.File] == nil || absMS(seg.StartMS-chapterMS) > 1 || seg.DurationMS <= 0 || seg.DurationMS > 45000 {
				return ErrRecordedArchive
			}
			seen[seg.File] = true
			chapterMS = seg.StartMS + seg.DurationMS
		}
		if absMS(chapterMS-ch.DurationMS) > 1 {
			return ErrRecordedArchive
		}
		duration += ch.DurationMS
	}
	if duration != m.Audiobook.Recording.DurationMS || len(seen) != len(files)-2 {
		return ErrRecordedArchive
	}
	if err = os.MkdirAll(root, 0755); err != nil {
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
	book := Book{ID: id, Title: m.Name, Language: m.Audiobook.Work.Language, Origin: origin, Revision: hash}
	stage, err := os.MkdirTemp(root, ".recorded-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for i, ch := range m.Audiobook.Chapters {
		chapter := Chapter{Title: ch.Title, Audio: chapterName(i) + ".ffconcat", Seconds: 0}
		for _, seg := range ch.Segments {
			if err = ctx.Err(); err != nil {
				return err
			}
			dest := filepath.Join(stage, seg.File)
			if err = os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
				return err
			}
			input, e := files[seg.File].Open()
			if e != nil {
				return ErrRecordedArchive
			}
			output, e := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if e != nil {
				input.Close()
				return e
			}
			digest := sha256.New()
			n, e := io.Copy(io.MultiWriter(output, digest), io.LimitReader(input, (2<<20)+1))
			input.Close()
			syncErr := output.Sync()
			closeErr := output.Close()
			if e != nil || n <= 0 || n > 2<<20 || hex.EncodeToString(digest.Sum(nil)) != sums[seg.File] {
				return ErrRecordedArchive
			}
			if syncErr != nil {
				return syncErr
			}
			if closeErr != nil {
				return closeErr
			}
			job, cancel := context.WithTimeout(ctx, 30*time.Second)
			cmd := exec.CommandContext(job, "ffmpeg", "-nostdin", "-v", "error", "-protocol_whitelist", "file,pipe", "-f", "ogg", "-i", dest, "-map", "0:a:0", "-vn", "-t", "46", "-ar", "16000", "-ac", "1", "-f", "s16le", "pipe:1")
			decoded := &previewPCM{dst: io.Discard, limit: 46 * sampleRate * 2}
			cmd.Stdout = decoded
			e = cmd.Run()
			cancel()
			ms := decoded.n * 1000 / (sampleRate * 2)
			if e != nil || decoded.n%2 != 0 || ms < seg.DurationMS-120 || ms > seg.DurationMS+120 {
				return ErrRecordedArchive
			}
			chapter.Seconds += float64(decoded.n) / (sampleRate * 2)
			chapter.Segments = append(chapter.Segments, Segment{Audio: seg.File, Bytes: n, SHA256: sums[seg.File], Seconds: float64(decoded.n) / (sampleRate * 2)})
		}
		if err = os.WriteFile(filepath.Join(stage, chapter.Audio), []byte(concatChapter(chapter)), 0644); err != nil {
			return err
		}
		if err = speak(ctx, filepath.Join(stage, chapterName(i)+"-title.sln16"), ch.Title); err != nil {
			return err
		}
		book.Chapters = append(book.Chapters, chapter)
	}
	if err = speak(ctx, filepath.Join(stage, "title.sln16"), book.Title); err != nil {
		return err
	}
	if err = os.Chmod(stage, 0755); err != nil {
		return err
	}
	return publishImported(ctx, root, stage, book, catalog, menuSource)
}

// The pack builder rounds starts and lengths independently to milliseconds.
// Adjacent boundaries may differ by exactly one millisecond, never more.
func absMS(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
