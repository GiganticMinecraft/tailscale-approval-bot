# syntax=docker/dockerfile:1
FROM golang:1.25-trixie AS build-base

WORKDIR /src

RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,source=go.mod,target=go.mod \
    --mount=type=bind,source=go.sum,target=go.sum \
    go mod download

FROM build-base AS build-api

RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,target=. \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/api ./cmd/api

FROM build-base AS build-discord

RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,target=. \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/discord ./cmd/discord

FROM gcr.io/distroless/static-debian13:nonroot AS api

LABEL org.opencontainers.image.authors="outductor <inductor.kela+seichi@gmail.com>"
LABEL org.opencontainers.image.url="https://github.com/GiganticMinecraft/tailscale-approval-bot"
LABEL org.opencontainers.image.source="https://github.com/GiganticMinecraft/tailscale-approval-bot/blob/main/Dockerfile"
LABEL org.opencontainers.image.title="tailscale-approval-bot-api"
LABEL org.opencontainers.image.description="API server for Tailscale device approval workflow"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.vendor="GiganticMinecraft"

COPY --from=build-api /bin/api /api

ENTRYPOINT ["/api"]

FROM gcr.io/distroless/static-debian13:nonroot AS discord

LABEL org.opencontainers.image.authors="outductor <inductor.kela+seichi@gmail.com>"
LABEL org.opencontainers.image.url="https://github.com/GiganticMinecraft/tailscale-approval-bot"
LABEL org.opencontainers.image.source="https://github.com/GiganticMinecraft/tailscale-approval-bot/blob/main/Dockerfile"
LABEL org.opencontainers.image.title="tailscale-approval-bot-discord"
LABEL org.opencontainers.image.description="Discord bot for Tailscale device approval workflow"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.vendor="GiganticMinecraft"

COPY --from=build-discord /bin/discord /discord

ENTRYPOINT ["/discord"]
