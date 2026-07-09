package helm

import (
	"strings"
	"testing"

	"dockerfile-parser/model"
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

func TestParseResolvesClusterFQDN(t *testing.T) {
	tpl := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-app
spec:
  template:
    spec:
      containers:
        - name: app
          image: myorg/app
          env:
            - name: DB
              value: postgres://{{ .Release.Name }}-db.default.svc.cluster.local:5432/app
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-db
`
	g, err := Parse([]byte(tpl), nil, Options{Release: "myapp"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if edge(g, "myapp-app", "myapp-db") == nil {
		t.Fatalf("k8s-FQDN должен свернуться к своему myapp-db; рёбра: %+v", g.Edges)
	}
	if node(g, "myapp-db.default.svc.cluster.local") != nil {
		t.Fatalf("не должно быть фантомного внешнего узла")
	}
}

const envfromTemplates = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}-config
data:
  DATABASE_URL: postgres://{{ .Release.Name }}-db:5432/app
  CACHE_HOST: {{ .Release.Name }}-redis
  BROKER_URL: amqp://{{ .Release.Name }}-rabbitmq:5672/
  LOG_LEVEL: debug
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-app
spec:
  template:
    spec:
      containers:
        - name: app
          image: myorg/app
          envFrom:
            - configMapRef:
                name: {{ .Release.Name }}-config
          env:
            - name: BROKER
              valueFrom:
                configMapKeyRef:
                  name: {{ .Release.Name }}-config
                  key: BROKER_URL
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-db
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-redis
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-rabbitmq
`

func TestParseEnvFromConfigMap(t *testing.T) {
	g, err := Parse([]byte(envfromTemplates), nil, Options{Release: "myapp"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// envFrom подтянул весь ConfigMap → связи к db и redis
	if edge(g, "myapp-app", "myapp-db") == nil {
		t.Fatalf("DATABASE_URL из ConfigMap (envFrom) должен дать ребро к db; рёбра: %+v", g.Edges)
	}
	if edge(g, "myapp-app", "myapp-redis") == nil {
		t.Fatalf("CACHE_HOST из ConfigMap (envFrom) должен дать ребро к redis")
	}
	// valueFrom.configMapKeyRef → конкретный ключ
	if edge(g, "myapp-app", "myapp-rabbitmq") == nil {
		t.Fatalf("BROKER_URL из valueFrom.configMapKeyRef должен дать ребро к rabbitmq")
	}
	// LOG_LEVEL=debug не должен породить узел
	if node(g, "debug") != nil {
		t.Fatalf("значение LOG_LEVEL=debug не должно становиться узлом")
	}
}

// --- range по массивам values ---

func TestRenderExpandsRangeIndexElem(t *testing.T) {
	tpl := "{{- range $i, $db := .Values.shards }}\n" +
		"host{{ $i }}={{ $db.host }}:{{ $db.port }}\n" +
		"{{- end }}"
	vals := map[string]string{
		"shards.0.host": "pg-a.internal", "shards.0.port": "5432",
		"shards.1.host": "pg-b.internal", "shards.1.port": "5433",
	}
	got := Render(tpl, "c", "r", vals)
	for _, want := range []string{"host0=pg-a.internal:5432", "host1=pg-b.internal:5433"} {
		if !strings.Contains(got, want) {
			t.Fatalf("range не развернулся: нет %q в %q", want, got)
		}
	}
}

func TestRenderExpandsRangeDotForm(t *testing.T) {
	tpl := "{{- range .Values.hosts }}\n- {{ . }}\n{{- end }}"
	vals := map[string]string{"hosts.0": "node-a.internal", "hosts.1": "node-b.internal"}
	got := Render(tpl, "c", "r", vals)
	for _, want := range []string{"node-a.internal", "node-b.internal"} {
		if !strings.Contains(got, want) {
			t.Fatalf("range . не развернулся: нет %q в %q", want, got)
		}
	}
}

// Полный чарт: range по массиву шардов должен дать рёбра ко всем элементам.
func TestParseRangeShardEdges(t *testing.T) {
	tpl := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-engine
spec:
  template:
    spec:
      containers:
        - name: engine
          image: core:v1
          env:
            - name: SHARDS
              value: >-
                [
                {{- range $i, $db := .Values.shardedDatabases }}
                {"url": "jdbc:postgresql://{{ $db.host }}:{{ $db.port }}/data"}
                {{- end }}
                ]
`
	vals := `
shardedDatabases:
  - name: us
    host: pg-shard-us.aws.internal
    port: 5432
  - name: eu
    host: pg-shard-eu.aws.internal
    port: 5432
`
	g, err := Parse([]byte(tpl), []byte(vals), Options{Release: "myapp"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, host := range []string{"pg-shard-us.aws.internal", "pg-shard-eu.aws.internal"} {
		if edge(g, "myapp-engine", host) == nil {
			t.Fatalf("нет ребра myapp-engine→%s из range; рёбра: %+v", host, g.Edges)
		}
	}
}

func TestRenderExpandsRangeMap(t *testing.T) {
	tpl := "{{- range $k, $v := .Values.endpoints }}\n{{ $k }}={{ $v }}\n{{- end }}"
	vals := map[string]string{
		"endpoints.datadog":    "trace.datadoghq.com",
		"endpoints.prometheus": "prom.monitoring.svc",
	}
	got := Render(tpl, "c", "r", vals)
	for _, want := range []string{"trace.datadoghq.com", "prom.monitoring.svc"} {
		if !strings.Contains(got, want) {
			t.Fatalf("range по map не развернулся: нет %q в %q", want, got)
		}
	}
}

func TestRenderWithContext(t *testing.T) {
	tpl := "{{- with .Values.externalDB }}\nhost={{ .host }}:{{ .port }}\n{{- end }}"
	vals := map[string]string{"externalDB.host": "db.external.io", "externalDB.port": "5432"}
	got := Render(tpl, "c", "r", vals)
	if !strings.Contains(got, "host=db.external.io:5432") {
		t.Fatalf("with-контекст не развернулся: %q", got)
	}
}

func TestRenderWithAbsentSkipsBody(t *testing.T) {
	tpl := "before{{- with .Values.missing }}\nX={{ .host }}\n{{- end }}after"
	got := Render(tpl, "c", "r", nil)
	if strings.Contains(got, "X=") {
		t.Fatalf("тело with при отсутствующем значении должно пропускаться: %q", got)
	}
}

func TestRenderDefineInclude(t *testing.T) {
	tpl := `{{- define "mychart.dbenv" -}}
value: postgres://{{ .Release.Name }}-db:5432/app
{{- end -}}
env:
  {{- include "mychart.dbenv" . }}`
	got := Render(tpl, "mychart", "myapp", nil)
	if !strings.Contains(got, "postgres://myapp-db:5432/app") {
		t.Fatalf("include тела define не подставился: %q", got)
	}
	if strings.Contains(got, "define") {
		t.Fatalf("блок define не должен попадать в вывод: %q", got)
	}
}

func TestRenderPrintf(t *testing.T) {
	vals := map[string]string{"name": "cache"}
	got := Render(`{{ printf "%s-headless.%s.svc" .Values.name .Release.Namespace }}`, "c", "r", vals)
	if got != "cache-headless.default.svc" {
		t.Fatalf("printf не собрал хост: %q", got)
	}
}

// Сквозной чарт: named template + with + map-range дают рёбра ко всем хостам.
func TestParseDefineWithMapRangeEdges(t *testing.T) {
	tpl := `
{{- define "app.brokers" -}}
{{- range $name, $addr := .Values.brokers }}
- name: BROKER_{{ $name }}
  value: {{ $addr }}
{{- end }}
{{- end -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-app
spec:
  template:
    spec:
      containers:
        - name: app
          image: myorg/app
          env:
            {{- include "app.brokers" . | nindent 12 }}
            {{- with .Values.cache }}
            - name: CACHE
              value: redis://{{ .host }}:{{ .port }}/0
            {{- end }}
`
	vals := `
brokers:
  east: kafka-east.msg.internal:9092
  west: kafka-west.msg.internal:9092
cache:
  host: redis-primary.cache.internal
  port: 6379
`
	g, err := Parse([]byte(tpl), []byte(vals), Options{Release: "myapp"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, host := range []string{
		"kafka-east.msg.internal", "kafka-west.msg.internal", "redis-primary.cache.internal",
	} {
		if edge(g, "myapp-app", host) == nil {
			t.Fatalf("нет ребра myapp-app→%s; рёбра: %+v", host, g.Edges)
		}
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
