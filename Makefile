# SPDX-License-Identifier: Apache-2.0

BIN      := bin/tv-ad-detect
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X main.commit=$(COMMIT) -X main.buildDate=$(DATE)

.PHONY: build test vet fmt check fixtures bench clean

# Requires the FFmpeg development libraries (libavformat, libavcodec,
# libavutil) discoverable through pkg-config.
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w cmd pkg

check: vet test
	@test -z "$$(gofmt -l cmd pkg)" || (echo "gofmt needed:"; gofmt -l cmd pkg; exit 1)

# Media fixtures for the adapter integration tests are synthesized with the
# ffmpeg command-line tool (test tooling only; the product never runs it).
fixtures:
	./scripts/make-fixtures.sh

bench:
	go test -run '^$$' -bench . -benchmem ./pkg/...

clean:
	rm -f $(BIN)
