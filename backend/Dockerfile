# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25.0
ARG GOPROXY=https://goproxy.cn,direct
ARG GOSUMDB=sum.golang.google.cn

FROM golang:${GO_VERSION} AS build
ARG GOPROXY
ARG GOSUMDB
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB} \
    CGO_ENABLED=0
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY backend/ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && \
    go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

FROM alpine:3.21 AS runtime
RUN apk add --no-cache ca-certificates tzdata su-exec && \
    adduser -D -H -s /sbin/nologin app
WORKDIR /app
COPY --from=build /src/config ./config
COPY backend/docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh && \
    mkdir -p /app/.run/uploads && \
    chown -R app:app /app

FROM runtime AS api
COPY --from=build /out/api /app/api
EXPOSE 8080 9001
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/api"]

FROM runtime AS worker
COPY --from=build /out/worker /app/worker
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/worker"]
