package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/swarm"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
)

// dockerAdapter converts docker.Client to web.ContainerLister.
type dockerAdapter struct{ c *docker.Client }

func (a *dockerAdapter) ListContainers(ctx context.Context) ([]web.ContainerSummary, error) {
	containers, err := a.c.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]web.ContainerSummary, len(containers))
	for i, c := range containers {
		result[i] = web.ContainerSummary{
			ID:     c.ID,
			Names:  c.Names,
			Image:  c.Image,
			Labels: c.Labels,
			State:  string(c.State),
			Ports:  convertPorts(c.Ports),
		}
	}
	return result, nil
}

func (a *dockerAdapter) ListAllContainers(ctx context.Context) ([]web.ContainerSummary, error) {
	containers, err := a.c.ListAllContainers(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]web.ContainerSummary, len(containers))
	for i, c := range containers {
		result[i] = web.ContainerSummary{
			ID:     c.ID,
			Names:  c.Names,
			Image:  c.Image,
			Labels: c.Labels,
			State:  string(c.State),
			Ports:  convertPorts(c.Ports),
		}
	}
	return result, nil
}

// convertPorts maps moby PortSummary to web PortMapping, keeping only
// ports that have a host binding (PublicPort > 0).
func convertPorts(ports []container.PortSummary) []web.PortMapping {
	if len(ports) == 0 {
		return nil
	}
	type portKey struct {
		host, container uint16
		proto           string
	}
	seen := make(map[portKey]bool)
	var result []web.PortMapping
	for _, p := range ports {
		if p.PublicPort == 0 {
			continue
		}
		k := portKey{p.PublicPort, p.PrivatePort, p.Type}
		if seen[k] {
			continue
		}
		seen[k] = true
		result = append(result, web.PortMapping{
			HostIP:        p.IP.String(),
			HostPort:      p.PublicPort,
			ContainerPort: p.PrivatePort,
			Protocol:      p.Type,
		})
	}
	return result
}

func (a *dockerAdapter) InspectContainer(ctx context.Context, id string) (web.ContainerInspect, error) {
	inspect, err := a.c.InspectContainer(ctx, id)
	if err != nil {
		return web.ContainerInspect{}, err
	}
	var ci web.ContainerInspect
	ci.ID = inspect.ID
	ci.Name = inspect.Name
	if inspect.Config != nil {
		ci.Image = inspect.Config.Image
	}
	if inspect.State != nil {
		ci.State.Status = string(inspect.State.Status)
		ci.State.Running = inspect.State.Running
		ci.State.Restarting = inspect.State.Restarting
	}
	return ci, nil
}

func (a *dockerAdapter) ContainerLogs(ctx context.Context, containerID string, lines int) (string, error) {
	return a.c.ContainerLogs(ctx, containerID, lines)
}

func (a *dockerAdapter) ContainerLogStream(ctx context.Context, containerID string, tail int) (io.ReadCloser, bool, error) {
	return a.c.ContainerLogStream(ctx, containerID, tail)
}

// restartAdapter bridges docker.Client to web.ContainerRestarter.
type restartAdapter struct{ c *docker.Client }

func (a *restartAdapter) RestartContainer(ctx context.Context, id string) error {
	return a.c.RestartContainer(ctx, id)
}

// stopAdapter bridges docker.Client to web.ContainerStopper.
type stopAdapter struct{ c *docker.Client }

func (a *stopAdapter) StopContainer(ctx context.Context, id string) error {
	return a.c.StopContainer(ctx, id, 10)
}

// startAdapter bridges docker.Client to web.ContainerStarter.
type startAdapter struct{ c *docker.Client }

func (a *startAdapter) StartContainer(ctx context.Context, id string) error {
	return a.c.StartContainer(ctx, id)
}

// imageAdapter bridges docker.Client to web.ImageManager.
type imageAdapter struct {
	client *docker.Client
}

func (a *imageAdapter) ListImages(ctx context.Context) ([]web.ImageInfo, error) {
	images, err := a.client.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]web.ImageInfo, len(images))
	for i, img := range images {
		result[i] = web.ImageInfo{
			ID:       img.ID,
			RepoTags: img.RepoTags,
			Size:     img.Size,
			Created:  img.Created,
			InUse:    img.InUse,
		}
	}
	return result, nil
}

func (a *imageAdapter) PruneImages(ctx context.Context) (web.ImagePruneReport, error) {
	result, err := a.client.PruneImages(ctx)
	if err != nil {
		return web.ImagePruneReport{}, err
	}
	return web.ImagePruneReport{
		ImagesDeleted:  result.ImagesDeleted,
		SpaceReclaimed: result.SpaceReclaimed,
	}, nil
}

func (a *imageAdapter) RemoveImageByID(ctx context.Context, id string) error {
	return a.client.RemoveImageByID(ctx, id)
}

// rollbackAdapter bridges engine.RollbackFromStore to web.ContainerRollback.
type rollbackAdapter struct {
	d   *docker.Client
	s   *store.Store
	log *logging.Logger
}

func (a *rollbackAdapter) RollbackContainer(ctx context.Context, name string) error {
	return engine.RollbackFromStore(ctx, a.d, a.s, name, a.log)
}

// swarmPorts converts a Swarm service's published endpoint ports to web.PortMapping.
func swarmPorts(svc swarm.Service) []web.PortMapping {
	if len(svc.Endpoint.Ports) == 0 {
		return nil
	}
	ports := make([]web.PortMapping, 0, len(svc.Endpoint.Ports))
	for _, p := range svc.Endpoint.Ports {
		if p.PublishedPort == 0 || p.PublishedPort > math.MaxUint16 || p.TargetPort > math.MaxUint16 {
			continue
		}
		ports = append(ports, web.PortMapping{
			HostPort:      uint16(p.PublishedPort), //nolint:gosec // bounded above
			ContainerPort: uint16(p.TargetPort),    //nolint:gosec // bounded above
			Protocol:      string(p.Protocol),
		})
	}
	return ports
}

// swarmAdapter bridges docker.Client + engine.Updater to web.SwarmProvider.
type swarmAdapter struct {
	client  *docker.Client
	updater *engine.Updater
}

func (a *swarmAdapter) IsSwarmMode() bool {
	return a.client.IsSwarmManager(context.Background())
}

func (a *swarmAdapter) ListServices(ctx context.Context) ([]web.ServiceSummary, error) {
	services, err := a.client.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]web.ServiceSummary, len(services))
	for i, svc := range services {
		replicas := ""
		var desired, running uint64
		if svc.ServiceStatus != nil {
			running = svc.ServiceStatus.RunningTasks
			desired = svc.ServiceStatus.DesiredTasks
			replicas = fmt.Sprintf("%d/%d", running, desired)
		}
		result[i] = web.ServiceSummary{
			ID:              svc.ID,
			Name:            svc.Spec.Name,
			Image:           svc.Spec.TaskTemplate.ContainerSpec.Image,
			Labels:          svc.Spec.Labels,
			Replicas:        replicas,
			DesiredReplicas: desired,
			RunningReplicas: running,
			Ports:           swarmPorts(svc),
		}
	}
	return result, nil
}

func (a *swarmAdapter) ListServiceDetail(ctx context.Context) ([]web.ServiceDetail, error) {
	services, err := a.client.ListServices(ctx)
	if err != nil {
		return nil, err
	}

	// Build node lookup (one call, not per-task).
	nodes, nodeErr := a.client.ListNodes(ctx)
	nodeMap := make(map[string]struct{ Name, Addr string })
	if nodeErr == nil {
		for _, n := range nodes {
			name := n.Description.Hostname
			addr := n.Status.Addr
			nodeMap[n.ID] = struct{ Name, Addr string }{name, addr}
		}
	}

	result := make([]web.ServiceDetail, 0, len(services))
	for _, svc := range services {
		replicas := ""
		var desired, running uint64
		if svc.ServiceStatus != nil {
			running = svc.ServiceStatus.RunningTasks
			desired = svc.ServiceStatus.DesiredTasks
			replicas = fmt.Sprintf("%d/%d", running, desired)
		}

		imageRef := svc.Spec.TaskTemplate.ContainerSpec.Image
		// Strip Swarm digest pinning for display.
		if i := strings.Index(imageRef, "@sha256:"); i > 0 {
			imageRef = imageRef[:i]
		}

		updateStatus := ""
		if svc.UpdateStatus != nil {
			updateStatus = string(svc.UpdateStatus.State)
		}

		summary := web.ServiceSummary{
			ID:              svc.ID,
			Name:            svc.Spec.Name,
			Image:           imageRef,
			Labels:          svc.Spec.Labels,
			Replicas:        replicas,
			DesiredReplicas: desired,
			RunningReplicas: running,
			Ports:           swarmPorts(svc),
		}

		// Fetch tasks for this service.
		tasks, taskErr := a.client.ListServiceTasks(ctx, svc.ID)
		var taskInfos []web.TaskInfo
		if taskErr == nil {
			taskInfos = make([]web.TaskInfo, 0, len(tasks))
			for _, t := range tasks {
				nodeName := t.NodeID
				nodeAddr := ""
				if info, ok := nodeMap[t.NodeID]; ok {
					nodeName = info.Name
					nodeAddr = info.Addr
				}
				taskImage := t.Spec.ContainerSpec.Image
				if i := strings.Index(taskImage, "@sha256:"); i > 0 {
					taskImage = taskImage[:i]
				}
				tag := ""
				if parts := strings.SplitN(taskImage, ":", 2); len(parts) == 2 {
					tag = parts[1]
				}
				errMsg := ""
				if t.Status.Err != "" {
					errMsg = t.Status.Err
				}
				taskInfos = append(taskInfos, web.TaskInfo{
					NodeID:   t.NodeID,
					NodeName: nodeName,
					NodeAddr: nodeAddr,
					State:    string(t.Status.State),
					Image:    taskImage,
					Tag:      tag,
					Slot:     t.Slot,
					Error:    errMsg,
				})
			}
		}

		result = append(result, web.ServiceDetail{
			ServiceSummary: summary,
			Tasks:          taskInfos,
			UpdateStatus:   updateStatus,
		})
	}
	return result, nil
}

func (a *swarmAdapter) UpdateService(ctx context.Context, id, name, targetImage string) error {
	return a.updater.UpdateService(ctx, id, name, targetImage)
}

func (a *swarmAdapter) RollbackService(ctx context.Context, id, name string) error {
	// Look up service by name if id is empty (rollback from UI).
	if id == "" {
		services, err := a.client.ListServices(ctx)
		if err != nil {
			return fmt.Errorf("list services: %w", err)
		}
		for _, svc := range services {
			if svc.Spec.Name == name {
				id = svc.ID
				break
			}
		}
		if id == "" {
			return fmt.Errorf("service %s not found", name)
		}
	}

	svc, err := a.client.InspectService(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect service %s: %w", name, err)
	}
	return a.client.RollbackService(ctx, id, svc.Meta.Version, svc.Spec)
}

func (a *swarmAdapter) ScaleService(ctx context.Context, name string, replicas uint64) error {
	services, err := a.client.ListServices(ctx)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	var id string
	for _, svc := range services {
		if svc.Spec.Name == name {
			id = svc.ID
			break
		}
	}
	if id == "" {
		return fmt.Errorf("service %s not found", name)
	}

	svc, err := a.client.InspectService(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect service %s: %w", name, err)
	}
	if svc.Spec.Mode.Replicated == nil {
		return fmt.Errorf("service %s is not replicated (global mode)", name)
	}
	svc.Spec.Mode.Replicated.Replicas = &replicas
	return a.client.UpdateService(ctx, id, svc.Meta.Version, svc.Spec, "")
}
