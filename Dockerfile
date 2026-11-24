# ---- build stage ----
# 베이스 이미지는 digest로 고정(재현성·공급망 보안). 업그레이드 시 digest도 함께 갱신.
FROM golang:1.26-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS build
WORKDIR /build

# Cache module downloads (go.sum 필수 — 재현 가능한 빌드).
COPY src/go.mod src/go.sum ./
RUN go mod download

COPY src/ .
# 빌드는 readonly 모드로 고정(go.mod/go.sum 변경 없이 동일 의존성 보장).
# Version comes from --build-arg APP_VERSION (default below); build time is stamped now.
ARG APP_VERSION=1.21.0
RUN CGO_ENABLED=0 GOOS=linux go build -mod=readonly \
    -ldflags="-s -w -X main.Version=${APP_VERSION} -X main.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /out/dashboard .

# ---- runtime stage ----
FROM alpine:3.21@sha256:bc21487101ae47172509130ee1092cc273a831968d64b617776d1f69c81e42ab
RUN adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/dashboard /app/dashboard
COPY web /app/web
RUN mkdir -p /data && chown -R app:app /data /app
USER app
EXPOSE 8180
ENTRYPOINT ["/app/dashboard"]
