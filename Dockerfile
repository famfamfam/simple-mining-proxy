# syntax=docker/dockerfile:1

# Admin UI: type check, lint, unit tests and the Vite build. Runs on the build
# machine's architecture; the output is static files.
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# The i18n test checks that every server message key is translated.
COPY internal/ /internal/
RUN npm run check && npm run build

FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS build
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
COPY --from=web /web/dist ./web/dist
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/proxy ./cmd/proxy
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build --chown=65532:65532 /out/proxy /app/proxy
COPY --from=build --chown=65532:65532 /out/data /data

USER 65532:65532
VOLUME ["/data"]
EXPOSE 13333 14443 18080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/app/proxy", "healthcheck"]

ENTRYPOINT ["/app/proxy"]
