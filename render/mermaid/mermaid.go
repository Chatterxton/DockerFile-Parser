// Package mermaid рендерит граф зависимостей в текст Mermaid (graph LR),
// который браузер рисует через mermaid.js.
package mermaid

import (
	"strings"

	"depgraph/model"
)

// Render возвращает Mermaid-код для графа. Если focus непустой — узел focus и
// его прямые соседи подсвечиваются, остальные приглушаются.
func Render(g *model.Graph, focus string) string {
	var b strings.Builder
	b.WriteString("graph LR\n")

	safe := make(map[string]string, len(g.Nodes))
	byID := make(map[string]model.Node, len(g.Nodes))
	for _, n := range g.Nodes {
		safe[n.ID] = safeID(n.ID)
		byID[n.ID] = n
	}

	// Каждый узел приписываем первой группе, где он встретился
	// (в Mermaid узел не может лежать в двух подграфах).
	groupOf := map[string]string{}
	for _, gr := range g.Groups {
		for _, id := range gr.Nodes {
			if _, ok := groupOf[id]; !ok {
				groupOf[id] = gr.Name
			}
		}
	}

	// Подграфы-сети.
	for _, gr := range g.Groups {
		b.WriteString("  subgraph " + gr.Name + "\n")
		for _, id := range gr.Nodes {
			if groupOf[id] != gr.Name {
				continue
			}
			if n, ok := byID[id]; ok {
				b.WriteString("    " + nodeDef(safe[id], n) + "\n")
			}
		}
		b.WriteString("  end\n")
	}

	// Узлы вне групп — в порядке появления.
	for _, n := range g.Nodes {
		if _, grouped := groupOf[n.ID]; grouped {
			continue
		}
		b.WriteString("  " + nodeDef(safe[n.ID], n) + "\n")
	}

	// Рёбра.
	for _, e := range g.Edges {
		b.WriteString("  " + edgeLine(safe[e.From], safe[e.To], e) + "\n")
	}

	if focus != "" {
		writeFocus(&b, g, safe, focus)
	}
	return b.String()
}

// writeFocus добавляет classDef и назначает классы: focus — выбранный узел,
// near — прямые соседи, dim — все остальные.
func writeFocus(b *strings.Builder, g *model.Graph, safe map[string]string, focus string) {
	neighbor := map[string]bool{}
	for _, e := range g.Edges {
		if e.From == focus {
			neighbor[e.To] = true
		} else if e.To == focus {
			neighbor[e.From] = true
		}
	}
	b.WriteString("  classDef focus fill:#fde68a,stroke:#d97706,stroke-width:2px;\n")
	b.WriteString("  classDef near fill:#dbeafe,stroke:#3b82f6;\n")
	b.WriteString("  classDef dim fill:#f4f4f5,stroke:#d4d4d8,color:#a1a1aa;\n")
	for _, n := range g.Nodes {
		class := "dim"
		switch {
		case n.ID == focus:
			class = "focus"
		case neighbor[n.ID]:
			class = "near"
		}
		b.WriteString("  class " + safe[n.ID] + " " + class + "\n")
	}
}

// nodeDef собирает определение узла с формой по типу и портами в подписи.
func nodeDef(id string, n model.Node) string {
	label := n.Label
	if len(n.Ports) > 0 {
		label += "<br/>:" + strings.Join(publishedPorts(n.Ports), ",")
	}
	switch n.Kind {
	case model.KindDatabase:
		return id + `[("` + label + `")]`
	case model.KindExternal:
		return id + `{{"` + label + `"}}`
	case model.KindEntry:
		return id + `(["` + label + `"])`
	default:
		return id + `["` + label + `"]`
	}
}

// edgeLine рисует ребро: сетевая связь — сплошная стрелка с подписью,
// иначе (только depends_on/link) — пунктирная с названием типа.
func edgeLine(from, to string, e model.Edge) string {
	if hasType(e.Types, model.EdgeNetwork) {
		if e.Detail != "" {
			return from + " -->|" + e.Detail + "| " + to
		}
		return from + " --> " + to
	}
	label := model.EdgeDependsOn
	if len(e.Types) > 0 {
		label = e.Types[0]
	}
	return from + " -. " + label + " .-> " + to
}

func publishedPorts(ports []string) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		pub, _, _ := strings.Cut(p, ":")
		out = append(out, pub)
	}
	return out
}

// safeID приводит имя к безопасному для Mermaid идентификатору.
func safeID(s string) string {
	b := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b = append(b, r)
		default:
			b = append(b, '_')
		}
	}
	return string(b)
}

func hasType(types []string, t string) bool {
	for _, v := range types {
		if v == t {
			return true
		}
	}
	return false
}
