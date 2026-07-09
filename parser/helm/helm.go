// Package helm разбирает Helm-чарты (шаблоны + values) в граф зависимостей.
// Полноценный рендер (helm template) не выполняется — вместо этого делается
// best-effort подстановка .Release/.Chart/.Values и распознавание ресурсов.
package helm

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
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
	text = expandRanges(text, values) // развернуть циклы по массивам до подстановки
	return tmplRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := tmplRe.FindStringSubmatch(m)
		return resolveExpr(sub[1], chartName, release, values)
	})
}

// expandRanges разворачивает {{ range [$i,] $e := .Values.LIST }} BODY {{ end }}
// в конкатенацию BODY по каждому элементу массива LIST из values. Ссылки на
// переменную цикла внутри тела ($e.field → .Values.LIST.N.field, $i → N)
// переписываются в обычные .Values-выражения, которые доберёт основной рендер.
// Нужно, чтобы хосты из элементов массива (шарды БД и т.п.) попадали в граф.
// Директивы управления без цикла ({{ if }}/{{ with }}) не трогаем — их снимает
// resolveExpr. Вложенные range разворачиваются последующими итерациями.
func expandRanges(text string, values map[string]string) string {
	for iter := 0; iter < 1000; iter++ { // предохранитель от кривых шаблонов
		tokens := tmplRe.FindAllStringSubmatchIndex(text, -1)
		ri := -1
		for i, tk := range tokens {
			if firstWord(text[tk[2]:tk[3]]) == "range" {
				ri = i
				break
			}
		}
		if ri < 0 {
			break
		}
		// ищем парный {{ end }} с учётом вложенности блочных директив
		depth, ei := 0, -1
		for i := ri; i < len(tokens); i++ {
			switch firstWord(text[tokens[i][2]:tokens[i][3]]) {
			case "range", "if", "with", "define", "block":
				depth++
			case "end":
				depth--
				if depth == 0 {
					ei = i
				}
			}
			if ei >= 0 {
				break
			}
		}
		if ei < 0 {
			break // нет парного end — оставляем как есть, resolveExpr снимет скобки
		}
		header := strings.TrimSpace(text[tokens[ri][2]:tokens[ri][3]])
		body := text[tokens[ri][1]:tokens[ei][0]]
		expanded := expandOneRange(header, body, values)
		text = text[:tokens[ri][0]] + expanded + text[tokens[ei][1]:]
	}
	return text
}

// expandOneRange разворачивает тело одного range-блока по всем индексам массива.
func expandOneRange(header, body string, values map[string]string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(header, "range"))
	var idxVar, elemVar, collExpr string
	if i := strings.Index(rest, ":="); i >= 0 {
		vars := strings.Split(strings.TrimSpace(rest[:i]), ",")
		if len(vars) == 2 {
			idxVar, elemVar = strings.TrimSpace(vars[0]), strings.TrimSpace(vars[1])
		} else if len(vars) == 1 {
			elemVar = strings.TrimSpace(vars[0])
		}
		collExpr = strings.TrimSpace(rest[i+2:])
	} else {
		collExpr = rest // форма {{ range .Values.LIST }} — переменная цикла это "."
	}

	prefix, ok := valuesPath(collExpr)
	if !ok {
		return "" // коллекция не из .Values — развернуть нечем
	}
	idxs := collectIndices(prefix, values)
	if len(idxs) == 0 {
		return "" // пустой/неизвестный массив → пустой цикл
	}

	var sb strings.Builder
	for _, n := range idxs {
		b := body
		elemRef := ".Values." + prefix + "." + strconv.Itoa(n)
		if elemVar != "" {
			b = replaceVar(b, elemVar, elemRef)
		} else { // {{ range .Values.LIST }}: тело обращается к элементу через "."
			b = replaceElemDot(b, elemRef)
		}
		if idxVar != "" {
			b = replaceVar(b, idxVar, strconv.Itoa(n))
		}
		sb.WriteString(b)
		sb.WriteString("\n")
	}
	return sb.String()
}

// replaceVar заменяет переменную цикла ($e) на выражение-ссылку, но только как
// целый идентификатор (чтобы $e не задел $employee).
func replaceVar(body, name, repl string) string {
	re := regexp.MustCompile(regexp.QuoteMeta(name) + `\b`)
	return re.ReplaceAllString(body, repl)
}

// replaceElemDot подставляет ссылку на элемент в форме {{ range .Values.LIST }},
// где тело обращается к полям через ведущую точку: {{ .host }} → элемент.host.
// Заменяет только внутри шаблонных скобок и не трогает .Values/.Release/.Chart.
func replaceElemDot(body, elemRef string) string {
	return tmplRe.ReplaceAllStringFunc(body, func(m string) string {
		sub := tmplRe.FindStringSubmatch(m)
		e := strings.TrimSpace(sub[1])
		switch {
		case e == ".":
			return strings.Replace(m, ".", elemRef, 1)
		case strings.HasPrefix(e, ".") && !strings.HasPrefix(e, ".Values") &&
			!strings.HasPrefix(e, ".Release") && !strings.HasPrefix(e, ".Chart"):
			return strings.Replace(m, e, elemRef+e, 1)
		}
		return m
	})
}

// valuesPath вытаскивает из выражения путь ключа после .Values. (устойчив к
// обёрткам и пайпам: "(.Values.list)", ".Values.list | ...").
func valuesPath(expr string) (string, bool) {
	k := strings.Index(expr, ".Values.")
	if k < 0 {
		return "", false
	}
	s := expr[k+len(".Values."):]
	end := len(s)
	for i, r := range s {
		if !(r == '.' || r == '_' || r == '-' ||
			r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			end = i
			break
		}
	}
	return s[:end], true
}

// collectIndices собирает индексы массива values[prefix.N...] по возрастанию.
func collectIndices(prefix string, values map[string]string) []int {
	seen := map[int]bool{}
	p := prefix + "."
	for k := range values {
		if !strings.HasPrefix(k, p) {
			continue
		}
		seg := k[len(p):]
		if d := strings.IndexByte(seg, '.'); d >= 0 {
			seg = seg[:d]
		}
		if n, err := strconv.Atoi(seg); err == nil {
			seen[n] = true
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// firstWord возвращает первое слово выражения (ключевое слово директивы).
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
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
	val, ok := resolveMain(strings.TrimSpace(parts[0]), chartName, release, values)
	if !ok { // значение не разрешилось — ищем `default "X"` в пайпе
		for _, p := range parts[1:] {
			if p = strings.TrimSpace(p); strings.HasPrefix(p, "default ") {
				val, ok = unquote(strings.TrimSpace(p[len("default "):])), true
				break
			}
		}
	}
	if !ok {
		return ""
	}
	// пайпы форматирования (важно для tpl-инъекций в env)
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if n, ok := strings.CutPrefix(p, "nindent "); ok {
			val = "\n" + indentLines(val, atoiSafe(n))
		} else if n, ok := strings.CutPrefix(p, "indent "); ok {
			val = indentLines(val, atoiSafe(n))
		}
	}
	return val
}

// resolveMain разрешает основное выражение: простое .Values/.Release/.Chart,
// tpl (рекурсивный рендер значения) и index (доступ в массивы/карты).
func resolveMain(expr, chartName, release string, values map[string]string) (string, bool) {
	if v, ok := resolveValue(expr, chartName, release, values); ok {
		return v, true
	}
	if _, err := strconv.Atoi(expr); err == nil { // числовой литерал ({{ $i }} → N)
		return expr, true
	}
	if rest, ok := strings.CutPrefix(expr, "tpl "); ok {
		f := strings.Fields(rest)
		if len(f) >= 1 && strings.HasPrefix(f[0], ".Values.") {
			if raw, ok := values[f[0][len(".Values."):]]; ok {
				return Render(raw, chartName, release, values), true
			}
		}
		return "", false
	}
	if strings.HasPrefix(expr, "index ") || strings.HasPrefix(expr, "(index ") {
		inner, field := expr, ""
		if strings.HasPrefix(inner, "(") {
			if c := strings.IndexByte(inner, ')'); c > 0 {
				if after := strings.TrimSpace(inner[c+1:]); strings.HasPrefix(after, ".") {
					field = after[1:]
				}
				inner = strings.TrimSpace(inner[1:c])
			}
		}
		return resolveIndex(inner, field, values)
	}
	return "", false
}

// resolveIndex: "index .Values.a.b ARG..." → значение по ключу a.b.ARG...(.field).
// ARG может быть числом, строкой или другим .Values.* (динамический ключ).
func resolveIndex(inner, field string, values map[string]string) (string, bool) {
	f := strings.Fields(inner)
	if len(f) < 2 || f[0] != "index" || !strings.HasPrefix(f[1], ".Values.") {
		return "", false
	}
	key := f[1][len(".Values."):]
	for _, a := range f[2:] {
		a = strings.Trim(a, `"'`)
		if strings.HasPrefix(a, ".Values.") {
			v, ok := values[a[len(".Values."):]]
			if !ok {
				return "", false
			}
			a = v
		}
		key += "." + a
	}
	if field != "" {
		key += "." + field
	}
	v, ok := values[key]
	return v, ok
}

func indentLines(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i := range lines {
		if strings.TrimSpace(lines[i]) != "" {
			lines[i] = pad + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
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
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, val, out)
		}
	case []any:
		for i, val := range t { // индекс массива как ключ: shardedDatabases.0.host
			key := strconv.Itoa(i)
			if prefix != "" {
				key = prefix + "." + key
			}
			flatten(key, val, out)
		}
	default:
		if prefix != "" && v != nil {
			out[prefix] = fmt.Sprint(v)
		}
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
			for _, host := range heuristic.ExtractHosts(heuristic.ExpandEnv(ev.value)) {
				if host == "" || ignore[host] || !heuristic.IsValidHost(host) {
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
		// command/args — в них тоже прячутся хосты (init-контейнеры, healthcheck-скрипты)
		for _, key := range []string{"command", "args"} {
			if list, ok := c[key].([]any); ok {
				for _, item := range list {
					if s := str(item); s != "" {
						envs = append(envs, envPair{key, s})
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
