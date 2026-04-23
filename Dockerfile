# syntax=docker/dockerfile:1.7
# =============================================================================
#  log_analyser — multi-stage, CGO-free, distroless/nonroot
#  Сборка: docker buildx build --platform linux/amd64,linux/arm64 -t ghcr.io/qcoretech/log_analyser:dev .
#  См. docs/plans/12-devops.md §4 и ADR-0002 (modernc.org/sqlite — CGO-free).
# =============================================================================

# -------- Stage 1: builder ---------------------------------------------------
FROM --platform=${BUILDPLATFORM} golang:1.22-alpine AS builder

# Args, которые buildx прокидывает автоматически
ARG TARGETOS
ARG TARGETARCH
# Мета-данные сборки для -ldflags
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

# ca-certs и tzdata — пригодятся и в build-time (go test против внешних API)
RUN apk add --no-cache ca-certificates tzdata git

WORKDIR /src

# Сначала mod-файлы — ускоряем кэш зависимостей
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Весь код
COPY . .

# Статическая сборка. CGO=0 обязателен для distroless/static.
# -trimpath убирает локальные пути; -s -w режут debug-инфо; X.version/commit/date — метаданные.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w \
        -X main.version=${VERSION} \
        -X main.commit=${COMMIT} \
        -X main.date=${DATE}" \
      -o /out/log-analyser \
      ./cmd/log-analyser

# -------- Stage 2: runtime ---------------------------------------------------
# distroless/static — только ca-certs + tzdata + /etc/passwd (nonroot uid=65532).
# :nonroot вариант уже выставляет USER 65532.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

# Повторяем args для labels
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

# OCI-labels: GHCR использует для связи image ↔ репо ↔ релиз
LABEL org.opencontainers.image.title="log_analyser" \
      org.opencontainers.image.description="Daemon для ежедневного отчёта об ошибках торговых серверов в Telegram" \
      org.opencontainers.image.vendor="QCoreTech" \
      org.opencontainers.image.licenses="Proprietary" \
      org.opencontainers.image.source="https://github.com/QCoreTech/log_analyser" \
      org.opencontainers.image.url="https://github.com/QCoreTech/log_analyser" \
      org.opencontainers.image.documentation="https://github.com/QCoreTech/log_analyser/blob/master/README.md" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${DATE}"

# Дефолты — могут быть переопределены через env/compose/k8s
ENV TZ=Europe/Moscow \
    LOG_LEVEL=info \
    LOG_FORMAT=json \
    METRICS_ADDR=:9090 \
    HOSTS=t1,ali-t1,t2,aws-t3,t5 \
    SCHEDULE_CRON="0 8 * * *" \
    LEVELS=error,critical \
    NOISE_K=3 \
    TOP_N=20 \
    REPORT_FORMAT=md \
    REPORTS_DIR=/var/lib/log_analyser/reports \
    REPORTS_RETENTION_DAYS=30 \
    STATE_DB_PATH=/var/lib/log_analyser/state.db \
    GRAFANA_ORG_ID=1 \
    GRAFANA_VL_DS_TYPE=victoriametrics-logs-datasource

# nonroot == uid 65532, gid 65532 (из distroless)
USER 65532:65532

# Volume для state.db и reports/ — монтируется persistent-ом в compose/k8s
VOLUME ["/var/lib/log_analyser"]

# Prometheus /metrics + /healthz + /readyz
EXPOSE 9090

# Бинарь в PATH distroless/static не нужен, но /usr/local/bin — традиция
COPY --from=builder /out/log-analyser /usr/local/bin/log-analyser

# HEALTHCHECK в Dockerfile не ставим: distroless не содержит curl/wget.
# Проверка живости — через HTTP GET /readyz снаружи (docker-compose healthcheck / k8s probe).
# TODO(devops): если перейдём на alpine-base, добавить HEALTHCHECK CMD wget -qO- http://localhost:9090/readyz

ENTRYPOINT ["/usr/local/bin/log-analyser"]
CMD ["run"]
