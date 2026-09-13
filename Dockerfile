FROM node:24-alpine AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts --no-fund --no-audit
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vkurilke ./cmd/app

FROM alpine:3.23
RUN apk add --no-cache ca-certificates su-exec tzdata \
    && addgroup -S app && adduser -S -u 10001 -G app app \
    && mkdir /data && chown app:app /data
COPY --from=backend /out/vkurilke /usr/local/bin/vkurilke
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh
ENV PORT=8080 DB_PATH=/data/vkurilke.db TZ=Asia/Novosibirsk
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=60s \
  CMD wget -q -O /dev/null "http://127.0.0.1:${PORT:-8080}/healthz" || exit 1
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
