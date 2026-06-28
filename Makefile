BINARY  := sff

# Version metadata injected into main at link time. Override on the command line,
# e.g. `make build VERSION=v1.2.3`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

.PHONY: build install test vet fmt clean

## build: compile the sff binary into the working directory
build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) .

## install: install sff into $GOBIN (or $GOPATH/bin) with version metadata
install:
	go install -ldflags '$(LDFLAGS)' .

## test: run the test suite
test:
	go test ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format all Go sources
fmt:
	gofmt -w .

## clean: remove the built binary
clean:
	rm -f $(BINARY)
