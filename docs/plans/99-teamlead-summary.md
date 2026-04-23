# 99. Summary для TeamLead — `log_analyser` v0.1..v0.3

> **Цель документа:** за 5 минут дать PM/TL достаточный контекст, чтобы (1) закрыть блокирующие OQ, (2) утвердить roadmap, (3) принять спорные трейд-оффы там, где команда разделилась во мнениях.
>
> **Источник истины:** детальные планы по ролям — `docs/plans/00..13-*.md` + ADR в `docs/adr/0001..0013-*.md`. Ниже — выжимка.

## 0. TL;DR

**Задача:** ежедневно в 08:00 MSK формировать Telegram-отчёт об ошибках (`error` / `critical`) за 24 ч с 5 торговых серверов (`t1`, `ali-t1`, `t2`, `aws-t3`, `t5`) из VictoriaLogs. Cover + 5 файлов + Grafana Explore deeplinks. Issue: [QCoreTech/awesome#908](https://github.com/QCoreTech/awesome/issues/908).

**Что уже есть на момент сдачи плана (2026-04-23):**

- 5 ролевых планов (аналитик, архитектор, SRE, DevOps, QA) на ~240 КБ Markdown. Все рекомендации **проверены через Context7 MCP** (требование пользователя).
- 13 ADR'ов в `docs/adr/` со статусом (Accepted / Proposed).
- Рабочий каркас репо: `CLAUDE.md`, `README.md`, `CHANGELOG.md`, `VERSION=0.0.0`, `.env.example`, `.gitignore`, `.dockerignore`, `Dockerfile`, `.golangci.yml`, `.goreleaser.yaml`, `commitlint.config.js`.
- Рабочие GitHub Actions workflows (CI, release, commitlint, codeql, trivy, stale) — **валидированы YAML-парсером**, но требуют первого `main.go`, чтобы реально заработать.
- `deploy/docker-compose.dev.yml` + Prometheus + Grafana provisioning.
- `tests/README.md` + `tests/fixtures/README.md` со спецификацией фикстур.

**Стек, подтверждённый всеми ролями через Context7:**

Go 1.22+ • SQLite WAL на pure-Go драйвере `modernc.org/sqlite` • stdlib `log/slog` + `text/template` + `net/http` • `robfig/cron/v3` • `kelseyhightower/envconfig` + YAML overlay • собственный тонкий Telegram-клиент (~400 LOC) • Prometheus `client_golang` • Docker multi-stage (builder → `distroless/static:nonroot`) • GitHub Actions + goreleaser + cosign keyless + SBOM.

## 1. Фазированный roadmap

| Версия | Scope | Целевой срок | Критерий перехода |
|--------|-------|--------------|-------------------|
| **v0.1.0** (MVP) | Daemon + scheduler 08:00 MSK + VL-ingest (5 хостов) + dedup по fingerprint'у + рендер `.md` + TG delivery (cover + `sendMediaGroup`) + Grafana deeplinks + SQLite state + idempotency + Prometheus/healthchecks + Docker + GitHub CI/CD. Без LLM, без realtime, без прокси, без UI. | 2 спринта (≈ 4 нед) после закрытия блокеров OQ | 7 дней подряд успешных daily deliveries + ручная сверка 1 отчёта с VL + все 5 deeplinks кликабельны + CI зелёный + sign-off TL. |
| **v0.2.0** | Realtime алертинг на новые `critical`-fingerprint'ы с throttle + прокси (SOCKS5/HTTP) для TG **и** VL (отдельные env) + опциональное LLM-резюме под флагом + staging environment. | 2 спринта после v0.1 GA | 2 недели стабильно; false-positive rate realtime < 10%; Security sign-off на LLM-pipeline. |
| **v0.3.0** | Read-only UI в том же бинарнике (`embed.FS` + SSR через `html/template`), фильтры (host/app/level/pattern), ретроспектива за 30 дней, export `.md`. | 3 спринта после v0.2 | 100% покрытие данных из state за 30 дней; auth-контур определён в ADR. |

**Предварительная оценка трудозатрат** (с учётом плотности уже накопленного плана):
- v0.1.0 — **~20 man-days** (1 senior Go dev + поддержка SRE/DevOps по мере CI/CD).
- v0.2.0 — **~12 man-days**.
- v0.3.0 — **~15 man-days** (включая UI).

Итого до полной функциональности — ~10 недель календаря при команде из 1–1.5 Go-инженеров.

## 2. Блокеры OQ — требуют решения TL **до старта кода**

| OQ | Вопрос | Статус | Блокирует |
|----|--------|--------|-----------|
| **OQ-1** | URL VictoriaLogs + схема авторизации. | ✅ **Закрыт (2026-04-23):** VL находится внутри корпоративной сети, **авторизация не требуется**. `VL_URL` задаётся через env (см. `.env.example`); `VL_BASIC_USER` / `VL_BASIC_PASS` остаются пустыми. Daemon должен деплоиться в той же сети. | — |
| **OQ-2** | Нужен ли прокси для исходящих запросов к VL? | ✅ **Закрыт (2026-04-23):** прокси для VL **не требуется** (VL в той же корпсети, что и сервис). `VL_PROXY_URL` остаётся в env как optional — не задаётся. | — |
| **OQ-3** | Grafana base URL, `orgId`, **UID datasource VictoriaLogs**, `type` плагина. | ✅ **Закрыт (2026-04-23):** TL выполнил `GET /api/datasources`, найден единственный рабочий VL-datasource (`name="VictoriaLogs"`, тип `victoriametrics-logs-datasource`, `orgId=1`). Конкретные значения записаны в локальный `.env` (gitignored, вне коммита). В репозитории публикуются только имена env-переменных. | — |
| **OQ-4** | Формат файла отчёта. | ✅ **Принято:** `.md` по умолчанию (подтверждено TL). Опции `.html` / `.txt` доступны через `REPORT_FORMAT`. | — |
| **OQ-5** | Поля `host` / `app` / `level` — стрим или нет? | 🔴 **Открыт:** требует подтверждения от SRE/VL-admin. Preflight `/select/logsql/stream_field_names` при старте daemon снимет вопрос автоматически (см. R-10 в `00-analysis.md`). | v0.1 collector (perf) — не критично для старта разработки |
| **OQ-8** | TG-доставка — один чат или threads? | 🔴 **Открыт:** требует решения. Предложение команды — один чат в v0.1, threads в v0.2 под realtime. | v0.1 delivery, v0.2 realtime |

Остальные 10 OQ (OQ-6, 7, 9–15) не блокируют старт v0.1 и будут закрываться по ходу — их адресаты и тайминг см. `docs/plans/00-analysis.md §9`.

### 2.1. Команда для получения Grafana datasource UID

После получения URL Grafana и токена API, выполнить одну из команд (подставив значения):

```bash
# Bearer token (Service account / API key):
curl -sS -H "Authorization: Bearer $GRAFANA_TOKEN" \
  "$GRAFANA_URL/api/datasources" \
  | jq '.[] | select(.type | test("victoria")) | {name, uid, type, url, orgId: (.orgId // 1)}'

# Basic auth:
curl -sS -u "$GRAFANA_USER:$GRAFANA_PASS" \
  "$GRAFANA_URL/api/datasources" \
  | jq '.[] | select(.type | test("victoria")) | {name, uid, type, url, orgId: (.orgId // 1)}'
```

Ожидаемый ответ — `uid` пропишется в env `GRAFANA_VL_DS_UID`, `type` — в `GRAFANA_VL_DS_TYPE` (обычно `victoriametrics-logs-datasource`).

> **Замечание по результату API (2026-04-23):** у TL в Grafana зарегистрированы **два** datasource'а типа `victoriametrics-logs-datasource` — один рабочий (`name="VictoriaLogs"`, `url` заполнен), один с пустым `url` (возможно, остаток после теста плагина). В конфиг берётся рабочий. Рекомендация SRE: убрать дубликат из Grafana, чтобы исключить случайный выбор в будущем.

## 3. Согласованные решения (консенсус всех ролей)

1. **Один процесс-daemon**, внутренний scheduler (`robfig/cron/v3` с явным `Europe/Moscow`). Никаких внешних CronJob'ов — self-contained.
2. **SQLite + WAL**, драйвер `modernc.org/sqlite` (**pure Go**, без CGO). Это блокирующее требование для `distroless/static:nonroot` base image — выбор между «distroless» и «CGO» сделан в пользу distroless (безопасность).
3. **Собственный тонкий Telegram-клиент** (~400 строк) вместо библиотек `go-telegram-bot-api/v5` (Medium reputation в Context7, score 39) или `go-telegram/bot` (избыточный фреймворк). Нужны только `getMe`, `sendMessage`, `sendDocument`, `sendMediaGroup` — это умещается в ~4 функции с ретраями и rate-limit.
4. **Dedup fingerprint** = `sha1(host + app + normalize(_msg))`. Regex-профиль нормализации — в **YAML-конфиге** (`configs/fingerprint-profile.yaml`), а не в коде. Позволяет менять правила без релиза.
5. **HTML parse_mode** для Telegram (не MarkdownV2) — меньше шансов injection при автогенерации.
6. **Идемпотентная доставка**: pre-commit markers в state (`phase in {pre_cover, pre_media_group, done}`). Перезапуск daemon внутри цикла → продолжение с нужной фазы, без дублей.
7. **Observability:** Prometheus `/metrics` + `/healthz` + `/readyz` (кэшированный 30-60 с, 5 чеков: VL reachable, TG getMe OK, state.db writable, reports dir ok, disk free > 1 ГБ).
8. **Secrets:** только через env/k8s Secret/Vault; маскирование токенов в HTTP middleware; no-root distroless image.
9. **Git Flow + Conventional Commits на русском + GPG-signed + SemVer** — как в глобальных правилах пользователя. `VERSION` и `CHANGELOG.md` обновляются **в одном PR**.
10. **CI/CD:** lint + test (race+cover) + govulncheck + cross-build amd64/arm64 + docker-build; release — goreleaser + GHCR + SBOM (Syft) + cosign keyless + GitHub Release.

## 4. Спорные точки (role disagreements — требуют решения TL)

> Команда намеренно оставила эти вопросы открытыми для TL, т.к. это компромиссы между skill-областями.

### 4.1. SLO на доставку daily digest — 96.4% или 99%?

- **Аналитик (00-analysis §13.2):** ≥ 99.0% (бюджет 1 run / месяц).
- **SRE (11-sre §5):** ≥ 96.4% на 28-дневном rolling (1 run / 28 дней), partial delivery (4/5 файлов) = success.
- **Почему SRE снижает:** при 1 событии в сутки 99% → бюджет 0.3 run — неоперируемо, burn-rate не работает.
- **Решение TL:** принять позицию SRE (**96.4%**), иначе алертинг на `DigestOverdue` станет ложно-положительным. Если бизнес требует строже — пусть TL обоснует через escalation-path.

### 4.2. `NOISE_K` (шум-порог) — 3 или 5 в v0.1?

- **Аналитик:** 3 по умолчанию.
- **QA (13-qa §4 + §18):** рекомендует **5** для торговых серверов + A/B первую неделю. На 3 слишком много шума попадёт в основной блок отчёта, что убьёт читаемость.
- **Решение TL:** пойти с **5 + A/B логирование** (метрика `noise_suppressed_count`). Затюнить по 7 дням данных.

### 4.3. `sendMediaGroup` — primary или всегда fallback на 5× `sendDocument`?

- **Аналитик:** `sendMediaGroup` primary, fallback только «при недоступности».
- **Архитектор (ADR-0008 + 10-architecture §5):** после 2 retry обязательный fallback на `sendDocument×5`. `sendMediaGroup` часто ломается на больших файлах и не даёт внятных error-codes.
- **Решение TL:** принять позицию архитектора — **retry 2× + жёсткий fallback**. Стоит 100 строк кода, выигрыш — стабильная доставка.

### 4.4. Прокси для VictoriaLogs в v0.1 или v0.2?

- **Аналитик:** `TG_PROXY_URL` — в v0.2, прокси для VL — отдельный OQ-2.
- **Архитектор + DevOps:** ввести `VL_PROXY_URL` **сразу в v0.1** как отдельную env-переменную (не общий proxy). В проде VL часто за bastion, а TG — за corp-прокси.
- **Решение TL:** добавить `VL_PROXY_URL` **в v0.1** (стоимость — 30 минут кода, экономит релиз при OQ-2 «да»).

### 4.5. SBOM / cosign — в v0.1 или v0.2?

- **Аналитик:** не упоминает.
- **DevOps (12-devops §7, `.goreleaser.yaml`):** SBOM через Syft + cosign keyless **уже в v0.1**. Стоимость — 0 (включается флагом в goreleaser).
- **Решение TL:** принять — бесплатная security-гигиена на старте лучше, чем retrofit.

### 4.6. UI v0.3 — embedded в daemon или отдельный сервис?

- **Аналитик:** оставил вопрос открытым.
- **Архитектор (10-architecture §9):** **embedded** в тот же бинарник. Причина: shared SQLite state, WAL multi-process writer склонен к deadlock'ам; разделение оправдано только при миграции на Postgres (v2.0+).
- **Решение TL:** принять embedded, явно зафиксировать в ADR v0.3.

### 4.7. LLM-резюме — внешний API или локальный?

- **Аналитик (OQ-12):** открытый вопрос.
- **Security-риск:** логи могут содержать PII / торговые данные. Отправка во внешний LLM API (OpenAI, Anthropic) требует sign-off от Security.
- **Решение TL:** либо локальный `llama.cpp` / свой инференс-эндпоинт, либо явно задисейблить LLM в v0.2, сдвинуть в v0.3+.

## 5. Риски верхнего уровня

| # | Риск | Вероятность / Воздействие | Митигация |
|---|------|---------------------------|-----------|
| R-1 | VL недоступна — digest не уходит | M / H | Retry + `[delivery-failed]` алерт в тот же чат + CLI `once` для ручного прогона |
| R-3 | Файл > 50 МБ (лимит TG Bot API) | M / M | dedup + truncation с маркером + gzip |
| R-4 | Нормализация «склеивает» разные ошибки | H / M | Regex-профиль в YAML (не код), A/B первую неделю вручную |
| R-5 | Grafana deeplink «мёртвый» (UID протух / схема поменялась) | M / H | Smoke-тест в CI на парсинг URL + ручная проверка при ротации |
| R-10 | `host/app/level` оказались **не** полями стрима | M / M | Preflight-чек на старте daemon (`/select/logsql/stream_field_names`) + warning в лог |

Полный список (12 рисков) — в `00-analysis.md §8`.

## 6. Что нужно от TL (action items)

- [ ] **Ответить на OQ-1, OQ-3, OQ-4, OQ-5, OQ-8** (блокеры v0.1) — см. §2.
- [ ] **Принять решения по §4.1..§4.7** — 7 мини-развилок, каждая с рекомендацией команды.
- [ ] **Дать имя репозитория в `QCoreTech/*`** (предложение: `QCoreTech/log_analyser`, ветка `master`, защита включена).
- [ ] **Утвердить roadmap v0.1/v0.2/v0.3** или запросить корректировку scope.
- [ ] **Организовать sign-off от Security** по PII-политике (отчёты с выдержками логов → приватный TG-канал с ограниченным составом).
- [ ] **Назначить dev-ресурс** (1 senior Go-инженер на ~4 недели для v0.1, по возможности тот же человек на v0.2/v0.3).

## 7. Следующий коммит после утверждения

После того как TL закроет блокеры из §2 и §6:

1. `feat(scaffold): инициализация go-модуля + cmd/log-analyser/main.go заглушка`
2. `feat(config): реализация envconfig + YAML overlay по ADR-0011`
3. `feat(collector): VictoriaLogs HTTP client по ADR-0007`
4. `feat(dedup): fingerprint + normalize по ADR-0009`
5. `feat(render): text/template шаблоны по ADR-0004`
6. `feat(delivery): telegram client + sendMediaGroup по ADR-0008`
7. `feat(scheduler): cron + graceful shutdown`
8. `feat(state): sqlite state store по ADR-0002`
9. `feat(grafana): deeplink builder`
10. `feat(observability): prometheus + healthchecks + slog`
11. `chore(release): v0.1.0` (VERSION bump + CHANGELOG)

Каждый коммит — GPG-signed, conventional на русском, один PR на один логический слой. Quality gates из `docs/plans/12-devops.md §12` применяются.

---

**Подготовили:** Claude (multi-role orchestration — senior analyst/architect/SRE/DevOps/QA).
**Дата:** 2026-04-23.
**Статус:** DRAFT — ожидает действий TL из §6.
