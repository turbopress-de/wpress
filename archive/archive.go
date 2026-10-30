// Package archive reads .wpress files written by the All-in-One WP Migration
// WordPress plugin.
//
// A .wpress file is a flat sequence of entries. Each entry is a fixed-size
// header followed by the raw, uncompressed file content:
//
//	name   255 bytes  NUL-padded file name
//	size    14 bytes  NUL-padded decimal content length
//	mtime   12 bytes  NUL-padded decimal Unix timestamp
//	prefix 4096 bytes NUL-padded directory, relative to the archive root
//
// The archive ends with an end-of-file header. Older plugin versions write
// only NUL bytes; newer ones leave the name empty, put the archive length
// (excluding the end header) into the size field and a hex CRC32 checksum
// into the last bytes of the prefix field.
package archive

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

const (
	nameLen   = 255
	sizeLen   = 14
	mtimeLen  = 12
	prefixLen = 4096

	// HeaderSize is the length of one entry header in bytes.
	HeaderSize = nameLen + sizeLen + mtimeLen + prefixLen
)

// ErrTruncated is returned when the archive ends without an end-of-file
// header, i.e. it is probably incomplete.
var ErrTruncated = errors.New("archive has no end marker, it is probably truncated")

// ErrUnsafePath is returned for entries whose path would leave the
// extraction directory (absolute paths, ".." components, NUL bytes).
var ErrUnsafePath = errors.New("unsafe path in archive")

// Header describes one archive entry.
type Header struct {
	// Path is the cleaned, slash-separated path relative to the archive
	// root. It never starts with "/" and never contains "..".
	Path  string
	Size  int64
	MTime int64
	// Offset is the position of the entry header in the archive.
	Offset int64
}

// Reader iterates over the entries of a .wpress archive.
type Reader struct {
	r      io.Reader
	seeker io.Seeker // non-nil if r can seek; used to skip content quickly
	size   int64     // total length if seekable, to detect seeks past the end
	buf    [HeaderSize]byte

	base      int64 // position of the archive start in r
	offset    int64 // position of the next unread byte
	remaining int64 // unread content bytes of the current entry
	done      bool

	// MissingEOF is set when the archive ended at an entry boundary
	// without an end-of-file header.
	MissingEOF bool
	// Checksum is the CRC32 (hex) from the end-of-file header, if present.
	// It is not verified.
	Checksum string
}

// NewReader returns a Reader for r. If r implements io.Seeker, skipped
// content is seeked over instead of read.
func NewReader(r io.Reader) *Reader {
	rd := &Reader{r: r}
	if s, ok := r.(io.Seeker); ok {
		// Probe: pipes and terminals implement Seek but fail.
		if pos, err := s.Seek(0, io.SeekCurrent); err == nil {
			if end, err := s.Seek(0, io.SeekEnd); err == nil {
				if _, err := s.Seek(pos, io.SeekStart); err == nil {
					rd.seeker = s
					rd.base = pos
					rd.offset = pos
					rd.size = end
				}
			}
		}
	}
	return rd
}

// Next advances to the next entry, skipping any unread content of the
// current one. It returns io.EOF at the end of the archive.
func (rd *Reader) Next() (*Header, error) {
	if rd.done {
		return nil, io.EOF
	}
	if err := rd.skip(rd.remaining); err != nil {
		return nil, fmt.Errorf("entry content ending at offset %d: %w", rd.offset+rd.remaining, err)
	}
	rd.remaining = 0

	start := rd.offset
	n, err := io.ReadFull(rd.r, rd.buf[:])
	rd.offset += int64(n)
	switch {
	case err == io.EOF:
		// Clean end at an entry boundary, but no EOF marker.
		rd.done = true
		rd.MissingEOF = true
		return nil, io.EOF
	case err == io.ErrUnexpectedEOF:
		return nil, fmt.Errorf("truncated header at offset %d", start)
	case err != nil:
		return nil, err
	}

	if allZero(rd.buf[:]) {
		rd.done = true
		return nil, rd.checkTrailing()
	}
	if field(rd.buf[:nameLen]) == "" {
		// End-of-file header of newer plugin versions.
		rd.done = true
		if s := field(rd.buf[nameLen : nameLen+sizeLen]); s != "" {
			length, err := parseDecimal(s)
			if err != nil {
				return nil, fmt.Errorf("end header at offset %d: invalid length %q", start, s)
			}
			if length != start-rd.base {
				return nil, fmt.Errorf("end header at offset %d declares archive length %d: archive is damaged", start, length)
			}
		}
		rd.Checksum = strings.TrimSpace(field(bytes.TrimLeft(rd.buf[nameLen+sizeLen+mtimeLen:], "\x00")))
		return nil, rd.checkTrailing()
	}

	h, err := parseHeader(rd.buf[:])
	if err != nil {
		return nil, fmt.Errorf("entry at offset %d: %w", start, err)
	}
	h.Offset = start
	rd.remaining = h.Size
	return h, nil
}

// checkTrailing makes sure nothing follows the end header. A zero-filled
// gap (preallocated or partially written file) would otherwise look like a
// complete archive.
func (rd *Reader) checkTrailing() error {
	if rd.seeker != nil {
		if extra := rd.size - rd.offset; extra > 0 {
			return fmt.Errorf("%d bytes of data after the end marker at offset %d: archive is damaged", extra, rd.offset-HeaderSize)
		}
		return io.EOF
	}
	var b [1]byte
	if n, _ := io.ReadFull(rd.r, b[:]); n > 0 {
		return fmt.Errorf("data after the end marker at offset %d: archive is damaged", rd.offset-HeaderSize)
	}
	return io.EOF
}

// Read reads content of the current entry.
func (rd *Reader) Read(p []byte) (int, error) {
	if rd.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > rd.remaining {
		p = p[:rd.remaining]
	}
	n, err := rd.r.Read(p)
	rd.offset += int64(n)
	rd.remaining -= int64(n)
	if err == io.EOF && rd.remaining > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

// Offset returns the current position in the archive.
func (rd *Reader) Offset() int64 { return rd.offset }

func (rd *Reader) skip(n int64) error {
	if n <= 0 {
		return nil
	}
	if rd.seeker != nil {
		if rd.offset+n > rd.size {
			return io.ErrUnexpectedEOF
		}
		pos, err := rd.seeker.Seek(n, io.SeekCurrent)
		if err != nil {
			return err
		}
		rd.offset = pos
		return nil
	}
	m, err := io.CopyN(io.Discard, rd.r, n)
	rd.offset += m
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

func parseHeader(b []byte) (*Header, error) {
	name := field(b[0:nameLen])
	sizeStr := field(b[nameLen : nameLen+sizeLen])
	mtimeStr := field(b[nameLen+sizeLen : nameLen+sizeLen+mtimeLen])
	prefix := field(b[nameLen+sizeLen+mtimeLen:])

	size, err := parseDecimal(sizeStr)
	if err != nil {
		return nil, fmt.Errorf("invalid size %q", sizeStr)
	}
	// mtime is informational; tolerate garbage.
	mtime, _ := parseDecimal(mtimeStr)

	p, err := SafePath(prefix, name)
	if err != nil {
		return nil, err
	}
	return &Header{Path: p, Size: size, MTime: mtime}, nil
}

// SafePath joins prefix and name into a cleaned relative path and rejects
// anything that could escape the extraction root. Backslashes are treated
// as separators because archives created on Windows hosts may contain them.
func SafePath(prefix, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty file name", ErrUnsafePath)
	}
	prefix = strings.ReplaceAll(prefix, `\`, "/")
	name = strings.ReplaceAll(name, `\`, "/")
	joined := name
	if prefix != "" {
		joined = prefix + "/" + name
	}
	for _, c := range []byte(joined) {
		if c < 0x20 || c == 0x7f {
			return "", fmt.Errorf("%w: control character in %q", ErrUnsafePath, joined)
		}
	}
	if strings.HasPrefix(prefix, "/") || strings.HasPrefix(name, "/") ||
		hasDriveLetter(prefix) || hasDriveLetter(name) {
		return "", fmt.Errorf("%w: absolute path %q", ErrUnsafePath, joined)
	}
	for _, seg := range strings.Split(joined, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: %q", ErrUnsafePath, joined)
		}
	}
	p := path.Clean(joined)
	if p == "." || p == "/" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, joined)
	}
	return p, nil
}

func hasDriveLetter(s string) bool {
	return len(s) >= 2 && s[1] == ':' &&
		((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z'))
}

func field(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func parseDecimal(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a decimal number")
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
