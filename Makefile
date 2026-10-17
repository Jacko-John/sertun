.PHONY: build test check dist

VERSION ?= dev

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/sertun ./cmd/sertun

test:
	go test ./...

check:
	go vet ./...
	go test -race ./...

dist:
	@mkdir -p dist
	@set -eu; for os in linux windows darwin; do \
		for arch in amd64 arm64; do \
			ext=""; if [ "$$os" = windows ]; then ext=".exe"; fi; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
				-ldflags "-s -w -X main.version=$(VERSION)" \
				-o dist/sertun-$$os-$$arch$$ext ./cmd/sertun; \
		done; \
	done
