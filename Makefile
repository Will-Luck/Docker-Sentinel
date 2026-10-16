VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BINARY  := sentinel
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)"

DEV_TAG     := dev-$(shell date +%Y%m%d-%H%M)
DEV_IMAGE   := docker-sentinel:$(DEV_TAG)
DEV_HOST    := user@192.0.2.10
DEV_SSH_KEY ?=
DEV_PORT    := 62850
DEV_CONTAINER := sentinel-test

GOLANGCI_LINT ?= golangci-lint

# Frontend bundling (esbuild)
ESBUILD     := go run github.com/evanw/esbuild/cmd/esbuild@v0.28.2
STATIC_DIR  := internal/web/static
JS_ENTRY    := $(STATIC_DIR)/src/js/main.js
JS_OUT      := $(STATIC_DIR)/app.js
CSS_ENTRY   := $(STATIC_DIR)/src/css/index.css
CSS_OUT     := $(STATIC_DIR)/style.css

.PHONY: build test test-ci lint docker clean proto dev-deploy frontend js css esbuild-warm

# Fetch and compile the pinned esbuild so a Docker layer can cache it.
esbuild-warm:
	$(ESBUILD) --version

js:
	$(ESBUILD) $(JS_ENTRY) --bundle --format=iife --sourcemap --outfile=$(JS_OUT)

css:
	$(ESBUILD) $(CSS_ENTRY) --bundle --sourcemap --external:/static/fonts/* --outfile=$(CSS_OUT)

frontend: js css

build: frontend
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/sentinel

test:
	go test -v -race -timeout 30m ./...

test-ci:
	go test -v -count=1 -timeout 30m ./...

lint:
	$(GOLANGCI_LINT) run ./...

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t docker-sentinel:$(VERSION) .

clean:
	rm -rf bin/

# Build the image, load it on DEV_HOST over SSH and (re)start the test
# container. DEV_HOST is a placeholder: point it at your own test machine.
# Pass the private key to use as a file path:
#   make dev-deploy DEV_HOST=me@test-box DEV_SSH_KEY=~/.ssh/id_ed25519
dev-deploy:
	$(if $(DEV_SSH_KEY),,$(error dev-deploy: set DEV_SSH_KEY to the path of the SSH private key for $(DEV_HOST)))
	@set -e; \
	DEV_SSH="ssh -i $(DEV_SSH_KEY) -o StrictHostKeyChecking=no $(DEV_HOST)"; \
	docker build --build-arg VERSION=$(DEV_TAG) --build-arg COMMIT=$(COMMIT) -t $(DEV_IMAGE) .; \
	docker save $(DEV_IMAGE) | $$DEV_SSH "docker load"; \
	$$DEV_SSH "docker stop $(DEV_CONTAINER) 2>/dev/null; docker rm $(DEV_CONTAINER) 2>/dev/null; \
		docker run -d --name $(DEV_CONTAINER) --network host \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v sentinel-data:/data \
		-e SENTINEL_WEB_PORT=$(DEV_PORT) \
		-e SENTINEL_CLUSTER=true \
		$(DEV_IMAGE)"; \
	echo "Deployed $(DEV_TAG) to $(DEV_HOST):$(DEV_PORT)"

PROTO_DIR := internal/cluster/proto

proto:
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/sentinel.proto
