# File Structure

Complete listing of every source file in the VidForge repository with its purpose.

```
VidForge/
│
├── main.go                          # Application entry point
├── go.mod                           # Go module definition & direct dependencies
├── go.sum                           # Dependency lock file
├── Dockerfile                       # Multi-stage Docker build (Go binary + FFmpeg + Whisper)
├── docker-compose.yml               # Docker Compose service definition
├── .env.example                     # Template environment file (has known inaccuracies — see ENVIRONMENT.md)
├── .gitignore                       # Git exclusions (.env, OAuth files, binaries, workspace, storage)
├── .dockerignore                    # Docker build context exclusions
│
├── config/
│   └── config.go                    # Environment variable loading, defaults, masked settings, directory setup
│
├── api/
│   ├── handlers.go                  # All REST API route registration + handler implementations
│   └── websocket.go                 # WebSocket upgrade, per-job client tracking, progress broadcasting
│
├── models/
│   ├── input.go                     # InputPayload struct, validation rules, default values
│   ├── job.go                       # JobContext (runtime state), JobDBRecord, ProgressEvent, JobStatus enum, StageNames
│   └── script.go                    # ScriptDocument, ScriptSegment, SubVisual, ShortScript
│
├── pipeline/
│   ├── orchestrator.go              # 7-stage sequential pipeline runner, approval gate, retry stub
│   ├── input_parser.go              # Stage 1: Input normalization (category→topic, event→narrative via Groq)
│   ├── script_gen.go                # Stage 2: Script generation (long/short/both), Groq JSON prompts, RefineScript
│   ├── voiceover.go                 # Stage 3: ElevenLabs TTS or manual base64→MP3, voice ID mapping
│   ├── visual.go                    # Stage 4: Pexels stock clips + Together AI/HuggingFace images, dedup, fallbacks
│   ├── music.go                     # Stage 5: Jamendo auto search by tone, manual download+crop, skip mode
│   ├── renderer.go                  # Stage 6: FFmpeg per-segment rendering, concat, Whisper captions, final mux
│   └── uploader.go                  # Stage 7: YouTube Data API v3 upload, scheduled publish, OAuth token handling
│
├── storage/
│   └── db.go                        # SQLite schema (jobs table), CRUD operations, WAL mode, migration
│
├── worker/
│   └── queue.go                     # Buffered channel job queue, configurable worker goroutines
│
├── cmd/
│   └── setup_auth/
│       └── main.go                  # Standalone YouTube OAuth setup wizard (browser flow → token.json)
│
└── ui/
    ├── index.html                   # Single-page dashboard (Create Video, Job History, Settings pages)
    ├── app.js                       # Frontend logic: API client, state, WebSocket, script chat, recording
    └── styles.css                   # Complete styling: design tokens, layout, components, animations
```

## Files NOT in Repository (Runtime-Generated)

| Path | Created By | Purpose |
|------|------------|---------|
| `storage/jobs.db` | `storage.InitDB()` | SQLite database |
| `workspace/job_<uuid>/` | Pipeline stages | Per-job working directory |
| `workspace/job_<uuid>/script.json` | Stage 2 | Generated script |
| `workspace/job_<uuid>/segments/` | Stages 3-6 | Voice audio, video clips, images, intermediate renders |
| `workspace/job_<uuid>/music.mp3` | Stage 5 | Background music track |
| `workspace/job_<uuid>/captions.srt` | Stage 6 | Whisper-generated subtitles |
| `workspace/job_<uuid>/final_output.mp4` | Stage 6 | Final rendered video |
| `client_secret.json` | User (manual) | YouTube OAuth client credentials |
| `token.json` | OAuth setup wizard | YouTube OAuth access/refresh token |
| `.env` | User (manual) | Environment configuration |
