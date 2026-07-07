package mermaid

import (
	"strings"
	"testing"

	"depgraph/model"
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

const wantMermaid = `graph LR
  subgraph backend
    api["api"]
    db[("db")]
  end
  web["web<br/>:80"]
  worker["worker"]
  api_stripe_com{{"api.stripe.com"}}
  web -->|API_URL| api
  api -->|DATABASE_URL| db
  api -->|PAY| api_stripe_com
  worker -. depends_on .-> api
`

func TestRenderGolden(t *testing.T) {
	got := Render(sampleGraph(), "")
	if got != wantMermaid {
		t.Fatalf("Mermaid не совпал.\n--- получили ---\n%s\n--- ожидалось ---\n%s", got, wantMermaid)
	}
}

func TestRenderEntryNodeShape(t *testing.T) {
	g := &model.Graph{Nodes: []model.Node{
		{ID: "internet", Label: "🌐 интернет", Kind: model.KindEntry},
	}}
	got := Render(g, "")
	if !strings.Contains(got, `internet(["🌐 интернет"])`) {
		t.Fatalf("узел-вход должен быть стадионом ([...]): %s", got)
	}
}

func TestRenderFocusHighlight(t *testing.T) {
	g := &model.Graph{
		Nodes: []model.Node{
			{ID: "a", Label: "a", Kind: model.KindService},
			{ID: "b", Label: "b", Kind: model.KindService},
			{ID: "c", Label: "c", Kind: model.KindService}, // не сосед a
		},
		Edges: []model.Edge{
			{From: "a", To: "b", Types: []string{model.EdgeNetwork}},
		},
	}
	got := Render(g, "a")

	for _, want := range []string{
		"classDef focus", "classDef near", "classDef dim",
		"class a focus", "class b near", "class c dim",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("в выводе с фокусом нет %q:\n%s", want, got)
		}
	}
}
