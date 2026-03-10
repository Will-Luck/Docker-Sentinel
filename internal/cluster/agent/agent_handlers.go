package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// receiveLoop reads ServerMessages from the bidirectional stream and
// dispatches them to the appropriate handler. Each command is handled
// in its own goroutine to avoid blocking the receive loop; the dedup
// tracker prevents duplicate execution on reconnect/replay.
func (a *Agent) receiveLoop(ctx context.Context, stream proto.AgentService_ChannelClient) error {
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return fmt.Errorf("server closed stream")
		}
		if err != nil {
			return fmt.Errorf("receive: %w", err)
		}

		reqID := msg.GetRequestId()

		// Dedup: skip if we've already processed this request.
		if reqID != "" && a.dedup.isSeen(reqID) {
			a.log.Debug("skipping duplicate request", "request_id", reqID)
			continue
		}

		// Dispatch based on the oneof payload type.
		switch p := msg.Payload.(type) {
		case *proto.ServerMessage_Heartbeat:
			a.log.Debug("server heartbeat received")

		case *proto.ServerMessage_ListContainers:
			go a.safeHandle("list-containers", reqID, func() error {
				return a.handleListContainers(ctx, stream, reqID)
			})

		case *proto.ServerMessage_UpdateContainer:
			go a.safeHandle("update-container", reqID, func() error {
				return a.handleUpdateContainer(ctx, stream, p.UpdateContainer, reqID)
			})

		case *proto.ServerMessage_ContainerAction:
			go a.safeHandle("container-action", reqID, func() error {
				return a.handleContainerAction(ctx, stream, p.ContainerAction, reqID)
			})

		case *proto.ServerMessage_FetchLogs:
			go a.safeHandle("fetch-logs", reqID, func() error {
				return a.handleFetchLogs(ctx, stream, p.FetchLogs, reqID)
			})

		case *proto.ServerMessage_PullImage:
			go a.safeHandle("pull-image", reqID, func() error {
				return a.handlePullImage(ctx, p.PullImage)
			})

		case *proto.ServerMessage_RunHook:
			go a.safeHandle("run-hook", reqID, func() error {
				return a.handleRunHook(ctx, stream, p.RunHook, reqID)
			})

		case *proto.ServerMessage_Rollback:
			// Phase 5 — needs local snapshot store. Log and skip for now.
			a.log.Warn("rollback not yet implemented", "container", p.Rollback.GetContainerName())

		case *proto.ServerMessage_PolicySync:
			a.handlePolicySync(p.PolicySync)

		case *proto.ServerMessage_SettingsSync:
			a.handleSettingsSync(p.SettingsSync)

		case *proto.ServerMessage_CertRenewalResponse:
			go a.safeHandle("cert-renewal", reqID, func() error {
				return a.handleCertRenewal(p.CertRenewalResponse)
			})

		default:
			a.log.Warn("unknown server message type", "request_id", reqID)
		}
	}
}

// safeHandle wraps a command handler so that panics and errors are logged
// but never crash the agent. This is critical — one bad command must not
// take down the entire agent process.
func (a *Agent) safeHandle(op, reqID string, fn func() error) {
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("handler panic", "op", op, "request_id", reqID, "panic", r)
		}
	}()

	if err := fn(); err != nil {
		a.log.Error("handler failed", "op", op, "request_id", reqID, "error", err)
	}
}

// --- Command Handlers ---

// handleListContainers lists local containers and sends the result back
// to the server.
func (a *Agent) handleListContainers(ctx context.Context, stream proto.AgentService_ChannelClient, requestID string) error {
	a.log.Info("listing containers", "request_id", requestID)

	containers, err := a.listLocalContainers(ctx)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	a.mu.Lock()
	a.containerCount = len(containers)
	a.mu.Unlock()

	msg := &proto.AgentMessage{
		Payload: &proto.AgentMessage_ContainerList{
			ContainerList: &proto.ContainerList{
				RequestId:  requestID,
				Containers: containers,
			},
		},
	}
	return a.sendMsg(stream, msg)
}

// handleUpdateContainer executes the full update lifecycle for a container.
// For regular containers: inspect -> pull -> stop -> remove -> create -> start.
// For self-containers (sentinel.self=true): uses rename-before-replace to
// avoid killing the agent mid-update.
func (a *Agent) handleUpdateContainer(ctx context.Context, stream proto.AgentService_ChannelClient, req *proto.UpdateContainerRequest, requestID string) error {
	name := req.GetContainerName()
	targetImage := req.GetTargetImage()
	a.log.Info("updating container", "name", name, "target", targetImage, "request_id", requestID)

	isSelf := a.isSelfContainer(ctx, name)
	if isSelf {
		a.log.Info("detected self-container, using rename-before-replace", "name", name)
	}

	start := time.Now()
	var oldImage, oldDigest, newDigest string
	var oldContainerID string
	var err error
	if isSelf {
		sr, selfErr := a.selfUpdateContainer(ctx, name, targetImage)
		oldImage, oldDigest, newDigest = sr.oldImage, sr.oldDigest, sr.newDigest
		oldContainerID = sr.oldContainerID
		err = selfErr
	} else {
		oldImage, oldDigest, newDigest, err = a.recreateContainer(ctx, name, targetImage)
	}
	dur := time.Since(start)

	result := &proto.UpdateResult{
		RequestId:     requestID,
		ContainerName: name,
		OldImage:      oldImage,
		OldDigest:     oldDigest,
		NewImage:      targetImage,
		NewDigest:     newDigest,
		Duration:      durationpb.New(dur),
	}

	if err != nil {
		result.Outcome = "failed"
		result.Error = err.Error()
		a.log.Error("update failed", "name", name, "error", err, "duration", dur)
	} else {
		result.Outcome = "success"
		a.log.Info("update succeeded", "name", name, "old_image", oldImage, "new_digest", newDigest, "duration", dur)
	}

	// For self-updates: guarantee the old container is stopped to release
	// the BoltDB file lock, regardless of whether the gRPC send succeeds.
	// Uses context.Background() because ctx may already be cancelled if the
	// stream broke during send.
	if isSelf && err == nil && oldContainerID != "" {
		defer func() {
			a.log.Info("self-update: stopping old container to release DB lock", "id", truncateID(oldContainerID))
			if stopErr := a.docker.StopContainer(context.Background(), oldContainerID, 10); stopErr != nil {
				a.log.Error("self-update: failed to stop old container", "error", stopErr)
			}
		}()
	}

	// Push a fresh container list BEFORE the result so the server cache
	// reflects the updated image/digest when the SSE-triggered row fetch
	// arrives. gRPC stream messages are processed in order, so the cache
	// update will complete before handleUpdateResult fires the SSE event.
	_ = a.handleListContainers(ctx, stream, "")

	msg := &proto.AgentMessage{
		Payload: &proto.AgentMessage_UpdateResult{
			UpdateResult: result,
		},
	}
	return a.sendMsg(stream, msg)
}

// handleContainerAction executes a stop, start, or restart action on a
// named container and sends the result back to the server.
func (a *Agent) handleContainerAction(ctx context.Context, stream proto.AgentService_ChannelClient, req *proto.ContainerActionRequest, requestID string) error {
	name := req.GetContainerName()
	action := req.GetAction()
	a.log.Info("container action", "name", name, "action", action, "request_id", requestID)

	cID, err := a.findContainerID(ctx, name)
	if err == nil {
		switch action {
		case "stop":
			err = a.docker.StopContainer(ctx, cID, 10)
		case "start":
			err = a.docker.StartContainer(ctx, cID)
		case "restart":
			err = a.docker.RestartContainer(ctx, cID)
		default:
			err = fmt.Errorf("unknown action: %s", action)
		}
	}

	result := &proto.ContainerActionResult{
		RequestId:     requestID,
		ContainerName: name,
		Action:        action,
	}
	if err != nil {
		result.Outcome = "failed"
		result.Error = err.Error()
		a.log.Error("container action failed", "name", name, "action", action, "error", err)
	} else {
		result.Outcome = "success"
		a.log.Info("container action succeeded", "name", name, "action", action)
	}

	if err := a.sendMsg(stream, &proto.AgentMessage{
		Payload: &proto.AgentMessage_ContainerActionResult{ContainerActionResult: result},
	}); err != nil {
		return err
	}

	// Push a fresh container list so the server cache and UI reflect the
	// new state immediately (e.g. a stopped container stays visible).
	return a.handleListContainers(ctx, stream, "")
}

// handleFetchLogs fetches the last N lines of a container's logs and sends
// the result back to the server.
func (a *Agent) handleFetchLogs(ctx context.Context, stream proto.AgentService_ChannelClient, req *proto.FetchLogsRequest, requestID string) error {
	name := req.GetContainerName()
	lines := int(req.GetLines())
	if lines <= 0 {
		lines = 50
	}
	if lines > 500 {
		lines = 500
	}

	a.log.Info("fetching logs", "container", name, "lines", lines, "request_id", requestID)

	cID, err := a.findContainerID(ctx, name)
	if err != nil {
		return a.sendFetchLogsResult(stream, requestID, name, "", lines, err.Error())
	}

	output, err := a.docker.ContainerLogs(ctx, cID, lines)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}

	return a.sendFetchLogsResult(stream, requestID, name, output, lines, errStr)
}

// sendFetchLogsResult sends a FetchLogsResult message back to the server.
func (a *Agent) sendFetchLogsResult(stream proto.AgentService_ChannelClient, requestID, container, logs string, lines int, errStr string) error {
	msg := &proto.AgentMessage{
		Payload: &proto.AgentMessage_FetchLogsResult{
			FetchLogsResult: &proto.FetchLogsResult{
				RequestId:     requestID,
				ContainerName: container,
				Logs:          logs,
				Lines:         int32(lines), //nolint:gosec // clamped to 0-500 by caller
				Error:         errStr,
			},
		},
	}
	return a.sendMsg(stream, msg)
}

// handlePullImage pulls an image on the local Docker host.
func (a *Agent) handlePullImage(ctx context.Context, req *proto.PullImageRequest) error {
	ref := req.GetImageRef()
	a.log.Info("pulling image", "ref", ref)

	if err := a.docker.PullImage(ctx, ref); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}

	a.log.Info("pull complete", "ref", ref)
	return nil
}

// handleRunHook executes a command inside a container and sends the
// result back to the server.
func (a *Agent) handleRunHook(ctx context.Context, stream proto.AgentService_ChannelClient, req *proto.RunHookRequest, requestID string) error {
	name := req.GetContainerName()
	phase := req.GetPhase()
	cmd := req.GetCommand()
	timeout := int(req.GetTimeoutSeconds())
	if timeout <= 0 {
		timeout = 30
	}

	a.log.Info("running hook", "container", name, "phase", phase, "command", cmd, "request_id", requestID)

	// Find the container ID by name.
	cID, err := a.findContainerID(ctx, name)
	if err != nil {
		return a.sendHookResult(stream, requestID, name, phase, -1, "", err.Error())
	}

	// Split command string into args. Simple shell-like split — hooks
	// are expected to be simple commands, not complex shell scripts.
	args := strings.Fields(cmd)
	if len(args) == 0 {
		return a.sendHookResult(stream, requestID, name, phase, -1, "", "empty command")
	}

	exitCode, output, execErr := a.docker.ExecContainer(ctx, cID, args, timeout)

	errStr := ""
	if execErr != nil {
		errStr = execErr.Error()
	}

	// Clamp exit code to int32 range. In practice Docker exit codes
	// are 0-255, so this is purely defensive.
	code := clampInt32(exitCode)

	return a.sendHookResult(stream, requestID, name, phase, code, output, errStr)
}

// handleCertRenewal saves a newly signed certificate received from the
// server during cert rotation.
func (a *Agent) handleCertRenewal(resp *proto.CertRenewalResponse) error {
	certPEM := resp.GetAgentCert()
	if len(certPEM) == 0 {
		return fmt.Errorf("empty cert in renewal response")
	}

	a.log.Info("saving renewed certificate")
	if err := os.WriteFile(a.certPath, certPEM, 0600); err != nil {
		return fmt.Errorf("write renewed cert: %w", err)
	}

	return nil
}

// sendHookResult sends a HookResult message back to the server.
func (a *Agent) sendHookResult(stream proto.AgentService_ChannelClient, requestID, container, phase string, exitCode int32, output, errStr string) error {
	msg := &proto.AgentMessage{
		Payload: &proto.AgentMessage_HookResult{
			HookResult: &proto.HookResult{
				RequestId:     requestID,
				ContainerName: container,
				Phase:         phase,
				ExitCode:      exitCode,
				Output:        output,
				Error:         errStr,
			},
		},
	}
	return a.sendMsg(stream, msg)
}
