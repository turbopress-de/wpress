package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turbopress-de/wpress/archive"
)

func entry(prefix, name string, data []byte) []byte {
	h := make([]byte, archive.HeaderSize)
	copy(h[0:], name)
	copy(h[255:], fmt.Sprint(len(data)))
	copy(h[269:], "1700000000")
	copy(h[281:], prefix)
	return append(h, data...)
}

func writeArchive(t *testing.T, eof bool, parts ...[]byte) string {
	t.Helper()
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	if eof {
		b = append(b, make([]byte, archive.HeaderSize)...)
	}
	p := filepath.Join(t.TempDir(), "a.wpress")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractJSONAndFlagsAfterArchive(t *testing.T) {
	a := writeArchive(t, true,
		entry(".", "database.sql", []byte("SQL")),
		entry("updraft", "old.zip", []byte("zipzip")),
	)
	out := filepath.Join(t.TempDir(), "out")
	var stdout, stderr bytes.Buffer
	code := run([]string{"extract", a, "-o", out, "-json", "-skip-backups"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	var res map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("stdout not JSON: %q", stdout.String())
	}
	if res["files"] != 1.0 || res["skipped_bytes"] != 6.0 {
		t.Fatalf("result %v", res)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr (progress on non-terminal?): %q", stderr.String())
	}
}

func TestExtractJSONOnError(t *testing.T) {
	a := writeArchive(t, false, entry(".", "database.sql", []byte("SQL")))
	var stdout, stderr bytes.Buffer
	code := run([]string{"extract", "-json", "-o", filepath.Join(t.TempDir(), "o"), a}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	var res map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("stdout not JSON: %q", stdout.String())
	}
	if !strings.Contains(fmt.Sprint(res["error"]), "end marker") || res["files"] != 1.0 {
		t.Fatalf("result %v", res)
	}
}

func TestExtractRefusesNonEmptyDir(t *testing.T) {
	a := writeArchive(t, true, entry(".", "x", []byte("x")))
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "existing"), nil, 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"extract", "-o", out, a}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if code := run([]string{"extract", "-force", "-o", out, a}, &stdout, &stderr); code != 0 {
		t.Fatalf("with -force: exit %d, %s", code, stderr.String())
	}
}

func TestListQuotesUnprintable(t *testing.T) {
	a := writeArchive(t, true, entry(".", "a bä.txt", nil), entry(".", "tab\tname", nil))
	var stdout, stderr bytes.Buffer
	if code := run([]string{"list", a}, &stdout, &stderr); code == 0 {
		t.Fatal("tab in name must be rejected")
	}
	a = writeArchive(t, true, entry(".", "a bä.txt", nil), entry(".", "back`tick", nil))
	stdout.Reset()
	if code := run([]string{"list", a}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "  a bä.txt\n") || !strings.Contains(stdout.String(), `"back`+"`"+`tick"`) {
		t.Fatalf("output %q", stdout.String())
	}
}

func TestUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{{}, {"bogus"}, {"extract"}, {"extract", "-exclude", "[", "-o", "x", "a"}} {
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}
