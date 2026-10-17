package docker

import (
	"context"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

// ContainerEvent is one event of the Docker daemon about a container.
type ContainerEvent struct {
	Action string // create, destroy, rename, start or die
	ID     string // the container's ID
	Name   string // the container's name; after a rename, its new name
}

// containerEventActions are the container events that change a container
// list: a container is created, removed, renamed, started, or stops (a
// stop, a kill and a crash all end in die).
var containerEventActions = []string{
	string(events.ActionCreate),
	string(events.ActionDestroy),
	string(events.ActionRename),
	string(events.ActionStart),
	string(events.ActionDie),
}

// ContainerEvents subscribes to the daemon's container events that change
// the container list (containerEventActions), filtered by the daemon. The
// subscription ends with one error on the error channel, which is then
// closed: the daemon ended the stream or went away, or ctx ended. Events
// between the end and the next subscription are lost, so a caller that
// subscribes again takes a fresh container list.
func (c *Client) ContainerEvents(ctx context.Context) (<-chan ContainerEvent, <-chan error) {
	res := c.api.Events(ctx, client.EventsListOptions{
		Filters: make(client.Filters).
			Add("type", string(events.ContainerEventType)).
			Add("event", containerEventActions...),
	})
	out := make(chan ContainerEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(errs)
		for {
			select {
			case m := <-res.Messages:
				ev := ContainerEvent{Action: string(m.Action), ID: m.Actor.ID, Name: m.Actor.Attributes["name"]}
				select {
				case out <- ev:
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				}
			case err := <-res.Err:
				errs <- err
				return
			}
		}
	}()
	return out, errs
}
