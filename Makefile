# Call Me Maybe — build and deploy
#
# The deliverable is one static binary. Build it anywhere, scp it to the Pi.

BIN     := bin/doorman
GOFLAGS := -trimpath -ldflags '-s -w'

.PHONY: build test vet check cross run clean

build:
	go build $(GOFLAGS) -o $(BIN) ./cmd/doorman

test:
	go test ./...

vet:
	go vet ./...

## check: everything that must be green before a commit
check: vet test build

## cross: binaries for the Pi. arm64 for 64-bit Pi OS (uname -m = aarch64),
## armv7 for the 32-bit userland that many Pi installs still run (armv7l).
cross:
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o bin/doorman-linux-arm64 ./cmd/doorman
	GOOS=linux GOARCH=arm GOARM=7 go build $(GOFLAGS) -o bin/doorman-linux-armv7 ./cmd/doorman

## run: local dev with .env sourced (fails fast without a reachable Asterisk,
## which is correct behaviour, not a bug)
run:
	@set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/doorman

clean:
	rm -rf bin
