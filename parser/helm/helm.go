// Package helm разбирает Helm-чарты (шаблоны + values) в граф зависимостей.
// Полноценный рендер (helm template) не выполняется — вместо этого делается
// best-effort подстановка .Release/.Chart/.Values и распознавание ресурсов.
package helm

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"dockerfile-parser/heuristic"
	"dockerfile-parser/model"
)

// Options управляет разбором чарта.
type Options struct {
	Release          string   // имя релиза для {{ .Release.Name }} (по умолчанию "release")
	ChartName        string   // имя чарта для {{ .Chart.Name }} / *.fullname
	ExternalPatterns []string // паттерны внешних сервисов
	Ignore           []string // хосты, которые не считать связями
}

// Ресурсы, которые становятся узлами графа.
var nodeKinds = map[string]bool{
	"Service": true, "Deployment": true, "StatefulSet": true, "DaemonSet": true,
	"ReplicaSet": true, "Job": true, "CronJob": true, "Pod": true,
}

// Ресурсы с pod-спекой (в них живут env и образы контейнеров).
var workloadKinds = map[string]bool{
	"Deployment": true, "StatefulSet": true, "DaemonSet": true,
	"ReplicaSet": true, "Job": true, "CronJob": true, "Pod": true,
}

var defaultIgnore = []string{"localhost", "127.0.0.1", "0.0.0.0", "::1"}

var tmplRe = regexp.MustCompile(`\{\{-?\s*(.*?)\s*-?\}\}`)
var docSep = regexp.MustCompile(`(?m)^---\s*$`)

// Render подставляет значения в текст шаблона: .Release.Name, .Chart.Name,
// .Values.*, include "*.fullname"; директивы управления ({{ if }}/{{ end }}/…)
// убираются. Неразрешённые выражения заменяются пустой строкой.
func Render(text, chartName, release string, values map[string]string) string {
	if release == "" {
		release = "release"
	}
	return tmplRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := tmplRe.FindStringSubmatch(m)
		return resolveExpr(sub[1], chartName, release, values)
	})
}

func resolveExpr(expr, chartName, release string, values map[string]string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" || strings.HasPrefix(expr, "/*") {
		return ""
	}
	first := expr
	if i := strings.IndexAny(expr, " \t"); i >= 0 {
		first = expr[:i]
	}
	switch first {
	case "if", "else", "end", "range", "with", "define", "block":
		return ""
	case "include", "template":
		return resolveInclude(expr, chartName, release)
	}

	parts := strings.Split(expr, "|")
	if v, ok := resolveValue(strings.TrimSpace(parts[0]), chartName, release, values); ok {
		return v
	}
	// значение не разрешилось — ищем `default "X"` в пайпе
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "default ") {
			return unquote(strings.TrimSpace(p[len("default "):]))
		}
	}
	return ""
}

func resolveValue(expr, chartName, release string, values map[string]string) (string, bool) {
	switch expr {
	case ".Release.Name":
		return release, true
	case ".Release.Namespace":
		return "default", true
	case ".Chart.Name":
		return chartName, true
	}
	if strings.HasPrefix(expr, ".Values.") {
		v, ok := values[expr[len(".Values."):]]
		return v, ok
	}
	return "", false
}

func resolveInclude(expr, chartName, release string) string {
	name := firstQuoted(expr)
	switch {
	case strings.HasSuffix(name, ".fullname"):
		return release + "-" + chartName
	case strings.HasSuffix(name, ".name"):
		return chartName
	default:
		return release
	}
}

// FlattenValues разворачивает вложенный values.yaml в плоские точечные ключи.
func FlattenValues(v map[string]any) map[string]string {
	out := map[string]string{}
	flatten("", v, out)
	return out
}

func flatten(prefix string, v any, out map[string]string) {
	if m, ok := v.(map[string]any); ok {
		for k, val := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, val, out)
		}
		return
	}
	if prefix != "" && v != nil {
		out[prefix] = fmt.Sprint(v)
	}
}

type resource struct {
	kind string
	name string
	doc  map[string]any
}

type envPair struct{ name, value string }

// Parse строит граф из шаблонов и values.
func Parse(templates, valuesYAML []byte, opts Options) (*model.Graph, error) {
	if opts.Release == "" {
		opts.Release = "release"
	}

	var rawVals map[string]any
	if strings.TrimSpace(string(valuesYAML)) != "" {
		if err := yaml.Unmarshal(valuesYAML, &rawVals); err != nil {
			return nil, fmt.Errorf("ошибка разбора values.yaml: %w", err)
		}
	}
	values := FlattenValues(rawVals)

	rendered := Render(string(templates), opts.ChartName, opts.Release, values)

	// Разбираем каждый документ; ошибочные пропускаем (best-effort).
	var resources []resource
	names := map[string]bool{}
	entryTargets := map[string]string{}       // сервис снаружи → источник (ingress/тип)
	sources := map[string]map[string]string{} // ConfigMap/Secret → его данные
	for _, part := range docSep.Split(rendered, -1) {
		if strings.TrimSpace(part) == "" {
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(part), &doc); err != nil || doc == nil {
			continue
		}
		kind := str(doc["kind"])
		name := metaName(doc)

		switch kind {
		case "ConfigMap":
			sources[name] = strMap(doc["data"])
		case "Secret":
			sources[name] = strMap(doc["stringData"]) // data — base64, пропускаем
		}

		if kind == "Ingress" {
			for _, b := range ingressBackends(doc) {
				entryTargets[b] = "ingress"
			}
		}
		if kind == "Service" {
			if t := serviceType(doc); t == "LoadBalancer" || t == "NodePort" {
				entryTargets[name] = t
			}
		}

		if name == "" || !nodeKinds[kind] {
			continue
		}
		resources = append(resources, resource{kind, name, doc})
		names[name] = true
	}

	// БД по образу контейнера (в дополнение к распознаванию по имени).
	dbByImage := map[string]bool{}
	for _, r := range resources {
		if workloadKinds[r.kind] {
			for _, img := range containerImages(r.doc) {
				if heuristic.IsDatabaseImage(img) {
					dbByImage[r.name] = true
				}
			}
		}
	}

	g := &model.Graph{}

	// Узлы (уникальные имена, по алфавиту — для детерминизма).
	sortedNames := make([]string, 0, len(names))
	for n := range names {
		sortedNames = append(sortedNames, n)
	}
	sort.Strings(sortedNames)
	for _, name := range sortedNames {
		kind := model.KindService
		if heuristic.IsDatabaseName(name) || dbByImage[name] {
			kind = model.KindDatabase
		}
		g.AddNode(model.Node{ID: name, Label: name, Kind: kind})
	}

	// Точки входа: Ingress-бэкенды и сервисы LoadBalancer/NodePort.
	entryNames := make([]string, 0, len(entryTargets))
	for t := range entryTargets {
		entryNames = append(entryNames, t)
	}
	sort.Strings(entryNames)
	for _, t := range entryNames {
		if !names[t] {
			continue
		}
		g.AddNode(model.Node{
			ID: model.EntryNodeID, Label: "🌐 интернет", Kind: model.KindEntry, External: true,
		})
		g.AddEdge(model.EntryNodeID, t, model.EdgeNetwork, entryTargets[t])
	}

	// Рёбра: из env рабочих нагрузок ищем ссылки на хосты.
	ignore := map[string]bool{}
	for _, h := range append(append([]string{}, defaultIgnore...), opts.Ignore...) {
		ignore[h] = true
	}
	workloads := make([]resource, 0, len(resources))
	for _, r := range resources {
		if workloadKinds[r.kind] {
			workloads = append(workloads, r)
		}
	}
	sort.SliceStable(workloads, func(i, j int) bool { return workloads[i].name < workloads[j].name })

	for _, r := range workloads {
		for _, ev := range containerEnv(r.doc, sources) {
			for _, host := range heuristic.ExtractHosts(ev.value) {
				if host == "" || ignore[host] {
					continue
				}
				if target, ok := heuristic.ResolveInternal(host, names); ok {
					if target != r.name {
						g.AddEdge(r.name, target, model.EdgeNetwork, ev.name)
					}
					continue
				}
				if heuristic.IsExternalHost(host, opts.ExternalPatterns) {
					g.AddNode(model.Node{ID: host, Label: host, Kind: model.KindExternal, External: true})
					g.AddEdge(r.name, host, model.EdgeNetwork, ev.name)
				}
			}
		}
	}

	return g, nil
}

// --- обход pod-спеки ---

func containerImages(doc map[string]any) []string {
	var imgs []string
	eachContainer(doc, func(c map[string]any) {
		if img := str(c["image"]); img != "" {
			imgs = append(imgs, img)
		}
	})
	return imgs
}

// containerEnv собирает пары (имя, значение) окружения контейнеров: инлайновые
// env[].value, env[].valueFrom (configMapKeyRef/secretKeyRef) и envFrom
// (весь ConfigMap/Secret) — значения берутся из sources.
func containerEnv(doc map[string]any, sources map[string]map[string]string) []envPair {
	var envs []envPair
	eachContainer(doc, func(c map[string]any) {
		if list, ok := c["env"].([]any); ok {
			for _, item := range list {
				e, ok := item.(map[string]any)
				if !ok {
					continue
				}
				name := str(e["name"])
				if v := str(e["value"]); v != "" {
					envs = append(envs, envPair{name, v})
					continue
				}
				if vf, ok := e["valueFrom"].(map[string]any); ok {
					if v, ok := refValue(vf, "configMapKeyRef", sources); ok {
						envs = append(envs, envPair{name, v})
					} else if v, ok := refValue(vf, "secretKeyRef", sources); ok {
						envs = append(envs, envPair{name, v})
					}
				}
			}
		}
		// envFrom: подтягиваем все ключи ConfigMap/Secret целиком.
		if list, ok := c["envFrom"].([]any); ok {
			for _, item := range list {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				for _, refKey := range []string{"configMapRef", "secretRef"} {
					ref, ok := m[refKey].(map[string]any)
					if !ok {
						continue
					}
					data := sources[str(ref["name"])]
					for _, k := range sortedKeys(data) {
						envs = append(envs, envPair{k, data[k]})
					}
				}
			}
		}
	})
	return envs
}

// refValue достаёт значение из configMapKeyRef/secretKeyRef.
func refValue(vf map[string]any, refKey string, sources map[string]map[string]string) (string, bool) {
	ref, ok := vf[refKey].(map[string]any)
	if !ok {
		return "", false
	}
	data, ok := sources[str(ref["name"])]
	if !ok {
		return "", false
	}
	v, ok := data[str(ref["key"])]
	return v, ok
}

// eachContainer вызывает fn для каждого контейнера/initконтейнера в документе,
// независимо от вложенности (Deployment, CronJob и т.п.).
func eachContainer(v any, fn func(c map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "containers" || k == "initContainers" {
				if list, ok := val.([]any); ok {
					for _, item := range list {
						if c, ok := item.(map[string]any); ok {
							fn(c)
						}
					}
				}
			}
			eachContainer(val, fn)
		}
	case []any:
		for _, item := range t {
			eachContainer(item, fn)
		}
	}
}

// ingressBackends собирает имена сервисов-бэкендов из Ingress (v1 и legacy).
func ingressBackends(doc map[string]any) []string {
	var names []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if b, ok := t["backend"].(map[string]any); ok {
				names = appendBackend(names, b)
			}
			if db, ok := t["defaultBackend"].(map[string]any); ok {
				names = appendBackend(names, db)
			}
			for _, val := range t {
				walk(val)
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(doc)
	return names
}

func appendBackend(names []string, b map[string]any) []string {
	if sn := str(b["serviceName"]); sn != "" { // extensions/v1beta1
		names = append(names, sn)
	}
	if svc, ok := b["service"].(map[string]any); ok { // networking.k8s.io/v1
		if n := str(svc["name"]); n != "" {
			names = append(names, n)
		}
	}
	return names
}

func serviceType(doc map[string]any) string {
	if spec, ok := doc["spec"].(map[string]any); ok {
		return str(spec["type"])
	}
	return ""
}

// --- мелкие помощники ---

func metaName(doc map[string]any) string {
	if meta, ok := doc["metadata"].(map[string]any); ok {
		return str(meta["name"])
	}
	return ""
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// strMap приводит map[string]any к map[string]string (данные ConfigMap/Secret).
func strMap(v any) map[string]string {
	out := map[string]string{}
	if m, ok := v.(map[string]any); ok {
		for k, val := range m {
			out[k] = fmt.Sprint(val)
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstQuoted(s string) string {
	i := strings.IndexByte(s, '"')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i+1:], '"')
	if j < 0 {
		return ""
	}
	return s[i+1 : i+1+j]
}

func unquote(s string) string {
	return strings.Trim(s, `"`)
}
