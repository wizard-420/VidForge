# Docker Setup

VidForge includes Docker support for containerized deployment.

## Dockerfile (Multi-Stage Build)

### Stage 1: Builder
- Base: `golang:1.25-bookworm`
- Compiles a static Linux binary: `CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o yt-studio main.go`
- Binary name: `yt-studio`

### Stage 2: Runtime
- Base: `python:3.11-slim-bookworm`
- Installs: FFmpeg (`apt-get`), OpenAI Whisper (`pip`)
- Copies: Go binary from builder, `ui/` directory
- Creates: `workspace/`, `exports/`, `logs/`, `storage/` directories
- Exposes: Port 8000
- Health check: `wget -qO- http://localhost:8000/api/status` every 30s
- Entry point: `./yt-studio`

## docker-compose.yml

```yaml
services:
  yt-studio:
    build: .
    container_name: yt-automation-studio
    ports:
      - "8000:8000"
    env_file:
      - .env
    volumes:
      - ./workspace:/app/workspace       # Job working directories
      - ./exports:/app/exports           # Exports
      - ./logs:/app/logs                 # Logs
      - yt-storage:/app/storage          # SQLite database (named volume)
      - ./client_secret.json:/app/client_secret.json:ro  # YouTube OAuth (read-only)
      - ./token.json:/app/token.json     # YouTube OAuth token
    environment:
      - WORKSPACE_DIR=/app/workspace
      - EXPORT_DIR=/app/exports
      - SERVER_PORT=8000
    restart: unless-stopped

volumes:
  yt-storage:                            # Named volume for persistent DB
```

## Usage

### Build and run:
```bash
docker compose up --build -d
```

### View logs:
```bash
docker compose logs -f
```

### Stop:
```bash
docker compose down
```

### Access:
- Dashboard: http://localhost:8000
- API: http://localhost:8000/api/status

## Volume Mapping

| Host Path | Container Path | Purpose |
|-----------|---------------|---------|
| `./workspace` | `/app/workspace` | Per-job files survive container restarts |
| `./exports` | `/app/exports` | Export files |
| `./logs` | `/app/logs` | Application logs |
| `yt-storage` (named) | `/app/storage` | SQLite database |
| `./client_secret.json` | `/app/client_secret.json` | YouTube OAuth (read-only mount) |
| `./token.json` | `/app/token.json` | YouTube OAuth token |

## Notes

- The `.env` file must exist on the host with API keys configured
- YouTube OAuth files are optional — upload will be skipped if not mounted
- The named volume `yt-storage` ensures the SQLite database persists across `docker compose down/up`
- Container auto-restarts unless explicitly stopped (`restart: unless-stopped`)
