FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git make

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
RUN go install github.com/evanw/esbuild/cmd/esbuild@latest

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
RUN make frontend
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /sentinel ./cmd/sentinel

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /sentinel /usr/local/bin/sentinel

VOLUME /data

ENV SENTINEL_DB_PATH=/data/sentinel.db
ENV SENTINEL_POLL_INTERVAL=6h
ENV SENTINEL_GRACE_PERIOD=30s
ENV SENTINEL_DEFAULT_POLICY=manual
ENV SENTINEL_LOG_JSON=true
ENV SENTINEL_WEB_ENABLED=true
ENV SENTINEL_WEB_PORT=8080

EXPOSE 8080

LABEL sentinel.self=true

ENTRYPOINT ["sentinel"]
