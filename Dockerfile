FROM golang:1.25-alpine AS build

WORKDIR /src

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/server-sync-agent .

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
