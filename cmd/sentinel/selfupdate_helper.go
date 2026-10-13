package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/selfupdate"
)

// runSelfUpdateHelper is the hidden "self-update-helper" subcommand. A
// running Sentinel starts it in a short-lived container created from its own
// image (so it exists whatever the target image is) with the Docker socket
// mounted: it stops the old container, starts the
// new one, waits for it to settle and puts the old one back if it does not.
// The exit code is 0 unless the swap failed and could not be undone.
func runSelfUpdateHelper() int {
	log := logging.New(true)

	var spec selfupdate.Spec
	if err := json.Unmarshal([]byte(os.Getenv(selfupdate.SpecEnv)), &spec); err != nil {
		fmt.Fprintf(os.Stderr, "self-update helper: no usable spec in %s: %v\n", selfupdate.SpecEnv, err)
		return 2
	}

	client, err := docker.NewClient(selfupdate.DefaultSocket, nil)
	if err != nil {
		log.Error("self-update helper: cannot reach the Docker daemon", "error", err)
		return 1
	}
	defer client.Close()

	// The swap must finish even if this container is signalled: it is the only
	// thing that can put the old instance back.
	signal.Ignore(syscall.SIGTERM, syscall.SIGINT)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	out := selfupdate.Runner{Docker: client, Log: log}.Run(ctx, spec)
	log.Info("self-update helper finished", "result", out.Result, "error", out.Error)
	if out.Result == selfupdate.ResultFailed {
		return 1
	}
	return 0
}
