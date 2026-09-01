package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These cover handleFile's decision logic: which files get sent, which get
// discarded, which are left alone. The send itself is faked -- what matters
// here is the decision, not SMTP.

// withFakeSend swaps the sender for the duration of a test and returns a
// pointer to the list of paths it was asked to send.
var errSendFailed = errors.New("smtp refused the message")

func withFakeSend(t *testing.T, err error) *[]string {
	t.Helper()
	var sent []string
	original := sendFile
	sendFile = func(path string) error {
		sent = append(sent, path)
		return err
	}
	t.Cleanup(func() { sendFile = original })
	return &sent
}

// writeFile creates a file of exactly size bytes and returns its path.
func writeFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestSendsAnAcceptedFormat(t *testing.T) {
	sent := withFakeSend(t, nil)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handleFile(path)

	if len(*sent) != 1 || (*sent)[0] != path {
		t.Errorf("expected the epub to be sent, got %v", *sent)
	}
}

// The counterpart to the test above: a format Amazon will not accept must not
// be sent. Without this, only the happy path is pinned down and the extension
// check could be dropped entirely without a test failing.
func TestDoesNotSendAnUnacceptedFormat(t *testing.T) {
	sent := withFakeSend(t, nil)
	path := writeFile(t, t.TempDir(), "book.cbz", 1024)

	handleFile(path)

	if len(*sent) != 0 {
		t.Errorf("a format Amazon rejects was sent anyway: %v", *sent)
	}
}

func TestRemovesAfterSending(t *testing.T) {
	withFakeSend(t, nil)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handleFile(path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the file was still present after a successful send")
	}
}

// The bug this guards: an oversized file used to fail send the same way on
// every restart, silently, forever. It must be left in place -- deleting it
// would destroy the only copy over a limit that is a property of the
// transport, not the book.
func TestLeavesAnOversizedFileInPlace(t *testing.T) {
	sent := withFakeSend(t, nil)
	path := writeFile(t, t.TempDir(), "huge.epub", maxSourceBytes+1)

	handleFile(path)

	if len(*sent) != 0 {
		t.Errorf("an oversized file was handed to send: %v", *sent)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("an oversized file was removed; it should be left for a human")
	}
}

// Exactly at the limit is the boundary the check has to get right.
func TestSendsAFileExactlyAtTheLimit(t *testing.T) {
	sent := withFakeSend(t, nil)
	path := writeFile(t, t.TempDir(), "big.epub", maxSourceBytes)

	handleFile(path)

	if len(*sent) != 1 {
		t.Errorf("a file exactly at the limit was not sent: %v", *sent)
	}
}

// A failed send must not delete the file -- the send can be retried, but the
// book cannot be recovered.
func TestKeepsTheFileWhenSendingFails(t *testing.T) {
	withFakeSend(t, errSendFailed)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handleFile(path)

	if _, err := os.Stat(path); err != nil {
		t.Error("the file was removed even though the send failed")
	}
}

func TestIgnoresDirectories(t *testing.T) {
	sent := withFakeSend(t, nil)
	dir := filepath.Join(t.TempDir(), "Author")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	handleFile(dir)

	if len(*sent) != 0 {
		t.Errorf("a directory was handed to send: %v", *sent)
	}
}

func TestIgnoresAMissingFile(t *testing.T) {
	sent := withFakeSend(t, nil)

	handleFile(filepath.Join(t.TempDir(), "gone.epub"))

	if len(*sent) != 0 {
		t.Errorf("a missing file was handed to send: %v", *sent)
	}
}

// Extensions arrive in whatever case the release used.
func TestMatchesExtensionsCaseInsensitively(t *testing.T) {
	sent := withFakeSend(t, nil)
	path := writeFile(t, t.TempDir(), "book.EPUB", 1024)

	handleFile(path)

	if len(*sent) != 1 {
		t.Errorf("an uppercase extension was not recognised: %v", *sent)
	}
}

// The current watch directory is flat, so a nested file is never reached.
// This records that limitation rather than asserting it is correct: it is the
// thing library mode has to change.
func TestAcceptedFormatsCoverAmazonsList(t *testing.T) {
	for _, ext := range []string{".epub", ".pdf", ".txt"} {
		if !kindleAcceptedExt[ext] {
			t.Errorf("%s should be accepted by Amazon's service", ext)
		}
	}
	// Amazon dropped these for email delivery around 2022.
	for _, ext := range []string{".mobi", ".azw3"} {
		if kindleAcceptedExt[ext] {
			t.Errorf("%s is no longer accepted for email delivery", ext)
		}
	}
}

func TestBuildsAMessageWithTheFileAttached(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "My Book.epub", 32)

	msg, err := buildMessage(path, "from@example.com", "to@kindle.com")
	if err != nil {
		t.Fatalf("build message: %v", err)
	}

	got := string(msg)
	for _, want := range []string{
		"From: from@example.com",
		"To: to@kindle.com",
		"Subject: My Book.epub",
		`filename="My Book.epub"`,
		"Content-Transfer-Encoding: base64",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message is missing %q", want)
		}
	}
}
