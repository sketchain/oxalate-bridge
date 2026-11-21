# syntax=docker/dockerfile:1

# Targets:
#   bridge  (default) the relay and panel server, frontend embedded
#   fakecpa           fake CLIProxyAPI instances for the Docker demo
#                     (docker-compose.demo.yml); never used in production

# 1. Frontend build (pnpm). The output lands in backend/internal/webui/dist,
#    which the Go binary embeds.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
RUN corepack enable
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
COPY api/ /src/api/
RUN mkdir -p /src/backend/internal/webui && pnpm build

# 2. Go sources and modules (shared by both binaries).
FROM --platform=$BUILDPLATFORM golang:1.27 AS gosrc
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
ARG TARGETOS TARGETARCH
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH

# 3a. fakecpa (needs no frontend).
FROM gosrc AS build-fakecpa
RUN go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/fakecpa ./cmd/fakecpa

# 3b. bridge (cross-compiled for the target platform).
FROM gosrc AS build
COPY --from=web /src/backend/internal/webui/dist ./internal/webui/dist
RUN go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/bridge ./cmd/bridge

# 4a. Demo-only fake CPA image.
FROM gcr.io/distroless/static-debian12:nonroot AS fakecpa
COPY --from=build-fakecpa /out/fakecpa /fakecpa
EXPOSE 18400-18403
USER nonroot:nonroot
ENTRYPOINT ["/fakecpa"]
CMD ["-listen", "0.0.0.0:18400", "-n", "4"]

# 4b. Minimal runtime image (the default target).
FROM gcr.io/distroless/static-debian12:nonroot AS bridge
COPY --from=build /out/bridge /bridge
ENV BRIDGE_LISTEN=:8080
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=5 CMD ["/bridge", "healthcheck"]
ENTRYPOINT ["/bridge"]
