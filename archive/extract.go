package archive

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// copyBufSize is the read/write block size for file content. Large blocks
// keep the syscall count low; 512-byte reads were the bottleneck this tool
// was written to fix.
const copyBufSize = 4 << 20

// Options control Extract.
type Options struct {
	// Exclude holds patterns in path.Match syntax. An entry is skipped if
	// the pattern matches its path or any of its parent directories, so
	// "updraft" skips everything below updraft/.
	Exclude []string
	// Progress, if set, is called after each entry with the archive offset.
	Progress func(offset int64)
}

// Stats summarises an extraction or listing.
type Stats struct {
	Files        int64            `json:"files"`
	Bytes        int64            `json:"bytes"`
	SkippedFiles int64            `json:"skipped_files"`
	SkippedBytes int64            `json:"skipped_bytes"`
	SkippedBy    map[string]int64 `json:"skipped_bytes_by_pattern,omitempty"`
	MissingEOF   bool             `json:"missing_eof_marker,omitempty"`
	Checksum     string           `json:"crc32,omitempty"`
}

// Excluded reports which pattern, if any, excludes p.
func Excluded(p string, patterns []string) (string, bool) {
	for _, pat := range patterns {
		pat = strings.Trim(pat, "/")
		if pat == "" {
			continue
		}
		for d := p; d != "." && d != "/" && d != ""; d = path.Dir(d) {
			if ok, _ := path.Match(pat, d); ok {
				return pat, true
			}
		}
	}
	return "", false
}

// ValidatePatterns checks exclude patterns for syntax errors.
func ValidatePatterns(patterns []string) error {
	for _, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("invalid exclude pattern %q: %w", p, err)
		}
	}
	return nil
}

// Extract writes all entries of rd below dir. dir is created if missing.
func Extract(rd *Reader, dir string, opt Options) (*Stats, error) {
	if err := ValidatePatterns(opt.Exclude); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	st := &Stats{SkippedBy: map[string]int64{}}
	buf := make([]byte, copyBufSize)
	for {
		h, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return st, err
		}

		if pat, ok := Excluded(h.Path, opt.Exclude); ok {
			st.SkippedFiles++
			st.SkippedBytes += h.Size
			st.SkippedBy[pat] += h.Size
			if opt.Progress != nil {
				opt.Progress(h.Offset + HeaderSize + h.Size)
			}
			continue
		}

		if err := writeEntry(rd, h, root, buf); err != nil {
			return st, err
		}
		st.Files++
		st.Bytes += h.Size
		if opt.Progress != nil {
			opt.Progress(rd.Offset())
		}
	}
	st.MissingEOF = rd.MissingEOF
	st.Checksum = rd.Checksum
	if len(st.SkippedBy) == 0 {
		st.SkippedBy = nil
	}
	return st, nil
}

func writeEntry(rd *Reader, h *Header, root string, buf []byte) error {
	target := filepath.Join(root, filepath.FromSlash(h.Path))
	// Defence in depth: SafePath already rejected "..", but verify the
	// joined result is still below root.
	if rel, err := filepath.Rel(root, target); err != nil || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q", ErrUnsafePath, h.Path)
	}
	if err := mkdirNoSymlink(root, filepath.Dir(target)); err != nil {
		return fmt.Errorf("%s: %w", h.Path, err)
	}

	// O_NOFOLLOW: never write through a symlink that might already exist
	// in the target directory.
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|oNoFollow, 0o644)
	if err != nil {
		return fmt.Errorf("%s: %w", h.Path, err)
	}
	n, err := io.CopyBuffer(f, onlyReader{rd}, buf)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != h.Size {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return fmt.Errorf("%s: %w", h.Path, err)
	}
	if h.MTime > 0 {
		t := time.Unix(h.MTime, 0)
		_ = os.Chtimes(target, t, t)
	}
	return nil
}

// mkdirNoSymlink creates dir (below root) and refuses to traverse existing
// symlinks, so a pre-existing link cannot redirect writes outside root.
func mkdirNoSymlink(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := root
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(cur, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		case err != nil:
			return err
		case fi.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%w: symlink in path %q", ErrUnsafePath, cur)
		case !fi.IsDir():
			return fmt.Errorf("%q exists and is not a directory", cur)
		}
	}
	return nil
}

// onlyReader hides Reader's other methods so io.CopyBuffer uses buf.
type onlyReader struct{ r io.Reader }

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }
