VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BINARY  := sentinel
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build test lint docker clean

build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/sentinel

test:
	go test -v -race ./...

test-ci:
	go test -v -count=1 ./...

lint:
	golangci-lint run ./...

docker:
	docker build -t docker-sentinel:$(VERSION) .

clean:
	rm -rf bin/
