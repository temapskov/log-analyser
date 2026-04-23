# log_analyser

Сервис-демон на Go: ежедневный Telegram-отчёт об ошибках (`error` / `critical`) на торговых серверах.
Логи читаются из **VictoriaLogs**, доставка — через **Telegram Bot API**, каждый инцидент сопровождается deeplink в **Grafana Explore**.

> Issue: [QCoreTech/awesome#908](https://github.com/QCoreTech/awesome/issues/908).
> Статус: **pre-alpha / pre-v0.1** (каркас + планы). См. [CHANGELOG](CHANGELOG.md).

## Возможности (roadmap)

- **v0.1.0 (MVP):** ежедневный digest в 08:00 MSK, 1 cover + 5 файлов, Grafana-deeplinks, dedup/шумоподавление, daemon-режим, Docker + GitHub CI/CD.
- **v0.2.0:** real-time алертинг, прокси для Telegram (SOCKS5/HTTP), опциональное LLM-резюме.
- **v0.3.0:** read-only веб-UI для просмотра накопленных инцидентов.

Серверы по умолчанию: `t1`, `ali-t1`, `t2`, `aws-t3`, `t5`.

## Документация (разработчикам)

- [CLAUDE.md](CLAUDE.md) — память проекта для Claude Code.
- [docs/plans/00-analysis.md](docs/plans/00-analysis.md) — системный анализ (FR/NFR/риски/OQ).
- [docs/plans/10-architecture.md](docs/plans/10-architecture.md) — архитектурный план + ADR-ссылки.
- [docs/plans/11-sre.md](docs/plans/11-sre.md) — SLO и наблюдаемость.
- [docs/plans/12-devops.md](docs/plans/12-devops.md) — CI/CD и релизная модель.
- [docs/plans/13-qa.md](docs/plans/13-qa.md) — тест-план.
- [docs/plans/99-teamlead-summary.md](docs/plans/99-teamlead-summary.md) — краткая сводка для TL.
- [docs/adr/](docs/adr/) — Architecture Decision Records.

## Быстрый старт (будет доступно с v0.1.0)

```bash
# 1. Склонировать и собрать
git clone git@github.com:QCoreTech/log_analyser.git
cd log_analyser
cp .env.example .env
# заполнить TG_BOT_TOKEN, TG_CHAT_ID, VL_URL, GRAFANA_* и т.д.

# 2. Запустить в докере
docker run --rm --env-file .env \
  -v $PWD/data:/var/lib/log_analyser \
  ghcr.io/qcoretech/log_analyser:latest run

# 3. Ручной прогон за конкретную дату
docker run --rm --env-file .env \
  ghcr.io/qcoretech/log_analyser:latest once --date=2026-04-20
```

## Конфигурация (env)

| Переменная              | Обязат. | По умолчанию              | Описание |
|-------------------------|---------|---------------------------|----------|
| `TG_BOT_TOKEN`          | да      | —                         | Bot API token. |
| `TG_CHAT_ID`            | да      | —                         | Куда слать cover + файлы. |
| `VL_URL`                | да      | —                         | База VictoriaLogs (`https://vl.example.com`). |
| `GRAFANA_URL`           | да      | —                         | База Grafana. |
| `GRAFANA_ORG_ID`        | да      | `1`                       | orgId для Explore URL. |
| `GRAFANA_VL_DS_UID`     | да      | —                         | UID плагина VictoriaLogs в Grafana. |
| `HOSTS`                 | нет     | `t1,ali-t1,t2,aws-t3,t5`  | Список серверов (csv). |
| `SCHEDULE_CRON`         | нет     | `0 8 * * *`               | Время запуска digest (в TZ). |
| `TZ`                    | нет     | `Europe/Moscow`           | IANA tz. |
| `LEVELS`                | нет     | `error,critical`          | Какие уровни логов тащить. |
| `NOISE_K`               | нет     | `3`                       | Минимум повторов для попадания в основную часть. |
| `TOP_N`                 | нет     | `20`                      | Сколько топ-инцидентов в отчёте на хост. |
| `TG_PROXY_URL`          | нет     | —                         | (v0.2) `socks5://...` или `http://...`. |

Полный список — в `.env.example`.

## Стек

Go 1.22+ • SQLite (WAL) • Prometheus • Docker • GitHub Actions. Подробности — в [CLAUDE.md §2](CLAUDE.md#2-стек-зафиксирован-аналитиком-подтверждён-context7).

## Лицензия

TBD (внутренний проект QCoreTech).
