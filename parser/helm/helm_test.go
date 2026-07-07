package helm

import (
	"strings"
	"testing"

	"depgraph/model"
)

// --- Render (подстановка шаблонов) ---

func TestRenderReleaseName(t *testing.T) {
	got := Render("{{ .Release.Name }}-db", "mychart", "myapp", nil)
	if got != "myapp-db" {
		t.Fatalf("ожидалось myapp-db, получили %q", got)
	}
}

func TestRenderChartName(t *testing.T) {
	got := Render("{{ .Chart.Name }}", "mychart", "myapp", nil)
	if got != "mychart" {
		t.Fatalf("ожидалось mychart, получили %q", got)
	}
}

func TestRenderValues(t *testing.T) {
	vals := map[string]string{"image.repository": "myorg/api", "image.tag": "1.2.3"}
	got := Render("{{ .Values.image.repository }}:{{ .Values.image.tag }}", "c", "r", vals)
	if got != "myorg/api:1.2.3" {
		t.Fatalf("ожидалось myorg/api:1.2.3, получили %q", got)
	}
}

func TestRenderValuesDefault(t *testing.T) {
	got := Render(`{{ .Values.missing | default "fallback" }}`, "c", "r", nil)
	if got != "fallback" {
		t.Fatalf("ожидался fallback из default, получили %q", got)
	}
}

func TestRenderIncludeFullname(t *testing.T) {
	got := Render(`{{ include "mychart.fullname" . }}`, "mychart", "myapp", nil)
	if got != "myapp-mychart" {
		t.Fatalf("ожидалось myapp-mychart, получили %q", got)
	}
}

func TestRenderDropsControlDirectives(t *testing.T) {
	in := "{{- if .Values.enabled }}\nfoo: bar\n{{- end }}"
	got := Render(in, "c", "r", nil)
	if strings.Contains(got, "{{") || strings.Contains(got, "}}") {
		t.Fatalf("остались шаблонные скобки: %q", got)
	}
	if !strings.Contains(got, "foo: bar") {
		t.Fatalf("потеряли содержимое: %q", got)
	}
}

// --- FlattenValues ---

func TestFlattenValues(t *testing.T) {
	v := map[string]any{
		"image":    map[string]any{"repository": "myorg/api", "tag": "1.2.3"},
		"replicas": 3,
	}
	got := FlattenValues(v)
	if got["image.repository"] != "myorg/api" {
		t.Fatalf("image.repository: %q", got["image.repository"])
	}
	if got["image.tag"] != "1.2.3" {
		t.Fatalf("image.tag: %q", got["image.tag"])
	}
	if got["replicas"] != "3" {
		t.Fatalf("replicas: %q", got["replicas"])
	}
}

// --- Parse (полный чарт) ---

const templates = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-api
spec:
  template:
    spec:
      containers:
        - name: api
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
          env:
            - name: DATABASE_URL
              value: postgres://{{ .Release.Name }}-postgres:5432/app
            - name: REDIS_HOST
              value: {{ .Release.Name }}-redis
            - name: PAYMENTS_URL
              value: {{ .Values.externalApi }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-api
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-postgres
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-redis
`

const valuesYAML = `
image:
  repository: myorg/api
  tag: "1.2.3"
externalApi: https://api.stripe.com
`

func parseFixture(t *testing.T) *model.Graph {
	t.Helper()
	g, err := Parse([]byte(templates), []byte(valuesYAML),
		Options{Release: "myapp", ChartName: "mychart"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return g
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

func TestParseCreatesServiceNodes(t *testing.T) {
	g := parseFixture(t)
	for _, id := range []string{"myapp-api", "myapp-postgres", "myapp-redis"} {
		if node(g, id) == nil {
			t.Fatalf("нет узла %q; узлы: %+v", id, g.Nodes)
		}
	}
}

func TestParseDeduplicatesWorkloadAndService(t *testing.T) {
	g := parseFixture(t)
	// myapp-api есть и как Deployment, и как Service — должен быть один узел
	count := 0
	for _, n := range g.Nodes {
		if n.ID == "myapp-api" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("myapp-api должен быть один узел, получили %d", count)
	}
}

func TestParseDatabaseByName(t *testing.T) {
	g := parseFixture(t)
	if n := node(g, "myapp-postgres"); n == nil || n.Kind != model.KindDatabase {
		t.Fatalf("myapp-postgres должен быть database: %+v", n)
	}
	if n := node(g, "myapp-redis"); n == nil || n.Kind != model.KindDatabase {
		t.Fatalf("myapp-redis должен быть database: %+v", n)
	}
	if n := node(g, "myapp-api"); n == nil || n.Kind != model.KindService {
		t.Fatalf("myapp-api должен быть service: %+v", n)
	}
}

func TestParseEnvEdges(t *testing.T) {
	g := parseFixture(t)
	if edge(g, "myapp-api", "myapp-postgres") == nil {
		t.Fatalf("нет ребра myapp-api→myapp-postgres (DATABASE_URL); рёбра: %+v", g.Edges)
	}
	if edge(g, "myapp-api", "myapp-redis") == nil {
		t.Fatalf("нет ребра myapp-api→myapp-redis (REDIS_HOST)")
	}
}

const entryTemplates = `
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: {{ .Release.Name }}-ing
spec:
  rules:
    - http:
        paths:
          - backend:
              service:
                name: {{ .Release.Name }}-api
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-api
spec:
  type: ClusterIP
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-gateway
spec:
  type: LoadBalancer
`

func TestParseIngressAndLoadBalancerEntries(t *testing.T) {
	g, err := Parse([]byte(entryTemplates), nil, Options{Release: "myapp"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if n := node(g, model.EntryNodeID); n == nil || n.Kind != model.KindEntry {
		t.Fatalf("ожидался узел-интернет: %+v", n)
	}
	if edge(g, model.EntryNodeID, "myapp-api") == nil {
		t.Fatalf("ожидалось ребро internet→myapp-api (из Ingress)")
	}
	if edge(g, model.EntryNodeID, "myapp-gateway") == nil {
		t.Fatalf("ожидалось ребро internet→myapp-gateway (Service LoadBalancer)")
	}
	// Ingress сам узлом быть не должен
	if node(g, "myapp-ing") != nil {
		t.Fatalf("Ingress не должен становиться узлом")
	}
}

func TestParseExternalFromValues(t *testing.T) {
	g := parseFixture(t)
	if n := node(g, "api.stripe.com"); n == nil || !n.External {
		t.Fatalf("ожидался внешний узел api.stripe.com (из .Values.externalApi): %+v", n)
	}
	if edge(g, "myapp-api", "api.stripe.com") == nil {
		t.Fatalf("нет ребра myapp-api→api.stripe.com")
	}
}
