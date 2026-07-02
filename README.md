# Hetzner Cloud Sync Server Agent

This service exposes an HTTP API that can list running Hetzner Cloud servers and delete a running server when called.

## GitHub Deployment

1. Create a GitHub repository for this project and push `main`.
2. Enable GitHub Actions and GitHub Container Registry for the repository.
3. The workflow at `.github/workflows/publish-ghcr.yml` publishes the image to:
   `ghcr.io/<github-owner>/hetzner-cloud-sync-server-agent`
4. On `main`, it publishes `:latest`.
5. On tags like `v1.0.0`, it also publishes versioned tags.

## Portainer Deployment

Use `docker-compose.yml` as your stack file.

Set these environment variables in Portainer or a `.env` file:

```bash
GITHUB_REPOSITORY_OWNER=your-github-username-or-org
HETZNER_TOKEN=your-hetzner-token
HETZNER_ENDPOINT=
HETZNER_POLL_INTERVAL=2s
LISTEN_ADDR=:8080
IMAGE_TAG=latest
```

The stack pulls the GHCR image automatically and exposes the service on port `8080`.

## Local Run

```bash
cp .env.example .env
go run .
```

## API

- `GET /healthz`
- `GET /servers/running`
- `POST /delete`
- `DELETE /servers/{id-or-name}`
