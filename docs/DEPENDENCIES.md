# Dependencies

## Go Module Dependencies

Defined in `go.mod`. Module name: `yt-automation-studio`, Go version: `1.25.0`.

### Direct Dependencies

| Package | Version | Purpose | Used In |
|---------|---------|---------|---------|
| `github.com/google/uuid` | v1.6.0 | Generate unique job IDs | `api/handlers.go` |
| `github.com/gorilla/websocket` | v1.5.3 | WebSocket upgrade & messaging for real-time progress | `api/websocket.go` |
| `github.com/joho/godotenv` | v1.5.1 | Load `.env` file into environment variables | `config/config.go` |
| `golang.org/x/oauth2` | v0.36.0 | OAuth2 client for YouTube API authentication | `pipeline/uploader.go`, `cmd/setup_auth/main.go` |
| `google.golang.org/api` | v0.277.0 | Google API client (YouTube Data API v3) | `pipeline/uploader.go`, `cmd/setup_auth/main.go` |
| `modernc.org/sqlite` | v1.29.10 | Pure-Go SQLite driver (no CGO required) | `storage/db.go` |

### Notable Indirect Dependencies

| Package | Purpose |
|---------|---------|
| `cloud.google.com/go/auth` | Google Cloud authentication infrastructure |
| `go.opentelemetry.io/otel` | Telemetry (pulled by Google API client) |
| `golang.org/x/crypto` | Cryptographic libraries |
| `modernc.org/libc` | C standard library emulation for pure-Go SQLite |

## System Dependencies

| Tool | Required? | Purpose | Install |
|------|-----------|---------|---------|
| **FFmpeg** | Yes | Video rendering, audio conversion, music mixing, caption burning | `apt install ffmpeg` or download from ffmpeg.org |
| **Whisper** (openai-whisper) | Optional | Caption/subtitle generation (SRT) | `pip install openai-whisper` |

If FFmpeg is missing, Stage 6 (Video Render) will fail.
If Whisper is missing, captions are skipped (non-fatal).

## External API Dependencies

| Service | API Endpoint | Auth Method | Free Tier? |
|---------|-------------|-------------|------------|
| **Groq** | `https://api.groq.com/openai/v1/chat/completions` | Bearer token | Yes (rate-limited) |
| **ElevenLabs** | `https://api.elevenlabs.io/v1/text-to-speech/{voice_id}` | `xi-api-key` header | Yes (limited characters) |
| **Pexels** | `https://api.pexels.com/videos/search` | `Authorization` header | Yes |
| **Together AI** | `https://api.together.xyz/v1/images/generations` | Bearer token | Yes (free model tier) |
| **Hugging Face** | `https://api-inference.huggingface.co/models/...` | Bearer token | Yes (rate-limited, model loading delays) |
| **Jamendo** | `https://api.jamendo.com/v3.0/tracks/` | `client_id` query param | Yes |
| **YouTube Data API** | `https://www.googleapis.com/youtube/v3/videos` | OAuth2 | Yes (quota-limited) |

## Frontend Dependencies

**None.** The frontend is vanilla HTML/CSS/JS with no npm packages, no build tools, and no framework.

External resources loaded at runtime:
- Google Fonts (via CSS `@import` or `<link>`)

## AI Models Used

| Model | Provider | Task |
|-------|----------|------|
| `llama-3.3-70b-versatile` | Groq | Script generation, input parsing, script refinement |
| `eleven_multilingual_v2` | ElevenLabs | Text-to-speech voiceover |
| `FLUX.1-schnell-Free` | Together AI | AI image generation (primary) |
| `FLUX.1-schnell` | Hugging Face | AI image generation (fallback) |
| `base` | OpenAI Whisper (local) | Speech-to-text captions |
