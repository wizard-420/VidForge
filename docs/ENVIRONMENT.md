# Environment Variables & Configuration

Configuration is loaded from `.env` at startup via `godotenv` (silently skipped if missing). All values are read into the global `config.App` struct in `config/config.go`.

## API Keys

| Variable | Required For | Used In | Description |
|----------|-------------|---------|-------------|
| `GROQ_API_KEY` | Stages 1, 2 + script refinement | `pipeline/input_parser.go`, `pipeline/script_gen.go` | Groq API key for Llama 3.3 70B. **Primary LLM for all text generation.** |
| `ELEVENLABS_API_KEY` | Stage 3 (AI voiceover) | `pipeline/voiceover.go` | ElevenLabs TTS API key. Not needed if using manual voiceover or Google TTS. |
| `GOOGLE_CLOUD_TTS_API_KEY` | Stage 3 (Google TTS voiceover) | `pipeline/gcp_tts.go`, `api/handlers.go` | Google Cloud Text-to-Speech API key. Required only for `gcp_tts` voiceover mode. |
| `PEXELS_API_KEY` | Stage 4 (stock video) | `pipeline/visual.go` | Pexels API key for stock video clip search/download. |
| `TOGETHER_API_KEY` | Stage 4 (AI images, primary) | `pipeline/visual.go` | Together AI key for FLUX.1-schnell image generation. |
| `HF_API_KEY` | Stage 4 (AI images, fallback) | `pipeline/visual.go` | Hugging Face Inference API key. Used when Together AI fails. |
| `JAMENDO_CLIENT_ID` | Stage 5 (auto music) | `pipeline/music.go`, `api/handlers.go` | Jamendo API client ID. **Default: `b6747d04`** — works without setting. |

### Keys in Config But NOT Used in Pipeline

| Variable | Notes |
|----------|-------|
| `PIXABAY_API_KEY` | Loaded into config, shown in health check. **Not referenced by any pipeline stage.** |
| `OPENAI_API_KEY` | Loaded into config, shown in health check. **Not referenced by any pipeline stage.** |

## YouTube OAuth

| Variable | Default | Description |
|----------|---------|-------------|
| `YOUTUBE_CLIENT_SECRET_FILE` | `client_secret.json` | Path to Google OAuth client secret JSON file |
| `YOUTUBE_TOKEN_FILE` | `token.json` | Path to saved OAuth token JSON file |

YouTube upload is **optional**. If these files don't exist, Stage 7 succeeds with a local file path.

## Application Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `WORKSPACE_DIR` | `./workspace` | Root directory for per-job working directories |
| `EXPORT_DIR` | `./exports` | Export directory (created at startup, currently underused) |
| `LOG_LEVEL` | `INFO` | Log level (loaded but no extensive level routing implemented) |
| `MAX_CONCURRENT_JOBS` | `1` | Number of worker goroutines processing jobs in parallel |
| `SERVER_PORT` | `8000` | HTTP server listen port |
| `CLEANUP_AFTER_DAYS` | `7` | Days after which jobs could be cleaned up (config only — **no cleanup job implemented**) |

## .env.example

The `.env.example` file documents all configurable keys. Notable points:

- `GOOGLE_CLOUD_TTS_API_KEY` is optional — only needed if using Google TTS voiceover mode.
- `ELEVENLABS_API_KEY` is optional — only needed if using AI voiceover mode.
- At least one TTS provider key is required if using AI-generated voiceover.

## Minimum Viable Configuration

For the most basic usage (AI voiceover + stock footage + no upload):

```env
GROQ_API_KEY=your-groq-key
ELEVENLABS_API_KEY=your-elevenlabs-key
PEXELS_API_KEY=your-pexels-key
```

For AI image generation, add:
```env
TOGETHER_API_KEY=your-together-key
HF_API_KEY=your-hf-key
```

For YouTube upload, additionally set up OAuth:
```bash
go run ./cmd/setup_auth
```

## Configuration Loading Flow

```
main.go → config.Load()
  ├── godotenv.Load()           # Read .env file
  ├── Populate config.App       # All getEnv() calls with defaults
  └── ensureDir() for:
      ├── workspace/
      ├── exports/
      ├── logs/
      └── storage/
```
