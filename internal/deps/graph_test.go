package deps

import (
	"testing"
)

func TestLinearChainSorted(t *testing.T) {
	containers := []ContainerInfo{
		{Name: "app", Labels: map[string]string{"sentinel.depends-on": "db"}},
		{Name: "db", Labels: map[string]string{}},
		{Name: "proxy", Labels: map[string]string{"sentinel.depends-on": "app"}},
	}

	g := Build(containers)
	order, err := g.Sort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// db must come before app, app before proxy
	idx := make(map[string]int)
	for i, name := range order {
		idx[name] = i
	}

	if idx["db"] >= idx["app"] {
		t.Errorf("db should come before app: %v", order)
	}
	if idx["app"] >= idx["proxy"] {
		t.Errorf("app should come before proxy: %v", order)
	}
}

func TestDiamondDependency(t *testing.T) {
	containers := []ContainerInfo{
		{Name: "top", Labels: map[string]string{"sentinel.depends-on": "left,right"}},
		{Name: "left", Labels: map[string]string{"sentinel.depends-on": "bottom"}},
		{Name: "right", Labels: map[string]string{"sentinel.depends-on": "bottom"}},
		{Name: "bottom", Labels: map[string]string{}},
	}

	g := Build(containers)
	order, err := g.Sort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	idx := make(map[string]int)
	for i, name := range order {
		idx[name] = i
	}

	if idx["bottom"] >= idx["left"] || idx["bottom"] >= idx["right"] {
		t.Errorf("bottom should come first: %v", order)
	}
	if idx["left"] >= idx["top"] || idx["right"] >= idx["top"] {
		t.Errorf("top should come last: %v", order)
	}
}

func TestCycleDetection(t *testing.T) {
	containers := []ContainerInfo{
		{Name: "a", Labels: map[string]string{"sentinel.depends-on": "b"}},
		{Name: "b", Labels: map[string]string{"sentinel.depends-on": "c"}},
		{Name: "c", Labels: map[string]string{"sentinel.depends-on": "a"}},
	}

	g := Build(containers)
	cycles := g.DetectCycles()
	if len(cycles) == 0 {
		t.Error("expected cycle to be detected")
	}

	_, err := g.Sort()
	if err == nil {
		t.Error("Sort should return error for cyclic graph")
	}
}

func TestNoDepsOriginalOrder(t *testing.T) {
	containers := []ContainerInfo{
		{Name: "alpha", Labels: map[string]string{}},
		{Name: "beta", Labels: map[string]string{}},
		{Name: "gamma", Labels: map[string]string{}},
	}

	g := Build(containers)
	order, err := g.Sort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(order) != 3 {
		t.Errorf("expected 3 containers, got %d", len(order))
	}
}

func TestNetworkDependency(t *testing.T) {
	containers := []ContainerInfo{
		{Name: "vpn", Labels: map[string]string{}},
		{Name: "torrent", Labels: map[string]string{}, NetworkMode: "container:vpn"},
	}

	g := Build(containers)
	order, err := g.Sort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	idx := make(map[string]int)
	for i, name := range order {
		idx[name] = i
	}

	if idx["vpn"] >= idx["torrent"] {
		t.Errorf("vpn should come before torrent: %v", order)
	}
}

func TestDependents(t *testing.T) {
	containers := []ContainerInfo{
		{Name: "db", Labels: map[string]string{}},
		{Name: "app", Labels: map[string]string{"sentinel.depends-on": "db"}},
		{Name: "worker", Labels: map[string]string{"sentinel.depends-on": "db"}},
	}

	g := Build(containers)
	dependents := g.Dependents("db")
	if len(dependents) != 2 {
		t.Fatalf("expected 2 dependents, got %d: %v", len(dependents), dependents)
	}
}

func TestUnknownDepsIgnored(t *testing.T) {
	containers := []ContainerInfo{
		{Name: "app", Labels: map[string]string{"sentinel.depends-on": "nonexistent"}},
	}

	g := Build(containers)
	order, err := g.Sort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 1 || order[0] != "app" {
		t.Errorf("expected [app], got %v", order)
	}
}
