// Package dot рендерит граф зависимостей в формат Graphviz DOT
// (для экспорта в PNG/SVG через `dot`).
package dot

import (
	"strings"

	"dockerfile-parser/model"
)

// Render возвращает DOT-код для графа.
func Render(g *model.Graph) string {
	var b strings.Builder
	b.WriteString("digraph deps {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [fontname=\"sans-serif\"];\n")

	byID := make(map[string]model.Node, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	groupOf := map[string]string{}
	for _, gr := range g.Groups {
		for _, id := range gr.Nodes {
			if _, ok := groupOf[id]; !ok {
				groupOf[id] = gr.Name
			}
		}
	}

	// Кластеры-сети.
	for _, gr := range g.Groups {
		b.WriteString("  subgraph cluster_" + safeID(gr.Name) + " {\n")
		b.WriteString("    label=\"" + gr.Name + "\";\n")
		for _, id := range gr.Nodes {
			if groupOf[id] != gr.Name {
				continue
			}
			if n, ok := byID[id]; ok {
				b.WriteString("    " + nodeDef(n) + "\n")
			}
		}
		b.WriteString("  }\n")
	}

	// Узлы вне кластеров.
	for _, n := range g.Nodes {
		if _, grouped := groupOf[n.ID]; grouped {
			continue
		}
		b.WriteString("  " + nodeDef(n) + "\n")
	}

	// Рёбра.
	for _, e := range g.Edges {
		b.WriteString("  " + edgeLine(e) + "\n")
	}

	b.WriteString("}\n")
	return b.String()
}

func nodeDef(n model.Node) string {
	label := n.Label
	if len(n.Ports) > 0 {
		label += `\n:` + strings.Join(publishedPorts(n.Ports), ",")
	}
	var shape, extra string
	switch n.Kind {
	case model.KindDatabase:
		shape = "cylinder"
	case model.KindExternal:
		shape = "hexagon"
		extra = ",style=dashed"
	case model.KindEntry:
		shape = "house"
		extra = `,style=filled,fillcolor="#dcfce7"`
	default:
		shape = "box"
	}
	return `"` + n.ID + `" [shape=` + shape + extra + `,label="` + label + `"];`
}

func edgeLine(e model.Edge) string {
	head := `"` + e.From + `" -> "` + e.To + `"`
	if hasType(e.Types, model.EdgeNetwork) {
		if e.Detail != "" {
			return head + ` [label="` + e.Detail + `"];`
		}
		return head + `;`
	}
	label := model.EdgeDependsOn
	if len(e.Types) > 0 {
		label = e.Types[0]
	}
	return head + ` [style=dashed,label="` + label + `"];`
}

func publishedPorts(ports []string) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		pub, _, _ := strings.Cut(p, ":")
		out = append(out, pub)
	}
	return out
}

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
