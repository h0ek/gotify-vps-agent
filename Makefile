SHELL := /usr/bin/env bash
PROGRAM := gotify-vps-agent
MODULE := github.com/h0ek/gotify-vps-agent
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= $(shell git show -s --format=%cI HEAD 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT) -X $(MODULE)/internal/version.Date=$(BUILD_DATE)

.PHONY: all fmt fmt-check vet test test-race fuzz security shellcheck build build-release release-local clean

all: fmt-check shellcheck vet test test-race build

fmt:
	gofmt -w $$(find . -name '*.go' -type f)
	shfmt -w scripts/*.sh

fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -type f))" || { gofmt -l $$(find . -name '*.go' -type f); exit 1; }
	shfmt -d scripts/*.sh

shellcheck:
	bash -n scripts/*.sh
	shellcheck scripts/*.sh

vet:
	go vet ./...

test:
	go test -count=1 ./...

test-race:
	go test -race -count=1 ./...

fuzz:
	go test ./internal/config -run=^$$ -fuzz=FuzzUnmarshal -fuzztime=10s
	go test ./internal/gotify -run=^$$ -fuzz=FuzzValidateURL -fuzztime=10s

security:
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

build:
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -buildvcs=true -ldflags '$(LDFLAGS)' -o dist/$(PROGRAM) ./cmd/$(PROGRAM)

build-release:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=true -ldflags '$(LDFLAGS)' -o dist/$(PROGRAM)_linux_amd64 ./cmd/$(PROGRAM)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=true -ldflags '$(LDFLAGS)' -o dist/$(PROGRAM)_linux_arm64 ./cmd/$(PROGRAM)

release-local: build-release
	./scripts/package-release.sh '$(VERSION)'

clean:
	rm -rf dist
