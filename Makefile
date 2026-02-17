VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BINARY  := sentinel
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build test test-ci lint docker clean proto

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

PROTO_DIR := internal/cluster/proto

proto:
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/sentinel.proto
