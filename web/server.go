// Package web поднимает HTTP-сервер: страницу с формой и API построения графа.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"

	"depgraph/analyze"
	"depgraph/model"
	"depgraph/parser/compose"
	"depgraph/parser/helm"
	"depgraph/render/dot"
	"depgraph/render/mermaid"
)

//go:embed index.html
var indexHTML []byte

//go:embed assets/*
var assets embed.FS

type buildRequest struct {
	YAML             string   `json:"yaml"`             // compose-файл или шаблоны Helm
	Format           string   `json:"format"`           // "compose" | "helm"
	Values           string   `json:"values"`           // values.yaml (для Helm)
	Release          string   `json:"release"`          // имя релиза (для Helm)
	ChartName        string   `json:"chartName"`        // имя чарта (для Helm)
	ExternalPatterns []string `json:"externalPatterns"` //
	Focus            string   `json:"focus"`
	NeighborsOnly    bool     `json:"neighborsOnly"`
}

type buildResponse struct {
	Graph   *model.Graph `json:"graph,omitempty"`
	Mermaid string       `json:"mermaid,omitempty"`
	Dot     string       `json:"dot,omitempty"`
	Error   string       `json:"error,omitempty"`
}

// NewServer возвращает обработчик со всеми маршрутами.
func NewServer() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/api/graph", buildHandler)
	mux.Handle("/assets/", http.FileServer(http.FS(assets)))
	return mux
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func buildHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, buildResponse{Error: "нужен POST"})
		return
	}
	var req buildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, buildResponse{Error: "тело не JSON: " + err.Error()})
		return
	}
	if req.Format == "" {
		req.Format = "compose"
	}

	g, err := buildGraph(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, buildResponse{Error: err.Error()})
		return
	}

	// В режиме «только соседи» рисуем окрестность выбранного узла,
	// но в ответ отдаём полный граф — чтобы список фокуса оставался полным.
	display := g
	if req.Focus != "" && req.NeighborsOnly {
		display = g.Neighborhood(req.Focus)
	}

	writeJSON(w, http.StatusOK, buildResponse{
		Graph:   g,
		Mermaid: mermaid.Render(display, req.Focus),
		Dot:     dot.Render(display),
	})
}

// buildGraph выбирает парсер по формату и строит граф.
func buildGraph(req buildRequest) (*model.Graph, error) {
	switch req.Format {
	case "compose":
		c, err := compose.Parse([]byte(req.YAML))
		if err != nil {
			return nil, fmt.Errorf("ошибка разбора YAML: %w", err)
		}
		cfg := analyze.DefaultConfig()
		cfg.ExternalPatterns = req.ExternalPatterns
		return analyze.Build(c, cfg), nil
	case "helm":
		return helm.Parse([]byte(req.YAML), []byte(req.Values), helm.Options{
			Release:          req.Release,
			ChartName:        req.ChartName,
			ExternalPatterns: req.ExternalPatterns,
		})
	default:
		return nil, fmt.Errorf("формат %q не поддерживается (compose | helm)", req.Format)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
