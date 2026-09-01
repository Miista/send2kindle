package integration

// What a scenario does with the world once it is up: drop books into the
// watch directory, and read what smtpd was sent.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Drop writes a file into the watch directory, which is what a frontend --
// bindery, Shelfarr, a manual copy -- does in production.
//
// Written to a temporary name and renamed into place, because send2kindle
// debounces on write events: a file that appears complete is the case worth
// testing, and a partial one arriving in pieces is a different scenario.
func (s *Scenario) Drop(name string, size int) string {
	s.t.Helper()

	// name may carry a path: "Author/Title/book.epub" is what a library looks
	// like, and the nested case is the one recursion exists for.
	dir := filepath.Join(s.Dir, "watch", filepath.Dir(name))
	name = filepath.Base(name)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		s.t.Fatalf("preparing the watch directory: %v", err)
	}

	tmp := filepath.Join(dir, "."+name+".partial")
	if err := os.WriteFile(tmp, make([]byte, size), 0o666); err != nil {
		s.t.Fatalf("writing %s: %v", name, err)
	}
	final := filepath.Join(dir, name)
	if err := os.Rename(tmp, final); err != nil {
		s.t.Fatalf("renaming %s into place: %v", name, err)
	}
	return final
}

// Dropped reports whether a file is still in the watch directory. The name
// may carry a path, matching Drop.
//
// The question every scenario asks after an outcome: a sent book is removed,
// and one that could not be sent must still be there.
func (s *Scenario) Dropped(name string) bool {
	s.t.Helper()
	_, err := os.Stat(filepath.Join(s.Dir, "watch", filepath.FromSlash(name)))
	return err == nil
}

// Delivered is every message smtpd accepted, most recent last.
//
// Read from the spool rather than from send2kindle's own logs: what the tool
// says it did and what a server actually received are different claims, and
// only the second one is worth a container.
func (s *Scenario) Delivered() []string {
	s.t.Helper()

	entries, err := os.ReadDir(filepath.Join(s.Dir, "spool"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".msg") {
			names = append(names, e.Name())
		}
	}
	sortStrings(names)

	var messages []string
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(s.Dir, "spool", name))
		if err != nil {
			continue
		}
		messages = append(messages, string(b))
	}
	return messages
}

// WaitForDelivery blocks until smtpd has accepted want messages, and returns
// them.
func (s *Scenario) WaitForDelivery(want int) []string {
	s.t.Helper()
	var got []string
	s.waitFor(fmt.Sprintf("%d message(s) to reach smtpd", want), 60*time.Second,
		func() bool {
			got = s.Delivered()
			return len(got) >= want
		},
		func() string {
			return fmt.Sprintf("saw %d of %d\n%s", len(got), want, s.Logs(Subject))
		})
	return got
}

// TrustSMTPD copies the certificate smtpd generated into send2kindle's trust
// store and restarts it.
//
// send2kindle validates the certificate, and smtpd generates a fresh one each
// run, so the trust has to be established after smtpd is up and before
// send2kindle tries to deliver anything.
func (s *Scenario) TrustSMTPD() {
	s.t.Helper()

	ca := filepath.Join(s.Dir, "spool", "ca.crt")
	s.waitFor("smtpd to write its certificate", 30*time.Second,
		func() bool { _, err := os.Stat(ca); return err == nil },
		func() string { return s.Logs("smtpd") })

	b, err := os.ReadFile(ca)
	if err != nil {
		s.t.Fatalf("reading smtpd's certificate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "certs", "ca-certificates.crt"), b, 0o666); err != nil {
		s.t.Fatalf("writing the trust store: %v", err)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Restart stops and starts a service, which is how the "does it re-send the
// shelf?" question is asked: a restart is the moment the startup sweep runs
// against a library that is still full.
func (s *Scenario) Restart(service string) {
	s.t.Helper()
	if out, err := s.compose("restart", "-t", "2", service); err != nil {
		s.t.Fatalf("restarting %s: %v\n%s", service, err, out)
	}
}

// Notified is every notification the receiver has recorded, one per line.
//
// Read from the receiver rather than from send2kindle's own logs: what the
// tool says it announced and what actually arrived over the network are
// different claims, and only the second one is worth a container.
func (s *Scenario) Notified() []string {
	s.t.Helper()

	b, err := os.ReadFile(filepath.Join(s.Dir, "notifications", "notifications.log"))
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// WaitForNotification blocks until a notification containing want arrives, and
// returns it.
func (s *Scenario) WaitForNotification(want string) string {
	s.t.Helper()
	var found string
	s.waitFor("a notification mentioning "+want, 60*time.Second,
		func() bool {
			for _, line := range s.Notified() {
				if strings.Contains(line, want) {
					found = line
					return true
				}
			}
			return false
		},
		func() string {
			return strings.Join(s.Notified(), "\n") + "\n" + s.Logs(Subject)
		})
	return found
}
