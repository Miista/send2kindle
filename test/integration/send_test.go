//go:build integration

package integration

import (
	"strings"
	"testing"
)

// These run the shipped image against a real SMTP server. The unit tests fake
// the sender, so what they cannot show is whether the conversation
// send2kindle actually makes is one a server will accept -- the AUTH, the TLS
// negotiation, the MIME body on the wire -- or whether the container can read
// the files it is given at all.

func TestSendsADroppedBook(t *testing.T) {
	s := Up(t, "send/basic")
	s.TrustSMTPD()
	s.Drop("Service Model.epub", 1024)
	s.Start()

	got := s.WaitForDelivery(1)

	if !strings.Contains(got[0], "Subject: Service Model.epub") {
		t.Errorf("the message did not name the book:\n%s", first(got[0]))
	}
	if !strings.Contains(got[0], "X-Envelope-To: to@kindle.com") {
		t.Errorf("the message did not go to the configured kindle address:\n%s", first(got[0]))
	}
	if s.Dropped("Service Model.epub") {
		t.Error("the book was still in the watch directory after being sent")
	}
}

// The counterpart: a format Amazon will not accept must not reach the server.
// Without this the suite only pins down the happy path, and the extension
// check could be dropped without an integration failure.
func TestDoesNotSendAnUnacceptedFormat(t *testing.T) {
	s := Up(t, "send/basic")
	s.TrustSMTPD()
	s.Drop("comic.cbz", 1024)
	s.Start()

	s.WaitForLine(Subject, "discarding file")

	if got := s.Delivered(); len(got) != 0 {
		t.Errorf("a format Amazon rejects was delivered anyway: %d message(s)", len(got))
	}
}

// The bug this guards: a 115MB epub failed the same way on every restart for
// eighteen days, silently. It must be left in place -- deleting it would
// destroy the only copy over a limit that belongs to the transport, not the
// book -- and it must say so.
func TestLeavesAnOversizedBookInPlace(t *testing.T) {
	s := Up(t, "send/basic")
	s.TrustSMTPD()
	s.Drop("huge.epub", 19*1024*1024)
	s.Start()

	line := s.WaitForLine(Subject, "too large")

	if !strings.Contains(line, "huge.epub") {
		t.Errorf("the warning did not name the file:\n%s", line)
	}
	if !s.Dropped("huge.epub") {
		t.Error("an oversized book was removed; it should be left for a human")
	}
	if got := s.Delivered(); len(got) != 0 {
		t.Errorf("an oversized book was handed to the server anyway: %d message(s)", len(got))
	}
}

// A file already present when the container starts must be picked up: the
// watcher only reports events from the moment it is registered, so without
// the startup sweep anything dropped while the tool was down would sit there
// forever.
func TestSendsABookThatWasAlreadyThere(t *testing.T) {
	s := Up(t, "send/basic")
	s.TrustSMTPD()
	s.Drop("Waiting.epub", 1024)
	s.Start()

	got := s.WaitForDelivery(1)

	if !strings.Contains(got[0], "Subject: Waiting.epub") {
		t.Errorf("the book that was already there was not the one sent:\n%s", first(got[0]))
	}
}

// Two books must produce two messages, not one: a scenario that drove the
// loop twice and asserted "a message arrived" would pass on the first one
// still being there.
func TestSendsEachBookOnce(t *testing.T) {
	s := Up(t, "send/basic")
	s.TrustSMTPD()
	s.Drop("First.epub", 1024)
	s.Drop("Second.epub", 1024)
	s.Start()

	got := s.WaitForDelivery(2)

	if len(got) != 2 {
		t.Fatalf("expected two messages, got %d", len(got))
	}
	subjects := strings.Join(got, "\n")
	for _, want := range []string{"Subject: First.epub", "Subject: Second.epub"} {
		if !strings.Contains(subjects, want) {
			t.Errorf("no message for %q", want)
		}
	}
}

// The attachment has to survive the round trip: a message that arrives
// without the book in it is worse than one that never arrives, because
// nothing reports a failure.
func TestAttachesTheBook(t *testing.T) {
	s := Up(t, "send/basic")
	s.TrustSMTPD()
	s.Drop("Attached.epub", 4096)
	s.Start()

	got := s.WaitForDelivery(1)

	for _, want := range []string{
		`filename="Attached.epub"`,
		"Content-Transfer-Encoding: base64",
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the message is missing %q:\n%s", want, first(got[0]))
		}
	}
}

// --- library mode ---

// The contract library mode exists for: the book is sent and STAYS. A drop
// folder is a queue, a library is a shelf, and emailing something is not a
// reason to take it off the shelf.
func TestLibraryModeSendsWithoutRemoving(t *testing.T) {
	s := Up(t, "send/library")
	s.TrustSMTPD()
	s.Drop("Kept.epub", 1024)
	s.Start()

	got := s.WaitForDelivery(1)

	if !strings.Contains(got[0], "Subject: Kept.epub") {
		t.Errorf("the wrong book was sent:\n%s", first(got[0]))
	}
	if !s.Dropped("Kept.epub") {
		t.Error("library mode removed the book from the shelf")
	}
}

// And the reason the state file has to exist: without it the startup sweep
// would re-send the whole shelf on every restart, because nothing was removed
// to say otherwise.
func TestLibraryModeDoesNotResendAfterARestart(t *testing.T) {
	s := Up(t, "send/library")
	s.TrustSMTPD()
	s.Drop("Once.epub", 1024)
	s.Start()
	s.WaitForDelivery(1)

	s.Restart(Subject)
	s.WaitForLine(Subject, "watching for new ebooks")

	if got := s.Delivered(); len(got) != 1 {
		t.Errorf("the shelf was re-sent on restart: %d message(s), expected 1", len(got))
	}
}

// A library is nested: Shelfarr writes Author/Title/book.epub. The tool used
// to read one directory, so a real shelf was invisible to it -- every file
// sat there and nothing was ever sent.
func TestFindsABookInASubdirectory(t *testing.T) {
	s := Up(t, "send/library")
	s.TrustSMTPD()
	s.Drop("Adrian Tchaikovsky/Service Model/Service Model.epub", 1024)
	s.Start()

	got := s.WaitForDelivery(1)

	if !strings.Contains(got[0], "Subject: Service Model.epub") {
		t.Errorf("the nested book was not the one sent:\n%s", first(got[0]))
	}
}

// Staging directories are not shelves. Shelfarr keeps partial downloads in
// .shelfarr-staging-v2, and emailing a half-written file would be worse than
// missing it.
func TestSkipsHiddenDirectories(t *testing.T) {
	s := Up(t, "send/library")
	s.TrustSMTPD()
	s.Drop(".shelfarr-staging-v2/partial.epub", 1024)
	s.Drop("Author/Real.epub", 1024)
	s.Start()

	got := s.WaitForDelivery(1)

	if strings.Contains(strings.Join(got, "\n"), "partial.epub") {
		t.Error("a file from a staging directory was sent")
	}
	if !strings.Contains(got[0], "Subject: Real.epub") {
		t.Errorf("the real book was not sent:\n%s", first(got[0]))
	}
}

// first is the head of a message, for failure output: the whole thing is a
// base64 book and scrolling past it to find the headers helps nobody.
func first(msg string) string {
	lines := strings.Split(msg, "\n")
	if len(lines) > 15 {
		lines = lines[:15]
	}
	return strings.Join(lines, "\n")
}
