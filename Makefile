BINARY=kaiju
# Windows will not run a file with no extension, so the native build has to
# name one. The cross targets below already spell their own out, and this is
# empty everywhere else, so $(BINARY)$(EXE) is the right name on all three.
ifeq ($(OS),Windows_NT)
EXE=.exe
# Run from PowerShell or cmd, make finds no sh and falls back to cmd.exe, which
# has no tr, touch or /dev/null; Git for Windows' sh and the tools beside it in
# usr/bin bring all three. make runs a plain line such as touch itself, without
# the shell, so usr/bin goes on PATH as well.
GIT_SH:=$(wildcard C:/Program\ Files/Git/bin/sh.exe)
ifneq ($(GIT_SH),)
SHELL:=C:/Program Files/Git/bin/sh.exe
export PATH:=C:\Program Files\Git\usr\bin;$(PATH)
endif
endif
# The engine's number comes from the VERSION file, not from this line. See
# VERSIONING.md. COMMIT is which build of that release this is, and carries
# -dirty when the checkout held edits nobody committed.
VERSION?=$(shell tr -d '[:space:]' < VERSION 2>/dev/null)
COMMIT?=$(shell git describe --always --dirty 2>/dev/null || echo unknown)
ifeq ($(strip $(VERSION)),)
$(error VERSION is empty or the VERSION file is missing — a binary stamped "+$(COMMIT)" says nothing about which release it is)
endif
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)+$(COMMIT)"

.PHONY: build build-noui build-linux build-darwin build-windows clean test test-noui web

web:
	cd web && npm install && npm run build

build: web
	touch internal/gateway/embed.go
	go build $(LDFLAGS) -o $(BINARY)$(EXE) ./cmd/kaiju

# The same binary without the interface compiled into it.
#
# No dependency on `web` and none on npm: -tags noui selects
# internal/gateway/embed_noui.go, which has no go:embed, so the built assets
# under internal/gateway/web need not exist. That directory is gitignored, so a
# fresh clone can make this target and cannot make the one above.
#
# Writes over the same $(BINARY). The two are variants of one program, not two
# programs, and a build that served the interface yesterday should not still be
# sitting there after this one runs.
build-noui:
	go build $(LDFLAGS) -tags noui -o $(BINARY)$(EXE) ./cmd/kaiju

build-linux: web
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY)-linux-amd64 ./cmd/kaiju
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BINARY)-linux-arm64 ./cmd/kaiju

build-darwin: web
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY)-darwin-amd64 ./cmd/kaiju
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BINARY)-darwin-arm64 ./cmd/kaiju

build-windows: web
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY)-windows-amd64.exe ./cmd/kaiju

all: build-linux build-darwin build-windows

clean:
	rm -f $(BINARY) $(BINARY)$(EXE) $(BINARY)-*

test:
	go test ./...

# The files behind -tags noui, which `test` cannot reach: a build tag hides them
# from `go test ./...`, so without this target they are compiled by the build
# and checked by nothing.
test-noui:
	go test -tags noui ./...

