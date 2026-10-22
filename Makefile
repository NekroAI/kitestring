VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X github.com/NekroAI/kitestring/internal/version.Version=$(VERSION) -X github.com/NekroAI/kitestring/internal/version.Commit=$(COMMIT)

.PHONY: build test vet clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ks ./cmd/ks

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin dist
