package heuristic

import "testing"

func TestResolveInternal(t *testing.T) {
	known := map[string]bool{"db": true, "myapp-db": true, "api": true, "redis": true}

	cases := []struct {
		host     string
		wantName string
		wantOK   bool
	}{
		{"db", "db", true},                                       // короткое имя
		{"db.default", "db", true},                               // service.namespace
		{"db.default.svc", "db", true},                           // сокращённый FQDN
		{"db.default.svc.cluster.local", "db", true},             // полный FQDN
		{"myapp-db.default.svc.cluster.local", "myapp-db", true}, // имя с дефисом
		{"redis.svc.cluster.local", "redis", true},               // без namespace
		{"api.stripe.com", "", false},                            // публичный домен, не сворачиваем
		{"smtp.sendgrid.net", "", false},                         // публичный домен
		{"unknown.default.svc", "", false},                       // неизвестный сервис
		{"kafka", "", false},                                     // не в known
	}
	for _, c := range cases {
		gotName, gotOK := ResolveInternal(c.host, known)
		if gotOK != c.wantOK || gotName != c.wantName {
			t.Errorf("ResolveInternal(%q) = (%q,%v), ожидалось (%q,%v)",
				c.host, gotName, gotOK, c.wantName, c.wantOK)
		}
	}
}

func TestIsExternalHost(t *testing.T) {
	if !IsExternalHost("api.stripe.com", nil) {
		t.Error("домен с точкой должен быть внешним")
	}
	if IsExternalHost("redis", nil) {
		t.Error("голое имя без точки не внешнее")
	}
	if !IsExternalHost("mydb", []string{"mydb", "*.rds.amazonaws.com"}) {
		t.Error("совпадение с паттерном должно давать внешний")
	}
}

func TestIsDatabaseImageAndName(t *testing.T) {
	if !IsDatabaseImage("postgres:15") || !IsDatabaseImage("bitnami/redis:7") {
		t.Error("образы БД должны распознаваться")
	}
	if IsDatabaseImage("nginx:alpine") {
		t.Error("nginx не БД")
	}
	if !IsDatabaseName("myapp-postgresql") || !IsDatabaseName("release-redis") {
		t.Error("имена с ключевыми словами БД должны распознаваться")
	}
	if IsDatabaseName("myapp-web") {
		t.Error("web не БД")
	}
}
