# Developer shortcuts. Tool versions match CI.
SQLC_VERSION    := v1.31.1
OAPI_VERSION    := v2.8.2
GOLANGCI_VERSION := v2.15.0

# Local development (make dev): docker compose runs PostgreSQL and Redis on
# these host ports; fakecpa, the bridge and Vite run on the host.
DEV_PG_PORT     ?= 55432
DEV_REDIS_PORT  ?= 56379
DEV_ENV = BRIDGE_DATABASE_URL=postgres://bridge:bridge@127.0.0.1:$(DEV_PG_PORT)/bridge?sslmode=disable \
	BRIDGE_REDIS_URL=redis://127.0.0.1:$(DEV_REDIS_PORT)/0 \
	BRIDGE_SECRET_KEY=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY= \
	BRIDGE_BOOTSTRAP_ADMIN_USERNAME=admin BRIDGE_BOOTSTRAP_ADMIN_PASSWORD=admin-pass-123 \
	BRIDGE_COOKIE_SECURE=false BRIDGE_LISTEN=127.0.0.1:8080 BRIDGE_METRICS_LISTEN=127.0.0.1:9090 \
	BRIDGE_LOG_LEVEL=info

.PHONY: generate web web-deps build test test-web lint lint-web run dev dev-down dev-reset e2e screenshots

generate:
	cd backend && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd backend && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_VERSION) -config oapi-codegen.yaml ../api/openapi.yaml
	cd web && pnpm install --frozen-lockfile && pnpm gen:api

web-deps:
	cd web && pnpm install --frozen-lockfile

web: web-deps
	cd web && pnpm build

build: web
	cd backend && CGO_ENABLED=0 go build -trimpath -o bridge ./cmd/bridge

# Needs BRIDGE_TEST_DATABASE_URL and BRIDGE_TEST_REDIS_URL for the
# integration and end-to-end tests (they are skipped otherwise).
test:
	cd backend && go test -race -count=1 ./...

test-web: web-deps
	cd web && pnpm typecheck && pnpm test && pnpm check:i18n

lint:
	cd backend && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION) run ./...

lint-web: web-deps
	cd web && pnpm lint

run: build
	cd backend && ./bridge

# One command for local development: PostgreSQL + Redis (compose), fakecpa,
# demo data (first run only), the bridge and the Vite dev server with hot
# reload on http://localhost:5173 (admin / admin-pass-123, alice / demo-pass-123).
# Ctrl+C stops everything except the containers (make dev-down).
dev: web-deps
	docker compose -f docker-compose.dev.yml up -d --wait
	cd backend && go build -o ../.dev/bridge ./cmd/bridge && go build -o ../.dev/fakecpa ./cmd/fakecpa
	@trap 'kill 0' INT TERM EXIT; \
	./.dev/fakecpa -listen 127.0.0.1:18400 & \
	sleep 1; \
	$(DEV_ENV) ./.dev/bridge seed-demo -fakecpa http://127.0.0.1:18400 && \
	( $(DEV_ENV) ./.dev/bridge & \
	  cd web && BRIDGE_DEV_BACKEND=http://127.0.0.1:8080 pnpm dev --host 127.0.0.1 & \
	  wait )

dev-down:
	docker compose -f docker-compose.dev.yml down

# Drops the dev database and Redis data (the next make dev seeds again).
dev-reset:
	docker compose -f docker-compose.dev.yml down -v

# Playwright smoke tests against a fresh build (empty test databases needed:
# BRIDGE_DATABASE_URL, BRIDGE_REDIS_URL).
e2e: web-deps
	./scripts/e2e.sh --project=smoke

# Regenerates docs/screenshots (same requirements as e2e).
screenshots: web-deps
	./scripts/e2e.sh --project=screenshots
