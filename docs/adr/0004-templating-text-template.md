# ADR-0004: Templating — `text/template` stdlib + обязательный FuncMap safeguards

- **Статус:** Accepted
- **Дата:** 2026-04-23
- **Автор:** architect
- **Связанные ADR:** [ADR-0003](0003-report-format-markdown.md), [ADR-0010](0010-internal-module-structure.md).

## Контекст

Нужно рендерить:
1. `.md`-файлы отчётов (plain-text output, данные из логов).
2. HTML-cover для Telegram (parse_mode=HTML, ограниченный tags-set).

Данные — **user-controlled** (контент логов, имена app, выдержки `_msg`): возможен injection в URL, HTML, Markdown. Требуется предсказуемое escaping.

Stdlib предоставляет два шаблонизатора: `text/template` (нейтральный) и `html/template` (HTML-aware, auto-escaping). Есть внешние (templ, hermes, pug и т.п.).

## Решение

**`text/template`** для всех шаблонов проекта — единая ментальная модель, одинаковая работа FuncMap, один lint.

### Safeguards (обязательные, не опциональные)

FuncMap на каждый шаблон:
```go
funcMap := template.FuncMap{
    "safeURL":  url.QueryEscape,                                  // для query-параметров
    "panesJSON": func(v any) string {                             // JSON → URL-encoded для Grafana
        b, _ := json.Marshal(v)
        return url.QueryEscape(string(b))
    },
    "tgText": html.EscapeString,                                  // для HTML-cover (parse_mode=HTML)
    "mdEscape": markdownEscape,                                   // для .md (экранировать `*_[]`)
    "trim":    strings.TrimSpace,
    "truncate": truncate,                                         // для выдержек _msg
}
```

### Правила шаблона

1. **Ни один скаляр пользовательских данных не выводится напрямую.** Только через FuncMap: `{{ .Msg | tgText }}`, `{{ .Host | mdEscape }}`.
2. **URL — всегда через `safeURL`/`panesJSON`.** Ручной `{{ .URL }}` в `href=` — lint error.
3. **`html/template` не используется** (вместо него — наш ручной `tgText`). Причина: `html/template` делает auto-escape, но для TG HTML мы поддерживаем **только** ограниченный tag-set (`<b>`, `<i>`, `<code>`, `<pre>`, `<a>`). `html/template` знает весь HTML, это лишняя сложность.
4. **Шаблоны — в `internal/render/templates/*.tmpl`**, не inline-строки. CI-lint проверяет, что все `.tmpl` парсятся + fuzz-тесты на злонамеренный контент.

### Структура

```
internal/render/templates/
  cover.tmpl            # HTML для TG sendMessage
  host_report_md.tmpl   # .md для sendDocument
  (будущее: host_report_html.tmpl)
```

## Последствия

**Плюсы**
- Нулевая внешняя зависимость.
- Явный контроль escaping — аудируемо, lint-проверяемо.
- `text/template` покрыт тестами в stdlib, стабилен десятилетия.
- Один шаблонизатор на всё — меньше когнитивная нагрузка.

**Минусы / trade-offs**
- Нужно ручное определение FuncMap и его дисциплинированное применение. Минус митигируется CI-линтером.
- Нет compile-time type safety (`templ`, `a-h/templ` — даёт, но это 3rd-party + code generation).
- HTML-для-TG мы эскейпим сами, а не `html/template`. Больше ответственности. Компенсируется тестами fuzz + ручной аудит.

## Альтернативы

1. **`html/template` (stdlib).** Отвергнуто: auto-escaping для полного HTML-контекста избыточен и вводит в заблуждение — TG принимает **урезанный** HTML, и автoescape может конфликтовать с нашим манипулированием (`<pre>…</pre>` с уже экранированным содержимым). Для UI v0.3 — возможный кандидат, решим отдельно.
2. **`a-h/templ`.** Отвергнуто: code-gen шаг, `.templ` → `.go`, type-safe — красиво, но это ещё одна зависимость, build step, и мы генерируем **текст** (md), а `templ` в первую очередь HTML-ориентирован.
3. **`pug`/`jet`/`amber`.** Отвергнуто: экзотика, никто в команде не знает, нет нужды.
4. **Ручная конкатенация/`fmt.Sprintf`.** Отвергнуто: невозможно поддерживать, injection-прон, нечитаемо.

## Ссылки

- `pkg.go.dev/text/template`, `pkg.go.dev/html/template`.
- `docs/plans/10-architecture.md §5.5`, §12.2.
