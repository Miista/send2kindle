# join <(...) needs a shell with process substitution; make defaults to sh.
SHELL := /bin/bash
BINARY    := send2kindle

# Coverage output lives under $(HOME), not /tmp: the integration suite runs
# the binary in a container writing to a bind mount, and on macOS Docker
# Desktop only shares certain host paths -- /tmp is not one of them.
COVER_DIR := $(HOME)/.cache/send2kindle-cover

.PHONY: all build image test test-unit test-integration cover cover-html clean

all: build

build:
	go build -trimpath -o $(BINARY) .

# The image the Dockerfile ships, built locally. The integration suite builds
# this same file rather than one of its own: a suite that builds its own image
# tests an artifact nobody ships, and the two drift.
image:
	docker build -t $(BINARY):dev .

# --- tests ---------------------------------------------------------------
# test-unit needs nothing but Go. test-integration needs docker, and runs
# everything locally: its own image, its own SMTP server. Nothing upstream is
# contacted, so it works offline -- and no scenario can fail because a mail
# provider was slow.
#
# Only test-integration passes -count=1. Go caches a passing result and
# replays it when nothing it can see has changed, which is right for pure code
# and wrong for these: they depend on a daemon, on images and on containers,
# none of which the cache knows about, so a cached pass would say the world was
# fine when only the source was unchanged.

test: test-unit test-integration

test-unit:
	go test -shuffle=on .

# -p 1 keeps packages sequential: the scenarios share one testbed directory
# and one compose project name.
#
# -shuffle=on because they share that testbed and one daemon, so a test can
# pass on what a previous one left behind. Go prints the seed, and
# -shuffle=<seed> replays a failure exactly.
test-integration:
	go test -tags integration -count=1 -p 1 -shuffle=on ./test/...

# --- coverage ------------------------------------------------------------
# Reported per suite, not merged.
#
# Merging is what the other repos do, and it does not work here: the unit
# binary and the container binary are different builds with different coverage
# meta hashes, so `go tool covdata merge` reports a weighted average rather
# than a union -- a "merged" figure that came out LOWER than the integration
# suite alone, with handle at 81% where the unit tests alone reach 90%. A
# single number that loses coverage is worse than two honest ones.
#
# What each is for:
#
#   unit         the decisions -- which files are sent, what is recorded, how
#                a mode changes the outcome, what gets announced. No docker.
#   integration  the process boundary -- the scan loop, and the SMTP
#                conversation. A unit test cannot reach these without mocking
#                net/smtp, which tests the mock.
#
# Together they cover every function; separately they say which suite to look
# at when something regresses.
cover:
	@rm -rf $(COVER_DIR) && mkdir -p $(COVER_DIR)/integration
	@echo "== unit"
	@go test . -coverprofile=$(COVER_DIR)/unit.txt -covermode=set >/dev/null
	@go tool cover -func=$(COVER_DIR)/unit.txt | tail -1
	@echo
	@echo "== integration (instrumented image)"
	@GOCOVERDIR=$(COVER_DIR)/integration go test -tags integration -count=1 -p 1 ./test/... >/dev/null
	@go tool covdata textfmt -i=$(COVER_DIR)/integration -o=$(COVER_DIR)/integration.txt
	@go tool cover -func=$(COVER_DIR)/integration.txt | tail -1
	@echo
	@echo "== per function, by suite"
	@printf '%-40s %8s %8s\n' FUNCTION UNIT INTEGRATION
	@join -j 1 -a 1 -a 2 -o 0,1.2,2.2 -e '-' \
	  <(go tool cover -func=$(COVER_DIR)/unit.txt        | grep -v '^total:' | awk '{print $$2, $$3}' | sort) \
	  <(go tool cover -func=$(COVER_DIR)/integration.txt | grep -v '^total:' | awk '{print $$2, $$3}' | sort) \
	  | while read -r fn unit integ; do printf '%-40s %8s %8s\n' "$$fn" "$$unit" "$$integ"; done

cover-html: cover
	go tool cover -html=$(COVER_DIR)/unit.txt

clean:
	rm -f $(BINARY)
	rm -rf $(COVER_DIR) .coverage testbed
