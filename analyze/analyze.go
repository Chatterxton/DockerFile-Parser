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

	eff := effectiveServices(c) // учёт extends: сервис наследует поля родителя

	// Узлы своих сервисов.
	for _, name := range services {
		svc := eff[name]
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
		for _, a := range eff[name].Networks.Aliases {
			if !known[a] {
				aliasMap[a] = name
			}
		}
	}
	for _, name := range services {
		for _, link := range eff[name].Links {
			if t, a, ok := strings.Cut(link, ":"); ok && a != "" && known[t] && !known[a] {
				aliasMap[a] = t
			}
		}
	}

	// connect: хост → своя связь (сервис / k8s-FQDN / алиас) или внешний узел.
	connect := func(from, host, detail string) {
		if host == "" || host == from || ignore[host] || !heuristic.IsValidHost(host) {
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
		svc := eff[name]

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
		// Прочие поля (entrypoint/command/healthcheck, <<-якорь): сканируем все
		// строки — мусор из shell/JSON отсекается валидатором хоста в connect.
		scanExtra(svc.Extra, "", func(key, s string) {
			for _, host := range heuristic.ExtractHosts(heuristic.ExpandEnv(s)) {
				connect(name, host, key)
			}
		})
	}

	// Точки входа: сервис с опубликованными портами доступен снаружи.
	for _, name := range services {
		ports := []string(eff[name].Ports)
		if len(ports) == 0 {
			continue
		}
		g.AddNode(model.Node{
			ID: model.EntryNodeID, Label: "🌐 интернет", Kind: model.KindEntry, External: true,
		})
		g.AddEdge(model.EntryNodeID, name, model.EdgeNetwork, strings.Join(publishedPorts(ports), ","))
	}

	g.Groups = buildGroups(eff, services)
	return g
}

// effectiveServices применяет extends: каждый сервис получает поля родителя
// (environment сливается, ребёнок переопределяет; depends_on/links объединяются).
func effectiveServices(c *compose.Compose) map[string]compose.Service {
	eff := map[string]compose.Service{}
	var resolve func(name string, stack map[string]bool) compose.Service
	resolve = func(name string, stack map[string]bool) compose.Service {
		if s, ok := eff[name]; ok {
			return s
		}
		svc := c.Services[name]
		if p := string(svc.Extends); p != "" && !stack[p] {
			if _, ok := c.Services[p]; ok {
				stack[name] = true
				svc = mergeService(resolve(p, stack), svc)
			}
		}
		eff[name] = svc
		return svc
	}
	for name := range c.Services {
		resolve(name, map[string]bool{})
	}
	return eff
}

func mergeService(parent, child compose.Service) compose.Service {
	out := child
	if out.Image == "" {
		out.Image = parent.Image
	}
	if len(out.Ports) == 0 {
		out.Ports = parent.Ports
	}
	if out.NetworkMode == "" {
		out.NetworkMode = parent.NetworkMode
	}
	if len(out.Networks.Names) == 0 {
		out.Networks = parent.Networks
	}
	env := compose.EnvMap{}
	for k, v := range parent.Environment {
		env[k] = v
	}
	for k, v := range child.Environment {
		env[k] = v
	}
	out.Environment = env
	out.DependsOn = compose.KeyList(unionStr(parent.DependsOn, child.DependsOn))
	out.Links = unionStr(parent.Links, child.Links)
	ex := map[string]any{}
	for k, v := range parent.Extra {
		ex[k] = v
	}
	for k, v := range child.Extra {
		ex[k] = v
	}
	out.Extra = ex
	return out
}

func unionStr(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range append(append([]string{}, a...), b...) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
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
func buildGroups(eff map[string]compose.Service, services []string) []model.Group {
	netNodes := map[string][]string{}
	for _, name := range services {
		for _, net := range eff[name].Networks.Names {
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
