package integration

// What the package needs before any scenario can run: the image under test,
// and the SMTP server that stands in for Gmail.
//
// Here rather than in main_test.go because standing a scenario up by hand and
// testing one are the same work.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Subject is the compose service name of the thing under test, and the name a
// fixture uses for it.
//
// The harness holds the world up around it: the scenario copies in a fixture
// and brings it up, and Start() runs the subject once the world is finished.
const Subject = "send2kindle"

// label marks everything this suite creates, so a sweep can find it even after
// a run that was killed before its cleanup.
const label = "io.github.miista.send2kindle.integration"

// suiteImage is the send2kindle image every scenario refers to, built once per
// suite from the repository it lives in.
const suiteImage = "send2kindle:integration"

// smtpdImage stands in for Gmail: it accepts the SMTP conversation and spools
// what it was sent, for the scenarios that assert on what actually went out.
const smtpdImage = "send2kindle-smtpd:integration"

// Setup builds both images.
func Setup() (string, error) {
	root, err := repoRootDir()
	if err != nil {
		return "", err
	}
	if err := buildImage(root); err != nil {
		return "", fmt.Errorf("building %s: %w", suiteImage, err)
	}
	if err := buildSMTPD(root); err != nil {
		return "", fmt.Errorf("building %s: %w", smtpdImage, err)
	}
	return root, nil
}

// Teardown removes anything still labelled from this suite.
func Teardown(root string) {
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label="+label).Output()
	if err != nil {
		return
	}
	if ids := fields(string(out)); len(ids) > 0 {
		_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
	}
}

// buildImage builds send2kindle from the shipped Dockerfile.
//
// The real one, deliberately: a suite that builds its own image tests an
// artifact nobody ships, and the two drift. That is not hypothetical -- the
// shipped image is FROM scratch and runs as 1000:1000, and a test image built
// any other way would not show a file the tool cannot read.
func buildImage(root string) error {
	cmd := exec.Command("docker", "build", "-q",
		"--label", label+"=integration", "-t", suiteImage, ".")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// buildSMTPD builds the fake SMTP server from its own source, rather than a
// heredoc: it is ordinary Go that compiles and vets with everything else.
func buildSMTPD(root string) error {
	dir, err := os.MkdirTemp("", "send2kindle-smtpd")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	build := exec.Command("go", "build", "-ldflags", "-s -w", "-o",
		filepath.Join(dir, "smtpd"), "./test/integration/smtpd")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}

	// FROM scratch: nothing is pulled, so the suite works offline.
	dockerfile := "FROM scratch\nCOPY smtpd /smtpd\nCMD [\"/smtpd\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return err
	}

	cmd := exec.Command("docker", "build", "-q",
		"--label", label+"=integration", "-t", smtpdImage, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func repoRootDir() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("finding the repo root: %w", err)
	}
	return trimmed(out), nil
}
