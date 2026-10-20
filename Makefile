BINARY  := formance2blnk
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none) -X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: all build test race cover lint fmt vet check docs docs-serve release-check snapshot clean

all: check build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test ./...

race:
	go test -race -count=1 ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

fmt:
	gofmt -s -w .

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null && golangci-lint run || echo "golangci-lint not installed, skipping"

check: vet lint race
	@test -z "$$(gofmt -s -l .)" || (echo "run make fmt" && gofmt -s -l . && exit 1)

docs:
	mkdocs build --strict
	touch docs/.nojekyll

docs-serve:
	mkdocs serve

release-check:
	goreleaser check

snapshot:
	goreleaser release --snapshot --clean --skip=publish

clean:
	rm -rf bin dist site coverage.out
