# send2kindle

A tiny daemon that watches a folder for ebooks and emails each one to a
Kindle's `@kindle.com` address, then deletes it.

No Calibre, no format conversion, no subprocess calls — Amazon's
Send-to-Kindle email service accepts `epub`, `pdf`, `doc`, `docx`, `txt`,
`rtf`, `htm`/`html`, and common image formats directly and converts
server-side, so there's nothing to convert locally. Anything else dropped in
the watched folder is discarded, not sent.

Built to sit downstream of [Bindery](https://github.com/vavallee/bindery)'s
"Calibre-Web-Automated ingest folder" hand-off: Bindery copies each
successfully imported ebook into a shared folder for a CWA-style consumer to
pick up. send2kindle plays that role, but doesn't run CWA or keep a second
permanent library — it just forwards the file to the Kindle and removes it.

## How it works

1. Watches a directory (`fsnotify`) for new files.
2. On startup, also sweeps the directory for anything already there — the
   watcher only sees future events, so files present before the container
   started would otherwise be silently ignored.
3. Debounces each path for 5 seconds after the last write, so a large file
   still being copied in isn't read mid-write.
4. Filters by extension. Non-ebook or non-Kindle-accepted files are deleted
   without being sent.
5. Sends the file as an email attachment over SMTP (native Go `net/smtp` +
   `mime/multipart`, no external mail client).
6. Deletes the source file only after a confirmed successful send. A failed
   send leaves the file in place for the next debounce cycle or manual retry.

## Configuration

All via environment variables — none are optional:

| Variable | Example | Description |
|---|---|---|
| `SMTP_HOST` | `smtp.gmail.com` | SMTP server hostname |
| `SMTP_PORT` | `587` | `587` uses STARTTLS; `465` uses implicit TLS |
| `SMTP_USER` | `you@gmail.com` | SMTP auth username |
| `SMTP_PASS` | *(app password)* | SMTP auth password — for Gmail, a generated App Password, not your account password |
| `FROM_EMAIL` | `you@gmail.com` | Sender address. Must be whitelisted in Amazon's Personal Document Settings, or delivery is silently rejected |
| `KINDLE_EMAIL` | `you_a1b2c3@kindle.com` | Destination Kindle address (Amazon → Content & Devices → Devices) |

The directory to watch is fixed at `/watch` inside the container — mount your
real folder there rather than configuring the path via an env var, so the
mount and the setting can't drift out of sync.

## Running

```yaml
services:
  send2kindle:
    image: ghcr.io/miista/send2kindle:latest
    restart: unless-stopped
    user: "1000:1000"
    environment:
      SMTP_HOST: smtp.gmail.com
      SMTP_PORT: "587"
      SMTP_USER: you@gmail.com
      SMTP_PASS: ${SMTP_PASS}
      FROM_EMAIL: you@gmail.com
      KINDLE_EMAIL: you_a1b2c3@kindle.com
    volumes:
      - ./ingest:/watch
```

## Building

```sh
docker build -t send2kindle:latest .
```

The image is built `FROM scratch` — just the static binary and a CA
certificate bundle for verifying the SMTP server's TLS certificate. No shell,
no package manager: ~2 MB compressed, ~7 MB unpacked.

## Modes

`WATCH_MODE` selects how the watched directory is treated. The two are not
variations on a setting -- they are different contracts about who owns the
files.

| | `drop` (default) | `library` |
| --- | --- | --- |
| The directory is | a queue | a shelf |
| After a successful send | the file is removed | the file stays |
| Repeats prevented by | the file being gone | `/state/sent.json` |
| Unsendable formats | discarded | left alone |
| Mount it | read-write | read-only |

Drop mode is what send2kindle was built for: a frontend that could not
hardlink made a copy specifically to be consumed, so consuming it was correct.

Library mode watches a directory the tool does not own -- one filled by
Shelfarr, bindery, Readarr or anything else -- and records what it has sent
instead of deleting it. Outcomes are keyed by inode and size rather than path,
so a book renamed by a metadata refresh is still the same book, and two
hardlinks to one inode are one book.

Failures are recorded too. An oversized file warns once rather than on every
restart, which is how a 115MB epub logged the same error for eighteen days
without anyone seeing it. A *failed send* is deliberately not recorded: the
server may have been briefly unreachable, and a duplicate on the Kindle is a
better outcome than a book that silently never arrives.

```yaml
environment:
  WATCH_MODE: library
volumes:
  - /path/to/library:/watch:ro
  - ./state:/state
```

## Notifications

`slog.Error` writes to stdout, and nobody reads stdout. That is the whole
reason this exists: the size guard was correct for weeks while a 115MB epub
failed on every restart, and eighteen days passed before anyone noticed,
because the warning had nowhere to go.

Set `NTFY_URL` and `NTFY_TOPIC` to publish; `NTFY_TOKEN` if the topic needs
one. Unset, notifications are off and nothing else changes.

What is announced, and what deliberately is not:

| | Announced | Priority |
| --- | --- | --- |
| Too large to email | yes | high |
| Discarded in drop mode | yes | high |
| Send failed | yes | default |
| Skipped in library mode | no | |
| Sent successfully | no | |

A successful send is visible on the Kindle, so announcing it would only make
the channel noise -- and noise is why the original warnings went unread.
A skipped file in library mode is still on the shelf, so there is nothing to
act on. A *discarded* file in drop mode is gone, which is the one outcome no
log can recover.

Each is announced once, not on every scan: the state file records failures as
well as successes, so an oversized book warns the first time and then stays
quiet.

```yaml
environment:
  NTFY_URL: https://ntfy.example.com
  NTFY_TOPIC: books
  SCAN_INTERVAL: 1m
```
