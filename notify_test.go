package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// published is one notification as ntfy received it.
type published struct {
	Title    string
	Priority string
	Body     string
	Auth     string
}

// fakeNtfy stands up a server and points the package notifier at it.
func fakeNtfy(t *testing.T) *[]published {
	t.Helper()

	var mu sync.Mutex
	var got []published

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, published{
			Title:    r.Header.Get("Title"),
			Priority: r.Header.Get("Priority"),
			Body:     string(body),
			Auth:     r.Header.Get("Authorization"),
		})
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)

	original := notifier
	notifier = &Notifier{URL: srv.URL, Topic: "books"}
	t.Cleanup(func() { notifier = original })

	return &got
}

func TestNotifiesWhenAFileIsTooLarge(t *testing.T) {
	withFakeSend(t, nil)
	sent := fakeNtfy(t)
	path := writeFile(t, t.TempDir(), "huge.epub", maxSourceBytes+1)

	handle(path, ModeLibrary, newState(t))

	if len(*sent) != 1 {
		t.Fatalf("expected one notification, got %d", len(*sent))
	}
	n := (*sent)[0]
	if !strings.Contains(n.Title, "huge.epub") {
		t.Errorf("the notification did not name the file: %q", n.Title)
	}
	if n.Priority != "4" {
		t.Errorf("expected high priority, got %q", n.Priority)
	}
	// The size is the actionable part -- it says which alternative to reach
	// for -- and bytes are the wrong unit on a phone.
	if !strings.Contains(n.Body, "MB") {
		t.Errorf("the notification did not give a readable size: %q", n.Body)
	}
}

// The whole point: a warning fires once, not on every scan. This is the bug
// that logged the same error for eighteen days.
func TestNotifiesAboutAnOversizedFileOnlyOnce(t *testing.T) {
	withFakeSend(t, nil)
	sent := fakeNtfy(t)
	st := newState(t)
	path := writeFile(t, t.TempDir(), "huge.epub", maxSourceBytes+1)

	handle(path, ModeLibrary, st)
	handle(path, ModeLibrary, st)
	handle(path, ModeLibrary, st)

	if len(*sent) != 1 {
		t.Errorf("the same problem was announced %d times", len(*sent))
	}
}

// A destroyed file is the one thing that cannot be recovered from a log.
func TestNotifiesWhenDropModeDiscardsAFile(t *testing.T) {
	withFakeSend(t, nil)
	sent := fakeNtfy(t)
	path := writeFile(t, t.TempDir(), "comic.cbz", 1024)

	handle(path, ModeDrop, newState(t))

	if len(*sent) != 1 {
		t.Fatalf("deleting a file went unannounced: %d notification(s)", len(*sent))
	}
	if !strings.Contains((*sent)[0].Body, "deleted") {
		t.Errorf("the notification did not say the file was deleted: %q", (*sent)[0].Body)
	}
}

// Library mode kept the file, so nothing was lost and there is nothing to act
// on. The counterpart to the test above: silence is correct here.
func TestDoesNotNotifyWhenLibraryModeSkipsAFile(t *testing.T) {
	withFakeSend(t, nil)
	sent := fakeNtfy(t)
	path := writeFile(t, t.TempDir(), "comic.cbz", 1024)

	handle(path, ModeLibrary, newState(t))

	if len(*sent) != 0 {
		t.Errorf("skipping a file without deleting it was announced: %v", *sent)
	}
}

func TestNotifiesWhenSendingFails(t *testing.T) {
	withFakeSend(t, errSendFailed)
	sent := fakeNtfy(t)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handle(path, ModeLibrary, newState(t))

	if len(*sent) != 1 {
		t.Fatalf("a failed send went unannounced: %d", len(*sent))
	}
	// Default priority: this retries on the next scan, and a high-priority
	// alert for a transient blip trains you to ignore the channel.
	if (*sent)[0].Priority != "3" {
		t.Errorf("expected default priority for a retryable failure, got %q", (*sent)[0].Priority)
	}
}

// A successful send is visible on the Kindle. Announcing it too would make
// the channel noise, and noise is why the real warnings went unread.
func TestDoesNotNotifyOnSuccess(t *testing.T) {
	withFakeSend(t, nil)
	sent := fakeNtfy(t)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handle(path, ModeLibrary, newState(t))

	if len(*sent) != 0 {
		t.Errorf("a successful send was announced: %v", *sent)
	}
}

// Unconfigured must be silent rather than an error: this is a useful tool
// without ntfy, and an unset NTFY_URL is the default.
func TestUnconfiguredNotifierDoesNothing(t *testing.T) {
	if (&Notifier{}).Configured() {
		t.Error("an empty notifier claimed to be configured")
	}
	if (&Notifier{URL: "http://ntfy"}).Configured() {
		t.Error("a notifier with no topic claimed to be configured")
	}
	// Must not panic.
	(&Notifier{}).Notify("title", "body", priorityHigh)
}

func TestSendsTheTokenWhenOneIsSet(t *testing.T) {
	sent := fakeNtfy(t)
	notifier.Token = "tk_secret"

	notifier.Notify("title", "body", priorityDefault)

	if len(*sent) != 1 {
		t.Fatal("nothing was published")
	}
	if (*sent)[0].Auth != "Bearer tk_secret" {
		t.Errorf("the token was not sent: %q", (*sent)[0].Auth)
	}
}

// An ntfy outage must not change what happened to the book: the send already
// succeeded by the time this is called.
func TestAFailingNtfyDoesNotAffectTheOutcome(t *testing.T) {
	withFakeSend(t, nil)
	original := notifier
	notifier = &Notifier{URL: "http://127.0.0.1:1", Topic: "books"}
	t.Cleanup(func() { notifier = original })

	st := newState(t)
	path := writeFile(t, t.TempDir(), "book.epub", 1024)

	handle(path, ModeLibrary, st)

	key, _ := keyOf(path)
	if !st.Done(key) {
		t.Error("an unreachable ntfy stopped the send being recorded")
	}
}
