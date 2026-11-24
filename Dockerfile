FROM --platform=$BUILDPLATFORM node:24-alpine AS frontend-builder

WORKDIR /src/frontend
RUN corepack enable && corepack prepare pnpm@9.15.9 --activate
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY frontend/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS backend-builder

WORKDIR /src/backend
ENV CGO_ENABLED=0
ARG TARGETOS
ARG TARGETARCH
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/siteadmin .

FROM alpine:3.22 AS runtime

WORKDIR /app
LABEL org.opencontainers.image.title="yunlan" \
    org.opencontainers.image.description="Independent NewAPI owner administration workspace" \
    org.opencontainers.image.source="https://github.com/wanfemg/yunlan"

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && mkdir -p /app/frontend/dist /app/data \
    && chown -R app:app /app

COPY --from=backend-builder --chown=app:app /out/siteadmin /app/siteadmin
COPY --from=frontend-builder --chown=app:app /src/frontend/dist/ /app/frontend/dist/
COPY --chown=app:app LICENSE THIRD_PARTY_NOTICES.md /app/

ENV PORT=18080 \
    DATA_DIR=/app/data \
    STATIC_DIR=/app/frontend/dist \
    TZ=Asia/Shanghai

EXPOSE 18080 18081
USER app:app
ENTRYPOINT ["/app/siteadmin"]
