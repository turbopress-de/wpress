package archive

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	prefix, name string
	data         []byte
	mtime        int64
}

func header(prefix, name, size, mtime string) []byte {
	b := make([]byte, HeaderSize)
	copy(b[0:nameLen], name)
	copy(b[nameLen:], size)
	copy(b[nameLen+sizeLen:], mtime)
	copy(b[nameLen+sizeLen+mtimeLen:], prefix)
	return b
}

func build(entries []entry, eof bool) []byte {
	var buf bytes.Buffer
	for _, e := range entries {
		buf.Write(header(e.prefix, e.name, fmt.Sprint(len(e.data)), fmt.Sprint(e.mtime)))
		buf.Write(e.data)
	}
	if eof {
		buf.Write(make([]byte, HeaderSize))
	}
	return buf.Bytes()
}

func writeTemp(t *testing.T, b []byte) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.wpress")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

var sample = []entry{
	{".", "package.json", []byte(`{"SiteURL":"https://example.org"}`), 1700000000},
	{".", "database.sql", []byte("CREATE TABLE x;"), 1700000001},
	{"plugins/foo", "foo.php", []byte("<?php // foo"), 1700000002},
	{"updraft", "backup-db.gz", bytes.Repeat([]byte{0xAB}, 10000), 1700000003},
	{"uploads/2024/01", "big.bin", bytes.Repeat([]byte("x"), 3*copyBufSize+17), 1700000004},
	{"uploads/backwpup-abc", "old.zip", []byte("zip"), 1700000005},
	{"themes/t", "empty.css", nil, 1700000006},
}

func TestExtractRoundTrip(t *testing.T) {
	for _, seekable := range []bool{true, false} {
		t.Run(fmt.Sprintf("seekable=%v", seekable), func(t *testing.T) {
			raw := build(sample, true)
			var r io.Reader = bytes.NewReader(raw)
			if !seekable {
				r = io.MultiReader(r) // hides Seek
			}
			dir := t.TempDir()
			st, err := Extract(NewReader(r), dir, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if st.Files != int64(len(sample)) || st.SkippedFiles != 0 || st.MissingEOF {
				t.Fatalf("stats %+v", st)
			}
			for _, e := range sample {
				p := filepath.Join(dir, e.prefix, e.name)
				got, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, e.data) {
					t.Fatalf("%s: content mismatch (%d vs %d bytes)", p, len(got), len(e.data))
				}
				fi, _ := os.Stat(p)
				if fi.ModTime().Unix() != e.mtime {
					t.Fatalf("%s: mtime %d, want %d", p, fi.ModTime().Unix(), e.mtime)
				}
			}
		})
	}
}

func TestExtractExclude(t *testing.T) {
	for _, seekable := range []bool{true, false} {
		t.Run(fmt.Sprintf("seekable=%v", seekable), func(t *testing.T) {
			var r io.Reader = bytes.NewReader(build(sample, true))
			if !seekable {
				r = io.MultiReader(r)
			}
			dir := t.TempDir()
			st, err := Extract(NewReader(r), dir, Options{Exclude: []string{"updraft", "uploads/backwpup-*"}})
			if err != nil {
				t.Fatal(err)
			}
			if st.SkippedFiles != 2 || st.SkippedBytes != 10003 || st.Files != 5 {
				t.Fatalf("stats %+v", st)
			}
			if st.SkippedBy["updraft"] != 10000 || st.SkippedBy["uploads/backwpup-*"] != 3 {
				t.Fatalf("skipped by %+v", st.SkippedBy)
			}
			for _, p := range []string{"updraft", "uploads/backwpup-abc"} {
				if _, err := os.Stat(filepath.Join(dir, p)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s should not exist", p)
				}
			}
			// The entry after the skipped one must be intact.
			got, _ := os.ReadFile(filepath.Join(dir, "uploads/2024/01/big.bin"))
			if len(got) != 3*copyBufSize+17 {
				t.Fatalf("big.bin: %d bytes", len(got))
			}
		})
	}
}

func TestExcludedMatchesParents(t *testing.T) {
	cases := []struct {
		path, pat string
		want      bool
	}{
		{"updraft/a.zip", "updraft", true},
		{"updraft", "updraft", true},
		{"uploads/updraft/a.zip", "updraft", false},
		{"updraftplus/x.php", "updraft", false},
		{"plugins/updraftplus/x.php", "updraft", false},
		{"uploads/backwpup-1a2b/x.zip", "uploads/backwpup-*", true},
		{"uploads/2024/backwpup-1a2b", "uploads/backwpup-*", false},
		{"ai1wm-backups/x.wpress", "/ai1wm-backups/", true},
	}
	for _, c := range cases {
		_, got := Excluded(c.path, []string{c.pat})
		if got != c.want {
			t.Errorf("Excluded(%q, %q) = %v, want %v", c.path, c.pat, got, c.want)
		}
	}
}

func TestSafePathRejects(t *testing.T) {
	bad := []struct{ prefix, name string }{
		{"..", "x"},
		{"a/../..", "x"},
		{"a/..", "x"},
		{".", "../x"},
		{"/etc", "passwd"},
		{".", "/etc/passwd"},
		{`..\..`, "x"},
		{`\windows`, "x"},
		{"C:/x", "y"},
		{".", "C:x"},
		{".", ""},
		{".", "."},
		{".", "a\x00b"},
	}
	for _, b := range bad {
		if p, err := SafePath(b.prefix, b.name); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("SafePath(%q, %q) = %q, %v; want ErrUnsafePath", b.prefix, b.name, p, err)
		}
	}
	good := map[[2]string]string{
		{".", "package.json"}:           "package.json",
		{"plugins/foo", "a.php"}:        "plugins/foo/a.php",
		{`plugins\foo`, "a.php"}:        "plugins/foo/a.php",
		{"./uploads//2024/", "img.jpg"}: "uploads/2024/img.jpg",
		{"", "x"}:                       "x",
		{"uploads", "..hidden"}:         "uploads/..hidden",
	}
	for in, want := range good {
		got, err := SafePath(in[0], in[1])
		if err != nil || got != want {
			t.Errorf("SafePath(%q, %q) = %q, %v; want %q", in[0], in[1], got, err, want)
		}
	}
}

func TestTraversalEntryAborts(t *testing.T) {
	raw := build([]entry{{"../../tmp", "evil", []byte("x"), 1}}, true)
	parent := t.TempDir()
	dir := filepath.Join(parent, "out")
	_, err := Extract(NewReader(bytes.NewReader(raw)), dir, Options{})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v, want ErrUnsafePath", err)
	}
}

func TestSymlinkInTargetNotFollowed(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "plugins")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if err := os.Symlink(filepath.Join(outside, "f"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	raw := build([]entry{{"plugins", "x.php", []byte("x"), 1}}, true)
	if _, err := Extract(NewReader(bytes.NewReader(raw)), dir, Options{}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("dir symlink: err = %v, want ErrUnsafePath", err)
	}
	raw = build([]entry{{".", "link.txt", []byte("x"), 1}}, true)
	if _, err := Extract(NewReader(bytes.NewReader(raw)), dir, Options{}); err == nil {
		t.Fatal("file symlink: expected error")
	}
	if _, err := os.Stat(filepath.Join(outside, "f")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote through symlink")
	}
}

func TestTruncated(t *testing.T) {
	raw := build(sample[:3], true)
	cases := map[string][]byte{
		"mid-content": raw[:HeaderSize+10],
		"mid-header":  raw[:HeaderSize/2],
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			for _, seekable := range []bool{true, false} {
				var r io.Reader = bytes.NewReader(b)
				if !seekable {
					r = io.MultiReader(r)
				}
				_, err := Extract(NewReader(r), t.TempDir(), Options{})
				if err == nil {
					t.Fatalf("seekable=%v: expected error", seekable)
				}
			}
		})
	}
}

func TestTruncatedInsideSkippedEntry(t *testing.T) {
	raw := build(sample, true)
	// Cut inside the updraft entry, which is excluded.
	cut := bytes.Index(raw, bytes.Repeat([]byte{0xAB}, 100)) + 50
	f := writeTemp(t, raw[:cut])
	_, err := Extract(NewReader(f), t.TempDir(), Options{Exclude: []string{"updraft"}})
	if err == nil {
		t.Fatal("expected error for archive truncated inside a skipped entry")
	}
}

func TestMissingEOFMarker(t *testing.T) {
	st, err := Extract(NewReader(bytes.NewReader(build(sample[:2], false))), t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.MissingEOF || st.Files != 2 {
		t.Fatalf("stats %+v", st)
	}
}

func TestInvalidSize(t *testing.T) {
	for _, size := range []string{"", "-1", "12a", "0x10"} {
		raw := append(header(".", "a", size, "1"), make([]byte, HeaderSize)...)
		_, err := Extract(NewReader(bytes.NewReader(raw)), t.TempDir(), Options{})
		if err == nil || !strings.Contains(err.Error(), "invalid size") {
			t.Errorf("size %q: err = %v", size, err)
		}
	}
}

func TestInvalidPattern(t *testing.T) {
	if _, err := Extract(NewReader(bytes.NewReader(nil)), t.TempDir(), Options{Exclude: []string{"["}}); err == nil {
		t.Fatal("expected pattern error")
	}
}

// newEOF builds the end header written by newer plugin versions.
func newEOF(length int, crc string) []byte {
	b := header("", "", fmt.Sprint(length), "")
	copy(b[HeaderSize-len(crc):], crc)
	return b
}

func TestNewStyleEOFHeader(t *testing.T) {
	body := build(sample[:3], false)
	raw := append(append([]byte{}, body...), newEOF(len(body), "69a8404c")...)
	for _, seekable := range []bool{true, false} {
		var r io.Reader = bytes.NewReader(raw)
		if !seekable {
			r = io.MultiReader(r)
		}
		st, err := Extract(NewReader(r), t.TempDir(), Options{})
		if err != nil {
			t.Fatalf("seekable=%v: %v", seekable, err)
		}
		if st.Files != 3 || st.MissingEOF || st.Checksum != "69a8404c" {
			t.Fatalf("seekable=%v: stats %+v", seekable, st)
		}
	}
}

func TestNewStyleEOFHeaderLengthMismatch(t *testing.T) {
	body := build(sample[:3], false)
	raw := append(append([]byte{}, body...), newEOF(len(body)+4096, "69a8404c")...)
	_, err := Extract(NewReader(bytes.NewReader(raw)), t.TempDir(), Options{})
	if err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("err = %v", err)
	}
}
