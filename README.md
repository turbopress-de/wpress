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
- **Safe on untrusted input**: rejects absolute paths, `..` components and NUL
  bytes, never follows symlinks in the target directory, detects truncated
  archives (including the length check in the end header of newer plugin
  versions).
- **Scriptable**: `-json` summary, `list -json` with one object per entry,
  non-zero exit code on any error.

## Install

Download a binary from the [releases](https://github.com/turbopress-de/wpress/releases)
(Linux, macOS, Windows; amd64 and arm64) or build from source:

```bash
go install github.com/turbopress-de/wpress/cmd/wpress@latest
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

`extract` refuses a non-empty output directory unless `-force` is given.

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

`missing_eof_marker: true` means the archive ended without an end header and
may be incomplete. `crc32` is the checksum stored by the plugin; it is
reported, not verified.

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

Encrypted archives (a feature of a paid plugin extension) are not supported.

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
