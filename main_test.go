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

// --- library mode ---
//
// The difference that matters: drop mode consumes what it sends, library mode
// leaves it and remembers. These assert both halves, because a mode that sent
// correctly but deleted the book would pass any test that only checked
// delivery.

// newState is an in-memory state backed by a temp file.
func newState(t *testing.T) *State {
	t.Helper()
	return LoadState(filepath.Join(t.TempDir(), "sent.json"))
}

func TestLibraryModeKeepsTheFile(t *testing.T) {
	withFakeSend(t, nil)
	st := newState(t)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handle(path, ModeLibrary, st)

	if _, err := os.Stat(path); err != nil {
		t.Error("library mode removed the book from the shelf")
	}
}

func TestLibraryModeRecordsWhatItSent(t *testing.T) {
	sent := withFakeSend(t, nil)
	st := newState(t)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handle(path, ModeLibrary, st)

	if len(*sent) != 1 {
		t.Fatalf("the book was not sent: %v", *sent)
	}
	key, _ := keyOf(path)
	if !st.Done(key) {
		t.Error("the send was not recorded")
	}
}

// The point of recording: a second pass must not send it again. Without this,
// every restart re-sends the whole shelf.
func TestLibraryModeDoesNotSendTwice(t *testing.T) {
	sent := withFakeSend(t, nil)
	st := newState(t)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handle(path, ModeLibrary, st)
	handle(path, ModeLibrary, st)

	if len(*sent) != 1 {
		t.Errorf("the book was sent %d times, expected once", len(*sent))
	}
}

// A failure is a decision too: the oversized warning must fire once, not on
// every restart. This is the bug that logged the same error for eighteen days.
func TestLibraryModeWarnsAboutAnOversizedBookOnlyOnce(t *testing.T) {
	withFakeSend(t, nil)
	st := newState(t)
	path := writeFile(t, t.TempDir(), "huge.epub", maxSourceBytes+1)

	handle(path, ModeLibrary, st)
	key, _ := keyOf(path)
	if !st.Done(key) {
		t.Fatal("the oversized outcome was not recorded")
	}
	if got := st.Entries[key].Outcome; got != OutcomeTooLarge {
		t.Errorf("recorded outcome was %q, expected %q", got, OutcomeTooLarge)
	}
}

// A send that failed is NOT terminal: the server may have been briefly
// unreachable, and a book that never arrives and never retries is worse than
// a duplicate.
func TestLibraryModeRetriesAfterAFailedSend(t *testing.T) {
	sent := withFakeSend(t, errSendFailed)
	st := newState(t)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handle(path, ModeLibrary, st)
	key, _ := keyOf(path)
	if st.Done(key) {
		t.Error("a failed send was recorded as a final outcome")
	}

	handle(path, ModeLibrary, st)
	if len(*sent) != 2 {
		t.Errorf("a failed send was not retried: %d attempt(s)", len(*sent))
	}
}

// Library mode must never delete, even for a format Amazon will not take.
// Drop mode discards those -- the file was a copy made to be consumed -- but
// a shelf is not ours to delete from.
func TestLibraryModeKeepsAnUnacceptedFormat(t *testing.T) {
	sent := withFakeSend(t, nil)
	st := newState(t)
	path := writeFile(t, t.TempDir(), "comic.cbz", 1024)

	handle(path, ModeLibrary, st)

	if len(*sent) != 0 {
		t.Errorf("an unaccepted format was sent: %v", *sent)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("library mode deleted a file Amazon cannot take; it is still the owner's book")
	}
}

// Drop mode is unchanged by any of this: it still consumes what it sends, and
// still does not need a state file.
func TestDropModeStillRemovesTheFile(t *testing.T) {
	withFakeSend(t, nil)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handle(path, ModeDrop, newState(t))

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("drop mode left the file behind")
	}
}

// And drop mode does not consult the state file: a file it has seen before is
// gone, so there is nothing to suppress.
func TestDropModeIgnoresTheState(t *testing.T) {
	sent := withFakeSend(t, nil)
	st := newState(t)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)
	key, _ := keyOf(path)
	st.Record(key, OutcomeSent, "book.epub", 1024)

	handle(path, ModeDrop, st)

	if len(*sent) != 1 {
		t.Error("drop mode skipped a file because of a state entry")
	}
}

// A library is not a queue of things you chose to send: it is full of files
// that came with the book. Amazon accepts .txt and .jpg happily, so the
// drop-mode list mails scene notes and cover art to the device alongside the
// books.
//
// Not hypothetical -- switching a real library to this mode did exactly that.
func TestLibraryModeSendsOnlyBooks(t *testing.T) {
	for _, ext := range []string{".txt", ".jpg", ".png", ".html", ".docx", ".pdf"} {
		sent := withFakeSend(t, nil)
		path := writeFile(t, t.TempDir(), "release-notes"+ext, 512)

		handle(path, ModeLibrary, newState(t))

		if len(*sent) != 0 {
			t.Errorf("library mode sent a %s: %v", ext, *sent)
		}
	}
}

// The counterpart: it must still send the one format a library holds.
func TestLibraryModeSendsEpub(t *testing.T) {
	sent := withFakeSend(t, nil)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handle(path, ModeLibrary, newState(t))

	if len(*sent) != 1 {
		t.Errorf("library mode did not send an .epub: %v", *sent)
	}
}

// Drop mode is unchanged: a file put there deliberately is one you meant to
// send, whatever its extension.
func TestDropModeStillSendsAnythingAmazonAccepts(t *testing.T) {
	for _, ext := range []string{".txt", ".pdf"} {
		sent := withFakeSend(t, nil)
		path := writeFile(t, t.TempDir(), "note"+ext, 512)

		handle(path, ModeDrop, newState(t))

		if len(*sent) != 1 {
			t.Errorf("drop mode stopped sending a %s: %v", ext, *sent)
		}
	}
}
