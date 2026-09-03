// smtpd stands in for Gmail: it accepts the SMTP conversation send2kindle
// makes and records each message where a test can read it.
//
// A container like the tool under test, rather than a server in the test
// process. send2kindle runs in a container and connects over the network, so
// a server on the host would be reachable only through a gateway address
// nothing in production uses.
//
// It exists because the unit tests fake the sender, so what they cannot show
// is whether the SMTP conversation send2kindle actually makes is one a server
// will accept -- the AUTH, the STARTTLS negotiation, the MIME body on the
// wire. That needs something on the other end of the socket.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// spoolDir is where accepted messages are written, mounted from the scenario
// so a test can read them without entering the container.
const spoolDir = "/spool"

// healthcheck is how the fixture knows smtpd is accepting connections before
// send2kindle is allowed to start. Without it send2kindle can come up first,
// fail its first send against a closed port, and leave the test asserting on
// a race rather than on behaviour.
func healthcheck() {
	conn, err := net.DialTimeout("tcp", "localhost:465", 2*time.Second)
	if err != nil {
		os.Exit(1)
	}
	conn.Close()
	os.Exit(0)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		healthcheck()
	}

	// Generated here rather than mounted in: the certificate is written to the
	// spool volume, which the scenario already reads, and copied into
	// send2kindle's trust store from there. send2kindle validates the
	// certificate -- there is no skip-verify flag, and adding one for tests
	// would weaken the real thing -- so being a CA it trusts is the only way
	// to a working handshake.
	cert, caPEM, err := selfSignedCert()
	if err != nil {
		fmt.Fprintf(os.Stderr, "smtpd: certificate: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(spoolDir, 0o777); err != nil {
		fmt.Fprintf(os.Stderr, "smtpd: spool: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(spoolDir, "ca.crt"), caPEM, 0o666); err != nil {
		fmt.Fprintf(os.Stderr, "smtpd: writing ca: %v\n", err)
		os.Exit(1)
	}

	// Implicit TLS on 465: send2kindle calls StartTLS unconditionally on any
	// other port, so a plaintext listener would fail the negotiation.
	ln, err := tls.Listen("tcp", ":465", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		fmt.Fprintf(os.Stderr, "smtpd: listen: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("smtpd: listening on :465")

	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go serve(conn)
	}
}

// selfSignedCert returns the certificate smtpd serves, and the same
// certificate in PEM form for send2kindle to trust.
func selfSignedCert() (tls.Certificate, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "smtpd"},
		DNSNames:              []string{"smtpd", "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pemBytes, nil
}

// serve walks one client through the SMTP conversation. Only the verbs
// send2kindle uses are implemented; anything else gets a bare 250 so an
// unexpected verb fails the test loudly rather than hanging.
func serve(conn net.Conn) {
	defer conn.Close()

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	say := func(format string, a ...any) {
		fmt.Fprintf(w, format+"\r\n", a...)
		w.Flush()
	}

	say("220 smtpd test server")

	var from, rcpt string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.TrimSpace(line))

		switch {
		case strings.HasPrefix(verb, "EHLO"), strings.HasPrefix(verb, "HELO"):
			// 250-STARTTLS advertises the upgrade; AUTH PLAIN is what
			// send2kindle authenticates with.
			say("250-smtpd")
			say("250-AUTH PLAIN LOGIN")
			say("250 OK")

		case strings.HasPrefix(verb, "AUTH"):
			say("235 Authentication successful")

		case strings.HasPrefix(verb, "MAIL FROM"):
			from = extractAddr(line)
			say("250 OK")

		case strings.HasPrefix(verb, "RCPT TO"):
			rcpt = extractAddr(line)
			say("250 OK")

		case strings.HasPrefix(verb, "DATA"):
			say("354 End data with <CR><LF>.<CR><LF>")
			body, err := readData(r)
			if err != nil {
				return
			}
			if err := spool(from, rcpt, body); err != nil {
				say("451 Local error: %v", err)
				continue
			}
			say("250 OK: queued")

		case strings.HasPrefix(verb, "QUIT"):
			say("221 Bye")
			return

		default:
			say("250 OK")
		}
	}
}

// readData collects the message until the lone-dot terminator.
func readData(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		if line == ".\r\n" || line == ".\n" {
			return b.String(), nil
		}
		b.WriteString(line)
	}
}

// spool writes one message per file so a test can count them as well as read
// them: "sent once, not on every restart" is a thing worth asserting.
func spool(from, rcpt, body string) error {
	if err := os.MkdirAll(spoolDir, 0o777); err != nil {
		return err
	}
	name := filepath.Join(spoolDir, fmt.Sprintf("%d.msg", time.Now().UnixNano()))
	content := fmt.Sprintf("X-Envelope-From: %s\nX-Envelope-To: %s\n%s", from, rcpt, body)
	return os.WriteFile(name, []byte(content), 0o666)
}

func extractAddr(line string) string {
	start := strings.Index(line, "<")
	end := strings.Index(line, ">")
	if start < 0 || end < start {
		return strings.TrimSpace(line)
	}
	return line[start+1 : end]
}
