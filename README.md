# depgraph — визуализатор зависимостей сервисов

Веб-сервис на Go, который парсит `docker-compose.yaml` **и Helm-чарты** и строит
наглядную схему зависимостей: **какой сервис с каким общается по сети**. Глядя на
схему, новый сотрудник за несколько минут понимает, с какими компонентами связано
приложение.

> Производственная практика. Фаза 1 (docker-compose) и Фаза 2 (Helm) — готовы.

## Возможности

- **Два формата на входе:** `docker-compose.yaml` и Helm-чарты (шаблоны + values).
- Разбор `docker-compose.yaml` во всех «кривых» формах YAML (списки/карты для
  `depends_on`, `environment`, `networks`, длинная форма портов).
- Определение связей из **четырёх** источников:
  - `depends_on` и `links` — явные (порядок запуска), рисуются пунктиром;
  - **эвристика по `environment`** — хост из `DATABASE_URL=postgres://db:5432/app`
    или `REDIS_HOST=redis` сопоставляется с именем сервиса → реальная сетевая
    связь, сплошная стрелка с подписью-переменной;
  - `ports` — опубликованные порты в подписи узла.
- **Внешние сервисы** (`api.stripe.com`, `*.rds.amazonaws.com`) отличаются
  автоматически (на хост ссылаются, но сервиса в файле нет) + настраиваемые
  паттерны. Рисуются отдельной формой.
- **Базы/хранилища** (postgres, redis, mysql, kafka, …) опознаются по образу.
- **Сети** становятся рамками-группами (а не рёбрами «каждый-с-каждым»).
- **Точки входа** (взгляд «снаружи внутрь»): узел «🌐 интернет» указывает на
  сервисы, доступные извне — опубликованные `ports` в compose, ресурсы `Ingress`
  и сервисы `LoadBalancer`/`NodePort` в Helm.
- **Фокус на сервисе**: выбор сервиса подсвечивает его и соседей, режим
  «только соседи» оставляет одну окрестность — под требование «понять за 5 минут».
- Вывод: **Mermaid** (рисуется в браузере) + экспорт картинки одним кликом —
  **SVG**, **PNG** и **Graphviz `.dot`**.

### Helm

Helm-чарт не рендерится полноценным `helm template` (не нужен установленный Helm) —
делается best-effort подстановка:

- `{{ .Release.Name }}`, `{{ .Chart.Name }}`, `{{ .Values.path.to.key }}`
  (значения берутся из `values.yaml`), `{{ include "*.fullname" . }}` →
  `<release>-<chart>`, `default "x"` из пайпов;
- директивы управления (`{{ if }}`, `{{ range }}`, `{{ end }}`) убираются;
- узлы — ресурсы `Service`/`Deployment`/`StatefulSet`/…; **динамические имена**
  вида `{{ .Release.Name }}-db` подставляются и сопоставляются между собой;
- связи — из `env` контейнеров, а также из `envFrom` и `valueFrom` (значения
  подтягиваются из `ConfigMap.data` и `Secret.stringData`);
- **адреса Kubernetes** сворачиваются к своему сервису: `db`, `db.namespace`,
  `db.namespace.svc.cluster.local` → один узел `db` (публичные домены вроде
  `api.stripe.com` при этом остаются внешними).

Ограничение: это эвристика, а не полный движок Helm — сложные `range`/`with` и
`_helpers.tpl` обрабатываются приблизительно.

## Запуск

```bash
go run ./cmd/depgraph            # http://localhost:8080
go run ./cmd/depgraph -addr :9000
```

Откройте http://localhost:8080 — страница уже содержит пример. Вставьте свой
`docker-compose.yaml` или загрузите файл и нажмите «Построить схему».

Бинарник самодостаточный: `mermaid.js` вшит в него через `go:embed`, интернет
для работы не нужен.

```bash
go build -o depgraph.exe ./cmd/depgraph   # один исполняемый файл
```

## Развёртывание на сервере

Сервис упакован в Docker-образ. Бинарник самодостаточный (`mermaid.js` вшит через
`go:embed`, внешних зависимостей нет), поэтому образ получается крошечным:
многоступенчатая сборка + distroless-база.

### 1. Dockerfile

В корне репозитория — [`Dockerfile`](Dockerfile):

```dockerfile
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /depgraph ./cmd/depgraph

FROM gcr.io/distroless/static-debian12
COPY --from=build /depgraph /depgraph
EXPOSE 8080
ENTRYPOINT ["/depgraph"]
```

### 2. Сборка и запуск контейнера

```bash
docker build -t depgraph .
docker run -d --name depgraph --restart unless-stopped \
  -p 127.0.0.1:8080:8080 depgraph
```

Контейнер слушает `8080` и публикуется только на localhost — наружу его выставит
nginx.

### 3. nginx как обратный прокси

`/etc/nginx/sites-available/depgraph` (затем symlink в `sites-enabled/`):

```nginx
server {
    listen 80;
    listen [::]:80;
    server_name depgraph.example.com;

    # compose-файлы и Helm-чарты бывают большими — поднимаем лимит тела запроса
    client_max_body_size 5m;

    # статика (mermaid.js ~3 МБ) — отдаём с длинным кэшем
    location /assets/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        expires 30d;
        add_header Cache-Control "public, immutable";
    }

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

```bash
sudo ln -s /etc/nginx/sites-available/depgraph /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

### 4. HTTPS (по желанию)

```bash
sudo certbot --nginx -d depgraph.example.com
```

certbot сам добавит блок на `443` и редирект с `http`. После этого сервис доступен
по `https://depgraph.example.com`.

## API

`POST /api/graph`

```json
{ "yaml": "services:\n  ...", "format": "compose",
  "externalPatterns": ["*.rds.amazonaws.com"],
  "focus": "api", "neighborsOnly": false }
```

Для Helm: `"format": "helm"`, шаблоны — в `yaml`, а также поля `values`
(содержимое `values.yaml`), `release` и `chartName`:

```json
{ "format": "helm", "yaml": "kind: Service\n...", "values": "image:\n  tag: 1.0",
  "release": "myapp", "chartName": "mychart" }
```

Ответ:

```json
{ "graph": { "nodes": [...], "edges": [...], "groups": [...] },
  "mermaid": "graph LR\n ...", "dot": "digraph deps { ... }" }
```

## Архитектура

Данные текут в одну сторону; каждый пакет — одна ответственность:

```
compose ─→ parser/compose ─→ analyze ─┐
                                       ├─→ model.Graph ─→ render/{mermaid,dot}
helm ────→ parser/helm ───────────────┘         ↓
                                        JSON ─→ страница (mermaid.js)
        общие эвристики: пакет heuristic
```

| Пакет | Ответственность |
|-------|-----------------|
| `model` | Граф (узлы, рёбра, группы); слияние рёбер; окрестность узла |
| `heuristic` | Общее: хосты из строк, внешние сервисы, распознавание БД |
| `parser/compose` | docker-compose → нормализованная структура |
| `parser/helm` | Helm (шаблоны + values) → `model.Graph` (best-effort рендер) |
| `analyze` | Compose → граф: связи из env, внешние, группы-сети |
| `render/mermaid` | Граф → Mermaid (+ подсветка фокуса) |
| `render/dot` | Граф → Graphviz DOT |
| `web` | HTTP-сервер, страница, API |
| `cmd/depgraph` | Точка входа |

## Тесты

```bash
go test ./...
```

Table-driven тесты на парсер и эвристики, golden-тесты на рендеры.

## Примеры

- `examples/docker-compose.yaml` — веб-приложение с БД, кэшем, брокером и внешними
  API (Stripe, SendGrid). Показывает все типы узлов и связей.
- `examples/helm/` — минимальный Helm-чарт (`templates.yaml` + `values.yaml`) с
  динамическими именами `{{ .Release.Name }}-*`.

## Возможные улучшения

Все три фазы (compose, Helm, точки входа + экспорт SVG/PNG) реализованы. Дальше
можно: рендерить `.dot` в PNG прямо на сервере через Graphviz, если он установлен;
хранить историю схем; сравнивать две версии compose/чарта (diff зависимостей).
См. [спецификацию](docs/superpowers/specs/2026-07-05-depgraph-design.md).
