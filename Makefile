## Makefile — локальные разработческие удобства.
## Прод-сборка и релизы — через GitHub Actions и goreleaser (см. .goreleaser.yaml).

# ---- Конфигурация ---------------------------------------------------------
BINARY      ?= log-analyser
PKG_MAIN    := ./cmd/log-analyser
PKG_ALL     := ./...
GOFLAGS     ?=
COVERAGE    ?= coverage.out

VERSION     ?= $(shell cat VERSION 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo none)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
  -X github.com/QCoreTech/log_analyser/internal/version.Version=$(VERSION) \
  -X github.com/QCoreTech/log_analyser/internal/version.Commit=$(COMMIT) \
  -X github.com/QCoreTech/log_analyser/internal/version.BuildDate=$(BUILD_DATE)

# ---- Цели -----------------------------------------------------------------
.PHONY: help build run test test-race test-integration cover lint fmt vet tidy clean docker

help: ## Показать доступные цели
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Собрать бинарник в bin/
	@mkdir -p bin
	@CGO_ENABLED=0 go build $(GOFLAGS) -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(PKG_MAIN)
	@echo "bin/$(BINARY) готов ($(VERSION) / $(COMMIT))"

run: build ## Собрать и запустить version
	@./bin/$(BINARY) version

test: ## Запустить unit-тесты
	@go test $(GOFLAGS) $(PKG_ALL)

test-race: ## Тесты с -race и coverage
	@go test $(GOFLAGS) -race -covermode=atomic -coverprofile=$(COVERAGE) $(PKG_ALL)
	@go tool cover -func=$(COVERAGE) | tail -1

test-integration: ## Integration-тесты против реальных VL/Grafana (читает .env)
	@test -f .env || { echo '.env не найден — нужны GRAFANA_URL/UID/TYPE и т.п.' >&2; exit 1; }
	@set -a; . ./.env; set +a; go test $(GOFLAGS) -tags=integration -count=1 -race -run Integration $(PKG_ALL)

cover: test-race ## Открыть HTML-отчёт покрытия
	@go tool cover -html=$(COVERAGE)

lint: ## golangci-lint (должен быть установлен: brew install golangci-lint)
	@command -v golangci-lint >/dev/null || { echo 'golangci-lint не установлен; brew install golangci-lint' >&2; exit 1; }
	@golangci-lint run

fmt: ## gofmt -s -w по всему проекту
	@gofmt -s -w .

vet: ## go vet
	@go vet $(PKG_ALL)

tidy: ## go mod tidy
	@go mod tidy

clean: ## Убрать bin/ и coverage.out
	@rm -rf bin $(COVERAGE)

docker: ## Локальная сборка Docker-образа (buildx, amd64)
	@docker buildx build --platform linux/amd64 -t log-analyser:$(VERSION)-$(COMMIT) --load .
