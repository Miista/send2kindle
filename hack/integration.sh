#!/usr/bin/env bash
# Runs the integration suite: builds the shipped image and a fake SMTP server,
# then drives send2kindle against them in docker.
#
# Separate from `go test ./...` behind a build tag, because these need a docker
# daemon and take ~25s where the unit tests take under a second.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
exec go test -tags integration ./test/integration/ "$@"
