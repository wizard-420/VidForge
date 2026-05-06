# API Reference

Base URL: `http://localhost:8000`

All routes are registered in `api/handlers.go` via `RegisterRoutes()`. CORS is enabled for all origins (`*`) via middleware in `main.go`.

---

## Job Management

### POST /api/jobs
**Create and queue a new video job.**

Request body: `InputPayload` JSON (see [DATA_MODELS.md](DATA_MODELS.md) for full schema).

```json
{
  "raw_input": "The Fall of the Roman Empire",
  "input_type": "topic",
  "format": "long",
  "duration_min": 8,
  "aspect_ratio": "landscape",
  "fit_mode": "fill",
  "output_quality": "standard",
  "voiceover_mode": "ai",
  "voice_id": "adam",
  "video_mode": "auto",
  "video_style": "stock",
  "music_mode": "auto",
  "script_tone": "dramatic",
  "language": "english",
  "upload_schedule": "immediate",
  "caption_style": "bold_white",
  "auto_upload": false,
  "clip_count": 0,
  "image_count": 0,
  "seconds_per_visual": 6,
  "ai_image_percent": 0,
  "aspect_ratio": "landscape",
  "fit_mode": "fill"
}
```

Visual pacing — `seconds_per_visual` (3–15, default 6) controls how often a new
visual appears: roughly one new shot per N seconds of narration. `ai_image_percent`
(0–100, default 0) controls the split between AI images and stock footage.
When `clip_count` and `image_count` are both 0, the backend derives them from the
pacing fields and the estimated video duration; if either is explicitly set,
those values are honored instead (legacy behaviour).

Aspect ratio — `aspect_ratio` is one of `landscape` (1920×1080, 16:9, default for
`long`/`both`), `portrait` (1080×1920, 9:16, default for `short`), or `square`
(1080×1080, 1:1). It is independent of `format`, so you can render Shorts in
landscape (e.g. for X/LinkedIn) or long-form videos in portrait (e.g. for
TikTok/Reels). Both stock footage padding and AI-image generation honor this
setting.

Fit mode — `fit_mode` is one of `fill` (default) or `fit`:
- `fill` — zoom-and-crop. Source is upscaled until it fully covers the target
  frame; overflowing edges are center-cropped. No black bars. Best for the modern
  Shorts/TikTok/Reels look where every pixel is content.
- `fit` — letterbox/pillarbox. Source is shrunk to fit entirely inside the target
  frame; remaining space is filled with black bars (top/bottom for landscape→portrait,
  sides for portrait→landscape). Preserves the entire original frame — useful when
  important content sits near the edges and you cannot afford any cropping.

Response `202 Accepted`:
```json
{
  "job_id": "uuid-string",
  "status": "queued",
  "message": "Job queued successfully. Pipeline starting.",
  "eta_sec": 300
}
```

### GET /api/jobs
**List all jobs (newest first, max 50).**

Response `200`: Array of `JobDBRecord` objects.

### GET /api/jobs/{id}
**Get full job status.**

Returns active `JobContext` (in-memory) if running, otherwise falls back to `JobDBRecord` from SQLite.

### DELETE /api/jobs/{id}
**Delete a job from memory and database.**

### GET /api/jobs/{id}/script
**Get the generated script for a job.**

Returns `ScriptDocument` JSON. Tries in-memory first, then reads from `workspace/job_{id}/script.json`.

### GET /api/jobs/{id}/download
**Download the final rendered MP4 video.**

Streams `workspace/job_{id}/final_output.mp4` as `video/mp4` with `Content-Disposition: attachment`.

### POST /api/jobs/{id}/retry
**Retry a failed job.**

Re-enqueues the job. Note: currently re-runs the full pipeline from Stage 1 regardless of `StartStage` (resume-from-stage is not yet implemented).

Response `202`: `{ "message": "Retrying job from stage N", "job_id": "..." }`

### POST /api/jobs/{id}/approve
**Approve a pending job for YouTube upload.**

Only works when job status is `pending_approval`. Sets `approved = true` and enqueues Stage 7 (Upload).

Response `202`: `{ "message": "Job approved and queued for upload", "job_id": "..." }`

---

## Script Preview & Refinement

### POST /api/preview-script
**Generate a script without running the full pipeline.**

Runs only Stages 1-2 (Input Parsing + Script Generation). Returns the `ScriptDocument`.

Request body: Same `InputPayload` as POST /api/jobs.

### POST /api/refine-script
**Refine an existing script via AI chat.**

```json
{
  "current_script": { /* ScriptDocument */ },
  "user_prompt": "Make the hook more dramatic",
  "raw_input": "original topic",
  "format": "long",
  "duration_min": 8,
  "script_tone": "dramatic",
  "language": "english",
  "clip_count": 16,
  "image_count": 0
}
```

Response `200`: Updated `ScriptDocument`.

---

## System

### GET /api/status
**Health check endpoint.**

```json
{
  "status": "healthy",
  "version": "1.0.0",
  "api_keys": {
    "groq": true,
    "elevenlabs": true,
    "pexels": true,
    "pixabay": false,
    "openai": false,
    "together": true,
    "hf": true
  },
  "ffmpeg": true,
  "whisper": false,
  "timestamp": "2026-05-04T08:00:00Z"
}
```

### GET /api/settings
**Get current configuration (API keys masked).**

### PUT /api/settings
**Update settings.** (Stub — returns message to edit `.env` directly.)

### GET /api/voices
**List available ElevenLabs TTS voices.**

```json
[
  { "id": "adam", "name": "Adam", "description": "Deep, authoritative male voice", "accent": "American" },
  { "id": "rachel", "name": "Rachel", "description": "Warm, professional female voice", "accent": "American" },
  { "id": "domi", "name": "Domi", "description": "Strong, confident female voice", "accent": "American" },
  { "id": "josh", "name": "Josh", "description": "Young, energetic male voice", "accent": "American" }
]
```

---

## Google Cloud TTS

### GET /api/gcp-tts/voices
**List available Google Cloud TTS voices.**

Query parameters:
- `language` (optional): BCP-47 language code (e.g. `en-US`, `hi-IN`). If omitted, returns all voices.

Response:
```json
{
  "voices": [
    {
      "name": "en-US-Neural2-D",
      "languageCodes": ["en-US"],
      "ssmlGender": "MALE",
      "naturalSampleRateHertz": 24000
    }
  ]
}
```

Returns HTTP 503 if neither `GOOGLE_CLOUD_TTS_API_KEY` nor a service account is configured.

The response also includes:
```json
{
  "voices": [...],
  "service_account_configured": true,
  "premium_voices_available": true
}
```

Each voice object now has a `premium: true` flag for Chirp 3 HD / Studio voices.

**Auth modes & which voices are returned:**

| Mode | Voices returned | Premium voices? |
| --- | --- | --- |
| API key only (`GOOGLE_CLOUD_TTS_API_KEY`) | Standard, Wavenet, Neural2, News, Casual, Polyglot, regular Chirp HD | Filtered out — would fail with Google's misleading "requires a model name" 400 |
| Service account (`GOOGLE_APPLICATION_CREDENTIALS_JSON` or `GOOGLE_APPLICATION_CREDENTIALS`) | All of the above **plus** Chirp 3 HD and Studio | Yes, premium voices included |

Set `GOOGLE_APPLICATION_CREDENTIALS_JSON` (raw JSON in the env, Docker-friendly) or `GOOGLE_APPLICATION_CREDENTIALS` (path to a JSON file). The service account needs the `roles/texttospeech.user` role.

### POST /api/gcp-tts/synthesize
**Synthesize text to speech using Google Cloud TTS (for preview).**

Request body:
```json
{
  "text": "Hello, world!",
  "voice_name": "en-US-Neural2-D",
  "language_code": "en-US"
}
```

Response:
```json
{
  "audio_base64": "<base64-encoded MP3>",
  "content_type": "audio/mpeg"
}
```

Returns HTTP 503 if API key is not configured, HTTP 502 on upstream failures.

---

## Music

### GET /api/music/jamendo/search
**Server-side proxy for Jamendo music search (bypasses CORS/adblock).**

Query parameters:
| Param | Default | Description |
|-------|---------|-------------|
| `q` | — | Search query |
| `mood` | `cinematic` (if `q` is empty) | Fuzzy mood tags |
| `speed` | — | Track speed filter |
| `min_dur` | `60` | Minimum duration in seconds |
| `max_dur` | `600` | Maximum duration in seconds |
| `limit` | `10` | Max results |

Response: `{ "tracks": [ { "id", "name", "artist", "album", "duration", "cover", "stream_url", "download_url", "genre", "speed", "license_url", "downloadable" } ] }`

---

## WebSocket

### WS /ws/{id}
**Real-time progress stream for a specific job.**

Connect to `ws://localhost:8000/ws/{job_id}` to receive `ProgressEvent` JSON messages:

```json
{
  "job_id": "uuid",
  "stage": 3,
  "stage_name": "Voiceover",
  "progress_pct": 45,
  "message": "Generating voice for segment 3 of 8...",
  "status": "",
  "youtube_url": "",
  "duration": "",
  "timestamp": "2026-05-04T08:05:00Z"
}
```

Special `status` values: `"failed"`, `"completed"`, `"pending_approval"`.

Multiple clients can connect to the same job ID simultaneously.
