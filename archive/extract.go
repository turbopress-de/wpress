package archive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
)

// copyBufSize is the read/write block size for file content. Large blocks
// keep the syscall count low; 512-byte reads were the bottleneck this tool
// was written to fix.
const copyBufSize = 4 << 20

// maxPackageJSON caps how much of package.json is buffered for inspection.
const maxPackageJSON = 16 << 20

// ErrEncrypted is returned for archives created with the encryption option
// of the plugin. Their content cannot be extracted meaningfully.
var ErrEncrypted = errors.New("archive is encrypted (package.json has \"Encrypted\": true), not supported")

// Options control Extract.
type Options struct {
	// Exclude holds patterns in path.Match syntax. An entry is skipped if
	// the pattern matches its path or any of its parent directories, so
	// "updraft" skips everything below updraft/.
	Exclude []string
	// AllowMissingEOF accepts archives without end-of-file header instead
	// of returning ErrTruncated.
	AllowMissingEOF bool
	// Progress, if set, is called after each entry with the archive offset.
	Progress func(offset int64)
}

// Stats summarises an extraction.
type Stats struct {
	Files        int64            `json:"files"`
	Bytes        int64            `json:"bytes"`
	SkippedFiles int64            `json:"skipped_files"`
	SkippedBytes int64            `json:"skipped_bytes"`
	SkippedBy    map[string]int64 `json:"skipped_bytes_by_pattern,omitempty"`
	MissingEOF   bool             `json:"missing_eof_marker,omitempty"`
	Checksum     string           `json:"crc32,omitempty"`
}

func normPattern(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return path.Clean(p)
}

// Excluded reports which pattern, if any, excludes p.
func Excluded(p string, patterns []string) (string, bool) {
	for _, raw := range patterns {
		pat := normPattern(raw)
		if pat == "" || pat == "." {
			continue
		}
		for d := p; d != "." && d != "/" && d != ""; d = path.Dir(d) {
			if ok, _ := path.Match(pat, d); ok {
				return raw, true
			}
		}
	}
	return "", false
}

// ValidatePatterns checks exclude patterns for syntax errors.
func ValidatePatterns(patterns []string) error {
	for _, p := range patterns {
		if _, err := path.Match(normPattern(p), ""); err != nil {
			return fmt.Errorf("invalid exclude pattern %q: %w", p, err)
		}
	}
	return nil
}

// Extract writes all entries of rd below dir. dir is created if missing.
// All file system access goes through an os.Root, so neither archive paths
// nor symlinks already present in dir can reach outside of it.
// On error the returned Stats describe what was extracted so far.
func Extract(rd *Reader, dir string, opt Options) (*Stats, error) {
	st := &Stats{SkippedBy: map[string]int64{}}
	defer func() {
		if len(st.SkippedBy) == 0 {
			st.SkippedBy = nil
		}
	}()
	if err := ValidatePatterns(opt.Exclude); err != nil {
		return st, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return st, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return st, err
	}
	defer root.Close()

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

		var src io.Reader = rd
		if h.Path == "package.json" && h.Size <= maxPackageJSON {
			b, err := io.ReadAll(rd)
			if err != nil {
				return st, fmt.Errorf("%s: %w", h.Path, err)
			}
			if encrypted(b) {
				return st, ErrEncrypted
			}
			src = bytes.NewReader(b)
		}

		if err := writeEntry(root, h, src, buf); err != nil {
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
	if st.MissingEOF && !opt.AllowMissingEOF {
		return st, ErrTruncated
	}
	return st, nil
}

func encrypted(packageJSON []byte) bool {
	var cfg struct {
		Encrypted bool `json:"Encrypted"`
	}
	return json.Unmarshal(packageJSON, &cfg) == nil && cfg.Encrypted
}

func writeEntry(root *os.Root, h *Header, src io.Reader, buf []byte) error {
	if dir := path.Dir(h.Path); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("%s: %w", h.Path, err)
		}
	}
	// Remove whatever exists first and create exclusively: never truncate
	// through an existing hard link, never block on a FIFO.
	if err := root.Remove(h.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", h.Path, err)
	}
	f, err := root.OpenFile(h.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("%s: %w", h.Path, err)
	}
	n, err := io.CopyBuffer(f, onlyReader{src}, buf)
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
		_ = root.Chtimes(h.Path, t, t)
	}
	return nil
}

// onlyReader hides other methods (WriterTo) so io.CopyBuffer uses buf.
type onlyReader struct{ r io.Reader }

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }
