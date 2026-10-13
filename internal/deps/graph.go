package deps

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// ContainerInfo holds the minimal info needed to build the dependency graph.
type ContainerInfo struct {
	// ID is the container ID. The daemon stores network_mode container:<name>
	// as container:<ID>, so it is needed to resolve network dependencies.
	ID          string
	Name        string
	Labels      map[string]string
	NetworkMode string // from HostConfig.NetworkMode
}

// Compose labels used to resolve compose depends_on entries, which name
// services of the same project rather than containers.
const (
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
)

// Graph represents a directed acyclic graph of container dependencies.
type Graph struct {
	adj map[string][]string // container -> its dependencies (what it depends ON)
	all map[string]bool     // all known container names
	// restart holds the edges along which a dependent is restarted when its
	// dependency changes: every edge except a compose depends_on entry
	// without `restart: true`. Keyed by edge{dependent, dependency}.
	restart map[edge]bool
}

type edge struct{ from, to string }

// hostOf returns the cluster host prefix of a dashboard container name
// ("host::name"), or "" for a local container.
func hostOf(name string) string {
	if i := strings.Index(name, "::"); i >= 0 {
		return name[:i]
	}
	return ""
}

// Build constructs the dependency graph from container info.
//
// Edges come from sentinel.depends-on (container names), compose
// depends_on (services of the same compose project and host, every
// container of a scaled service; a name that is no such service is taken
// as a container name), and a container: network mode (by name or ID).
func Build(containers []ContainerInfo) *Graph {
	g := &Graph{
		adj:     make(map[string][]string),
		all:     make(map[string]bool),
		restart: make(map[edge]bool),
	}

	byID := make(map[string]string)
	type service struct{ host, project, name string }
	byService := make(map[service][]string)
	for _, c := range containers {
		g.all[c.Name] = true
		if c.ID != "" {
			byID[c.ID] = c.Name
		}
		if p, s := c.Labels[composeProjectLabel], c.Labels[composeServiceLabel]; p != "" && s != "" {
			k := service{hostOf(c.Name), p, s}
			byService[k] = append(byService[k], c.Name)
		}
	}

	for _, c := range containers {
		var deps []string
		add := func(dep string, restart bool) {
			if !g.all[dep] || dep == "" {
				return
			}
			if !slices.Contains(deps, dep) {
				deps = append(deps, dep)
			}
			if restart {
				g.restart[edge{c.Name, dep}] = true
			}
		}

		for _, dep := range ParseSentinelDependsOn(c.Labels) {
			add(dep, true)
		}

		for _, dep := range ParseComposeDependsOn(c.Labels) {
			names := byService[service{hostOf(c.Name), c.Labels[composeProjectLabel], dep.Service}]
			if c.Labels[composeProjectLabel] == "" || len(names) == 0 {
				names = []string{dep.Service}
			}
			for _, n := range names {
				add(n, dep.Restart)
			}
		}

		// Network namespace dependency, by name or by container ID.
		if netDep := ParseNetworkDependency(c.NetworkMode); netDep != "" {
			if name, ok := byID[netDep]; ok {
				netDep = name
			}
			add(netDep, true)
		}

		if len(deps) > 0 {
			g.adj[c.Name] = deps
		}
	}

	return g
}

// Sort returns container names in topological order (dependencies first) using Kahn's algorithm.
// Returns error if cycles are detected.
func (g *Graph) Sort() ([]string, error) {
	// Build in-degree map (reversed: who depends on me?)
	inDegree := make(map[string]int)
	reverse := make(map[string][]string) // dep -> dependents

	for name := range g.all {
		inDegree[name] = 0
	}

	for name, deps := range g.adj {
		for _, dep := range deps {
			inDegree[name]++
			reverse[dep] = append(reverse[dep], name)
		}
	}

	// Start with nodes that have no dependencies
	var queue []string
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}
	sort.Strings(queue) // deterministic ordering

	var result []string
	for len(queue) > 0 {
		// Pop first (sorted for determinism)
		node := queue[0]
		queue = queue[1:]
		result = append(result, node)

		// Reduce in-degree of dependents
		dependents := reverse[node]
		sort.Strings(dependents)
		for _, dep := range dependents {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if len(result) != len(g.all) {
		return result, fmt.Errorf("dependency cycle detected: processed %d of %d containers", len(result), len(g.all))
	}

	return result, nil
}

// Order returns every container name with each container after the
// containers it depends on, like Sort, but never fails: when only containers
// in a cycle are left, the first of them by name goes next, which breaks the
// cycle. cycles lists the cycles found, for logging (nil when there are
// none). Ties are broken by name, so the order is stable.
func (g *Graph) Order() (order []string, cycles [][]string) {
	pending := make(map[string]int) // name -> dependencies not yet ordered
	dependents := make(map[string][]string)
	for name := range g.all {
		pending[name] = 0
	}
	for name, deps := range g.adj {
		for _, dep := range deps {
			pending[name]++
			dependents[dep] = append(dependents[dep], name)
		}
	}
	broken := false
	for len(pending) > 0 {
		next := ""
		for name, n := range pending {
			if n == 0 && (next == "" || name < next) {
				next = name
			}
		}
		if next == "" {
			broken = true
			for name := range pending {
				if next == "" || name < next {
					next = name
				}
			}
		}
		delete(pending, next)
		order = append(order, next)
		for _, d := range dependents[next] {
			if _, ok := pending[d]; ok {
				pending[d]--
			}
		}
	}
	if broken {
		cycles = g.DetectCycles()
	}
	return order, cycles
}

// DetectCycles uses three-colour DFS to find circular dependencies.
func (g *Graph) DetectCycles() [][]string {
	const (
		white = 0 // unvisited
		grey  = 1 // in progress
		black = 2 // done
	)

	color := make(map[string]int)
	parent := make(map[string]string)
	var cycles [][]string

	var dfs func(node string)
	dfs = func(node string) {
		color[node] = grey
		for _, dep := range g.adj[node] {
			if color[dep] == grey {
				// Found cycle -- trace back
				cycle := []string{dep, node}
				cur := node
				for cur != dep {
					cur = parent[cur]
					if cur == "" || cur == dep {
						break
					}
					cycle = append(cycle, cur)
				}
				cycles = append(cycles, cycle)
			} else if color[dep] == white {
				parent[dep] = node
				dfs(dep)
			}
		}
		color[node] = black
	}

	for name := range g.all {
		if color[name] == white {
			dfs(name)
		}
	}

	return cycles
}

// Dependents returns containers that depend on the given container.
func (g *Graph) Dependents(name string) []string {
	var result []string
	for container, deps := range g.adj {
		for _, dep := range deps {
			if dep == name {
				result = append(result, container)
				break
			}
		}
	}
	sort.Strings(result)
	return result
}

// RestartDependents returns the containers to restart after the given
// container changed: its dependents, except those that depend on it only
// through a compose depends_on entry without `restart: true`.
func (g *Graph) RestartDependents(name string) []string {
	var result []string
	for _, dependent := range g.Dependents(name) {
		if g.restart[edge{dependent, name}] {
			result = append(result, dependent)
		}
	}
	return result
}

// Dependencies returns what the given container depends on.
func (g *Graph) Dependencies(name string) []string {
	deps := g.adj[name]
	if deps == nil {
		return nil
	}
	result := make([]string, len(deps))
	copy(result, deps)
	sort.Strings(result)
	return result
}
