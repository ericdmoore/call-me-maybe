# Call Me Maybe — build and deploy
#
# The deliverable is one static binary. Build it anywhere, scp it to the Pi.

BIN     := bin/doorman

# Stamped into the binary so `doorman version` matches the release it came
# from: "0.4.1" on a tag, "0.4.1-3-gabc1234-dirty" three commits past one.
# Deliberately no --always: with no tags in the tree this stays empty and the
# stamp is skipped, so the binary reports main.go's own default rather than a
# bare sha. One source of truth for the version, and it lives in the code.
VERSION := $(shell git describe --tags --dirty 2>/dev/null | sed 's/^v//')
ifeq ($(strip $(VERSION)),)
GOFLAGS := -trimpath -ldflags '-s -w'
else
GOFLAGS := -trimpath -ldflags '-s -w -X main.version=$(VERSION)'
endif

.PHONY: build test vet check cross run clean fmt fmt-check cover hooks release-tag lint lint-test man schema

build:
	go build $(GOFLAGS) -o $(BIN) ./cmd/doorman

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

## fmt-check: what CI and the pre-push hook enforce
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; \
		echo "run: make fmt"; exit 1; \
	fi

## lint: the static analysis `go vet` does not do. vet already runs 35
## analyzers from x/tools (lostcancel, copylocks, waitgroup, testinggoroutine
## — the ones that matter for goroutine-per-call code). This adds nilness,
## which needs SSA and so is not in vet's default set, plus nologsecrets: the
## house rule that caller IDs and PINs never reach the logs. It lives in the
## tools/ submodule so x/tools never enters the daemon's dependency graph.
lint:
	@cd tools && go build -o ../bin/cmmlint ./cmd/cmmlint
	@./bin/cmmlint ./... && echo "✓ cmmlint clean"

## lint-test: the analyzer's own tests
lint-test:
	cd tools && go test ./...

## man: read the man page straight from the working tree
man:
	@man ./docs/doorman.1

## schema: the config surface as JSON Schema (see also llms.txt)
schema: build
	@./$(BIN) schema

## site-assets: generate everything the site serves to models
##   Generated, not copied: llms.txt drifted from site/public once already and
##   only CI caught it, after the push.
.PHONY: site-assets
site-assets: build
	@mkdir -p site/public/schema
	@cp llms.txt site/public/llms.txt
	@cp llms-policy.txt site/public/llms-policy.txt
	@for n in policy handsets env; do \
		./bin/doorman schema $$n > site/public/schema/$$n.json; \
	done
	@echo "✓ site/public: llms.txt, llms-policy.txt, schema/{policy,handsets,env}.json"

## check: everything that must be green before a commit
check: fmt-check vet lint test build

## cover: tests with -race plus the per-package coverage floors
cover:
	bash scripts/coverage.sh

## hooks: install the versioned pre-push gate (fmt, vet, tests, secret scan)
hooks:
	@chmod +x .githooks/*
	git config core.hooksPath .githooks
	@echo "✓ pre-push hook active (bypass once with: git push --no-verify)"

## cross: binaries for the Pi. arm64 for 64-bit Pi OS (uname -m = aarch64),
## armv7 for the 32-bit userland that many Pi installs still run (armv7l).
cross:
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o bin/doorman-linux-arm64 ./cmd/doorman
	GOOS=linux GOARCH=arm GOARM=7 go build $(GOFLAGS) -o bin/doorman-linux-armv7 ./cmd/doorman

## release-tag: tag and push, which triggers the release workflow and
## publishes binaries + checksums. Usage: make release-tag TAG=0.5.0
release-tag:
	@test -n "$(TAG)" || { echo "usage: make release-tag TAG=0.5.0"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "refusing to tag a dirty tree"; exit 1; }
	@$(MAKE) --no-print-directory check
	git tag -a "v$(TAG)" -m "v$(TAG)"
	git push origin "v$(TAG)"
	@echo "✓ pushed v$(TAG) — watch it with: gh run watch"

## run: local dev with .env sourced (fails fast without a reachable Asterisk,
## which is correct behaviour, not a bug)
run:
	@set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/doorman

clean:
	rm -rf bin coverage.out
