# Developer shortcuts. Tool versions match CI.
SQLC_VERSION    := v1.31.1
OAPI_VERSION    := v2.8.2
GOLANGCI_VERSION := v2.15.0

.PHONY: generate web build test lint run

generate:
	cd backend && go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	cd backend && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_VERSION) -config oapi-codegen.yaml ../api/openapi.yaml

web:
	cd web && npm run build

build: web
	cd backend && CGO_ENABLED=0 go build -trimpath -o bridge ./cmd/bridge

# Needs BRIDGE_TEST_DATABASE_URL and BRIDGE_TEST_REDIS_URL for the
# integration and end-to-end tests (they are skipped otherwise).
test:
	cd backend && go test -race -count=1 ./...

lint:
	cd backend && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION) run ./...

run: build
	cd backend && ./bridge
