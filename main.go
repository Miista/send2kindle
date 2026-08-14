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

	slog.Info("watching for new ebooks", "dir", watchDir)

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
	info, err := os.Stat(path)
	if err != nil {
		// File already gone (moved/deleted before the quiet period elapsed).
		return
	}
	if info.IsDir() {
		return
	}

	ext := strings.ToLower(filepath.Ext(path))
	if !kindleAcceptedExt[ext] {
		slog.Info("discarding file Amazon's Send-to-Kindle email won't accept", "path", path, "ext", ext)
		os.Remove(path)
		return
	}

	// Gmail's outbound SMTP limit is 25MB, and base64 encoding inflates the
	// attachment by ~37% — so the real ceiling on the source file is lower
	// than 25MB. Without this check, an oversized file fails send() the same
	// way every retry forever (SMTP server aborts mid-transfer), silently
	// piling up. Leave the file in place — it needs a human decision
	// (different delivery method, or it doesn't belong in this pipeline at
	// all), not an automatic delete.
	const maxSourceBytes = 18 * 1024 * 1024
	if info.Size() > maxSourceBytes {
		slog.Error("file too large for Gmail SMTP, leaving in place — needs manual handling",
			"path", path, "size_bytes", info.Size(), "limit_bytes", maxSourceBytes)
		return
	}

	if err := send(path); err != nil {
		slog.Error("send failed, leaving file in place for manual retry", "path", path, "error", err)
		return
	}

	slog.Info("sent to kindle", "title", filepath.Base(path))
	if err := os.Remove(path); err != nil {
		slog.Error("failed to remove source file after send", "path", path, "error", err)
	}
}

func send(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	from := os.Getenv("FROM_EMAIL")
	to := os.Getenv("KINDLE_EMAIL")
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
		return fmt.Errorf("create body part: %w", err)
	}
	bodyPart.Write([]byte("Sent by kindle-sender.\r\n"))

	attachHeader := textproto.MIMEHeader{
		"Content-Type":              {mime.TypeByExtension(filepath.Ext(path)) + "; name=\"" + filename + "\""},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {"attachment; filename=\"" + filename + "\""},
	}
	attachPart, err := w.CreatePart(attachHeader)
	if err != nil {
		return fmt.Errorf("create attachment part: %w", err)
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
		return fmt.Errorf("close multipart writer: %w", err)
	}

	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	auth := smtp.PlainAuth("", os.Getenv("SMTP_USER"), os.Getenv("SMTP_PASS"), host)

	addr := host + ":" + port
	tlsConfig := &tls.Config{ServerName: host}

	var client *smtp.Client
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
	if _, err := wc.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("close message: %w", err)
	}
	return client.Quit()
}
