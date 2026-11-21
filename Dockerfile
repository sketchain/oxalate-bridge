# syntax=docker/dockerfile:1

# 1. Frontend build. The output lands in backend/internal/webui/dist, which
#    the Go binary embeds. (Placeholder build this round: no dependencies.)
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src
COPY web/ web/
RUN mkdir -p backend/internal/webui && cd web && npm run build

# 2. Go build (cross-compiled for the target platform).
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=web /src/backend/internal/webui/dist ./internal/webui/dist
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/bridge ./cmd/bridge

# 3. Minimal runtime image.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bridge /bridge
ENV BRIDGE_LISTEN=:8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/bridge"]
