# DockerFile-Parser — визуализатор зависимостей сервисов

Веб-сервис на Go, который парсит `docker-compose.yaml` **и Helm-чарты** и строит
наглядную схему зависимостей: **какой сервис с каким общается по сети**. Глядя на
схему, новый сотрудник за несколько минут понимает, с какими компонентами связано
приложение.

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
go run ./cmd/dockerfile-parser            # http://localhost:8080
go run ./cmd/dockerfile-parser -addr :9000
```

Откройте http://localhost:8080 — страница уже содержит пример. Вставьте свой
`docker-compose.yaml` или загрузите файл и нажмите «Построить схему».

Бинарник самодостаточный: `mermaid.js` вшит в него через `go:embed`, интернет
для работы не нужен.

```bash
go build -o dockerfile-parser.exe ./cmd/dockerfile-parser   # один исполняемый файл
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
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /dockerfile-parser ./cmd/dockerfile-parser

FROM gcr.io/distroless/static-debian12
COPY --from=build /dockerfile-parser /dockerfile-parser
EXPOSE 8080
ENTRYPOINT ["/dockerfile-parser"]
```

### 2. Сборка и запуск контейнера

```bash
docker build -t dockerfile-parser .
docker run -d --name dockerfile-parser --restart unless-stopped \
  -p 127.0.0.1:8080:8080 dockerfile-parser
```

Контейнер слушает `8080` и публикуется только на localhost — наружу его выставит
nginx.

### 3. nginx с HTTPS

Работаем только по HTTPS: весь HTTP-трафик редиректится на `443`.
`/etc/nginx/sites-available/dockerfile-parser` (затем symlink в `sites-enabled/`):

```nginx
# HTTP — только ACME-проверка Let's Encrypt и редирект на HTTPS
server {
    listen 80;
    listen [::]:80;
    server_name dockerfile-parser.example.com;

    location /.well-known/acme-challenge/ { root /var/www/certbot; }
    location /                            { return 301 https://$host$request_uri; }
}

# HTTPS
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name dockerfile-parser.example.com;

    ssl_certificate     /etc/letsencrypt/live/dockerfile-parser.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/dockerfile-parser.example.com/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;
    # браузер запомнит, что сюда только по HTTPS
    add_header Strict-Transport-Security "max-age=31536000" always;

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

### 4. Сертификат Let's Encrypt

Домен должен указывать на сервер. Сертификата при первом запуске ещё нет — блок
`443` не пройдёт `nginx -t`, поэтому сначала получаем сертификат (при первом выпуске
временно оставьте активным только HTTP-блок):

```bash
sudo apt install certbot
sudo mkdir -p /var/www/certbot
sudo certbot certonly --webroot -w /var/www/certbot -d dockerfile-parser.example.com
sudo ln -s /etc/nginx/sites-available/dockerfile-parser /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

Автопродление certbot ставит сам (systemd-таймер). Чтобы nginx подхватывал новый
сертификат после продления, добавьте hook:

```bash
sudo certbot renew --deploy-hook "systemctl reload nginx" --dry-run
```

Готово — сервис доступен по `https://dockerfile-parser.example.com`.

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
| `cmd/dockerfile-parser` | Точка входа |

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

- рендер `.dot` в PNG прямо на сервере через Graphviz (если установлен);
- история построенных схем;
- diff двух версий compose/чарта — что изменилось в зависимостях.

Детали архитектуры — в [дизайн-документе](docs/design.md).
