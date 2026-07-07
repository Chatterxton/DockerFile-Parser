package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/graph", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewServer().ServeHTTP(rec, req)
	return rec
}

func TestAPIBuildsGraph(t *testing.T) {
	body := `{"yaml":"services:\n  web:\n    image: nginx\n    depends_on: [api]\n    environment:\n      API_URL: http://api:8000\n  api:\n    image: myapp\n"}`
	rec := post(t, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, тело: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Graph struct {
			Nodes []struct{ ID string }       `json:"nodes"`
			Edges []struct{ From, To string } `json:"edges"`
		} `json:"graph"`
		Mermaid string `json:"mermaid"`
		Dot     string `json:"dot"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ответ не JSON: %v; тело: %s", err, rec.Body.String())
	}
	if resp.Error != "" {
		t.Fatalf("неожиданная ошибка: %s", resp.Error)
	}
	if len(resp.Graph.Nodes) != 2 {
		t.Fatalf("ожидалось 2 узла, получили %d", len(resp.Graph.Nodes))
	}
	if !strings.Contains(resp.Mermaid, "graph LR") {
		t.Fatalf("mermaid без заголовка graph LR: %q", resp.Mermaid)
	}
	if !strings.Contains(resp.Mermaid, "web -->|API_URL| api") {
		t.Fatalf("mermaid без сетевого ребра web→api: %q", resp.Mermaid)
	}
	if !strings.Contains(resp.Dot, "digraph deps") {
		t.Fatalf("dot без заголовка: %q", resp.Dot)
	}
}

func TestAPIInvalidYAML(t *testing.T) {
	// services должен быть картой, а тут список — parser вернёт ошибку
	rec := post(t, `{"yaml":"services:\n  - broken\n"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ожидался 400 на кривой YAML, получили %d", rec.Code)
	}
	var resp struct {
		Error string `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error == "" {
		t.Fatalf("ожидалось поле error в ответе")
	}
}

func TestAPIUnsupportedFormat(t *testing.T) {
	rec := post(t, `{"yaml":"services: {}","format":"kustomize"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ожидался 400 на неподдержанный формат, получили %d", rec.Code)
	}
}

func TestAPIBuildsHelmGraph(t *testing.T) {
	templates := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: {{ .Release.Name }}-api\nspec:\n  template:\n    spec:\n      containers:\n        - name: api\n          image: myorg/api\n          env:\n            - name: REDIS_HOST\n              value: {{ .Release.Name }}-redis\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: {{ .Release.Name }}-redis\n"
	body, _ := json.Marshal(map[string]any{
		"yaml": templates, "format": "helm", "release": "myapp",
	})
	rec := post(t, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, тело: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mermaid string `json:"mermaid"`
		Error   string `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error != "" {
		t.Fatalf("ошибка: %s", resp.Error)
	}
	if !strings.Contains(resp.Mermaid, "myapp-api") || !strings.Contains(resp.Mermaid, "myapp-redis") {
		t.Fatalf("mermaid без узлов Helm: %q", resp.Mermaid)
	}
}

func TestIndexPageServed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	NewServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / вернул %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("страница не похожа на HTML")
	}
}
