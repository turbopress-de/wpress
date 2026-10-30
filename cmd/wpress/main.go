// Command wpress lists and extracts .wpress archives created by the
// All-in-One WP Migration WordPress plugin.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/turbopress-de/wpress/archive"
)

var version = "dev"

// BackupDirs are folders where common WordPress backup plugins keep their
// own archives. Migrating them rarely makes sense and they often dwarf the
// actual site.
var BackupDirs = []string{
	"ai1wm-backups",
	"updraft",
	"backups-dup-lite",
	"backups-dup-pro",
	"wpvividbackups",
	"backwpup-*",
	"uploads/backwpup-*",
	"backup-guard",
	"uploads/backup-guard",
	"uploads/backupbuddy_backups",
	"backupwordpress-*",
	"uploads/backupwordpress-*",
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `wpress %s - extract All-in-One WP Migration (.wpress) archives

Usage:
  wpress extract [flags] <archive.wpress>
  wpress list    [flags] <archive.wpress>
  wpress version

Run "wpress <command> -h" for the flags of a command.
`, version)
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "extract":
		return cmdExtract(args[1:], stdout, stderr)
	case "list", "ls":
		return cmdList(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func cmdExtract(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "output directory (required)")
	force := fs.Bool("force", false, "allow a non-empty output directory")
	skipBackups := fs.Bool("skip-backups", false,
		"skip backup folders of common backup plugins:\n"+strings.Join(BackupDirs, ", "))
	asJSON := fs.Bool("json", false, "print the summary as JSON")
	quiet := fs.Bool("q", false, "no progress output")
	var excludes multiFlag
	fs.Var(&excludes, "exclude", "skip entries whose path or parent directory matches `pattern`\n(path.Match syntax, repeatable), e.g. -exclude updraft")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *out == "" {
		fmt.Fprintln(stderr, "usage: wpress extract -o <dir> [flags] <archive.wpress>")
		fs.PrintDefaults()
		return 2
	}
	if *skipBackups {
		excludes = append(excludes, BackupDirs...)
	}
	if err := archive.ValidatePatterns(excludes); err != nil {
		fmt.Fprintln(stderr, "wpress:", err)
		return 2
	}

	if !*force {
		if empty, err := dirEmptyOrMissing(*out); err != nil {
			fmt.Fprintln(stderr, "wpress:", err)
			return 1
		} else if !empty {
			fmt.Fprintf(stderr, "wpress: output directory %q is not empty (use -force)\n", *out)
			return 1
		}
	}

	f, err := os.Open(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "wpress:", err)
		return 1
	}
	defer f.Close()

	opt := archive.Options{Exclude: excludes}
	var p *progress
	if !*quiet {
		if fi, err := f.Stat(); err == nil {
			p = newProgress(stderr, fi.Size())
			opt.Progress = p.update
		}
	}

	start := time.Now()
	st, err := archive.Extract(archive.NewReader(f), *out, opt)
	if p != nil {
		p.finish()
	}
	if err != nil {
		fmt.Fprintln(stderr, "wpress:", err)
		return 1
	}
	printStats(stdout, st, time.Since(start), *asJSON)
	return 0
}

func cmdList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "one JSON object per entry")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: wpress list [-json] <archive.wpress>")
		return 2
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "wpress:", err)
		return 1
	}
	defer f.Close()

	rd := archive.NewReader(f)
	enc := json.NewEncoder(stdout)
	for {
		h, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fmt.Fprintln(stderr, "wpress:", err)
			return 1
		}
		if *asJSON {
			_ = enc.Encode(map[string]any{"path": h.Path, "size": h.Size, "mtime": h.MTime})
		} else {
			fmt.Fprintf(stdout, "%14d  %s  %s\n", h.Size,
				time.Unix(h.MTime, 0).UTC().Format("2006-01-02 15:04"), h.Path)
		}
	}
	if rd.MissingEOF {
		fmt.Fprintln(stderr, "wpress: warning: archive has no end marker, it may be truncated")
	}
	return 0
}

func printStats(w io.Writer, st *archive.Stats, d time.Duration, asJSON bool) {
	if asJSON {
		out := struct {
			*archive.Stats
			Seconds float64 `json:"seconds"`
		}{st, d.Seconds()}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	fmt.Fprintf(w, "extracted %d files (%s) in %s\n", st.Files, human(st.Bytes), d.Round(time.Second))
	if st.SkippedFiles > 0 {
		fmt.Fprintf(w, "skipped   %d files (%s)\n", st.SkippedFiles, human(st.SkippedBytes))
		for pat, b := range st.SkippedBy {
			fmt.Fprintf(w, "  %-28s %s\n", pat, human(b))
		}
	}
	if st.MissingEOF {
		fmt.Fprintln(w, "warning: archive has no end marker, it may be truncated")
	}
}

func dirEmptyOrMissing(dir string) (bool, error) {
	f, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return false, err
}

func human(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// progress prints a single updating line to a terminal-ish writer, at most
// a few times per second.
type progress struct {
	w     io.Writer
	total int64
	start time.Time
	last  time.Time
}

func newProgress(w io.Writer, total int64) *progress {
	return &progress{w: w, total: total, start: time.Now()}
}

func (p *progress) update(off int64) {
	now := time.Now()
	if now.Sub(p.last) < 500*time.Millisecond {
		return
	}
	p.last = now
	pct := 0.0
	if p.total > 0 {
		pct = float64(off) * 100 / float64(p.total)
	}
	rate := float64(off) / now.Sub(p.start).Seconds()
	fmt.Fprintf(p.w, "\r%5.1f%%  %s / %s  %s/s   ", pct, human(off), human(p.total), human(int64(rate)))
}

func (p *progress) finish() {
	if !p.last.IsZero() {
		fmt.Fprintln(p.w)
	}
}
