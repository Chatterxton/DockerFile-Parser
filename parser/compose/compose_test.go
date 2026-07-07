package compose

import (
	"reflect"
	"testing"
)

// fixture покрывает обе формы каждого «кривого» поля.
const fixture = `
services:
  web:
    image: nginx:alpine
    ports:
      - "80:80"
      - "443:443"
    depends_on:
      - api
    environment:
      API_URL: http://api:8000
    networks: [frontend]
  api:
    build: ./api
    depends_on:
      db:
        condition: service_healthy
    environment:
      - DATABASE_URL=postgres://db:5432/app
      - REDIS_HOST=redis
    links:
      - "db:database"
    networks:
      - frontend
      - backend
  db:
    image: postgres:15
    ports:
      - 5432
    networks: [backend]
  redis:
    image: redis:7
    networks: [backend]
`

func TestParseServiceCount(t *testing.T) {
	c, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatalf("Parse вернул ошибку: %v", err)
	}
	if len(c.Services) != 4 {
		t.Fatalf("ожидалось 4 сервиса, получили %d", len(c.Services))
	}
}

func TestParsePortsListForm(t *testing.T) {
	c, _ := Parse([]byte(fixture))
	got := []string(c.Services["web"].Ports)
	want := []string{"80:80", "443:443"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("web.Ports: ожидалось %v, получили %v", want, got)
	}
}

func TestParsePortNumericScalar(t *testing.T) {
	c, _ := Parse([]byte(fixture))
	got := []string(c.Services["db"].Ports)
	want := []string{"5432"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("db.Ports: ожидалось %v, получили %v", want, got)
	}
}

func TestParseDependsOnListForm(t *testing.T) {
	c, _ := Parse([]byte(fixture))
	got := []string(c.Services["web"].DependsOn)
	want := []string{"api"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("web.DependsOn: ожидалось %v, получили %v", want, got)
	}
}

func TestParseDependsOnMapForm(t *testing.T) {
	c, _ := Parse([]byte(fixture))
	got := []string(c.Services["api"].DependsOn)
	want := []string{"db"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("api.DependsOn (форма-карта): ожидалось %v, получили %v", want, got)
	}
}

func TestParseEnvironmentMapForm(t *testing.T) {
	c, _ := Parse([]byte(fixture))
	if got := c.Services["web"].Environment["API_URL"]; got != "http://api:8000" {
		t.Fatalf("web.Environment[API_URL]: ожидалось http://api:8000, получили %q", got)
	}
}

func TestParseEnvironmentListForm(t *testing.T) {
	c, _ := Parse([]byte(fixture))
	env := c.Services["api"].Environment
	if env["DATABASE_URL"] != "postgres://db:5432/app" {
		t.Fatalf("api DATABASE_URL: получили %q", env["DATABASE_URL"])
	}
	if env["REDIS_HOST"] != "redis" {
		t.Fatalf("api REDIS_HOST: получили %q", env["REDIS_HOST"])
	}
}

func TestParseNetworksListForm(t *testing.T) {
	c, _ := Parse([]byte(fixture))
	got := []string(c.Services["api"].Networks)
	want := []string{"frontend", "backend"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("api.Networks: ожидалось %v, получили %v", want, got)
	}
}

func TestParseLinks(t *testing.T) {
	c, _ := Parse([]byte(fixture))
	got := c.Services["api"].Links
	want := []string{"db:database"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("api.Links: ожидалось %v, получили %v", want, got)
	}
}
