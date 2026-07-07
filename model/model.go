// Package model описывает формат-независимый граф зависимостей сервисов.
// Это же — контракт JSON, который отдаётся веб-клиенту.
package model

// Виды узлов.
const (
	KindService  = "service"  // свой сервис из файла
	KindDatabase = "database" // свой сервис, опознанный как БД (по образу)
	KindExternal = "external" // внешний сервис (нет в файле)
	KindEntry    = "entry"    // точка входа снаружи (интернет/пользователь)
)

// EntryNodeID — идентификатор единственного узла-«интернета».
const EntryNodeID = "internet"

// Типы рёбер.
const (
	EdgeDependsOn = "depends_on" // порядок запуска (depends_on)
	EdgeLink      = "link"       // links
	EdgeNetwork   = "network"    // реальная сетевая связь (найдена в environment)
)

// Graph — узлы, рёбра и группы (сети).
type Graph struct {
	Nodes  []Node  `json:"nodes"`
	Edges  []Edge  `json:"edges"`
	Groups []Group `json:"groups"`
}

// Node — сервис (свой или внешний).
type Node struct {
	ID       string   `json:"id"`    // имя сервиса, уникальный ключ
	Label    string   `json:"label"` // подпись
	Kind     string   `json:"kind"`
	Image    string   `json:"image,omitempty"`
	Ports    []string `json:"ports,omitempty"`
	External bool     `json:"external"`
}

// Edge — направленная связь from→to. Между парой узлов ребро одно,
// но у него может быть несколько типов (например depends_on + network).
type Edge struct {
	From   string   `json:"from"`
	To     string   `json:"to"`
	Types  []string `json:"types"`
	Detail string   `json:"detail,omitempty"`
}

// Group — сеть и входящие в неё узлы (для рамок-подграфов).
type Group struct {
	Name  string   `json:"name"`
	Nodes []string `json:"nodes"`
}

// AddNode добавляет узел. Если узел с таким ID уже есть — не дублирует.
func (g *Graph) AddNode(n Node) {
	for i := range g.Nodes {
		if g.Nodes[i].ID == n.ID {
			return
		}
	}
	g.Nodes = append(g.Nodes, n)
}

// AddEdge добавляет связь from→to с типом typ. Если ребро между этой парой
// уже есть — тип добавляется к существующему ребру (без дублей), а detail
// проставляется, если ещё не задан.
func (g *Graph) AddEdge(from, to, typ, detail string) {
	for i := range g.Edges {
		if g.Edges[i].From == from && g.Edges[i].To == to {
			if !contains(g.Edges[i].Types, typ) {
				g.Edges[i].Types = append(g.Edges[i].Types, typ)
			}
			if g.Edges[i].Detail == "" {
				g.Edges[i].Detail = detail
			}
			return
		}
	}
	g.Edges = append(g.Edges, Edge{From: from, To: to, Types: []string{typ}, Detail: detail})
}

// Neighborhood возвращает подграф: узел focus, его прямые соседи и рёбра,
// инцидентные focus. Группы фильтруются к оставшимся узлам (пустые/из одного
// узла отбрасываются). Полезно для режима «только соседи».
func (g *Graph) Neighborhood(focus string) *Graph {
	in := map[string]bool{focus: true}
	sub := &Graph{}
	for _, e := range g.Edges {
		if e.From == focus {
			in[e.To] = true
			sub.Edges = append(sub.Edges, e)
		} else if e.To == focus {
			in[e.From] = true
			sub.Edges = append(sub.Edges, e)
		}
	}
	for _, n := range g.Nodes {
		if in[n.ID] {
			sub.Nodes = append(sub.Nodes, n)
		}
	}
	for _, gr := range g.Groups {
		kept := Group{Name: gr.Name}
		for _, id := range gr.Nodes {
			if in[id] {
				kept.Nodes = append(kept.Nodes, id)
			}
		}
		if len(kept.Nodes) >= 2 {
			sub.Groups = append(sub.Groups, kept)
		}
	}
	return sub
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
