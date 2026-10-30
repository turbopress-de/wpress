# wpress

Fast, safe extractor for `.wpress` backups created by the
[All-in-One WP Migration](https://wordpress.org/plugins/all-in-one-wp-migration/)
WordPress plugin. Single static binary, no PHP, Node or WordPress needed.

```
$ wpress extract -q -skip-backups -o site/ example-com-20261030.wpress
extracted 14682 files (3.9 GiB) in 10s
skipped   60 files (18.7 GiB)
  updraft                      18.7 GiB
```

Built by [turbopress](https://turbopress.de) for our WordPress migration
service, where customers upload multi-gigabyte `.wpress` files. The extractor
we used before read archives in 512-byte chunks and needed about 40 minutes
for the archive above; `wpress` copies in 4 MiB blocks and runs at disk speed.

## Features

- **Fast**: large block copies, skipped entries are seeked over instead of read.
- **Skip old backups**: `-skip-backups` leaves out the backup folders of common
  backup plugins (UpdraftPlus, Duplicator, BackWPup, WPvivid, ...). Sites often
  carry several times their own size in old backups. `-exclude` adds your own
  patterns.
- **Safe on untrusted input**: rejects absolute paths, `..` components and
  control characters; all writes go through Go's
  [`os.Root`](https://pkg.go.dev/os#Root), so neither archive paths nor
  symlinks in the target directory can reach outside of it; existing files
  are replaced, never truncated through hard links.
- **Detects damaged archives**: missing end marker, truncated entries,
  data after the end marker, and the length stored in the end header of newer
  plugin versions. Encrypted archives are refused instead of producing garbage.
- **Scriptable**: `-json` summary (also on errors, with an `error` field),
  `list -json` with one object per entry, non-zero exit code on any error.

## Install

Download a binary from the [releases](https://github.com/turbopress-de/wpress/releases)
(Linux, macOS, Windows; amd64 and arm64) or build from source:

```bash
go install github.com/turbopress-de/wpress/cmd/wpress@latest   # Go 1.25+
```

## Usage

```bash
# Extract everything
wpress extract -o out/ backup.wpress

# Extract without backup-plugin folders and without a custom folder
wpress extract -skip-backups -exclude 'uploads/big-videos' -o out/ backup.wpress

# Machine-readable summary, no progress line
wpress extract -q -json -o out/ backup.wpress

# List contents
wpress list backup.wpress
wpress list -json backup.wpress | jq -r 'select(.size > 100000000) | .path'
```

`extract` refuses a non-empty output directory unless `-force` is given; with
`-force`, files in the way are replaced. Flags may come before or after the
archive name. A progress line is shown only when stderr is a terminal.

Archives without an end marker are treated as truncated and fail with exit
code 1; `-allow-missing-eof` extracts them anyway.

Exit codes: `0` success, `1` error (damaged archive, I/O error, ...), `2`
invalid usage.

### Exclude patterns

Patterns use Go's [`path.Match`](https://pkg.go.dev/path#Match) syntax and are
matched against the entry path **and each of its parent directories**,
relative to the archive root (which is `wp-content` for regular backups, plus
`database.sql` and `package.json`). So `updraft` skips `updraft/` with
everything below it, `uploads/backwpup-*` skips `uploads/backwpup-1a2b3c/`,
and `*.log` skips log files at the top level only.

`-skip-backups` currently expands to:

```
ai1wm-backups  updraft  backups-dup-lite  backups-dup-pro  wpvividbackups
backwpup-*  uploads/backwpup-*  backup-guard  uploads/backup-guard
uploads/backupbuddy_backups  backupwordpress-*  uploads/backupwordpress-*
```

### JSON summary

```json
{"files":14682,"bytes":4189963001,"skipped_files":60,"skipped_bytes":20054850356,
 "skipped_bytes_by_pattern":{"updraft":20054850356},"crc32":"69a8404c","seconds":10.28}
```

On failure the object additionally has an `error` field and the counters
describe what was extracted before the error. `missing_eof_marker: true`
appears with `-allow-missing-eof` when the archive had no end header. `crc32`
is the checksum stored by the plugin; it is reported, not verified.

## File format

A `.wpress` file is a sequence of entries, each a 4377-byte header followed by
the raw file content (no compression):

| Field  | Bytes | Content                                   |
|--------|------:|-------------------------------------------|
| name   |   255 | file name, NUL-padded                     |
| size   |    14 | content length, decimal                   |
| mtime  |    12 | modification time, Unix seconds, decimal  |
| prefix |  4096 | directory relative to the archive root    |

The archive ends with a header that is either all NUL bytes (older plugin
versions) or has an empty name, the archive length in the size field and a
CRC32 checksum in the last bytes (newer versions).

Encrypted archives (`"Encrypted": true` in `package.json`) are not supported
and fail with an error.

Patterns and paths are case-sensitive. File names that are not valid UTF-8
are extracted byte for byte; `list -json` shows them with U+FFFD replacement
characters.

## Library

The `archive` package can be used on its own:

```go
rd := archive.NewReader(f)
for {
	h, err := rd.Next()
	if err == io.EOF {
		break
	}
	if err != nil {
		return err
	}
	fmt.Println(h.Path, h.Size)
	// io.Copy(dst, rd) reads the content of the current entry
}
```

## License

MIT. Not affiliated with ServMask or the All-in-One WP Migration plugin.
