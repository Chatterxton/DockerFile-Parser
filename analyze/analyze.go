// Package analyze превращает разобранный compose-файл в граф зависимостей,
// применяя эвристики: поиск хостов в environment, классификацию внешних
// сервисов и группировку по сетям.
package analyze

import (
	"sort"
	"strings"

	"depgraph/heuristic"
	"depgraph/model"
	"depgraph/parser/compose"
)

// Config управляет распознаванием внешних сервисов.
type Config struct {
	ExternalPatterns []string // glob-паттерны хостов, считаемых внешними (*.rds.amazonaws.com)
	Ignore           []string // хосты, которые не считать связями (localhost и т.п.)
}

// DefaultConfig — разумные значения по умолчанию.
func DefaultConfig() Config {
	return Config{
		Ignore: []string{"localhost", "127.0.0.1", "0.0.0.0", "::1"},
	}
}

// Build строит граф зависимостей из compose-файла.
func Build(c *compose.Compose, cfg Config) *model.Graph {
	g := &model.Graph{}

	services := sortedKeys(c.Services)
	known := make(map[string]bool, len(services))
	for _, name := range services {
		known[name] = true
	}
	ignore := make(map[string]bool, len(cfg.Ignore))
	for _, h := range cfg.Ignore {
		ignore[h] = true
	}

	// Узлы своих сервисов.
	for _, name := range services {
		svc := c.Services[name]
		kind := model.KindService
		if heuristic.IsDatabaseImage(svc.Image) {
			kind = model.KindDatabase
		}
		g.AddNode(model.Node{
			ID:    name,
			Label: name,
			Kind:  kind,
			Image: svc.Image,
			Ports: []string(svc.Ports),
		})
	}

	// Рёбра.
	for _, name := range services {
		svc := c.Services[name]

		for _, dep := range svc.DependsOn {
			if dep != name && known[dep] {
				g.AddEdge(name, dep, model.EdgeDependsOn, "")
			}
		}
		for _, link := range svc.Links {
			target, _, _ := strings.Cut(link, ":") // "db:alias" → "db"
			if target != name && known[target] {
				g.AddEdge(name, target, model.EdgeLink, "")
			}
		}
		for _, key := range sortedEnvKeys(svc.Environment) {
			for _, host := range heuristic.ExtractHosts(svc.Environment[key]) {
				if host == "" || host == name || ignore[host] {
					continue
				}
				switch {
				case known[host]:
					g.AddEdge(name, host, model.EdgeNetwork, key)
				case heuristic.IsExternalHost(host, cfg.ExternalPatterns):
					g.AddNode(model.Node{
						ID: host, Label: host, Kind: model.KindExternal, External: true,
					})
					g.AddEdge(name, host, model.EdgeNetwork, key)
				}
			}
		}
	}

	// Точки входа: сервис с опубликованными портами доступен снаружи.
	for _, name := range services {
		ports := []string(c.Services[name].Ports)
		if len(ports) == 0 {
			continue
		}
		g.AddNode(model.Node{
			ID: model.EntryNodeID, Label: "🌐 интернет", Kind: model.KindEntry, External: true,
		})
		g.AddEdge(model.EntryNodeID, name, model.EdgeNetwork, strings.Join(publishedPorts(ports), ","))
	}

	g.Groups = buildGroups(c, services)
	return g
}

// publishedPorts берёт опубликованную (левую) часть маппинга "80:8080" → "80".
func publishedPorts(ports []string) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		pub, _, _ := strings.Cut(p, ":")
		out = append(out, pub)
	}
	return out
}

// buildGroups собирает сети как группы. Пропускает бессмысленные: пустые/из
// одного узла и «дефолтную» сеть, в которую входят все сервисы.
func buildGroups(c *compose.Compose, services []string) []model.Group {
	netNodes := map[string][]string{}
	for _, name := range services {
		for _, net := range c.Services[name].Networks {
			netNodes[net] = append(netNodes[net], name)
		}
	}
	var groups []model.Group
	for _, net := range sortedKeys(netNodes) {
		nodes := netNodes[net]
		if len(nodes) < 2 || len(nodes) == len(services) {
			continue
		}
		groups = append(groups, model.Group{Name: net, Nodes: nodes})
	}
	return groups
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedEnvKeys(m compose.EnvMap) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
