package integration

import (
	"fmt"
	"strings"
	"time"
)

// Waiting for the world to settle.
//
// Almost every flaky integration test is a test that looked before the thing
// it was watching had happened. In shell that was a sleep, or a poll with a
// wall-clock window guessing which log lines belonged to which run -- both
// wrong often enough to matter, and silent when they were.
//
// These wait for a condition and say what they last saw when it never came
// true, so a timeout is a diagnosis rather than a mystery.

// waitFor blocks until cond returns true, or fails the test after timeout
// with what describe last reported.
func (s *Scenario) waitFor(what string, timeout time.Duration, cond func() bool, describe func() string) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatalf("timed out after %s waiting for %s\nlast saw:\n%s",
		timeout, what, indent(describe()))
}

func indent(s string) string {
	if s == "" {
		return "    <nothing>"
	}
	return "    " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n    ")
}

// ReadyLine is what the subject prints once it is running and has done
// something. Start waits for it, so a test never acts on a subject that has
// not looked at the world yet.
//
// A line rather than a container state, because "up" and "ready" are not the
// same: send2kindle is up the moment its process starts, but it is only
// useful once it has swept the watch directory and registered its watcher --
// a test that dropped a file in between would see nothing happen. Which line
// that is belongs to the subject, so it lives here rather than in the
// harness's logic.
var ReadyLine = "watching for new ebooks"

// ready reports whether the subject has announced itself.
func (s *Scenario) ready(service string) bool {
	return strings.Contains(s.Logs(service), ReadyLine)
}

// WaitForLine returns the first log line from a service containing want,
// waiting for it to appear.
func (s *Scenario) WaitForLine(service, want string) string {
	s.t.Helper()
	var found string
	s.waitFor(want+" in "+service+"'s output", 30*time.Second,
		func() bool {
			for _, line := range strings.Split(s.Logs(service), "\n") {
				if strings.Contains(line, want) {
					found = strings.TrimSpace(line)
					return true
				}
			}
			return false
		},
		func() string { return s.Logs(service) })
	return found
}

// Times is how often a service has printed a line containing want.
//
// A count rather than a yes/no, because "it happened" cannot tell a second
// occurrence from the first one still being there. A test that drives a
// container round the same loop twice needs to know the second lap happened,
// and Contains says yes to the line left over from the first.
func (s *Scenario) Times(service, want string) int {
	s.t.Helper()
	return strings.Count(s.Logs(service), want)
}

// WaitForMore blocks until a service has printed want more times than it had
// at was.
//
// The shape a test wants when it drives something round a loop: take a count,
// act, then wait for the count to move.
func (s *Scenario) WaitForMore(service, want string, was int) {
	s.t.Helper()
	s.waitFor(fmt.Sprintf("%s to print %q again", service, want), 60*time.Second,
		func() bool { return s.Times(service, want) > was },
		func() string { return s.Logs(service) })
}
