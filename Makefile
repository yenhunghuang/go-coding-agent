GO ?= go

.PHONY: all build test race vet fmt fmt-check cover run clean

all: fmt-check vet test build

build:
	mkdir -p build
	$(GO) build -o build/agent ./cmd/agent

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -s -w .

fmt-check:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

run:
	$(GO) run ./cmd/agent

clean:
	rm -rf build coverage.out
