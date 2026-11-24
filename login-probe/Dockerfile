FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
ARG TARGETOS
ARG TARGETARCH
COPY login-probe/ ./
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/login-probe .

FROM alpine:3.22
WORKDIR /app
LABEL org.opencontainers.image.title="yunlan-probe" \
    org.opencontainers.image.description="Login and registration observation gateway for yunlan" \
    org.opencontainers.image.source="https://github.com/wanfemg/yunlan"

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && mkdir -p /app/data \
    && chown app:app /app/data
COPY --from=builder --chown=app:app /out/login-probe /app/login-probe
COPY --chown=app:app LICENSE THIRD_PARTY_NOTICES.md /app/
USER app:app
EXPOSE 18081
ENTRYPOINT ["/app/login-probe"]
