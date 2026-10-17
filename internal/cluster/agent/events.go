package agent

import (
	"context"
	"errors"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
)

// containerEventSource is a Docker client that streams container events, as
// docker.Client does. It is kept out of DockerAPI, as the docker package
// keeps its optional interfaces out of docker.API, so the test doubles of
// the client need not implement it. With a client that lacks it the agent
// sends its container list only on connect, when the hub asks, and every
// listRefreshEvery heartbeats.
type containerEventSource interface {
	ContainerEvents(ctx context.Context) (<-chan docker.ContainerEvent, <-chan error)
}

var _ containerEventSource = (*docker.Client)(nil)

// After container events the agent sends its container list, so a change
// made on the host outside Sentinel (docker rm, a compose up, a crash)
// reaches the hub in seconds. Variables so tests can shorten them.
var (
	// eventQuiet is how long the event stream must stay quiet before the
	// list goes out: 2 seconds. One change gives several events in a row
	// (a compose up creates and starts a whole stack, a recreate is a
	// create, a die, a start and a destroy), and they go out in one list.
	eventQuiet = 2 * time.Second
	// eventMinGap is the least time from one list of a subscription to its
	// next: 10 seconds. Containers that each run a few seconds (CI jobs,
	// backups) give events just over eventQuiet apart, which would
	// otherwise send a list every 2 or 3 seconds, each one a container
	// list and an image inspect per container on the daemon, and each one
	// that adds or removes a name reloads every open dashboard on the hub.
	// With the gap a subscription sends at most six lists a minute, and a
	// change soon after a list still reaches the hub within 10 seconds of
	// it.
	eventMinGap = 10 * time.Second
	// eventMaxWait bounds the wait for the stream to go quiet, and the
	// minimum gap: the list goes out at the latest 30 seconds after the
	// first event it will report, or when the update ends if Sentinel is
	// updating a container on this host. A host whose containers never stop
	// changing (a CI runner) still reports each change within 30 seconds.
	eventMaxWait = 30 * time.Second
	// eventRetry is the wait before subscribing again after the event
	// stream failed (the daemon restarted or went away), doubling with
	// each failure in a row up to eventRetryMax: 1 second, then up to 30
	// seconds, the same as the agent's reconnects to the hub.
	eventRetry    = time.Second
	eventRetryMax = 30 * time.Second
	// eventHealthy is how long a subscription must stay up to count as
	// working: 1 minute. The back-off starts again from eventRetry after
	// one that worked, and its end is not a warning (see
	// watchContainerEvents).
	eventHealthy = time.Minute
)

// watchContainerEvents sends the container list (send) after container
// events, until ctx ends: once the stream has been quiet for eventQuiet, but
// not sooner than eventMinGap after the subscription's last list, and at the
// latest eventMaxWait after the first event the list reports. Each
// subscription, the first included, sends one list of its own, for what
// changed while the agent was not subscribed: the session's first list was
// taken before the first subscription, and a failed stream missed events.
// The minimum gap does not hold that list back: subscriptions are already
// spaced by the back-off. A failed stream is subscribed to again after the
// back-off. Nothing here ends the session: the heartbeat resend still
// covers a host whose daemon gives no events.
//
// A stream that ends after it worked (stayed up eventHealthy) is logged at
// Debug: a socket proxy that cuts idle connections ends one like that, as
// does a daemon restart, and the agent subscribes again after eventRetry,
// the back-off started over. A stream that fails before it works is a
// warning the first time, and Debug while it keeps failing (a daemon or
// proxy that refuses /events would otherwise warn every back-off period for
// as long as the agent runs); one Info record says when a stream works
// again.
func (a *Agent) watchContainerEvents(ctx context.Context, src containerEventSource, send func(context.Context) error) {
	bo := &backoff{base: eventRetry, maxDelay: eventRetryMax}
	warned := false
	for {
		worked, err := a.followContainerEvents(ctx, src, send, warned)
		if ctx.Err() != nil {
			return
		}
		if worked {
			warned = false
			bo.reset()
		}
		wait := bo.next()
		if worked || warned {
			a.log.Debug("the Docker event stream ended, subscribing again", "error", err, "backoff", wait)
		} else {
			warned = true
			a.log.Warn("the Docker event stream failed, subscribing again (until it works, further failures are logged at debug level)",
				"error", err, "backoff", wait)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// followContainerEvents runs one subscription to the container events (see
// watchContainerEvents). It returns whether the stream worked (stayed up
// eventHealthy), and the stream's error when the stream fails, or ctx's
// when ctx ends. If warned, the agent has warned of a failed stream, and
// this one logs it when it works.
func (a *Agent) followContainerEvents(ctx context.Context, src containerEventSource, send func(context.Context) error, warned bool) (bool, error) {
	evts, errs := src.ContainerEvents(ctx)
	worked := false

	// The subscription's own list is due as for an event now.
	first := time.Now()
	var sent time.Time // when the last list went out, if one did
	timer := time.NewTimer(eventQuiet)
	defer timer.Stop()
	healthy := time.NewTimer(eventHealthy)
	defer healthy.Stop()
	pending := true
	for {
		select {
		case <-ctx.Done():
			return worked, ctx.Err()
		case err := <-errs:
			return worked, err
		case <-healthy.C:
			worked = true
			if warned {
				a.log.Info("the Docker event stream works again")
			}
		case ev, ok := <-evts:
			if !ok {
				return worked, errors.New("the Docker event stream closed")
			}
			a.log.Debug("container event", "action", ev.Action, "name", ev.Name)
			now := time.Now()
			if !pending {
				pending, first = true, now
			}
			due := now.Add(eventQuiet)
			if gap := sent.Add(eventMinGap); due.Before(gap) {
				due = gap
			}
			if limit := first.Add(eventMaxWait); limit.Before(due) {
				due = limit
			}
			timer.Reset(due.Sub(now))
		case <-timer.C:
			if a.updatesRunning() {
				// An update on this host sends its own list when it ends.
				// Wait for that, rather than report each step of its swap.
				timer.Reset(eventQuiet)
				continue
			}
			pending = false
			if err := send(ctx); err != nil && ctx.Err() == nil {
				a.log.Warn("could not send the container list after container events", "error", err)
			}
			sent = time.Now()
		}
	}
}
