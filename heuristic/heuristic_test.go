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

func TestExtractHosts(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"http://auth-internal:8080", []string{"auth-internal"}},
		{"postgres://admin:p@ssw0rd_with/slashes@postgres-primary:5432/billing_db?sslmode=disable", []string{"postgres-primary"}},
		{"jdbc:postgresql://postgres-primary:5432/db", []string{"postgres-primary"}},
		{"-Dspring.datasource.url=jdbc:postgresql://postgres-primary:5432/db", []string{"postgres-primary"}},
		{"-Dremote.audit.host=audit-logger", []string{"audit-logger"}},
		{"kafka-node-1:9092,external-kafka.confluent.cloud:9092", []string{"kafka-node-1", "external-kafka.confluent.cloud"}},
		{"mongodb+srv://user:secret@mongo-replica-1:27017,mongo-replica-2:27017/users?replicaSet=rs0", []string{"mongo-replica-1", "mongo-replica-2"}},
		{"amqp://guest:guest@rabbitmq-broker:5672/vhost", []string{"rabbitmq-broker"}},
		{"dns:///billing-svc.default.svc.cluster.local:9090", []string{"billing-svc.default.svc.cluster.local"}},
	}
	for _, c := range cases {
		got := ExtractHosts(c.in)
		if len(got) != len(c.want) {
			t.Errorf("ExtractHosts(%q) = %v, ожидалось %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ExtractHosts(%q) = %v, ожидалось %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestExpandEnv(t *testing.T) {
	cases := [][2]string{
		{"https://${OAUTH_PROVIDER:-login.auth0.com}/oauth/token", "https://login.auth0.com/oauth/token"},
		{"${DB_HOST-postgres}", "postgres"},
		{"${UNSET}", ""},
		{"$BARE", ""},
		{"plain-host", "plain-host"},
	}
	for _, c := range cases {
		if got := ExpandEnv(c[0]); got != c[1] {
			t.Errorf("ExpandEnv(%q) = %q, ожидалось %q", c[0], got, c[1])
		}
	}
}

func TestIsValidHost(t *testing.T) {
	valid := []string{"redis", "consul-agent.infra", "ch-node-1.db", "vault-secret-server.security", "etcd-01.internal", "api.v1"}
	invalid := []string{"", ".", "secrets...\"", "{\"orders\"", "-Dspring", "a.", ".b", "has space"}
	for _, h := range valid {
		if !IsValidHost(h) {
			t.Errorf("IsValidHost(%q) = false, ожидалось true", h)
		}
	}
	for _, h := range invalid {
		if IsValidHost(h) {
			t.Errorf("IsValidHost(%q) = true, ожидалось false", h)
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
