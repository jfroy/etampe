# syntax=docker/dockerfile:1.25

ARG GO_VERSION=1.26.3

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" \
      -o /out/etampe ./cmd/etampe

FROM gcr.io/distroless/static-debian12:nonroot AS runtime

COPY --from=build /out/etampe /etampe

USER nonroot:nonroot
EXPOSE 2525 8080
ENTRYPOINT ["/etampe"]
