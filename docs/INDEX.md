# VidForge Documentation Index

Welcome to the VidForge project documentation. Use this index to navigate to the relevant section.

## Documentation Files

| Document | Description |
|----------|-------------|
| [OVERVIEW.md](OVERVIEW.md) | High-level project overview, architecture diagram, design decisions, runtime requirements |
| [FILE_STRUCTURE.md](FILE_STRUCTURE.md) | Complete file tree with purpose of every file, plus runtime-generated files |
| [PIPELINE.md](PIPELINE.md) | Detailed 7-stage pipeline architecture — every stage's logic, APIs, prompts, fallbacks |
| [API_REFERENCE.md](API_REFERENCE.md) | All REST endpoints and WebSocket protocol with request/response examples |
| [DATA_MODELS.md](DATA_MODELS.md) | Every Go struct: InputPayload, ScriptDocument, JobContext, etc. with all fields |
| [DATABASE.md](DATABASE.md) | SQLite schema, indexes, migrations, CRUD operations |
| [ENVIRONMENT.md](ENVIRONMENT.md) | All environment variables, which are actually used, and .env.example issues |
| [FRONTEND.md](FRONTEND.md) | UI architecture, state management, key workflows, page structure |
| [DEPENDENCIES.md](DEPENDENCIES.md) | Go modules, system tools, external APIs, AI models, frontend deps |
| [DOCKER.md](DOCKER.md) | Dockerfile, docker-compose, volumes, build and run instructions |
| [KNOWN_ISSUES.md](KNOWN_ISSUES.md) | Bugs, incomplete features, tech debt, and inconsistencies |

## Quick Reference

### How to run locally
```bash
# 1. Create .env with at minimum: GROQ_API_KEY, ELEVENLABS_API_KEY, PEXELS_API_KEY
cp .env.example .env
# Edit .env with your keys

# 2. Ensure FFmpeg is installed
ffmpeg -version

# 3. Run the server
go run main.go

# 4. Open http://localhost:8000
```

### How to run with Docker
```bash
docker compose up --build -d
# Open http://localhost:8000
```

### Key entry points for code changes

| If you need to change... | Look at... |
|--------------------------|------------|
| API endpoints | `api/handlers.go` |
| Pipeline stages | `pipeline/*.go` (one file per stage) |
| Pipeline flow/ordering | `pipeline/orchestrator.go` |
| Data models/validation | `models/*.go` |
| Database schema | `storage/db.go` |
| Configuration/env vars | `config/config.go` |
| UI layout/pages | `ui/index.html` |
| UI logic/API calls | `ui/app.js` |
| UI styling | `ui/styles.css` |
| Worker concurrency | `worker/queue.go` |
| YouTube OAuth setup | `cmd/setup_auth/main.go` |
| Docker build | `Dockerfile`, `docker-compose.yml` |
