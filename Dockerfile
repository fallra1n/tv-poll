FROM oven/bun:1.4.2 AS frontend-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/bun.lock ./
RUN bun install --frozen-lockfile
COPY frontend/ ./
ARG BUN_PUBLIC_API_URL
ENV BUN_PUBLIC_API_URL=$BUN_PUBLIC_API_URL
RUN bun run build

FROM golang:1.26-alpine AS backend-build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && addgroup -S app \
    && adduser -S -G app app
WORKDIR /app
COPY --from=backend-build --chown=app:app /out/api /usr/local/bin/api
COPY --from=backend-build --chown=app:app /out/migrate /usr/local/bin/migrate
COPY --from=frontend-build --chown=app:app /src/frontend/dist ./frontend
ENV HTTP_ADDR=:8080
ENV METRICS_ADDR=127.0.0.1:9090
ENV FRONTEND_DIR=/app/frontend
EXPOSE 8080
USER app
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/api"]
