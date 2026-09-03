package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Notifier publishes to ntfy.
//
// It exists because an error line goes to stdout and nobody reads stdout. The
// guards in handle were correct for weeks while a 115MB epub failed on every
// restart, and the only reason that went unnoticed for eighteen days is that
// the warning had nowhere to go.
type Notifier struct {
	URL   string
	Topic string
	Token string // empty = unauthenticated
}

// Priorities per the ntfy spec.
const (
	priorityDefault = 3
	priorityHigh    = 4
)

// notifier is what the handlers publish through. A no-op unless configured,
// so an unset NTFY_URL leaves behaviour exactly as it was.
var notifier = &Notifier{}

// notifierFromEnv reads the ntfy configuration. Unset URL or topic means
// notifications are off -- this is a useful tool without them, and refusing to
// start over an optional integration would be wrong.
func notifierFromEnv() *Notifier {
	return &Notifier{
		URL:   os.Getenv("NTFY_URL"),
		Topic: os.Getenv("NTFY_TOPIC"),
		Token: os.Getenv("NTFY_TOKEN"),
	}
}

// Configured reports whether there is anywhere to publish to.
func (n *Notifier) Configured() bool {
	return n != nil && n.URL != "" && n.Topic != ""
}

// Notify publishes one message, logging rather than returning a failure.
//
// A lost notification must not change what happened to the book: the send
// already succeeded or already failed by the time this is called, and an ntfy
// outage turning that into a different outcome would be worse than silence.
func (n *Notifier) Notify(title, message string, priority int) {
	if !n.Configured() {
		return
	}
	if err := n.publish(title, message, priority); err != nil {
		log.Warn().Err(err).Msg("could not publish the ntfy notification, so this failure is only in the log")
	}
}

func (n *Notifier) publish(title, message string, priority int) error {
	url := strings.TrimRight(n.URL, "/") + "/" + n.Topic
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(message))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", strconv.Itoa(priority))
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy: HTTP %d publishing to %s", resp.StatusCode, url)
	}
	return nil
}
