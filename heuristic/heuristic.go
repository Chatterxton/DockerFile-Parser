// Package heuristic содержит эвристики, общие для всех парсеров: извлечение
// хостов из строк (env, connection strings), распознавание внешних сервисов и
// баз данных. Не зависит ни от compose, ни от helm.
package heuristic

import (
	"net/url"
	"path"
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

func hostFromField(f string) string {
	if strings.Contains(f, "://") {
		if u, err := url.Parse(f); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	if i := strings.IndexByte(f, '/'); i >= 0 { // отрезаем путь
		f = f[:i]
	}
	if i := strings.LastIndexByte(f, '@'); i >= 0 { // user:pass@host
		f = f[i+1:]
	}
	if i := strings.IndexByte(f, ':'); i >= 0 { // host:port
		f = f[:i]
	}
	return f
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
