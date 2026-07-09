package analyze

import (
	"dockerfile-parser/model"
	"dockerfile-parser/parser/compose"
	"testing"
)

const fixture = `
services:
  web:
    image: nginx
    ports: ["80:80"]
    depends_on: [api]
    environment:
      API_URL: http://api:8000
    networks: [frontend]
  api:
    image: myapp
    depends_on:
      db:
        condition: service_healthy
    links: ["db"]
    environment:
      - DATABASE_URL=postgres://db:5432/app
      - REDIS_HOST=redis
      - PAYMENTS=https://api.stripe.com
      - SELF=http://api:8000
      - LOCAL=redis://localhost:6379
    networks: [frontend, backend]
  db:
    image: postgres:15
    networks: [backend]
  redis:
    image: redis:7
    networks: [backend]
`

func build(t *testing.T) *model.Graph {
	t.Helper()
	c, err := compose.Parse([]byte(fixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return Build(c, DefaultConfig())
}

func node(g *model.Graph, id string) *model.Node {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

func edge(g *model.Graph, from, to string) *model.Edge {
	for i := range g.Edges {
		if g.Edges[i].From == from && g.Edges[i].To == to {
			return &g.Edges[i]
		}
	}
	return nil
}

func hasType(e *model.Edge, typ string) bool {
	if e == nil {
		return false
	}
	for _, t := range e.Types {
		if t == typ {
			return true
		}
	}
	return false
}

func TestBuildCreatesServiceNodes(t *testing.T) {
	g := build(t)
	for _, id := range []string{"web", "api", "db", "redis"} {
		if node(g, id) == nil {
			t.Fatalf("нет узла %q", id)
		}
	}
}

func TestBuildDetectsDatabaseKind(t *testing.T) {
	g := build(t)
	if n := node(g, "db"); n == nil || n.Kind != model.KindDatabase {
		t.Fatalf("db должен быть database, получили %+v", n)
	}
	if n := node(g, "redis"); n == nil || n.Kind != model.KindDatabase {
		t.Fatalf("redis должен быть database, получили %+v", n)
	}
	if n := node(g, "web"); n == nil || n.Kind != model.KindService {
		t.Fatalf("web должен быть service, получили %+v", n)
	}
}

func TestBuildPortsOnNode(t *testing.T) {
	g := build(t)
	n := node(g, "web")
	if n == nil || len(n.Ports) != 1 || n.Ports[0] != "80:80" {
		t.Fatalf("web.Ports ожидалось [80:80], получили %+v", n)
	}
}

func TestBuildDependsOnEdge(t *testing.T) {
	g := build(t)
	e := edge(g, "web", "api")
	if !hasType(e, model.EdgeDependsOn) {
		t.Fatalf("ожидалось ребро web→api с типом depends_on, получили %+v", e)
	}
}

func TestBuildEnvNetworkEdges(t *testing.T) {
	g := build(t)
	if e := edge(g, "web", "api"); !hasType(e, model.EdgeNetwork) {
		t.Fatalf("web→api должно иметь тип network (API_URL), получили %+v", e)
	}
	if e := edge(g, "api", "db"); !hasType(e, model.EdgeNetwork) {
		t.Fatalf("api→db должно иметь тип network (DATABASE_URL), получили %+v", e)
	}
	if e := edge(g, "api", "redis"); !hasType(e, model.EdgeNetwork) {
		t.Fatalf("api→redis должно иметь тип network (REDIS_HOST), получили %+v", e)
	}
}

func TestBuildMergesEdgeTypes(t *testing.T) {
	g := build(t)
	e := edge(g, "api", "db")
	// api→db приходит из depends_on, links и environment одновременно
	for _, typ := range []string{model.EdgeDependsOn, model.EdgeLink, model.EdgeNetwork} {
		if !hasType(e, typ) {
			t.Fatalf("api→db должно иметь тип %q, получили %v", typ, e.Types)
		}
	}
}

func TestBuildExternalService(t *testing.T) {
	g := build(t)
	n := node(g, "api.stripe.com")
	if n == nil {
		t.Fatalf("ожидался внешний узел api.stripe.com")
	}
	if !n.External || n.Kind != model.KindExternal {
		t.Fatalf("api.stripe.com должен быть внешним, получили %+v", n)
	}
	if !hasType(edge(g, "api", "api.stripe.com"), model.EdgeNetwork) {
		t.Fatalf("ожидалось ребро api→api.stripe.com")
	}
}

func TestBuildNoSelfEdge(t *testing.T) {
	g := build(t)
	// api имеет SELF=http://api:8000 — не должно порождать петлю
	if edge(g, "api", "api") != nil {
		t.Fatalf("петля api→api не должна создаваться")
	}
}

func TestBuildIgnoresLocalhost(t *testing.T) {
	g := build(t)
	if node(g, "localhost") != nil {
		t.Fatalf("localhost не должен становиться узлом")
	}
}

func TestBuildEntryNodeForPublishedPorts(t *testing.T) {
	g := build(t)
	n := node(g, model.EntryNodeID)
	if n == nil || n.Kind != model.KindEntry {
		t.Fatalf("ожидался узел-интернет типа entry: %+v", n)
	}
	// web публикует 80:80 → есть вход снаружи
	if edge(g, model.EntryNodeID, "web") == nil {
		t.Fatalf("ожидалось ребро internet→web (опубликован порт)")
	}
	// api/db/redis без ports → снаружи не доступны
	if edge(g, model.EntryNodeID, "api") != nil {
		t.Fatalf("api без опубликованных портов не должен быть точкой входа")
	}
}

func TestBuildResolvesClusterFQDN(t *testing.T) {
	yaml := `
services:
  app:
    image: myapp
    environment:
      DB_URL: postgres://db.default.svc.cluster.local:5432/app
      REDIS: redis.default.svc
  db:
    image: postgres:15
  redis:
    image: redis:7
`
	c, err := compose.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g := Build(c, DefaultConfig())

	if edge(g, "app", "db") == nil {
		t.Fatalf("FQDN db.default.svc.cluster.local должен свернуться к своему db")
	}
	if edge(g, "app", "redis") == nil {
		t.Fatalf("redis.default.svc должен свернуться к своему redis")
	}
	if node(g, "db.default.svc.cluster.local") != nil {
		t.Fatalf("не должно быть фантомного внешнего узла для своего сервиса")
	}
}

func TestBuildGroupsFromNetworks(t *testing.T) {
	g := build(t)
	got := map[string]int{}
	for _, gr := range g.Groups {
		got[gr.Name] = len(gr.Nodes)
	}
	if got["frontend"] != 2 {
		t.Fatalf("группа frontend должна иметь 2 узла, получили %d", got["frontend"])
	}
	if got["backend"] != 3 {
		t.Fatalf("группа backend должна иметь 3 узла, получили %d", got["backend"])
	}
}
