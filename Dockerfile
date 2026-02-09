FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /sentinel ./cmd/sentinel

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /sentinel /usr/local/bin/sentinel

ENTRYPOINT ["sentinel"]
