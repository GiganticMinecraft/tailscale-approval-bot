# syntax=docker/dockerfile:1
FROM golang:1.25-trixie AS build

WORKDIR /src

RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,source=go.mod,target=go.mod \
    --mount=type=bind,source=go.sum,target=go.sum \
    go mod download

RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=bind,target=. \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/controller .

FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=build /bin/controller /controller

ENTRYPOINT ["/controller"]
