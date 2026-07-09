// Package heuristic содержит эвристики, общие для всех парсеров: извлечение
// хостов из строк (env, connection strings), распознавание внешних сервисов и
// баз данных. Не зависит ни от compose, ни от helm.
package heuristic

import (
	"path"
	"regexp"
	"strings"
)

// dbImages — базовые имена образов, по которым сервис считается БД/хранилищем.
var dbImages = map[string]bool{
	"postgres": true, "postgresql": true, "mysql": true, "mariadb": true,
	"mongo": true, "mongodb": true, "redis": true, "memcached": true,
	"rabbitmq": true, "kafka": true, "elasticsearch": true, "cassandra": true,
	"clickhouse": true, "cockroachdb": true, "influxdb": true, "couchdb": true,
	"neo4j": true, "etcd": true, "nats": true,
}

// dbKeywords — подстроки в имени сервиса, выдающие БД (для Helm, где образ
// известен не всегда, но имя вида "release-postgresql" говорит само за себя).
var dbKeywords = []string{
	"postgres", "postgresql", "mysql", "mariadb", "mongo", "redis",
	"memcached", "rabbitmq", "kafka", "elastic", "cassandra", "clickhouse",
	"cockroach", "influxdb", "couchdb", "neo4j", "etcd", "nats",
}

// ExtractHosts достаёт имена хостов из значения (env-переменной, URL, списка).
// Разбивает по разделителям списков, сохраняя ':' (порт) внутри токена, и
// корректно вытаскивает хост из URL (включая user:pass@host).
func ExtractHosts(val string) []string {
	fields := strings.FieldsFunc(val, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == ';'
	})
	var hosts []string
	for _, f := range fields {
		if h := hostFromField(f); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// hostFromField достаёт хост из одного токена. Устойчив к «злым» строкам:
// схемам jdbc:x://host, префиксам KEY=..., паролям с '@' и '/' внутри.
func hostFromField(f string) string {
	if i := strings.Index(f, "://"); i >= 0 {
		// есть схема — берём всё после ://; пароль может содержать '/' и '@',
		// поэтому сначала берём хост после ПОСЛЕДНЕГО '@'.
		f = strings.TrimLeft(f[i+3:], "/") // dns:///host — пустой authority
		if j := strings.LastIndexByte(f, '@'); j >= 0 {
			f = f[j+1:]
		}
	} else {
		// нет схемы: отрезаем query/путь, затем KEY= и user@
		if j := strings.IndexByte(f, '?'); j >= 0 {
			f = f[:j]
		}
		if j := strings.IndexByte(f, '/'); j >= 0 {
			f = f[:j]
		}
		if j := strings.LastIndexByte(f, '='); j >= 0 {
			f = f[j+1:]
		}
		if j := strings.LastIndexByte(f, '@'); j >= 0 {
			f = f[j+1:]
		}
	}
	if i := strings.IndexByte(f, '/'); i >= 0 { // путь
		f = f[:i]
	}
	if i := strings.IndexByte(f, ':'); i >= 0 { // порт
		f = f[:i]
	}
	return f
}

var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)((?::?-)([^}]*))?\}`)

// ExpandEnv подставляет compose-переменные окружения: ${VAR:-default} и
// ${VAR-default} → default; ${VAR} и $VAR (значение неизвестно) → пусто.
func ExpandEnv(s string) string {
	s = envVarRe.ReplaceAllStringFunc(s, func(m string) string {
		g := envVarRe.FindStringSubmatch(m)
		if g[2] != "" { // была форма :-default или -default
			return g[3]
		}
		return ""
	})
	// голый $VAR без значения
	return regexp.MustCompile(`\$[A-Za-z_][A-Za-z0-9_]*`).ReplaceAllString(s, "")
}

// ResolveInternal сопоставляет хост со своим сервисом, понимая адреса
// Kubernetes: service, service.namespace, service.namespace.svc,
// service.ns.svc.cluster.local. Возвращает имя сервиса и true, если хост
// указывает на свой сервис из known.
//
// Публичные домены не сворачиваются: "api.stripe.com" остаётся внешним, даже
// если есть сервис "api" (остаток "stripe.com" многосоставной и не кластерный).
func ResolveInternal(host string, known map[string]bool) (string, bool) {
	if known[host] {
		return host, true
	}
	first, rest, ok := strings.Cut(host, ".")
	if !ok || !known[first] {
		return "", false
	}
	// first — известный сервис; считаем внутренним, если остаток похож на
	// namespace (одна метка) или на кластерный DNS-суффикс.
	if !strings.Contains(rest, ".") ||
		strings.HasSuffix(host, ".svc") ||
		strings.HasSuffix(host, ".cluster.local") ||
		strings.Contains(host, ".svc.") {
		return first, true
	}
	return "", false
}

var validHostRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// IsValidHost проверяет, что строка похожа на реальное DNS-имя. Отсекает мусор
// из shell-команд и JSON (`.`, `secrets..."`, `{"orders"` и т.п.).
func IsValidHost(h string) bool {
	return h != "" && len(h) <= 253 && validHostRe.MatchString(h)
}

// IsExternalHost: хост внешний, если похож на доменное имя (есть точка) или
// матчит один из настроенных glob-паттернов.
func IsExternalHost(host string, patterns []string) bool {
	if strings.Contains(host, ".") {
		return true
	}
	for _, pat := range patterns {
		if ok, _ := path.Match(pat, host); ok {
			return true
		}
	}
	return false
}

// IsDatabaseImage распознаёт БД по имени docker-образа (postgres:15 → true).
func IsDatabaseImage(image string) bool {
	if image == "" {
		return false
	}
	base := image
	if i := strings.LastIndexByte(base, '/'); i >= 0 { // registry/repo/name → name
		base = base[i+1:]
	}
	if i := strings.IndexByte(base, ':'); i >= 0 { // name:tag → name
		base = base[:i]
	}
	return dbImages[strings.ToLower(base)]
}

// IsDatabaseName распознаёт БД по имени сервиса (release-postgresql → true).
func IsDatabaseName(name string) bool {
	low := strings.ToLower(name)
	for _, kw := range dbKeywords {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}
