FROM golang:1.27-alpine AS builder

# The golang image sets GOTOOLCHAIN=local. With auto, a cached base image
# older than go.mod's toolchain line fetches that release instead.
ENV GOTOOLCHAIN=auto

RUN apk add --no-cache git make

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY Makefile ./
RUN make esbuild-warm

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
RUN make frontend
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /sentinel ./cmd/sentinel

FROM alpine:3.24

ARG VERSION=dev
ARG BUILD_DATE=unknown
ARG COMMIT=unknown

LABEL org.opencontainers.image.source="https://github.com/Will-Luck/Docker-Sentinel" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.title="docker-sentinel" \
      org.opencontainers.image.description="Container update orchestrator with per-container policies and automatic rollback" \
      sentinel.self=true

RUN apk upgrade --no-cache && apk add --no-cache ca-certificates tzdata

COPY --from=builder /sentinel /usr/local/bin/sentinel

VOLUME /data

ENV SENTINEL_DB_PATH=/data/sentinel.db \
    SENTINEL_POLL_INTERVAL=6h \
    SENTINEL_GRACE_PERIOD=30s \
    SENTINEL_DEFAULT_POLICY=manual \
    SENTINEL_LOG_JSON=true \
    SENTINEL_WEB_ENABLED=true \
    SENTINEL_WEB_PORT=8080

EXPOSE 8080

ENTRYPOINT ["sentinel"]
