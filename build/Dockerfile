ARG GO_IMAGE=golang:1.27.1-alpine
ARG NODE_IMAGE=node:24-alpine
ARG RUNTIME_IMAGE=alpine:3.23

FROM ${NODE_IMAGE} AS web-builder

WORKDIR /src/web

COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web ./
RUN npm run build

FROM ${GO_IMAGE} AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY app ./app
COPY cmd ./cmd

RUN CGO_ENABLED=0 GOOS=linux GOMAXPROCS=1 go build -p=1 -trimpath -ldflags="-s -w" -o /out/pal-media-server ./cmd/server

FROM ${RUNTIME_IMAGE}

ARG ENV_MEDIA_DIR
ARG ENV_TMP_DIR
ARG ENV_PORT

RUN apk add --no-cache ca-certificates ffmpeg tzdata \
    && addgroup -S uploader \
    && adduser -S -G uploader uploader \
    && mkdir -p "${ENV_TMP_DIR}" "${ENV_MEDIA_DIR}" \
    && chown -R uploader:uploader "${ENV_TMP_DIR}" "${ENV_MEDIA_DIR}"

USER uploader
WORKDIR /app

COPY --from=builder /out/pal-media-server /app/pal-media-server
COPY --from=web-builder /src/web/dist /app/web/dist

EXPOSE ${ENV_PORT}

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${ENV_PORT}/readyz" || exit 1

ENTRYPOINT ["/app/pal-media-server"]
