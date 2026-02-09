FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /sentinel ./cmd/sentinel

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /sentinel /usr/local/bin/sentinel

VOLUME /data

ENV SENTINEL_DB_PATH=/data/sentinel.db
ENV SENTINEL_POLL_INTERVAL=6h
ENV SENTINEL_GRACE_PERIOD=30s
ENV SENTINEL_DEFAULT_POLICY=manual
ENV SENTINEL_LOG_JSON=true

LABEL sentinel.self=true

ENTRYPOINT ["sentinel"]
