#!/usr/bin/env bash
# Coverage, reported per suite.
#
# Two numbers rather than one, because there are two binaries. The unit tests
# instrument the test harness; the integration suite instruments the binary the
# Dockerfile ships. `go tool covdata merge` refuses to combine them, and it is
# right to -- their meta hashes differ because they are genuinely different
# builds, and a single blended percentage would be arithmetic rather than
# information.
#
# What each is for:
#
#   unit         the decisions -- which files are sent, what is recorded, how a
#                mode changes the outcome. Fast, no docker.
#   integration  the process boundary -- main's fsnotify loop, and the SMTP
#                conversation in send/deliver. A unit test cannot reach these
#                without mocking net/smtp, which tests the mock.
#
# Together they cover every function; separately they say which suite to look
# at when something regresses.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# Under the repository, not $TMPDIR: on macOS that lives behind a symlink, and
# the path the container is given resolves to something else -- the same reason
# the harness puts its testbed here.
work="$(pwd)/.coverage"
rm -rf "$work"
mkdir -p "$work/covdata"
trap 'rm -rf "$work"' EXIT

echo "==> unit"
go test . -coverprofile="$work/unit.out" -covermode=set
go tool cover -func="$work/unit.out" | tail -1

echo
echo "==> integration (instrumented image)"
GOCOVERDIR="$work/covdata" go test -tags integration ./test/integration/ -count=1

if [ -z "$(ls -A "$work/covdata" 2>/dev/null)" ]; then
  echo "coverage: the integration suite produced no data -- is the image built with COVER=1?" >&2
  exit 1
fi

go tool covdata textfmt -i="$work/covdata" -o="$work/integration.out"

echo
echo "==> per function, by suite"
printf '%-40s %8s %8s\n' FUNCTION UNIT INTEGRATION
join -j 1 -a 1 -a 2 -o 0,1.2,2.2 -e '-' \
  <(go tool cover -func="$work/unit.out" | grep -v '^total:' | awk '{print $2, $3}' | sort) \
  <(go tool cover -func="$work/integration.out" | grep -v '^total:' | awk '{print $2, $3}' | sort) \
  | while read -r fn unit integ; do printf '%-40s %8s %8s\n' "$fn" "$unit" "$integ"; done

echo
go tool cover -func="$work/integration.out" | tail -1 | sed 's/^/integration /'
