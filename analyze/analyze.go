// Package analyze превращает разобранный compose-файл в граф зависимостей,
// применяя эвристики: поиск хостов в environment, классификацию внешних
// сервисов и группировку по сетям.
package analyze

import (
	"sort"
	"strings"

	"dockerfile-parser/heuristic"
	"dockerfile-parser/model"
	"dockerfile-parser/parser/compose"
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

	// Карта алиасов → сервис: сетевые алиасы принадлежат своему сервису,
	// link-алиасы указывают на target (напр. "legacy:mono" → mono→legacy).
	aliasMap := map[string]string{}
	for _, name := range services {
		for _, a := range c.Services[name].Networks.Aliases {
			if !known[a] {
				aliasMap[a] = name
			}
		}
	}
	for _, name := range services {
		for _, link := range c.Services[name].Links {
			if t, a, ok := strings.Cut(link, ":"); ok && a != "" && known[t] && !known[a] {
				aliasMap[a] = t
			}
		}
	}

	// connect: хост → своя связь (сервис / k8s-FQDN / алиас) или внешний узел.
	connect := func(from, host, detail string) {
		if host == "" || host == from || ignore[host] {
			return
		}
		if target, ok := heuristic.ResolveInternal(host, known); ok {
			if target != from {
				g.AddEdge(from, target, model.EdgeNetwork, detail)
			}
			return
		}
		if target, ok := aliasMap[host]; ok {
			if target != from {
				g.AddEdge(from, target, model.EdgeNetwork, detail)
			}
			return
		}
		if heuristic.IsExternalHost(host, cfg.ExternalPatterns) {
			g.AddNode(model.Node{ID: host, Label: host, Kind: model.KindExternal, External: true})
			g.AddEdge(from, host, model.EdgeNetwork, detail)
		}
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
		// network_mode: service:X — сервис делит сетевой стек другого.
		if t, ok := strings.CutPrefix(svc.NetworkMode, "service:"); ok && known[t] && t != name {
			g.AddEdge(name, t, model.EdgeLink, "network_mode")
		}
		for _, key := range sortedEnvKeys(svc.Environment) {
			for _, host := range heuristic.ExtractHosts(heuristic.ExpandEnv(svc.Environment[key])) {
				connect(name, host, key)
			}
		}
		// Прочие поля (в т.ч. развёрнутый <<-якорь): сканируем строки со схемой.
		scanExtra(svc.Extra, "", func(key, s string) {
			if strings.Contains(s, "://") {
				for _, host := range heuristic.ExtractHosts(heuristic.ExpandEnv(s)) {
					connect(name, host, key)
				}
			}
		})
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

// scanExtra рекурсивно обходит прочие поля сервиса и вызывает fn для каждой
// строки (key — ключ карты, из которого она пришла).
func scanExtra(v any, key string, fn func(k, s string)) {
	switch t := v.(type) {
	case string:
		fn(key, t)
	case map[string]any:
		for k, val := range t {
			scanExtra(val, k, fn)
		}
	case []any:
		for _, item := range t {
			scanExtra(item, key, fn)
		}
	}
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
		for _, net := range c.Services[name].Networks.Names {
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
