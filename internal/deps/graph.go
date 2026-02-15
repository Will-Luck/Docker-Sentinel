package deps

import (
	"fmt"
	"sort"
)

// ContainerInfo holds the minimal info needed to build the dependency graph.
type ContainerInfo struct {
	Name        string
	Labels      map[string]string
	NetworkMode string // from HostConfig.NetworkMode
}

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

	for _, c := range containers {
		g.all[c.Name] = true
	}

	for _, c := range containers {
		var deps []string

		// Label-based dependencies
		for _, dep := range ParseDependsOn(c.Labels) {
			if g.all[dep] {
				deps = append(deps, dep)
			}
		}

		// Network namespace dependency
		if netDep := ParseNetworkDependency(c.NetworkMode); netDep != "" && g.all[netDep] {
			deps = append(deps, netDep)
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
