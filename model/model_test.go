package model

import "testing"

func TestAddNodeDedupsByID(t *testing.T) {
	var g Graph
	g.AddNode(Node{ID: "api", Label: "api"})
	g.AddNode(Node{ID: "api", Label: "api-again"})
	g.AddNode(Node{ID: "db", Label: "db"})

	if len(g.Nodes) != 2 {
		t.Fatalf("ожидалось 2 узла, получили %d: %+v", len(g.Nodes), g.Nodes)
	}
}

func TestAddEdgeMergesTypesForSamePair(t *testing.T) {
	var g Graph
	g.AddEdge("web", "api", EdgeDependsOn, "")
	g.AddEdge("web", "api", EdgeNetwork, "API_URL")

	if len(g.Edges) != 1 {
		t.Fatalf("ожидалось 1 ребро (слияние), получили %d: %+v", len(g.Edges), g.Edges)
	}
	e := g.Edges[0]
	if e.From != "web" || e.To != "api" {
		t.Fatalf("неверные концы ребра: %+v", e)
	}
	if len(e.Types) != 2 || e.Types[0] != EdgeDependsOn || e.Types[1] != EdgeNetwork {
		t.Fatalf("ожидались типы [depends_on network], получили %v", e.Types)
	}
	if e.Detail != "API_URL" {
		t.Fatalf("ожидался detail API_URL, получили %q", e.Detail)
	}
}

func TestAddEdgeDoesNotDuplicateSameType(t *testing.T) {
	var g Graph
	g.AddEdge("api", "db", EdgeNetwork, "DATABASE_URL")
	g.AddEdge("api", "db", EdgeNetwork, "DB_HOST")

	if len(g.Edges) != 1 {
		t.Fatalf("ожидалось 1 ребро, получили %d", len(g.Edges))
	}
	if len(g.Edges[0].Types) != 1 {
		t.Fatalf("тип не должен дублироваться, получили %v", g.Edges[0].Types)
	}
}

func TestNeighborhoodKeepsFocusAndDirectNeighbors(t *testing.T) {
	g := &Graph{
		Nodes: []Node{
			{ID: "web"}, {ID: "api"}, {ID: "db"}, {ID: "redis"}, {ID: "worker"}, {ID: "lonely"},
		},
		Edges: []Edge{
			{From: "web", To: "api", Types: []string{EdgeNetwork}},
			{From: "api", To: "db", Types: []string{EdgeNetwork}},
			{From: "api", To: "redis", Types: []string{EdgeNetwork}},
			{From: "worker", To: "api", Types: []string{EdgeDependsOn}},
			{From: "web", To: "lonely", Types: []string{EdgeNetwork}}, // не касается api
		},
	}
	n := g.Neighborhood("api")

	wantNodes := map[string]bool{"api": true, "web": true, "db": true, "redis": true, "worker": true}
	if len(n.Nodes) != len(wantNodes) {
		t.Fatalf("ожидалось %d узлов, получили %d: %+v", len(wantNodes), len(n.Nodes), n.Nodes)
	}
	for _, nd := range n.Nodes {
		if !wantNodes[nd.ID] {
			t.Fatalf("лишний узел %q в окрестности api", nd.ID)
		}
	}
	// рёбра — только инцидентные api (4 штуки), web→lonely исключается
	if len(n.Edges) != 4 {
		t.Fatalf("ожидалось 4 ребра вокруг api, получили %d: %+v", len(n.Edges), n.Edges)
	}
	for _, e := range n.Edges {
		if e.From != "api" && e.To != "api" {
			t.Fatalf("ребро %s→%s не касается api", e.From, e.To)
		}
	}
}

func TestAddEdgeKeepsDirection(t *testing.T) {
	var g Graph
	g.AddEdge("a", "b", EdgeNetwork, "")
	g.AddEdge("b", "a", EdgeNetwork, "")

	if len(g.Edges) != 2 {
		t.Fatalf("a→b и b→a — разные рёбра, ожидалось 2, получили %d", len(g.Edges))
	}
}
