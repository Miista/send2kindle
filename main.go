package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

const watchDir = "/watch"

// Mode is how the watched directory is treated. The two are not variations on
// a setting -- they are different contracts about who owns the files.
type Mode string

const (
	// ModeDrop: the directory is a queue. A file in it is work to be done,
	// and doing it means the file is gone. This is what send2kindle was
	// built for, when the frontend could not hardlink and a copy was made
	// specifically to be consumed.
	ModeDrop Mode = "drop"

	// ModeLibrary: the directory is a shelf. A file in it is content that
	// stays, so the record of what has been sent has to live somewhere other
	// than the filesystem -- see state.go.
	ModeLibrary Mode = "library"
)

// statePath is where library mode records what it has decided. Under /state
// rather than in the library, because the library is mounted read-only: the
// tool is a reader of the shelf, not its owner.
const statePath = "/state/sent.json"

// Gmail's outbound SMTP limit is 25MB, and base64 encoding inflates the
// attachment by ~37% — so the real ceiling on the source file is lower than
// 25MB. Without this check, an oversized file fails send() the same way every
// retry forever (SMTP server aborts mid-transfer), silently piling up. Leave
// the file in place — it needs a human decision (different delivery method,
// or it doesn't belong in this pipeline at all), not an automatic delete.
const maxSourceBytes = 18 * 1024 * 1024

// mode and state are what handleFile runs against. Package-level because
// fsnotify's callback has no room to carry them, and there is exactly one of
// each per process.
var (
	mode  = ModeDrop
	state = &State{Entries: map[string]Entry{}}
)

// parseMode reads WATCH_MODE. An unset value is drop, which is what every
// existing deployment is doing -- a new setting should not change what a
// running container does.
func parseMode(raw string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ModeDrop):
		return ModeDrop, nil
	case string(ModeLibrary):
		return ModeLibrary, nil
	default:
		return "", fmt.Errorf("WATCH_MODE must be %q or %q, got %q", ModeDrop, ModeLibrary, raw)
	}
}

// sendFile is a variable so tests can substitute a fake: the decision of
// WHICH files to send is the interesting logic, and pinning it down should
// not require an SMTP server.
var sendFile = send

// Formats Amazon's Send-to-Kindle email service accepts directly, converting
// server-side. Amazon dropped native mobi/azw3 support for email delivery
// around 2022 — anything else lands here unconvertible and gets discarded.
var kindleAcceptedExt = map[string]bool{
	".epub": true,
	".pdf":  true,
	".doc":  true,
	".docx": true,
	".txt":  true,
	".rtf":  true,
	".htm":  true,
	".html": true,
	".png":  true,
	".gif":  true,
	".jpg":  true,
	".jpeg": true,
	".bmp":  true,
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	required := []string{"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASS", "KINDLE_EMAIL", "FROM_EMAIL"}
	for _, k := range required {
		if os.Getenv(k) == "" {
			slog.Error("missing required env var", "var", k)
			os.Exit(1)
		}
	}

	m, err := parseMode(os.Getenv("WATCH_MODE"))
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	mode = m
	if mode == ModeLibrary {
		state = LoadState(statePath)
		slog.Info("library mode: files are left in place, outcomes recorded",
			"state", statePath, "known", len(state.Entries))
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("create watcher", "error", err)
		os.Exit(1)
	}
	defer watcher.Close()

	if err := watcher.Add(watchDir); err != nil {
		slog.Error("watch directory", "dir", watchDir, "error", err)
		os.Exit(1)
	}

	slog.Info("watching for new ebooks", "dir", watchDir, "mode", string(mode))

	// fsnotify only reports events after the watch starts — sweep for files
	// already sitting in the folder (e.g. from before this container existed).
	entries, err := os.ReadDir(watchDir)
	if err != nil {
		slog.Error("read watch directory", "dir", watchDir, "error", err)
		os.Exit(1)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		slog.Info("found existing file on startup", "path", entry.Name())
		handleFile(filepath.Join(watchDir, entry.Name()))
	}

	// Debounce: a file write emits multiple fsnotify events as data lands.
	// Wait for the path to go quiet before touching it.
	pending := map[string]*time.Timer{}
	const quietPeriod = 5 * time.Second

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			path := event.Name
			if t, exists := pending[path]; exists {
				t.Stop()
			}
			pending[path] = time.AfterFunc(quietPeriod, func() {
				delete(pending, path)
				handleFile(path)
			})
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Error("watcher error", "error", err)
		}
	}
}

func handleFile(path string) {
	handle(path, mode, state)
}

// handle decides what to do with one file.
//
// The decisions are the same in both modes -- can Amazon take this format, is
// it small enough, did it send -- and only what follows differs: drop mode
// removes the file, library mode records the outcome. Splitting on mode any
// earlier would be two copies of the same three checks.
func handle(path string, m Mode, st *State) {
	info, err := os.Stat(path)
	if err != nil {
		// File already gone (moved/deleted before the quiet period elapsed).
		return
	}
	if info.IsDir() {
		return
	}

	// In library mode the file stays, so "have I already dealt with this?" is
	// a question only the state file can answer. Asked before any of the
	// checks below, so a decision already made is not made again -- including
	// the failures: an oversized book warns once, not on every restart.
	var key string
	if m == ModeLibrary {
		if key, err = fileKey(info); err != nil {
			slog.Error("cannot identify file, skipping", "path", path, "error", err)
			return
		}
		if st.Done(key) {
			return
		}
	}

	name := filepath.Base(path)
	finish := func(outcome string) {
		if m != ModeLibrary {
			return
		}
		if err := st.Record(key, outcome, name, info.Size()); err != nil {
			slog.Error("failed to record outcome", "path", path, "outcome", outcome, "error", err)
		}
	}

	ext := strings.ToLower(filepath.Ext(path))
	if !kindleAcceptedExt[ext] {
		// Discarded in drop mode: the file was a copy made to be consumed.
		// NEVER in library mode -- the shelf is not ours to delete from, and a
		// format Amazon will not take is still a book someone owns.
		if m == ModeDrop {
			slog.Info("discarding file Amazon's Send-to-Kindle email won't accept", "path", path, "ext", ext)
			os.Remove(path)
		} else {
			slog.Info("skipping file Amazon's Send-to-Kindle email won't accept", "path", path, "ext", ext)
		}
		finish(OutcomeUnusable)
		return
	}

	if info.Size() > maxSourceBytes {
		slog.Error("file too large for Gmail SMTP, leaving in place — needs manual handling",
			"path", path, "size_bytes", info.Size(), "limit_bytes", maxSourceBytes)
		finish(OutcomeTooLarge)
		return
	}

	if err := sendFile(path); err != nil {
		// Not recorded as terminal: a send can fail because the SMTP server
		// was briefly unreachable, and that is worth retrying. The cost of
		// being wrong is a duplicate on the Kindle; the cost of recording it
		// would be a book that never arrives and never says so.
		slog.Error("send failed, leaving file in place for manual retry", "path", path, "error", err)
		return
	}

	slog.Info("sent to kindle", "title", name)
	finish(OutcomeSent)

	// Drop mode consumes what it sends; library mode has the state file
	// instead, and removing a book from the shelf because it was emailed
	// would be destroying the thing the shelf is for.
	if m == ModeDrop {
		if err := os.Remove(path); err != nil {
			slog.Error("failed to remove source file after send", "path", path, "error", err)
		}
	}
}

func send(path string) error {
	from := os.Getenv("FROM_EMAIL")
	to := os.Getenv("KINDLE_EMAIL")

	msg, err := buildMessage(path, from, to)
	if err != nil {
		return err
	}

	return deliver(msg, from, to)
}

// buildMessage assembles the MIME message with the book attached.
func buildMessage(path, from, to string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	filename := filepath.Base(path)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", to)
	fmt.Fprintf(&buf, "Subject: %s\r\n", filename)
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", w.Boundary())

	bodyPart, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"text/plain; charset=utf-8"},
	})
	if err != nil {
		return nil, fmt.Errorf("create body part: %w", err)
	}
	bodyPart.Write([]byte("Sent by kindle-sender.\r\n"))

	attachHeader := textproto.MIMEHeader{
		"Content-Type":              {mime.TypeByExtension(filepath.Ext(path)) + "; name=\"" + filename + "\""},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {"attachment; filename=\"" + filename + "\""},
	}
	attachPart, err := w.CreatePart(attachHeader)
	if err != nil {
		return nil, fmt.Errorf("create attachment part: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		attachPart.Write([]byte(encoded[i:end] + "\r\n"))
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	return buf.Bytes(), nil
}

// deliver opens the SMTP conversation and writes the message.
func deliver(msg []byte, from, to string) error {
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	auth := smtp.PlainAuth("", os.Getenv("SMTP_USER"), os.Getenv("SMTP_PASS"), host)

	addr := host + ":" + port
	tlsConfig := &tls.Config{ServerName: host}

	var client *smtp.Client
	var err error
	if port == "465" {
		conn, dialErr := tls.Dial("tcp", addr, tlsConfig)
		if dialErr != nil {
			return fmt.Errorf("tls dial: %w", dialErr)
		}
		client, err = smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("smtp client: %w", err)
		}
	} else {
		// Port 587 (and others) use STARTTLS on a plain connection, not implicit TLS.
		client, err = smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("dial: %w", err)
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	defer client.Close()

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("close message: %w", err)
	}
	return client.Quit()
}
