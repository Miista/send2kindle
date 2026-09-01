// receiver stands in for ntfy: it accepts send2kindle's notifications and
// records them where a test can read them.
//
// A container like everything else in a scenario, rather than a server in the
// test process. send2kindle runs in a container and posts over the network, so
// a receiver on the host would be reachable only through a gateway address
// nothing in production uses -- and the fixture could not simply declare it as
// a service.
//
// It exists because the unit tests point the notifier at an httptest server,
// so what they cannot show is whether the container can reach ntfy at all:
// DNS on the compose network, and a URL assembled from environment variables
// that only exist in a real deployment.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

// logPath is where notifications are recorded, mounted from the scenario so a
// test can read it without entering the container.
const logPath = "/log/notifications.log"

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		// One line per notification, title first: a test asserts on what was
		// announced, and the title is the part that names the book.
		line := fmt.Sprintf("topic=%s priority=%s title=%s body=%s\n",
			r.URL.Path, r.Header.Get("Priority"), r.Header.Get("Title"),
			// Newlines in the body would break one-line-per-notification.
			sanitise(string(body)))

		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
		if err == nil {
			_, _ = f.WriteString(line)
			f.Close()
		}
		w.WriteHeader(http.StatusOK)
	})

	fmt.Println("receiver: listening on :80")
	if err := http.ListenAndServe(":80", nil); err != nil {
		fmt.Fprintf(os.Stderr, "receiver: %v\n", err)
		os.Exit(1)
	}
}

func sanitise(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' {
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}
