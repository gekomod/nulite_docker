# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates build-base
COPY . .
# Generate/complete go.sum from go.mod and source imports before compiling.
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/nulite-docker .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=build /out/nulite-docker /app/nulite-docker
COPY web /app/web
RUN mkdir -p /data && chown -R app:app /app /data
USER app
ENV LISTEN_ADDR=:8080 ESP_URL=http://192.168.31.131 DB_PATH=/data/nulite.db POLL_INTERVAL=5s
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD wget -q -O - http://127.0.0.1:8080/api/v1/health >/dev/null || exit 1
ENTRYPOINT ["/app/nulite-docker"]
