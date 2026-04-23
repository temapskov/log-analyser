# 12. DevOps: Docker + GitHub CI/CD для `log_analyser`

> **Роль автора:** Senior DevOps / Platform Engineer.
> **Версия:** 0.1 (pre-implementation, синхронно с `00-analysis.md` v0.1).
> **Дата:** 2026-04-23.
> **Scope:** раздел 13.3 системного анализа — полная реализация CI/CD, контейнеризации, Git Flow, SemVer, релизного процесса.
> **Репозиторий:** `QCoreTech/log_analyser` (создаётся отдельно; везде в артефактах — TODO-маркер `QCoreTech/log_analyser`).

## Оглавление

1. [Git Flow](#1-git-flow)
2. [Conventional Commits + commitlint](#2-conventional-commits--commitlint)
3. [SemVer-контракт](#3-semver-контракт)
4. [Docker (multi-stage, distroless, CGO-free)](#4-docker-multi-stage-distroless-cgo-free)
5. [docker-compose для локальной разработки](#5-docker-compose-для-локальной-разработки)
6. [GitHub Actions workflows](#6-github-actions-workflows)
7. [Release-процесс (step-by-step)](#7-release-процесс-step-by-step)
8. [Dependabot / Renovate](#8-dependabot--renovate)
9. [Secrets в GitHub](#9-secrets-в-github)
10. [Environments (staging / production)](#10-environments-staging--production)
11. [Обратимость релиза / rollback](#11-обратимость-релиза--rollback)
12. [Quality gates](#12-quality-gates)
13. [Расхождения с аналитиком](#13-расхождения-с-аналитиком)

> Cross-refs: ADR-0002 (SQLite via `modernc.org/sqlite`, CGO-free), ADR-0009 (Модульность `internal/*`), ADR-0010 (envconfig), ADR-0004 (robfig/cron MSK), ADR-0007 (тонкий TG-клиент). Номера ADR — предварительные, архитектор может скорректировать.

---

## 1. Git Flow

### 1.1. Схема веток

| Ветка | Назначение | Правила | Живёт |
|---|---|---|---|
| `master` | Главная, всегда релизабельна. На ней висят теги `vX.Y.Z`. | Protected. Только PR. Только fast-forward/squash. Force-push запрещён. | постоянно |
| `feat/<scope>-<kebab>` | Новая функциональность. Пример: `feat/collector-retry`. | PR в `master`. Минимум 1 approve + зелёный CI. | до мёрджа |
| `fix/<scope>-<kebab>` | Исправление бага. | Аналогично `feat/*`. | до мёрджа |
| `hotfix/<scope>-<kebab>` | Срочный фикс уже выпущенного релиза. Стартует от тега `vX.Y.Z`, мёрджится в `master` → сразу тэгается `vX.Y.(Z+1)`. | Ускоренный ревью (1 approve тимлид). | до мёрджа |
| `release/vX.Y.Z` | Подготовка релиза: bump `VERSION`, обновить `CHANGELOG.md`, финальные фиксы. | Мёрджится в `master`, после чего создаётся тег. | до релиза |
| `chore/<scope>-<kebab>` | Техдолг, инфра, CI, deps. | Аналогично `feat/*`. | до мёрджа |
| `docs/<scope>-<kebab>` | Только документация / ADR. | Упрощённый ревью (CI docs-only). | до мёрджа |

### 1.2. Правила мёрджа

- **Стратегия:** `squash and merge` для `feat/*`, `fix/*`, `chore/*`, `docs/*` (1 PR = 1 коммит в истории `master`).
- **`release/v*` и `hotfix/*`:** `create a merge commit` — чтобы тег `vX.Y.Z` был на merge-коммите, а не на squash-коммите.
- **Rebase перед мёрджем:** обязателен, если base устарел > 10 коммитов. После rebase GPG-подпись слетает → `git commit --amend -S --no-edit` и `git push --force-with-lease` (помним gotcha из глобального CLAUDE.md).

### 1.3. Branch protection для `master`

Настроить в GitHub → Settings → Branches → add rule `master`:

| Параметр | Значение |
|---|---|
| Require pull request before merging | ON |
| Required approvals | **1** (v0.1), **2** (после v0.2) |
| Dismiss stale approvals on new commits | ON |
| Require review from Code Owners | ON (если `CODEOWNERS` заведён) |
| Require status checks to pass | ON: `lint`, `test`, `vuln`, `build`, `docker-build`, `commitlint` |
| Require branches to be up to date | ON |
| Require conversation resolution | ON |
| Require **signed commits** | **ON** (GPG из глобальных правил) |
| Require linear history | ON (кроме `release/*` merges — используем rulesets exception) |
| Include administrators | ON |
| Restrict who can push | ограничить командой `QCoreTech/log-analyser-maintainers` |
| Allow force pushes | **OFF** |
| Allow deletions | **OFF** |

### 1.4. PR-шаблон

Уже лежит в `.github/PULL_REQUEST_TEMPLATE.md` — **корректный**. Дополнений не требуется.

---

## 2. Conventional Commits + commitlint

### 2.1. Формат

```
<type>(<scope>): <описание на русском, с маленькой буквы, без точки в конце>

[опциональное тело]

[опциональные футеры]
```

**Типы (английские, Angular-conventional):**

| Тип | Когда | SemVer-эффект |
|---|---|---|
| `feat` | Новая фича, видимая пользователю/оператору. | MINOR |
| `fix` | Исправление бага, не меняет контракт. | PATCH |
| `perf` | Оптимизация, видимый эффект. | PATCH |
| `refactor` | Рефакторинг без изменения поведения. | — |
| `docs` | Только документация (включая ADR, CHANGELOG, комментарии). | — |
| `test` | Только тесты. | — |
| `build` | Сборка, Dockerfile, goreleaser. | — |
| `ci` | GitHub Actions, pre-commit, commitlint. | — |
| `chore` | Мелочь: зависимости, lint-фиксы, cleanup. | — |
| `style` | Форматирование (`gofmt`, `goimports`). | — |
| `revert` | Откат предыдущего коммита. | зависит |

**Breaking change:** `!` после типа/скоупа **или** футер `BREAKING CHANGE: <описание>` → MAJOR. Пример:

```
feat(config)!: переименовать TG_BOT_TOKEN → TELEGRAM_BOT_TOKEN

BREAKING CHANGE: старое имя ENV больше не поддерживается; обновите .env и деплой-манифесты.
```

**Скоупы (рекомендуемые, совпадают с `internal/*`):** `collector`, `dedup`, `render`, `delivery`, `grafana`, `scheduler`, `state`, `config`, `observability`, `cli`, `docker`, `ci`, `docs`, `deps`.

### 2.2. Примеры (все с GPG `-S`)

```bash
git commit -S -m "feat(collector): добавить retry для VL-запросов с экспоненциальным backoff"
git commit -S -m "fix(delivery): обрабатывать TG 429 через respect-retry-after"
git commit -S -m "chore(deps): поднять robfig/cron до v3.0.1"
git commit -S -m "docs(adr): добавить ADR-0008 о regex-профиле fingerprint"
git commit -S -m "build(docker): перейти на gcr.io/distroless/static:nonroot"
git commit -S -m "refactor(state): вынести SQLite запросы в sqlc-generated код"
git commit -S -m "feat(config)!: переименовать HOSTS → TRADING_HOSTS" -m "BREAKING CHANGE: ENV переименована, обновите .env"
```

### 2.3. Валидация

- **Локально:** pre-commit hook (опционально, отдельная ветка `chore/ci-precommit`).
- **На PR:** GitHub Action `commitlint.yml` (см. §6.4) + **проверка title PR** (squash-merge подставит title как единственный коммит).
- **Правила commitlint** (`commitlint.config.js`): стандарт `@commitlint/config-conventional` + лимит subject 100 символов, body wrap 100. Русские символы в subject/body — разрешены.

---

## 3. SemVer-контракт

`MAJOR.MINOR.PATCH`. Источник истины — файл `VERSION`, тег `git tag v$(cat VERSION)`.

### 3.1. Что считается breaking change (MAJOR)

1. **Переименование / удаление обязательных ENV** (`TG_BOT_TOKEN`, `VL_URL`, `GRAFANA_URL`, `GRAFANA_ORG_ID`, `GRAFANA_VL_DS_UID`, `TG_CHAT_ID`).
2. **Изменение формата state DB** (схема таблиц `runs`, `fingerprints`, `dead_letter`) без обратно-совместимой миграции. Миграция с fallback (читаем старое, пишем новое) → MINOR.
3. **Изменение формата файла отчёта**: смена дефолта `.md` на другое, изменение структуры секций (переименование заголовков, удаление блоков), изменение схемы имени файла.
4. **Изменение схемы cover-сообщения** (HTML-структура, поля, порядок таблицы).
5. **Удаление CLI-подкоманды** или смена её сигнатуры.
6. **Смена бинарного поведения scheduler** (например, `SCHEDULE_CRON` → `SCHEDULE_AT`).
7. **Изменение API Prometheus метрик**: удаление или переименование метрик/лейблов, которыми пользуются дашборды SRE (`digest_cycle_total`, `last_successful_delivery_timestamp_seconds` и т.д.).

### 3.2. MINOR (обратно-совместимо)

- Новая опциональная ENV с дефолтом.
- Новая CLI-подкоманда.
- Новое поле/секция в отчёте (не удаляя/не переупорядочивая существующих).
- Новая Prometheus метрика.
- Новая фича за feature-флагом выключенным по умолчанию (FR-13 realtime, FR-17 LLM).

### 3.3. PATCH

- Баг-фикс без изменения контракта.
- Обновление транзитивных зависимостей без API-эффекта.
- Performance-оптимизация без изменения наблюдаемого поведения.

### 3.4. Pre-1.0 (0.X.Y) нюанс

До `v1.0.0` «MAJOR» может не вводиться — вместо этого **breaking в MINOR** (0.1.x → 0.2.0). Это фиксируется в `CHANGELOG.md` жирным `**BREAKING**`. v1.0 выпускаем только после DoD v0.2 + 2 недель стабильности (см. §10.2 дорожной карты).

---

## 4. Docker (multi-stage, distroless, CGO-free)

### 4.1. Выбор базы

| Вариант | Плюсы | Минусы | Вердикт |
|---|---|---|---|
| `scratch` | Минимальный размер (~20 МБ) | Нет tzdata, нет CA, нет `/etc/passwd` | Требует ручной копии — усложняет. |
| `alpine:3.20` | `apk add tzdata ca-certificates`, удобно дебажить (`sh`) | musl может выстрелить с CGO; размер ~10 МБ | OK, но есть shell — атак-поверхность. |
| **`gcr.io/distroless/static-debian12:nonroot`** | tzdata + ca-certs уже есть; `USER nonroot:nonroot` по умолчанию; нет shell | Нет отладочного `sh` (для дебага — `:debug` вариант) | **Выбор MVP.** |

**Условие на distroless:** только `CGO_ENABLED=0` статический бинарь. Поэтому **обязателен `modernc.org/sqlite`** (pure Go) вместо `mattn/go-sqlite3` (CGO). См. ADR-0002.

### 4.2. Dockerfile (реализован в `/Dockerfile`)

Ключевые моменты:

- **Stage 1:** `golang:1.22-alpine` — сборка с `CGO_ENABLED=0`, `GOFLAGS=-trimpath`, `-ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE"`. Кэш модулей через `--mount=type=cache,target=/go/pkg/mod` и `--mount=type=cache,target=/root/.cache/go-build` (BuildKit).
- **Stage 2:** `gcr.io/distroless/static-debian12:nonroot` — только бинарь + tzdata (уже в образе). `USER 65532:65532` (nonroot). `ENTRYPOINT ["/usr/local/bin/log-analyser"]`, `CMD ["run"]`.
- **ARG:** `TARGETPLATFORM`, `TARGETOS`, `TARGETARCH`, `VERSION`, `COMMIT`, `DATE` — заполняются buildx.
- **ENV дефолты:** `TZ=Europe/Moscow`, `LOG_FORMAT=json`, `METRICS_ADDR=:9090`, `STATE_DB_PATH=/var/lib/log_analyser/state.db`, `REPORTS_DIR=/var/lib/log_analyser/reports`.
- **VOLUME:** `/var/lib/log_analyser` (state + reports на одном volume).
- **EXPOSE:** `9090` (`/metrics`, `/healthz`, `/readyz`).
- **HEALTHCHECK:** через HTTP на `/readyz`. Но distroless не содержит `curl` — делаем healthcheck **снаружи** (docker-compose / k8s probe). В Dockerfile оставляем комментарий-TODO.
- **LABELS:** OCI (`org.opencontainers.image.source`, `.revision`, `.created`, `.version`, `.licenses`, `.title`).

### 4.3. Размер цели

Финальный образ: **~15–20 МБ** на архитектуру (binary ~12 МБ после `-s -w`, база distroless static ~2 МБ). Multi-arch manifest `linux/amd64` + `linux/arm64`.

---

## 5. docker-compose для локальной разработки

Файл `deploy/docker-compose.dev.yml` (см. §6 — создаём вместе с прод-вариантом):

Сервисы:

1. **`log-analyser`** — сборка из локального `Dockerfile`, mount state/reports, env из `.env.dev`.
2. **`prometheus`** — `prom/prometheus:v2.55.0`, подхватывает `deploy/prometheus/prometheus.yml` с scrape `log-analyser:9090`.
3. **`grafana`** — `grafana/grafana:11.2.2`, preprovisioned datasource на локальный VL (ENV `VL_URL`), дашборд-стартер для проверки deeplinks.
4. **`victorialogs`** (опционально, профиль `full`) — `victoriametrics/victoria-logs:v1.5.0-victorialogs`, mock-запись логов через `/insert/jsonline`. Если у разработчика есть доступ к реальной VL — использовать `VL_URL` на реальную и профиль `light` (без этого контейнера).

Плюс мок-сервис `tg-mock` (профиль `full`, Go-stub `httptest` на порту 4443, эмулирует `getMe/sendMessage/sendMediaGroup`) — **создаётся в рамках QA-плана (13-qa.md), не этот файл**.

---

## 6. GitHub Actions workflows

### 6.1. `.github/workflows/ci.yml` — CI на PR и push в master

Создан в репозитории. Коротко:

- **Триггеры:** `pull_request` на `master`; `push` на `master` (для кэша и статусов).
- **Concurrency:** `group: ci-${{ github.ref }}`, `cancel-in-progress: true`.
- **Permissions:** минимум — `contents: read`.
- **Jobs:**
  - `lint` — `actions/setup-go@v5` + `actions/cache@v4` (Go modules + build cache) + `golangci/golangci-lint-action@v7` (v2 config). `go-version: '1.22'`.
  - `test` — `go test -race -cover -coverprofile=coverage.out ./...`, upload coverage в Codecov (если `CODECOV_TOKEN` задан — опционально).
  - `vuln` — `go install golang.org/x/vuln/cmd/govulncheck@latest` + `govulncheck ./...`. Провал → fail job.
  - `build` — cross-compile matrix `{goos: linux, goarch: [amd64, arm64]}`, артефакт `bin/log-analyser-$GOOS-$GOARCH`. Не для релиза, только для проверки «компилится».
  - `docker-build` — `docker/setup-qemu-action@v3` + `docker/setup-buildx-action@v3` + `docker/build-push-action@v6` с `push: false`, `platforms: linux/amd64,linux/arm64`, `cache-from`/`cache-to: type=gha`. На PR не пушим.

### 6.2. `.github/workflows/release.yml` — релиз по тегу

- **Триггер:** `push` тега `v*.*.*`.
- **Permissions:** `contents: write`, `packages: write`, `id-token: write` (для keyless cosign и attestations).
- **Jobs:** `goreleaser`:
  - `actions/checkout@v4` с `fetch-depth: 0`.
  - `actions/setup-go@v5` + `docker/setup-qemu-action@v3` + `docker/setup-buildx-action@v3`.
  - `docker/login-action@v3` → `registry: ghcr.io`, `username: ${{ github.actor }}`, `password: ${{ secrets.GITHUB_TOKEN }}`.
  - `sigstore/cosign-installer@v3`.
  - `anchore/sbom-action/download-syft@v0.17.9` (Syft для SBOM).
  - `goreleaser/goreleaser-action@v6` с `distribution: goreleaser`, `version: "~> v2"`, `args: release --clean`.
- **Что делает goreleaser** (см. `.goreleaser.yaml`):
  - Кросс-компиляция linux/amd64 + linux/arm64.
  - Архивы `.tar.gz`, `checksums.txt`.
  - Docker build per-arch + `docker_manifests` (single tag → manifest list).
  - Push в `ghcr.io/qcoretech/log_analyser:{{.Version}}`, `:v{{.Major}}.{{.Minor}}`, `:v{{.Major}}`, `:latest` (только для стабильных без pre-release).
  - SBOM через Syft → приложен к GitHub Release.
  - Docker images подписаны через `docker_signs` keyless cosign.
  - GitHub Release с auto-changelog (group by Conventional Commits type).

### 6.3. `.github/workflows/codeql.yml` — security analysis

- **Триггер:** `pull_request` на `master`, `push` на `master`, cron раз в неделю (`0 6 * * 1`).
- **Язык:** `go`.
- **Actions:** `github/codeql-action/init@v3`, `.../analyze@v3`.
- Создаётся как отдельный файл. Оставляю stub в списке ниже — полный файл приложен.

### 6.4. `.github/workflows/commitlint.yml` — проверка конвенции

- **Триггер:** `pull_request` на `master`.
- **Проверки:** все коммиты PR через `wagoid/commitlint-github-action@v6` + отдельный шаг проверки title PR (для squash-merge).
- Конфиг — `commitlint.config.js` в корне.

### 6.5. `.github/workflows/stale.yml` — автозакрытие (опционально)

- Cron `30 3 * * *`, `actions/stale@v9`, 30 дней неактивности → label `stale`, ещё 14 дней → close. Для issues и PR. Исключения: label `pinned`, `security`.

### 6.6. `.github/workflows/trivy.yml` — сканирование образа

- Отдельный workflow: на `workflow_run` от `release.yml` (success) — `aquasecurity/trivy-action@master` на пушенном образе с severity `HIGH,CRITICAL`, upload SARIF в `github/codeql-action/upload-sarif@v3`.
- **Не блокирует релиз**, только отчитывается. Блокирующий scan — в `ci.yml` через `trivy fs` на Dockerfile + lock-файлы (проще и быстрее).

---

## 7. Release-процесс (step-by-step)

Целевой процесс **v0.1**. После v0.2 возможна автоматизация через release-please (отдельный ADR).

### 7.1. Пример: выпуск `v0.1.0`

1. **Стартуем релизную ветку от актуального `master`:**
   ```bash
   git switch master && git pull --ff-only
   git switch -c release/v0.1.0
   ```
2. **Бампим версию и меняем CHANGELOG (в одном коммите):**
   ```bash
   echo "0.1.0" > VERSION
   # вручную или через tool: перенести [Unreleased] → [0.1.0] - 2026-05-15 в CHANGELOG.md
   git add VERSION CHANGELOG.md
   git commit -S -m "chore(release): подготовить релиз v0.1.0"
   ```
3. **PR `release/v0.1.0` → `master`:**
   ```bash
   git push -u origin release/v0.1.0
   gh pr create --base master --title "chore(release): v0.1.0" \
     --body-file .github/RELEASE_PR_TEMPLATE.md
   ```
4. **Ревью + зелёный CI + 1 approve.** Стратегия мёрджа — **create a merge commit** (не squash), чтобы тег указывал на merge-коммит и все релизные коммиты остались в истории.
5. **Мёрдж в `master`** через GitHub UI (button merge commit).
6. **Тэгируем merge-коммит:**
   ```bash
   git switch master && git pull --ff-only
   git tag -s v0.1.0 -m "release v0.1.0"      # -s = GPG signed tag
   git push origin v0.1.0
   ```
7. **`release.yml` запускается автоматически** по пушу тега:
   - goreleaser собирает бинари, Docker-манифест, подписывает cosign.
   - Публикует `ghcr.io/qcoretech/log_analyser:0.1.0`, `:v0.1`, `:v0` (если стабильный), `:latest`.
   - Создаёт GitHub Release с auto-changelog и SBOM.
8. **Smoke-test из GHCR:**
   ```bash
   docker pull ghcr.io/qcoretech/log_analyser:0.1.0
   docker run --rm ghcr.io/qcoretech/log_analyser:0.1.0 version
   cosign verify ghcr.io/qcoretech/log_analyser:0.1.0 \
     --certificate-identity-regexp '^https://github.com/QCoreTech/log_analyser' \
     --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
   ```
9. **Production deploy:** изменить `image` в `deploy/docker-compose.prod.yml` (pin на `0.1.0`, не `latest`) → PR → approve environment `production` → выкатка.

### 7.2. Hotfix

1. `git switch -c hotfix/delivery-retry v0.1.0`.
2. Фикс + тест + `git commit -S -m "fix(delivery): ..."`.
3. PR в `master`, accelerated review.
4. После мёрджа: bump `VERSION` → `0.1.1`, тег `v0.1.1`, пуш.

### 7.3. Коммиты — GPG

**Все** коммиты (включая merge-commit в release-процессе) должны быть GPG-signed. Проверяется branch protection rule `Require signed commits` + CI через `git log --show-signature` в post-job шаге `ci.yml` (опциональный шаг, не блокирующий — только warning).

Если после rebase подпись слетела (глобальный gotcha):
```bash
git rebase master
git commit --amend -S --no-edit
git push --force-with-lease
```

---

## 8. Dependabot / Renovate

**Выбор: Dependabot** (нативно в GitHub, меньше конфига, меньше шума — для MVP достаточно; Renovate — в v0.2 если потребуется grouped PR для Go modules).

Файл `.github/dependabot.yml` создан. Экосистемы:

- **`gomod`** (корень) — weekly, понедельник 07:00 MSK, label `chore`, `deps`, `go`; группировка minor+patch в один PR через `groups`.
- **`docker`** (корень, для `Dockerfile`) — weekly, label `chore`, `deps`, `docker`.
- **`github-actions`** (`.github/workflows/*.yml`) — weekly, label `chore`, `deps`, `ci`.

Все PR от Dependabot: автоматически назначается reviewer `@temapskov`, commit-message `chore(deps): ...`. Merge — ручной (auto-merge только для patch от уже доверенных major-версий — рассмотреть в v0.2).

---

## 9. Secrets в GitHub

### 9.1. Repository secrets

| Secret | Назначение | Workflow | Обязательность |
|---|---|---|---|
| `GITHUB_TOKEN` (встроенный) | push в GHCR, PR comments, SARIF upload. | `release.yml`, `ci.yml`, `codeql.yml`, `trivy.yml` | встроен, настроить permissions на workflow-уровне |
| `CODECOV_TOKEN` | Upload coverage (если используем приватный Codecov). | `ci.yml` → job `test` | **опционально** (v0.1 — off) |
| `COSIGN_KEY` / `COSIGN_PASSWORD` | Только если решим уйти от keyless. | `release.yml` | **не нужны** (keyless через OIDC) |

### 9.2. Environment secrets (см. §10)

| Environment | Secret | Назначение | Workflow |
|---|---|---|---|
| `staging` | `TG_BOT_TOKEN_STG` | Тестовый бот для staging. | `deploy-staging.yml` (v0.2) |
| `staging` | `TG_CHAT_ID_STG` | Тестовый chat (приватная группа DevOps). | `deploy-staging.yml` |
| `staging` | `VL_URL_STG` | Staging-VictoriaLogs (read-only). | `deploy-staging.yml` |
| `staging` | `GRAFANA_URL_STG` | Staging-Grafana. | `deploy-staging.yml` |
| `staging` | `SSH_DEPLOY_KEY_STG` | Если деплой на Docker host через SSH. | `deploy-staging.yml` |
| `production` | `TG_BOT_TOKEN_PROD` | Продовый бот (sign-off Security). | `deploy-prod.yml` |
| `production` | `TG_CHAT_ID_PROD` | Продовый chat. | `deploy-prod.yml` |
| `production` | `VL_URL_PROD`, `GRAFANA_URL_PROD` | Прод-endpoints. | `deploy-prod.yml` |
| `production` | `SSH_DEPLOY_KEY_PROD` / `KUBECONFIG_PROD` | Доступ для деплоя. | `deploy-prod.yml` |

### 9.3. Политика хранения

- **Никаких** секретов в `.env.example`, Dockerfile, workflows как plaintext.
- Ротация токенов бота — раз в 90 дней, владелец — Security.
- GHCR доступ — через `GITHUB_TOKEN` scope `packages:write`, OIDC keyless для cosign.

---

## 10. Environments (staging / production)

### 10.1. `staging`

- **Триггер:** `push` в `master` → `deploy-staging.yml` (создаётся в v0.2, в v0.1 не обязателен).
- **Что:** pull образа `ghcr.io/qcoretech/log_analyser:master-<sha>` + `docker compose up -d` на stage-хосте.
- **Protection:** no reviewers, но — restricted to `master`.

### 10.2. `production`

- **Триггер:** создание GitHub Release (workflow `deploy-prod.yml`) **или** ручной `workflow_dispatch` с параметром `tag`.
- **Что:** pull образа `ghcr.io/qcoretech/log_analyser:v0.1.0` (pin!) + `docker compose up -d` или `kubectl set image` — в зависимости от OQ-6.
- **Protection (environment rules):**
  - **Required reviewers:** `@temapskov` + 1 из TeamLead-команды.
  - **Wait timer:** 5 минут (окно отмены).
  - **Deployment branches:** только `master`, только теги `v*.*.*`.

### 10.3. Артефакты деплоя

- **Docker host (OQ-6 = compose):** `deploy/docker-compose.prod.yml` — с pin версией, mount state volume, env из environment secrets.
- **k8s (OQ-6 = k8s):** `deploy/k8s/` — Deployment, Service, ConfigMap, Secret (sealed-secrets или External Secrets Operator), ServiceMonitor для Prometheus.
  - **Пока OQ-6 не закрыт — оставляю TODO-плейсхолдер, файлы не создаю.**

---

## 11. Обратимость релиза / rollback

### 11.1. Быстрый rollback (< 5 минут)

1. В `deploy-prod.yml` (или руками): изменить env `APP_IMAGE_TAG` с `0.1.1` обратно на `0.1.0`.
2. Re-run workflow → pull старого образа → `docker compose up -d` (или `kubectl rollout undo deployment/log-analyser`).
3. Tag `v0.1.1` в GHCR **не удаляем** (нужен для пост-морта).

### 11.2. Если релиз сломал данные (state DB)

1. Остановить daemon: `docker compose stop log-analyser`.
2. Восстановить state из бэкапа (SQLite = один файл; бэкап снимаем через `VACUUM INTO` в side-car — отдельная задача SRE).
3. Откат образа по §11.1.
4. **Миграция state должна быть обратно-совместимой** для PATCH/MINOR; для MAJOR — обязателен скрипт-downgrade или явный процесс миграции документирован в `CHANGELOG.md`.

### 11.3. Yank «битого» тега

GitHub Releases позволяет **снять** Release (не удалять тег) + переименовать в pre-release. Тег остаётся в истории, но `latest` в GHCR переключается обратно через повторный push goreleaser'а от предыдущего тега (или вручную: `docker buildx imagetools create -t ghcr.io/...:latest ghcr.io/...:0.1.0`).

---

## 12. Quality gates

PR в `master` мёрджится только при **всех** условиях:

| Gate | Где проверяется | Блокирующий |
|---|---|---|
| `lint` — golangci-lint зелёный | `ci.yml` job `lint` | да |
| `test` — `go test -race -cover`, ≥ 70% coverage (v0.1, поднимем до 80% в v0.2) | `ci.yml` job `test` | да |
| `vuln` — `govulncheck` без findings (или все с документированным whitelist) | `ci.yml` job `vuln` | да |
| `build` — cross-compile amd64+arm64 ОК | `ci.yml` job `build` | да |
| `docker-build` — buildx multi-arch ОК | `ci.yml` job `docker-build` | да |
| `commitlint` — все коммиты/PR-title конвенциональны | `commitlint.yml` | да |
| `codeql` — без HIGH/CRITICAL | `codeql.yml` | да (можно перевести в warning на первое время) |
| 1+ approve на PR | branch protection | да |
| Signed commits (GPG) | branch protection | да |
| Conventional PR title | `commitlint.yml` (шаг title) | да |
| CHANGELOG обновлён, если меняется поведение | Ручная проверка ревьювером + label-бот (v0.2) | «мягкий» |
| ADR обновлён, если меняется архитектура | Ручная проверка ревьювером | «мягкий» |

---

## 13. Расхождения с аналитиком

> Всё ниже — **принципиальные** расхождения или уточнения. Не мелочёвка.

1. **SQLite-драйвер:** аналитик (§13.1 ADR-06) не фиксировал конкретный драйвер. DevOps **категорически** требует `modernc.org/sqlite` (CGO-free) — иначе ломается distroless/scratch и кросс-компиляция amd64↔arm64 без кросс-тулчейна. Это попадает в ADR-0002. Если архитектор предпочтёт `mattn/go-sqlite3` (CGO) — переходим на `alpine:3.20` как базу и добавляем `apk add sqlite-libs ca-certificates tzdata` + сложный buildx-сценарий (QEMU медленный для CGO). **Рекомендация DevOps: modernc.**
2. **Формат отчёта `.md` vs `.html`** (OQ-4, §15.1): аналитик ставит `.md` по умолчанию. DevOps принимает, но обращает внимание — если QA потребует HTML-превью в CI (визуальный diff отчётов), `.html` проще интегрировать. Остаётся OQ-4, не блокирует.
3. **Rate-limit в Telegram (NFR от аналитика):** аналитик упоминает token bucket в v0.2. DevOps считает, что для cover + `sendMediaGroup` 1 раз в сутки — token bucket излишен. Но **обязательно** добавить `respect Retry-After` из 429 response (это не token bucket, это reactive backoff) — **уже в v0.1**, не откладывать. Отражено в §6.1 `vuln`-стиле упоминания, в контракт QA не вношу.
4. **SBOM через goreleaser vs docker/build-push-action@v6:** аналитик не говорит про SBOM. DevOps решает генерировать SBOM **и в goreleaser** (приложен к GitHub Release, Syft), **и в docker/build-push-action** (attestation в registry, provenance). Это избыточно, но обеспечивает и supply-chain verify, и compliance. В v0.2 можно оставить только вариант buildx.
5. **Cosign keyless vs key-based:** DevOps выбирает keyless (OIDC через GitHub Actions) — нет ключа для ротации, подпись привязана к identity workflow. Аналитик не фиксировал.
6. **v1.0 критерии:** аналитик не говорит про `v1.0`. DevOps предлагает: v1.0 только после 2 недель стабильной работы v0.2 + sign-off Security + penetration review. До v1.0 — breaking changes допускаются в MINOR (0.X → 0.(X+1)) с явной пометкой `**BREAKING**` в CHANGELOG.
7. **Staging environment:** аналитик не упомянул. DevOps добавляет как **обязательный** — иначе нет площадки для проверки real-Telegram и real-VL перед продом. Но в v0.1 staging-workflow можно отложить, если OQ-6 (где запускать) ещё не закрыт.
8. **Healthcheck в Dockerfile:** distroless не содержит `curl`/`wget`, поэтому **в самом Dockerfile HEALTHCHECK не ставим** — только в docker-compose / k8s probe. Это тонкий момент, он противоречит ожиданию «healthcheck в Dockerfile» (которое можно прочесть из NFR-O3) — явное отступление, документировано в §4.2.

---

**Автор:** Claude (senior DevOps mode).
**Следующий шаг:** архитектор принимает ADR-0002 (SQLite driver); после — SRE фиксирует метрики и алерты в 11-sre.md; после — QA пишет тест-план.
