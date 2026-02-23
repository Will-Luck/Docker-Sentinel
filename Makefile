VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BINARY  := sentinel
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)"

DEV_TAG     := dev-$(shell date +%Y%m%d-%H%M)
DEV_IMAGE   := docker-sentinel:$(DEV_TAG)
DEV_HOST    := test1@192.168.1.60
DEV_PORT    := 62850
DEV_CONTAINER := sentinel-test
DEV_SSH_KEY := $(shell mktemp)
DEV_SSH     := ssh -i $(DEV_SSH_KEY) -o StrictHostKeyChecking=no $(DEV_HOST)

.PHONY: build test test-ci lint docker clean proto dev-deploy

build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/sentinel

test:
	go test -v -race ./...

test-ci:
	go test -v -count=1 ./...

lint:
	golangci-lint run ./...

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t docker-sentinel:$(VERSION) .

clean:
	rm -rf bin/

dev-deploy: docker
	docker tag docker-sentinel:$(VERSION) $(DEV_IMAGE)
	op read "op://Server-Keys/ssh-test-server-1/s7ela6vsq6eltvuj7g3orn4jd4" | sed 's/^concealed]=//' > $(DEV_SSH_KEY) && chmod 600 $(DEV_SSH_KEY)
	docker save $(DEV_IMAGE) | $(DEV_SSH) "docker load"
	$(DEV_SSH) "docker stop $(DEV_CONTAINER) 2>/dev/null; docker rm $(DEV_CONTAINER) 2>/dev/null; \
		docker run -d --name $(DEV_CONTAINER) --network host \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v sentinel-data:/data \
		-e SENTINEL_WEB_PORT=$(DEV_PORT) \
		-e SENTINEL_CLUSTER=true \
		$(DEV_IMAGE)"
	@rm -f $(DEV_SSH_KEY)
	@echo "Deployed $(DEV_TAG) to $(DEV_HOST):$(DEV_PORT)"

PROTO_DIR := internal/cluster/proto

proto:
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/sentinel.proto
