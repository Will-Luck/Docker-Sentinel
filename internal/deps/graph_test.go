package deps

import (
	"slices"
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

// The daemon stores network_mode container:<name> as container:<ID>.
func TestNetworkDependencyByContainerID(t *testing.T) {
	containers := []ContainerInfo{
		{ID: "0123456789abcdef", Name: "vpn"},
		{ID: "fedcba9876543210", Name: "torrent", NetworkMode: "container:0123456789abcdef"},
	}

	g := Build(containers)

	if got := g.Dependents("vpn"); len(got) != 1 || got[0] != "torrent" {
		t.Errorf("Dependents(vpn) = %v, want [torrent]", got)
	}
}

// compose depends_on names services of the same project, while compose
// names the containers <project>-<service>-1.
func TestComposeDependsOnResolvesServiceNames(t *testing.T) {
	containers := []ContainerInfo{
		{Name: "shop-db-1", Labels: map[string]string{
			"com.docker.compose.project": "shop", "com.docker.compose.service": "db",
		}},
		{Name: "shop-app-1", Labels: map[string]string{
			"com.docker.compose.project":    "shop",
			"com.docker.compose.service":    "app",
			"com.docker.compose.depends_on": "db:service_healthy:false",
		}},
		{Name: "blog-db-1", Labels: map[string]string{
			"com.docker.compose.project": "blog", "com.docker.compose.service": "db",
		}},
	}

	g := Build(containers)

	if got := g.Dependencies("shop-app-1"); len(got) != 1 || got[0] != "shop-db-1" {
		t.Errorf("Dependencies(shop-app-1) = %v, want [shop-db-1] (same project only)", got)
	}
	if got := g.Dependents("blog-db-1"); len(got) != 0 {
		t.Errorf("Dependents(blog-db-1) = %v, want none", got)
	}
}

func composeInfo(name, project, service, dependsOn string) ContainerInfo {
	labels := map[string]string{
		"com.docker.compose.project": project,
		"com.docker.compose.service": service,
	}
	if dependsOn != "" {
		labels["com.docker.compose.depends_on"] = dependsOn
	}
	return ContainerInfo{Name: name, Labels: labels}
}

// compose restarts a dependent after its dependency changes only with
// `restart: true`, written as the third field of the label. The edge is
// still shown.
func TestComposeDependsOnRestartsOnlyWithRestartTrue(t *testing.T) {
	g := Build([]ContainerInfo{
		composeInfo("p-db-1", "p", "db", ""),
		composeInfo("p-api-1", "p", "api", "db:service_started:true"),
		composeInfo("p-web-1", "p", "web", "db:service_started:false"),
		composeInfo("p-old-1", "p", "old", "db:service_started"),
		{Name: "tool", Labels: map[string]string{"sentinel.depends-on": "p-db-1"}},
	})

	if got := g.Dependents("p-db-1"); len(got) != 4 {
		t.Errorf("Dependents = %v, want all four shown", got)
	}
	got := g.RestartDependents("p-db-1")
	if len(got) != 2 || got[0] != "p-api-1" || got[1] != "tool" {
		t.Errorf("RestartDependents = %v, want [p-api-1 tool]", got)
	}
}

// A scaled service has several containers; a dependent depends on all.
func TestComposeDependsOnScaledService(t *testing.T) {
	g := Build([]ContainerInfo{
		composeInfo("p-db-1", "p", "db", ""),
		composeInfo("p-db-2", "p", "db", ""),
		composeInfo("p-api-1", "p", "api", "db:service_started:true"),
	})

	if got := g.Dependencies("p-api-1"); len(got) != 2 {
		t.Errorf("Dependencies = %v, want both db containers", got)
	}
}

// compose depends_on names a service of the same project, even when a
// container elsewhere happens to have that name.
func TestComposeDependsOnResolvesServiceFirst(t *testing.T) {
	g := Build([]ContainerInfo{
		{Name: "db", Labels: map[string]string{}},
		composeInfo("p-db-1", "p", "db", ""),
		composeInfo("p-api-1", "p", "api", "db:service_started:true"),
	})

	if got := g.Dependencies("p-api-1"); len(got) != 1 || got[0] != "p-db-1" {
		t.Errorf("Dependencies = %v, want [p-db-1]", got)
	}
}

// Containers of different cluster hosts (named host::name by the
// dashboard) never depend on each other through compose services.
func TestComposeDependsOnStaysOnItsHost(t *testing.T) {
	g := Build([]ContainerInfo{
		composeInfo("h1::p-db-1", "p", "db", ""),
		composeInfo("h2::p-db-1", "p", "db", ""),
		composeInfo("h1::p-api-1", "p", "api", "db:service_started:true"),
	})

	if got := g.Dependencies("h1::p-api-1"); len(got) != 1 || got[0] != "h1::p-db-1" {
		t.Errorf("Dependencies = %v, want [h1::p-db-1]", got)
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

// Order puts dependencies first like Sort, and breaks a cycle instead of
// failing: the first container of the cycle by name goes first, and the
// cycle is reported.
func TestOrderBreaksCycles(t *testing.T) {
	g := Build([]ContainerInfo{
		{Name: "web", Labels: map[string]string{"sentinel.depends-on": "api"}},
		{Name: "api", Labels: map[string]string{"sentinel.depends-on": "db"}},
		{Name: "db"},
		{Name: "a", Labels: map[string]string{"sentinel.depends-on": "b"}},
		{Name: "b", Labels: map[string]string{"sentinel.depends-on": "a"}},
		{Name: "c", Labels: map[string]string{"sentinel.depends-on": "a"}},
	})

	order, cycles := g.Order()

	pos := make(map[string]int)
	for i, n := range order {
		pos[n] = i
	}
	if len(order) != 6 {
		t.Fatalf("order = %v, want all six containers", order)
	}
	for _, e := range [][2]string{{"db", "api"}, {"api", "web"}, {"a", "b"}, {"a", "c"}} {
		if pos[e[0]] > pos[e[1]] {
			t.Errorf("order = %v, want %s before %s", order, e[0], e[1])
		}
	}
	if len(cycles) != 1 {
		t.Errorf("cycles = %v, want the a/b cycle", cycles)
	}

	order, cycles = Build([]ContainerInfo{{Name: "x"}, {Name: "y", Labels: map[string]string{"sentinel.depends-on": "x"}}}).Order()
	if !slices.Equal(order, []string{"x", "y"}) || cycles != nil {
		t.Errorf("order = %v cycles = %v, want [x y] and no cycle", order, cycles)
	}
}
