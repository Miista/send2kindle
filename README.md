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
    image: registry.guldmund.dk/send2kindle:latest
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
