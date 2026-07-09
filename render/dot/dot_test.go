package dot

import (
	"strings"
	"testing"

	"dockerfile-parser/model"
)

func sampleGraph() *model.Graph {
	return &model.Graph{
		Nodes: []model.Node{
			{ID: "web", Label: "web", Kind: model.KindService, Ports: []string{"80:80"}},
			{ID: "api", Label: "api", Kind: model.KindService},
			{ID: "db", Label: "db", Kind: model.KindDatabase},
			{ID: "worker", Label: "worker", Kind: model.KindService},
			{ID: "api.stripe.com", Label: "api.stripe.com", Kind: model.KindExternal, External: true},
		},
		Edges: []model.Edge{
			{From: "web", To: "api", Types: []string{model.EdgeDependsOn, model.EdgeNetwork}, Detail: "API_URL"},
			{From: "api", To: "db", Types: []string{model.EdgeNetwork}, Detail: "DATABASE_URL"},
			{From: "api", To: "api.stripe.com", Types: []string{model.EdgeNetwork}, Detail: "PAY"},
			{From: "worker", To: "api", Types: []string{model.EdgeDependsOn}},
		},
		Groups: []model.Group{
			{Name: "backend", Nodes: []string{"api", "db"}},
		},
	}
}

const wantDot = `digraph deps {
  rankdir=LR;
  node [fontname="sans-serif"];
  subgraph cluster_backend {
    label="backend";
    "api" [shape=box,label="api"];
    "db" [shape=cylinder,label="db"];
  }
  "web" [shape=box,label="web\n:80"];
  "worker" [shape=box,label="worker"];
  "api.stripe.com" [shape=hexagon,style=dashed,label="api.stripe.com"];
  "web" -> "api" [label="API_URL"];
  "api" -> "db" [label="DATABASE_URL"];
  "api" -> "api.stripe.com" [label="PAY"];
  "worker" -> "api" [style=dashed,label="depends_on"];
}
`

func TestRenderEntryNodeShape(t *testing.T) {
	g := &model.Graph{Nodes: []model.Node{
		{ID: "internet", Label: "internet", Kind: model.KindEntry},
	}}
	got := Render(g)
	if !strings.Contains(got, "shape=house") {
		t.Fatalf("узел-вход должен иметь shape=house: %s", got)
	}
}

func TestRenderGolden(t *testing.T) {
	got := Render(sampleGraph())
	if got != wantDot {
		t.Fatalf("DOT не совпал.\n--- получили ---\n%s\n--- ожидалось ---\n%s", got, wantDot)
	}
}
