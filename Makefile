.PHONY: build test check dist

VERSION ?= dev

build:
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$(VERSION)" -o bin/sertun ./cmd/sertun

test:
	go test ./...

check:
	go vet ./...
	go test -race ./...

dist:
	sh scripts/dist.sh "$(VERSION)"
