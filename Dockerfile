# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

WORKDIR /src

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -v -trimpath -ldflags="-s -w" -o /out/server-sync-agent .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 appuser

WORKDIR /app
ENV LISTEN_ADDR=:8080 \
	HETZNER_ENDPOINT="" \
	HETZNER_POLL_INTERVAL=2s \
	HETZNER_TOKEN=""
COPY --from=build /out/server-sync-agent /app/server-sync-agent

EXPOSE 8080
USER 10001:10001

ENTRYPOINT ["/app/server-sync-agent"]
