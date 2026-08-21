# ---- build stage ----
FROM golang:1.23-alpine AS build
WORKDIR /build

# Cache module downloads.
COPY src/go.mod src/go.sum* ./
RUN go mod download 2>/dev/null || true

COPY src/ .
# Resolve/verify deps, then build a static binary (modernc/sqlite is pure Go).
# Version comes from --build-arg APP_VERSION (default below); build time is stamped now.
ARG APP_VERSION=1.14.0
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.Version=${APP_VERSION} -X main.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /out/dashboard .

# ---- runtime stage ----
FROM alpine:3.20
RUN adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/dashboard /app/dashboard
COPY web /app/web
RUN mkdir -p /data && chown -R app:app /data /app
USER app
EXPOSE 8180
ENTRYPOINT ["/app/dashboard"]
