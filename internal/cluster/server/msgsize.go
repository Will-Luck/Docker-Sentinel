package server

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

// oversizeLogger logs which agent sent a message that gRPC refused for
// being larger than maxMessageSize. gRPC itself only tells the agent.
type oversizeLogger struct {
	log *slog.Logger
}

func (o *oversizeLogger) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}

func (o *oversizeLogger) HandleRPC(ctx context.Context, s stats.RPCStats) {
	end, ok := s.(*stats.End)
	if !ok || end.IsClient() || status.Code(end.Error) != codes.ResourceExhausted {
		return
	}
	hostID, err := extractHostID(ctx)
	if err != nil {
		hostID = "unknown"
	}
	o.log.Error("agent message larger than the cluster limit was refused; the host's container list is too big",
		"hostID", hostID, "limit_mb", maxMessageSize>>20, "error", end.Error)
}

func (o *oversizeLogger) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (o *oversizeLogger) HandleConn(context.Context, stats.ConnStats) {}
