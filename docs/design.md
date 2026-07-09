# DockerFile-Parser — инструмент визуализации зависимостей сервисов

**Дата:** 2026-07-05

## Цель

Веб-сервис, который парсит `docker-compose.yaml` и Helm-чарты и строит наглядную
схему зависимостей: какой сервис с каким общается по сети. Глядя на схему, за
несколько минут понятно, с какими компонентами общается выбранное приложение.

## Решения

| Вопрос | Решение |
|--------|---------|
| Язык / стек | **Go** (stdlib: `net/http`, `embed`; `gopkg.in/yaml.v3`; без веб-фреймворка) |
| Форма | **Веб-сервис**: HTTP API + страница, граф рисуется Mermaid.js в браузере |
| Входные форматы | **docker-compose** и **Helm** |
| Определение связей | Максимум: `depends_on`/`links` + эвристика по `environment` + `ports` + `networks` |
| Внешние сервисы | Авто (хост есть в ссылках, но нет как сервис ⇒ внешний) + настраиваемые паттерны |
| Основной вывод | **Mermaid** (в браузере); `.dot` (Graphviz) — экспорт |

## Архитектура

Конвейер данных в одну сторону:

```
YAML → parser → model.Graph → analyze (эвристики) → render → Mermaid/DOT
                    ↓
                  JSON ──→ веб-страница рисует Mermaid.js
```

### Пакеты

- **`model`** — структура графа (узлы, рёбра, группы). Формат-независимая. Контракт JSON.
- **`parser/compose`** — docker-compose → `model.Graph`.
- **`parser/helm`** — Helm → `model.Graph` (тот же интерфейс).
- **`analyze`** — эвристики: поиск хостов в env, классификация внешних, группировка по сетям.
- **`heuristic`** — общие эвристики (хосты из строк, внешние сервисы, распознавание БД).
- **`render/mermaid`**, **`render/dot`** — чистые функции `Graph → string`.
- **`web`** — HTTP-хендлеры + HTML-страница (ассеты вшиты через `//go:embed`, mermaid.js локально).
- **`cmd/dockerfile-parser`** — `main`, поднимает сервер.

Разделение ответственностей: парсинг, эвристики и рендер тестируются независимо на примерах
YAML; Helm подключается как второй парсер, не затрагивая остальное.

## Модель данных (контракт JSON)

```go
type Graph struct {
    Nodes  []Node  `json:"nodes"`
    Edges  []Edge  `json:"edges"`
    Groups []Group `json:"groups"` // сети как группы
}
type Node struct {
    ID       string   `json:"id"`       // имя сервиса
    Label    string   `json:"label"`
    Kind     string   `json:"kind"`     // "service" | "database" | "external" | "entry"
    Image    string   `json:"image,omitempty"`
    Ports    []string `json:"ports,omitempty"`
    External bool     `json:"external"`
}
type Edge struct {
    From   string   `json:"from"`
    To     string   `json:"to"`
    Types  []string `json:"types"`  // ["depends_on","network"] — может быть несколько
    Detail string   `json:"detail"` // напр. имя env-переменной
}
type Group struct {
    Name  string   `json:"name"`  // имя сети
    Nodes []string `json:"nodes"`
}
```

## Логика определения связей (docker-compose)

Для каждого сервиса собираем рёбра из четырёх источников, дедуплицируем по паре `(from,to)`,
**объединяя типы** в одном ребре:

1. `depends_on` → тип `depends_on` (порядок запуска, пунктиром). Формы: список и карта.
2. `links` → тип `link` (учитываем алиасы `service:alias`).
3. `environment` — главная эвристика: значение каждой переменной разбиваем на токены
   (по `/ : @ , ; пробел`), **вытаскиваем хост из URL**. Токен сверяем со списком **известных
   имён сервисов**. Совпало → тип `network` (сплошная стрелка), в `detail` — имя переменной.
   Сверка только с именами сервисов из файла резко снижает ложные срабатывания.
   Учитываем обе формы `environment`: список `KEY=val` и карту `KEY: val`.
4. Токены, похожие на хост (есть точка или матчат паттерн), но не являющиеся сервисом →
   узел `external` + ребро.

`ports` (опубликованные) идут в подпись узла и помечают доступность снаружи.

### Пример

```yaml
services:
  web:   { image: nginx, ports: ["80:80"], depends_on: [api],
           environment: { API_URL: http://api:8000 } }
  api:   { build: ./api,
           environment: { DATABASE_URL: postgres://db:5432/app,
                          REDIS_HOST: redis, PAY: https://api.stripe.com } }
  db:    { image: postgres:15 }
  redis: { image: redis:7 }
```

Ожидаемый граф: web →(API_URL, depends_on) api; api →(DATABASE_URL) db; api →(REDIS_HOST) redis;
api →(PAY) api.stripe.com (внешний).

## Внешние сервисы и сети

- **Внешние:** базовое правило — на хост ссылаются, но такого сервиса нет ⇒ внешний.
  Плюс `config` с паттернами (`*.rds.amazonaws.com`, `*.internal`) для классификации и ловли
  хостов без точки. Рисуются другой формой/цветом.
- **Сети → группировка, а не рёбра.** Сервисы с общей сетью оборачиваем в подграф-рамку.
  Правило против каши: сеть, куда входят **все** сервисы (дефолтная), как рамку не рисуем.

## Веб-слой и «фокус на сервисе»

- `GET /` — страница: поле для вставки/загрузки YAML + кнопка «Построить».
- `POST /api/graph` — тело: YAML (+ формат, + опц. конфиг) → JSON `{graph, mermaid, dot}`.
- **Фокус:** выпадающий список сервисов; выбор подсвечивает узел и соседей, остальное
  приглушает; переключатель «только соседи» (1 хоп). Кнопки экспорта SVG/PNG/`.dot`.

## Helm (кратко)

Тот же выход `model.Graph`, другой парсер: `Chart.yaml` + `values.yaml`, в `templates/*.yaml`
ищем `kind: Service` (узлы) и ссылки на хосты в env/configmap. Динамические имена
(`{{ .Release.Name }}-db`) — «умное» сопоставление: плейсхолдер релиза + нормализация имён
срезанием префикса релиза. `{{ .Values.x }}` резолвим по `values.yaml`; неразрешённое помечаем.
Best-effort; сложные случаи разбираются по месту.

## Тестирование

TDD, table-driven тесты Go:
- `parser` + `analyze`: фикстуры compose-файлов (web+api+db+redis+внешний RDS) → проверка
  набора узлов и рёбер.
- `render`: golden-файлы (`Graph → ожидаемый Mermaid/DOT`).

## План реализации

1. Ядро: model → parser/compose → analyze → render/mermaid+dot → web со страницей и фокусом.
   Рабочий веб-сервис для docker-compose.
2. parser/helm.
3. Точки входа (интернет → сервисы), экспорт SVG/PNG, настройка паттернов в UI.
