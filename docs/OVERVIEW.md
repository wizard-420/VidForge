# VidForge — Project Overview

**Internal module name:** `yt-automation-studio`
**Version:** 1.0.0
**Language:** Go 1.25 (backend), Vanilla HTML/CSS/JS (frontend)
**Database:** SQLite (pure-Go driver via `modernc.org/sqlite`)

## What is VidForge?

VidForge (branded as "YouTube Automation Studio" in the UI) is an AI-powered, end-to-end pipeline for creating faceless YouTube videos. Given a topic, category, or event description, it automatically:

1. Parses and normalizes the input using an LLM (Groq/Llama 3.3 70B)
2. Generates a full video script with segments, visuals cues, and metadata
3. Synthesizes voiceover audio (ElevenLabs TTS or manual recording)
4. Fetches stock video clips (Pexels) or generates AI images (Together AI / HuggingFace)
5. Downloads royalty-free background music (Jamendo)
6. Renders the final video with FFmpeg (visual stitching, captions via Whisper, music mixing)
7. Optionally uploads to YouTube via OAuth2

## High-Level Architecture

```
┌──────────────┐       ┌───────────────┐       ┌─────────────────┐
│  Browser UI  │◄─────►│  Go HTTP API  │──────►│  Worker Queue   │
│ (index.html) │  WS   │  (handlers.go)│       │  (queue.go)     │
└──────────────┘       └───────────────┘       └────────┬────────┘
                              │                         │
                              │                         ▼
                       ┌──────┴──────┐          ┌───────────────┐
                       │  SQLite DB  │          │  Orchestrator │
                       │  (jobs.db)  │          │  7 Stages     │
                       └─────────────┘          └───────────────┘
                                                        │
                                    ┌───────────────────┼───────────────────┐
                                    ▼                   ▼                   ▼
                              External APIs       FFmpeg/Whisper      File System
                              (Groq, 11Labs,      (rendering)         (workspace/)
                               Pexels, etc.)
```

## Key Design Decisions

- **No frontend framework** — The UI is a single `index.html` with vanilla JS for simplicity.
- **In-memory job context** — Active jobs live in a Go map (`activeJobs`); only metadata is persisted to SQLite.
- **Buffered channel worker pool** — Jobs are queued via a Go channel with configurable concurrency (default: 1 worker).
- **Human-in-the-loop** — When `auto_upload` is false, the pipeline pauses before Stage 7 (upload) and waits for user approval.
- **Graceful fallbacks** — Each stage has fallback paths (e.g., Together AI → HuggingFace for images, stock → AI image on failure).
- **WebSocket progress** — Real-time pipeline progress is streamed to the browser via per-job WebSocket connections.

## Runtime Requirements

| Dependency | Purpose | Required? |
|------------|---------|-----------|
| Go 1.25+ | Build/run the server | Yes |
| FFmpeg | Video rendering, audio conversion, music mixing | Yes |
| Whisper CLI (`openai-whisper`) | Caption generation (SRT) | Optional (captions skipped if missing) |
| `.env` file | API keys and configuration | Yes (for any AI features) |

## Directory Layout (Runtime)

```
VidForge/
├── workspace/           # Per-job working directories (job_{uuid}/)
│   └── job_<uuid>/
│       ├── script.json
│       ├── segments/    # Voice, clips, images, intermediate files
│       ├── music.mp3
│       ├── captions.srt
│       └── final_output.mp4
├── exports/             # (reserved, underused)
├── logs/                # (reserved)
├── storage/
│   └── jobs.db          # SQLite database
├── client_secret.json   # YouTube OAuth client (not in repo)
└── token.json           # YouTube OAuth token (not in repo)
```
