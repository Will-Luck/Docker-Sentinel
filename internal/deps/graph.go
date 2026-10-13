package deps

import (
	"fmt"
	"slices"
	"sort"
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
}

// Build constructs the dependency graph from container info.
func Build(containers []ContainerInfo) *Graph {
	g := &Graph{
		adj: make(map[string][]string),
		all: make(map[string]bool),
	}

	byID := make(map[string]string)
	type service struct{ project, name string }
	byService := make(map[service][]string)
	for _, c := range containers {
		g.all[c.Name] = true
		if c.ID != "" {
			byID[c.ID] = c.Name
		}
		if p, s := c.Labels[composeProjectLabel], c.Labels[composeServiceLabel]; p != "" && s != "" {
			k := service{p, s}
			byService[k] = append(byService[k], c.Name)
		}
	}

	for _, c := range containers {
		var deps []string

		// Label-based dependencies. A name that is not a container is looked
		// up as a compose service of the same project (compose names its
		// containers <project>-<service>-1), when exactly one container runs it.
		for _, dep := range ParseDependsOn(c.Labels) {
			if !g.all[dep] {
				if p := c.Labels[composeProjectLabel]; p != "" {
					if names := byService[service{p, dep}]; len(names) == 1 {
						dep = names[0]
					}
				}
			}
			if g.all[dep] && !slices.Contains(deps, dep) {
				deps = append(deps, dep)
			}
		}

		// Network namespace dependency, by name or by container ID.
		if netDep := ParseNetworkDependency(c.NetworkMode); netDep != "" {
			if name, ok := byID[netDep]; ok {
				netDep = name
			}
			if g.all[netDep] && !slices.Contains(deps, netDep) {
				deps = append(deps, netDep)
			}
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
