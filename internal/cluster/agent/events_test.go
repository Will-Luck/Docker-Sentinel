package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
)

// fakeEvents is a container event source whose subscriptions a test drives:
// it sends events and fails streams through the channels of each.
type fakeEvents struct {
	subs chan *eventSub
	// block makes a subscription wait for its context to end, as one to a
	// daemon that does not answer does.
	block bool
	// handOff makes the channels of each subscription unbuffered, so a
	// send on them returns only once the loop has taken the value. Not
	// with prime, which would block on them.
	handOff bool
	// prime, if set, fills each subscription (numbered from 0) before it
	// is returned, so its events (or its failure) are there at once.
	prime func(n int, s *eventSub)
	n     int // subscriptions so far
}

type eventSub struct {
	ctx  context.Context // what the loop subscribed with
	evts chan docker.ContainerEvent
	errs chan error
}

func newFakeEvents() *fakeEvents { return &fakeEvents{subs: make(chan *eventSub, 16)} }

func (f *fakeEvents) ContainerEvents(ctx context.Context) (<-chan docker.ContainerEvent, <-chan error) {
	s := &eventSub{ctx: ctx, evts: make(chan docker.ContainerEvent, 64), errs: make(chan error, 1)}
	if f.handOff {
		s.evts, s.errs = make(chan docker.ContainerEvent), make(chan error)
	}
	if f.prime != nil {
		f.prime(f.n, s)
	}
	f.n++
	select {
	case f.subs <- s:
	case <-ctx.Done():
	}
	if f.block {
		<-ctx.Done()
		s.errs <- ctx.Err()
	}
	return s.evts, s.errs
}

// next returns the next subscription.
func (f *fakeEvents) next(t *testing.T) *eventSub {
	t.Helper()
	select {
	case s := <-f.subs:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("no subscription to the container events")
		return nil
	}
}

// sendCounter is the list send of the events loop: it records when each
// call came.
type sendCounter struct{ calls chan time.Time }

func newSendCounter() *sendCounter { return &sendCounter{calls: make(chan time.Time, 64)} }

func (c *sendCounter) send(ctx context.Context) error {
	select {
	case c.calls <- time.Now():
	case <-ctx.Done():
	}
	return nil
}

// wait waits for n more sends, and returns when the last of them came.
func (c *sendCounter) wait(t *testing.T, n int) time.Time {
	t.Helper()
	var at time.Time
	for i := range n {
		select {
		case at = <-c.calls:
		case <-time.After(10 * time.Second):
			t.Fatalf("got %d of %d lists", i, n)
		}
	}
	return at
}

// none checks that nothing is sent for d.
func (c *sendCounter) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-c.calls:
		t.Fatal("a list more than wanted was sent")
	case <-time.After(d):
	}
}

// shortEventTimes shortens the events loop's timings for one test. It
// turns the minimum gap off and makes a subscription count as working only
// after an hour; a test that needs either sets eventMinGap or eventHealthy
// after it, and they are put back with the others.
func shortEventTimes(t *testing.T, quiet, maxWait, retry time.Duration) {
	t.Helper()
	q, g, m, r, rm, h := eventQuiet, eventMinGap, eventMaxWait, eventRetry, eventRetryMax, eventHealthy
	t.Cleanup(func() {
		eventQuiet, eventMinGap, eventMaxWait, eventRetry, eventRetryMax, eventHealthy = q, g, m, r, rm, h
	})
	eventQuiet, eventMinGap, eventMaxWait, eventRetry, eventRetryMax, eventHealthy = quiet, 0, maxWait, retry, retry, time.Hour
}

// watchEvents runs the events loop of a until the test ends, and returns
// what stops it and a channel closed once it has returned.
func watchEvents(t *testing.T, a *Agent, src containerEventSource, send func(context.Context) error) (stop func(), ended <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.watchContainerEvents(ctx, src, send)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return cancel, done
}

// logRecords is a log handler that passes each record to the test. It
// drops a record the test has no room for rather than hold up the loop.
type logRecords chan slog.Record

func (l logRecords) Enabled(context.Context, slog.Level) bool { return true }

func (l logRecords) Handle(_ context.Context, r slog.Record) error {
	select {
	case l <- r:
	default:
	}
	return nil
}

func (l logRecords) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l logRecords) WithGroup(string) slog.Handler      { return l }

// streamLevels returns the levels of the next n records about the event
// stream.
func (l logRecords) streamLevels(t *testing.T, n int) []slog.Level {
	t.Helper()
	var got []slog.Level
	deadline := time.After(10 * time.Second)
	for len(got) < n {
		select {
		case r := <-l:
			if strings.Contains(r.Message, "Docker event stream") {
				got = append(got, r.Level)
			}
		case <-deadline:
			t.Fatalf("got %d of %d log records about the event stream: %v", len(got), n, got)
		}
	}
	return got
}

// One change on a host gives several events in a row (a compose up creates
// and starts a whole stack). They go out in one list, not one each.
func TestWatchContainerEvents_BurstSendsOneList(t *testing.T) {
	// The quiet period is long next to the burst, which takes microseconds,
	// so a slow runner cannot split it.
	shortEventTimes(t, time.Second, time.Hour, time.Hour)
	a := newTestAgent(t.TempDir(), newMockDocker())
	src, sends := newFakeEvents(), newSendCounter()
	watchEvents(t, a, src, sends.send)

	sub := src.next(t)
	sends.wait(t, 1) // the subscription's own list
	for i := range 10 {
		sub.evts <- docker.ContainerEvent{Action: "start", ID: fmt.Sprintf("id-%d", i), Name: fmt.Sprintf("app%d", i)}
	}
	sends.wait(t, 1)
	sends.none(t, 2*eventQuiet)
}

// Containers that each run a few seconds (CI jobs, backups) give events just
// over the quiet period apart. They must not send a list each: lists go out
// at least the minimum gap apart, and the last change is still reported.
func TestWatchContainerEvents_ListsKeepTheMinimumGap(t *testing.T) {
	shortEventTimes(t, 50*time.Millisecond, time.Hour, time.Hour)
	eventMinGap = time.Second
	a := newTestAgent(t.TempDir(), newMockDocker())
	src, sends := newFakeEvents(), newSendCounter()
	watchEvents(t, a, src, sends.send)

	sub := src.next(t)
	own := sends.wait(t, 1) // the subscription's own list

	// One event every two quiet periods, for three gaps.
	var lists []time.Time
	var events int
	var lastEvent time.Time
	tick := time.NewTicker(2 * eventQuiet)
	defer tick.Stop()
	for start := time.Now(); time.Since(start) < 3*eventMinGap; {
		lastEvent = time.Now()
		sub.evts <- docker.ContainerEvent{Action: "die", ID: fmt.Sprintf("id-job%d", events), Name: fmt.Sprintf("job%d", events)}
		events++
		for ticked := false; !ticked; {
			select {
			case at := <-sends.calls:
				lists = append(lists, at)
			case <-tick.C:
				ticked = true
			}
		}
	}
	// The change of the last event goes out too.
	for len(lists) == 0 || lists[len(lists)-1].Before(lastEvent) {
		lists = append(lists, sends.wait(t, 1))
	}

	span := lists[len(lists)-1].Sub(own)
	if most := int(span / eventMinGap); len(lists) > most {
		t.Errorf("%d events %v apart sent %d lists in %v, want at most %d: one each %v",
			events, 2*eventQuiet, len(lists), span.Round(time.Millisecond), most, eventMinGap)
	}
}

// A host whose containers never stop changing (a CI runner) must still
// report: the list goes out within the maximum wait of the first event. The
// minimum gap is longer than the maximum wait here: the maximum wait still
// bounds it.
func TestWatchContainerEvents_ContinuousEventsSendWithinTheMaximumWait(t *testing.T) {
	shortEventTimes(t, 400*time.Millisecond, 1200*time.Millisecond, time.Hour)
	eventMinGap = time.Hour
	a := newTestAgent(t.TempDir(), newMockDocker())
	src, sends := newFakeEvents(), newSendCounter()
	watchEvents(t, a, src, sends.send)

	sub := src.next(t)
	sends.wait(t, 1)

	// One event every half quiet period, until the list is sent: the
	// stream never goes quiet.
	ev := docker.ContainerEvent{Action: "die", ID: "id-job", Name: "job"}
	firstEvent := time.Now()
	sub.evts <- ev
	stop := make(chan struct{})
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		tick := time.NewTicker(eventQuiet / 2)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				sub.evts <- ev
			}
		}
	}()
	defer func() {
		close(stop)
		<-pumped
	}()
	// A slow runner (the race detector, a loaded machine) can be late by a
	// second or two, not by the maximum wait several times over.
	if late := sends.wait(t, 1).Sub(firstEvent); late > eventMaxWait+3*time.Second {
		t.Errorf("the list went out %v after the first event, want within the maximum wait of %v (and 3s for a slow runner)",
			late.Round(time.Millisecond), eventMaxWait)
	}
}

// The daemon restarted or went away: the loop backs off, subscribes again
// and sends exactly one list for what it missed.
func TestWatchContainerEvents_StreamErrorResubscribesAndCatchesUpOnce(t *testing.T) {
	shortEventTimes(t, 200*time.Millisecond, time.Hour, 300*time.Millisecond)
	a := newTestAgent(t.TempDir(), newMockDocker())
	src, sends := newFakeEvents(), newSendCounter()
	watchEvents(t, a, src, sends.send)

	first := src.next(t)
	sends.wait(t, 1)

	failed := time.Now()
	first.errs <- io.EOF
	src.next(t)
	if waited := time.Since(failed); waited < eventRetry {
		t.Errorf("subscribed again %v after the stream failed, want a back-off of at least %v", waited, eventRetry)
	}
	sends.wait(t, 1)
	sends.none(t, 3*eventQuiet)
	select {
	case <-src.subs:
		t.Error("subscribed again without a stream failure")
	default:
	}
}

// A stream that fails before its list went out sends none: the list of the
// next subscription reports what the failed one would have.
func TestWatchContainerEvents_FailedStreamSendsNoListOfItsOwn(t *testing.T) {
	shortEventTimes(t, time.Second, time.Hour, 50*time.Millisecond)
	a := newTestAgent(t.TempDir(), newMockDocker())
	src, sends := newFakeEvents(), newSendCounter()
	src.prime = func(n int, s *eventSub) {
		if n == 0 {
			s.evts <- docker.ContainerEvent{Action: "destroy", ID: "id-web", Name: "web"}
			s.errs <- errors.New("Cannot connect to the Docker daemon")
		}
	}
	watchEvents(t, a, src, sends.send)

	src.next(t) // fails at once
	src.next(t)
	sends.wait(t, 1)
	sends.none(t, 2*eventQuiet)
}

// A daemon or socket proxy that refuses /events fails every subscription at
// once. That is one warning, not one each back-off period for as long as
// the agent runs.
func TestWatchContainerEvents_RefusedStreamWarnsOnce(t *testing.T) {
	shortEventTimes(t, time.Hour, time.Hour, 5*time.Millisecond)
	a := newTestAgent(t.TempDir(), newMockDocker())
	logs := make(logRecords, 64)
	a.log = slog.New(logs)
	src := newFakeEvents()
	src.prime = func(_ int, s *eventSub) { s.errs <- errors.New("Error response from daemon: forbidden") }
	watchEvents(t, a, src, newSendCounter().send)

	want := []slog.Level{slog.LevelWarn, slog.LevelDebug, slog.LevelDebug, slog.LevelDebug, slog.LevelDebug}
	if got := logs.streamLevels(t, len(want)); !slices.Equal(got, want) {
		t.Errorf("levels of the event stream's log records = %v, want %v", got, want)
	}
}

// Once a failing stream works again, one record says so. A stream that had
// worked and then ends (a socket proxy cutting idle connections, a daemon
// restart) is subscribed to again with no warning, and a failure after that
// warns again.
func TestWatchContainerEvents_LogsRecoveryOnceAndNoWarningAfterAWorkingStream(t *testing.T) {
	shortEventTimes(t, time.Hour, time.Hour, 5*time.Millisecond)
	eventHealthy = 200 * time.Millisecond
	a := newTestAgent(t.TempDir(), newMockDocker())
	logs := make(logRecords, 64)
	a.log = slog.New(logs)
	src := newFakeEvents()
	src.prime = func(n int, s *eventSub) {
		if n != 2 { // the third one works
			s.errs <- errors.New("Error response from daemon: forbidden")
		}
	}
	watchEvents(t, a, src, newSendCounter().send)

	src.next(t)
	src.next(t)
	working := src.next(t)
	got := logs.streamLevels(t, 3) // failed, failed again, works again
	working.errs <- io.EOF         // after it worked
	got = append(got, logs.streamLevels(t, 3)...)

	want := []slog.Level{slog.LevelWarn, slog.LevelDebug, slog.LevelInfo, slog.LevelDebug, slog.LevelWarn, slog.LevelDebug}
	if !slices.Equal(got, want) {
		t.Errorf("levels of the event stream's log records = %v, want %v", got, want)
	}
}

// An update on the host sends its own list when it ends; the loop does not
// report each step of its container swap, and its list waits for the end of
// the update, whatever the maximum wait.
func TestWatchContainerEvents_WaitsForARunningUpdate(t *testing.T) {
	shortEventTimes(t, 100*time.Millisecond, 100*time.Millisecond, time.Hour)
	a := newTestAgent(t.TempDir(), newMockDocker())
	if !a.beginUpdate("web") {
		t.Fatal("beginUpdate refused")
	}
	src, sends := newFakeEvents(), newSendCounter()
	watchEvents(t, a, src, sends.send)

	sub := src.next(t)
	for _, action := range []string{"rename", "create", "die", "start", "destroy"} {
		sub.evts <- docker.ContainerEvent{Action: action, ID: "id-web", Name: "web"}
	}
	sends.none(t, 5*eventQuiet)
	a.endUpdate("web")
	sends.wait(t, 1)
	sends.none(t, 3*eventQuiet)
}

// The loop belongs to one session: it ends with the session's context in
// every state it can be in.
func TestWatchContainerEvents_EndsWithTheSession(t *testing.T) {
	tests := []struct {
		name string
		// into sets the loop up for the state. It returns the send to use,
		// and reached, which returns once the loop is in the state.
		into func(t *testing.T, src *fakeEvents) (send func(context.Context) error, reached func(*testing.T))
	}{
		{"idle", func(t *testing.T, src *fakeEvents) (func(context.Context) error, func(*testing.T)) {
			shortEventTimes(t, 10*time.Millisecond, time.Hour, time.Hour)
			sends := newSendCounter()
			// Its own list sent, the subscription waits for events.
			return sends.send, func(t *testing.T) {
				src.next(t)
				sends.wait(t, 1)
			}
		}},
		{"waiting for the stream to go quiet", func(t *testing.T, src *fakeEvents) (func(context.Context) error, func(*testing.T)) {
			shortEventTimes(t, time.Hour, time.Hour, time.Hour)
			src.handOff = true
			// The loop has taken an event, and its list waits for the quiet.
			return newSendCounter().send, func(t *testing.T) {
				src.next(t).evts <- docker.ContainerEvent{Action: "start", ID: "id-web", Name: "web"}
			}
		}},
		{"backing off after a stream failure", func(t *testing.T, src *fakeEvents) (func(context.Context) error, func(*testing.T)) {
			shortEventTimes(t, time.Hour, time.Hour, time.Hour)
			src.handOff = true
			// The loop has taken the failure, and the back-off follows.
			return newSendCounter().send, func(t *testing.T) { src.next(t).errs <- io.EOF }
		}},
		{"subscribing to a daemon that does not answer", func(t *testing.T, src *fakeEvents) (func(context.Context) error, func(*testing.T)) {
			shortEventTimes(t, time.Hour, time.Hour, time.Hour)
			src.block = true
			// The subscription was asked for and does not return.
			return newSendCounter().send, func(t *testing.T) { src.next(t) }
		}},
		{"sending a list", func(t *testing.T, src *fakeEvents) (func(context.Context) error, func(*testing.T)) {
			shortEventTimes(t, time.Millisecond, time.Hour, time.Hour)
			sending := make(chan struct{}, 1)
			send := func(ctx context.Context) error {
				select {
				case sending <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return ctx.Err()
			}
			return send, func(t *testing.T) {
				select {
				case <-sending:
				case <-time.After(10 * time.Second):
					t.Fatal("no list send started")
				}
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestAgent(t.TempDir(), newMockDocker())
			src := newFakeEvents()
			send, reached := tc.into(t, src)
			stop, ended := watchEvents(t, a, src, send)
			reached(t)
			stop()
			select {
			case <-ended:
			case <-time.After(10 * time.Second):
				t.Fatal("the events loop did not end with its context")
			}
		})
	}
}

// eventsHost is a fakeHost whose client also streams container events, as
// the real client does.
type eventsHost struct {
	*fakeHost
	*fakeEvents
}

func newEventsHost() *eventsHost {
	host := &eventsHost{fakeHost: newFakeHost(), fakeEvents: newFakeEvents()}
	host.addContainer("web", "nginx:1", true)
	return host
}

// waitList waits for an unsolicited container list at the hub.
func waitList(t *testing.T, h *fakeHub) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case msg := <-h.got:
			if cl := msg.GetContainerList(); cl != nil && cl.GetRequestId() == "" {
				return
			}
		case <-deadline:
			t.Fatal("no container list reached the hub")
		}
	}
}

// hangUpHub is a fakeHub that ends the agent's stream from its side when
// hangUp is closed, as a hub that restarts does.
type hangUpHub struct {
	*fakeHub
	hangUp chan struct{}
}

func (h *hangUpHub) Channel(stream grpc.BidiStreamingServer[proto.AgentMessage, proto.ServerMessage]) error {
	done := make(chan error, 1)
	go func() { done <- h.fakeHub.Channel(stream) }()
	select {
	case err := <-done:
		return err
	case <-h.hangUp:
		return nil
	}
}

// newEventsHub returns a fake hub for the events tests.
func newEventsHub() *fakeHub {
	return &fakeHub{channel: make(chan struct{}, 1), got: make(chan *proto.AgentMessage, 64)}
}

// session is one session of an agent against a fake hub.
type session struct {
	hub    *fakeHub
	hangUp chan struct{} // closing it ends the session from the hub's side
	ended  chan struct{} // closed when runSession has returned
	err    error         // what runSession returned, once ended is closed
}

// startSession runs one session of an agent on d against a fake hub, until
// the hub ends it or the test ends. The agent's own context stays live
// until the test ends.
func startSession(t *testing.T, d DockerAPI) *session {
	t.Helper()
	caDir, dataDir := t.TempDir(), t.TempDir()
	s := &session{hub: newEventsHub(), hangUp: make(chan struct{}), ended: make(chan struct{})}
	addr := startFakeHub(t, caDir, &hangUpHub{fakeHub: s.hub, hangUp: s.hangUp})
	provisionAgent(t, caDir, dataDir, "host-ev", time.Now())
	a := New(Config{ServerAddr: addr, HostName: "ev", DataDir: dataDir}, d, noopLogger())
	a.hostID = "host-ev"
	jrnl, err := newJournal(dataDir) // Run sets it up before the first session
	if err != nil {
		t.Fatal(err)
	}
	a.journal = jrnl

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(s.ended)
		s.err = a.runSession(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-s.ended
	})
	select {
	case <-s.hub.channel:
	case <-s.ended:
		t.Fatalf("session ended before its stream opened: %v", s.err)
	case <-time.After(10 * time.Second):
		t.Fatal("agent never opened its stream")
	}
	return s
}

// A Docker event stream failure is the events loop's to retry: it never
// ends the gRPC session, and the hub gets the catch-up list over it. The
// agent runs with its reconnects (Run), so a session the failure ended
// would show at the hub as a second stream.
func TestRun_EventStreamFailureKeepsTheSession(t *testing.T) {
	shortEventTimes(t, 100*time.Millisecond, time.Hour, 100*time.Millisecond)
	host := newEventsHost()
	caDir, dataDir := t.TempDir(), t.TempDir()
	hub := newEventsHub()
	addr := startFakeHub(t, caDir, hub)
	provisionAgent(t, caDir, dataDir, "host-ev", time.Now())
	a := New(Config{ServerAddr: addr, HostName: "ev", DataDir: dataDir}, host, noopLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	first := host.next(t)
	waitList(t, hub) // the subscription's own list
	first.errs <- errors.New("Cannot connect to the Docker daemon")
	host.next(t)
	waitList(t, hub) // the catch-up list
	// The hub took that list from a running stream, so it has counted the
	// stream that brought it.
	if n := hub.opens.Load(); n != 1 {
		t.Errorf("agent opened %d streams, want 1: the event stream failure ended the session", n)
	}
}

// The events loop ends with its session, also when the hub ends the session
// while the agent's own context goes on: the loop's subscription is
// cancelled and it subscribes no more.
func TestRunSession_EventsLoopEndsWhenTheHubEndsTheSession(t *testing.T) {
	shortEventTimes(t, 50*time.Millisecond, time.Hour, 50*time.Millisecond)
	host := newEventsHost()
	s := startSession(t, host)

	sub := host.next(t)
	close(s.hangUp)
	select {
	case <-s.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end when the hub closed its stream")
	}
	select {
	case <-sub.ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the session ended and its events subscription is still open")
	}
	select {
	case <-host.subs:
		t.Fatal("the events loop subscribed again after its session ended")
	case <-time.After(10 * eventRetry):
	}
}

// A Docker client without container events (each test double of the
// agent's Docker client) runs no events loop: the session runs as before,
// and lists go out only on connect, on request and with the heartbeats.
func TestRunSession_NoEventsLoopWithoutContainerEvents(t *testing.T) {
	shortEventTimes(t, 50*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond)
	for _, d := range []DockerAPI{newFakeHost(), newMockDocker()} {
		if _, ok := d.(containerEventSource); ok {
			t.Fatalf("%T streams container events; the test needs a client that does not", d)
		}
	}
	host := newFakeHost()
	host.addContainer("web", "nginx:1", true)
	s := startSession(t, host)

	deadline := time.After(20 * eventQuiet)
	for {
		select {
		case msg := <-s.hub.got:
			if msg.GetContainerList() != nil {
				t.Fatal("a container list was sent with no events loop to send it")
			}
		case <-s.ended:
			t.Fatalf("session ended: %v", s.err)
		case <-deadline:
			return
		}
	}
}
