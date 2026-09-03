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
	// Chmod after creating, because MkdirAll's mode is masked by the process
	// umask -- 0o777 becomes 0o755 under the usual 022, and the shim runs as
	// uid 1000 (the shipped image sets USER 1000:1000) which is not the uid
	// that created it. Removing a file needs write permission on the
	// DIRECTORY, so drop mode could send a book and then fail to consume it.
	//
	// Invisible on macOS, where Docker Desktop and Colima paper over ownership
	// entirely; on a Linux CI runner it is "permission denied".
	chmodTree(dir)

	tmp := filepath.Join(dir, "."+name+".partial")
	if err := os.WriteFile(tmp, make([]byte, size), 0o666); err != nil {
		s.t.Fatalf("writing %s: %v", name, err)
	}
	_ = os.Chmod(tmp, 0o666)
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

// WaitForConsumed blocks until a dropped file has been removed from the watch
// directory.
//
// Separate from WaitForDelivery because they are different moments:
// smtpd spools the message when it accepts it, and the shim removes the file
// only after send() returns. A test that asserted on the file straight after
// delivery raced that gap -- it passed locally and failed on a slower CI
// runner, which is the worst version of a flake.
func (s *Scenario) WaitForConsumed(name string) {
	s.t.Helper()
	s.waitFor("the shim to remove "+name+" after sending it", 60*time.Second,
		func() bool { return !s.Dropped(name) },
		func() string {
			return "still in the watch directory\n" + s.Logs(Subject)
		})
}

// WaitForRecorded blocks until the shim has written its state file.
//
// The same race in library mode: the outcome is recorded after the send
// returns, so a restart between the two makes the startup sweep re-send a
// book that was already delivered.
func (s *Scenario) WaitForRecorded(name string) {
	s.t.Helper()
	s.waitFor("the shim to record "+name+" as handled", 60*time.Second,
		func() bool {
			b, err := os.ReadFile(filepath.Join(s.Dir, "state", "sent.json"))
			return err == nil && strings.Contains(string(b), name)
		},
		func() string {
			b, _ := os.ReadFile(filepath.Join(s.Dir, "state", "sent.json"))
			return "state file:\n" + string(b) + "\n" + s.Logs(Subject)
		})
}

// chmodTree makes a directory and everything under it writable by any uid.
//
// The suite's containers run as uid 1000 against bind mounts created by
// whatever uid runs the tests -- 501 on this laptop, something else on a CI
// runner. Rather than teach every fixture about ownership, the harness simply
// opens up the directories it creates: they live in the testbed, which is
// swept between scenarios.
func chmodTree(root string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			_ = os.Chmod(path, 0o777)
		} else {
			_ = os.Chmod(path, 0o666)
		}
		return nil
	})
}
