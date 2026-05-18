# Pipeline Architecture

The core of VidForge is a 7-stage sequential pipeline managed by the `Orchestrator` (`pipeline/orchestrator.go`). Each stage receives the shared `JobContext` and a `ProgressFunc` callback for real-time updates.

## Pipeline Flow

```
POST /api/jobs
      │
      ▼
  Worker Queue (channel, configurable concurrency)
      │
      ▼
  Orchestrator.Run()
      │
      ├── Stage 1: Input Parsing      ──► pipeline/input_parser.go
      ├── Stage 2: Script Generation   ──► pipeline/script_gen.go
      ├── Stage 3: Voiceover           ──► pipeline/voiceover.go
      ├── Stage 4: Visual Fetch        ──► pipeline/visual.go
      ├── Stage 5: Music               ──► pipeline/music.go
      ├── Stage 6: Video Render        ──► pipeline/renderer.go
      │
      ├── [APPROVAL GATE if auto_upload=false]
      │     Status → pending_approval
      │     Wait for POST /api/jobs/{id}/approve
      │
      └── Stage 7: Upload              ──► pipeline/uploader.go
              │
              ▼
          Status → completed
```

---

## Stage 1: Input Parsing (`pipeline/input_parser.go`)

**Purpose:** Normalize raw user input into a clean topic for script generation.

**Behavior by input_type:**
- `category` → Calls Groq LLM to generate a trending video topic from the category name
- `topic` → Passes through as-is (whitespace trimmed)
- `event` → Calls Groq LLM to extract a narrative and suggest a dramatic title

**Key function:** `RunInputParser(job, progress)`

**LLM calls:** `callGroq(systemPrompt, userPrompt)` → Groq API (`llama-3.3-70b-versatile`)
- Endpoint: `https://api.groq.com/openai/v1/chat/completions`
- Auth: `Authorization: Bearer {GROQ_API_KEY}`
- Timeout: 60s

**Helper:** `extractJSON(text)` — Extracts JSON from LLM responses that may be wrapped in markdown code blocks.

**Duration clamping:** Short format is forced to 1 min, long format is clamped to 5–20 min.

---

## Stage 2: Script Generation (`pipeline/script_gen.go`)

**Purpose:** Generate a full `ScriptDocument` with segments, visual cues, and YouTube metadata.

**Behavior by format:**
- `long` → `generateLongScript()` — Targets `duration_min * 130` words (130 WPM TTS pace)
- `short` → `generateShortScript()` — Targets 120 words, 45-60 seconds
- `both` → Generates long first, then short as `ShortVersion`

**Pre-generated script path:** If `payload.PreGeneratedScript` is set (from the preview step), it is used directly without calling the LLM.

**LLM prompt engineering:**
- System prompt establishes the scriptwriter persona and tone
- User prompt specifies: topic, duration, word count, tone, language, total visual budget, and the **pacing target** ("one sub_visual per N seconds")
- Critical visual rules enforce unique sub_visual queries that match narration context
- Response must be valid JSON matching the `ScriptDocument` schema

**Visual pacing enforcement (`enforceVisualPacing`):**
After the LLM returns, a post-processing pass caps each segment's `sub_visuals`
to `ceil(segment_duration / seconds_per_visual)`. Hook and CTA segments use a
~50% slower pace (and are hard-capped at 2 visuals each) so the message lands.
When the LLM produces too many sub_visuals, an evenly-spaced subset is kept to
preserve narrative coverage rather than truncating the tail. This guarantees
the user's pacing choice is respected even if the LLM drifts.

**Retry logic:** `callGroqForScript()` retries up to 3 times with 5s/10s/15s backoff on JSON parse failures.

**Script refinement:** `RefineScript(currentScript, userPrompt, config)` takes an existing script and modification instructions, sends both to Groq to produce an updated script.

**Output:** Saves `script.json` to `workspace/job_{id}/script.json`.

---

## Stage 3: Voiceover (`pipeline/voiceover.go`)

**Purpose:** Generate voice audio for each script segment.

**Modes:**

### AI Mode (`voiceover_mode = "ai"`)
- Uses ElevenLabs TTS API
- Model: `eleven_multilingual_v2`
- Voice settings: stability=0.5, similarity_boost=0.75, style=0.4, use_speaker_boost=true
- Endpoint: `https://api.elevenlabs.io/v1/text-to-speech/{voice_id}`
- Auth: `xi-api-key: {ELEVENLABS_API_KEY}`
- Retry: 3 attempts with 10s/20s/30s backoff
- Output: `segments/seg_XX_voice.mp3`

### Google Cloud TTS Mode (`voiceover_mode = "gcp_tts"`)
- Uses Google Cloud Text-to-Speech REST API (`pipeline/gcp_tts.go`)
- Endpoint: `POST https://texttospeech.googleapis.com/v1/text:synthesize?key={GOOGLE_CLOUD_TTS_API_KEY}`
- Voice selection: user picks `gcp_voice_name` (e.g. "en-US-Neural2-D") and `gcp_language_code` (e.g. "en-US")
- Audio encoding: MP3
- Retry: 3 attempts with 10s/20s/30s backoff
- Output: `segments/seg_XX_voice.mp3`
- Free tier: 1M chars/month for WaveNet/Neural2, 4M chars/month for Standard

**Authentication (two supported modes — pick one or both):**

| Mode | Env var(s) | Voice families unlocked |
| --- | --- | --- |
| API key | `GOOGLE_CLOUD_TTS_API_KEY` | Standard, Wavenet, Neural2, News, Casual, Polyglot, regular Chirp HD |
| Service account (OAuth) | `GOOGLE_APPLICATION_CREDENTIALS_JSON` (raw JSON) **or** `GOOGLE_APPLICATION_CREDENTIALS` (file path) | All of the above **plus** Chirp 3 HD and Studio |

Service account preference is automatic: when configured, every TTS call uses Bearer-token auth via `pipeline/gcp_auth.go` (which uses `golang.org/x/oauth2/google.CredentialsFromJSON` with scope `https://www.googleapis.com/auth/cloud-platform` and caches/refreshes tokens internally). Otherwise it falls back to `?key=<api_key>`.

**Why this matters:** Google rejects API-key auth for Chirp 3 HD / Studio voices with a misleading 400 "This voice requires a model name to be specified." Our auth helper short-circuits before the call, so:
- In API-key mode, premium voices are filtered out of `ListGCPVoices` so users can't pick them.
- In service-account mode, premium voices are returned (with a `premium: true` flag the UI uses to badge them).
- If a user ever supplies a premium voice name with only API-key auth (e.g. stale cached UI state), `SynthesizeGCPTTS` returns a clear local error before round-tripping to Google.

### Manual Mode (`voiceover_mode = "manual"`)
- Decodes base64 audio from `ManualAudioBase64[segment_id]`
- Converts WebM → MP3 via FFmpeg
- Falls back to silent placeholder audio if a segment's recording is missing

### Music-Only Mode (`voiceover_mode = "none"`)
- No narration. The script is still generated (it drives visual planning and segment timing) but no TTS or recording is performed.
- Stage 3 produces silent MP3 placeholders sized to each segment's planned `DurationSec` (using FFmpeg's `anullsrc` filter).
- Stage 6 detects this mode (`voiceMuted=true` flag derived from `payload.VoiceoverMode == "none"`) and:
  - **Skips Whisper caption generation** entirely — there's no speech to transcribe and Whisper would either return empty or hallucinate against pure silence.
  - **Boosts music volume** in `prepareFinalAudio` from `0.12` (ducked under voice) to `0.9` (near-full) so the soundtrack carries the video.
- Combined with `music_mode = "skip"`, this produces a fully silent video. Combined with `music_mode = "auto"` or `manual`, it produces a music-driven video with no narration — useful for highlight reels, mood / aesthetic shorts, and montages.

**Voice ID mapping (ElevenLabs):**
| ID | ElevenLabs Voice ID | Description |
|----|---------------------|-------------|
| `adam` | `pNInz6obpgDQGcFmaJgB` | Deep, authoritative male |
| `rachel` | `21m00Tcm4TlvDq8ikWAM` | Warm, professional female |
| `domi` | `AZnzlk1XvdvUeBnXmlld` | Strong, confident female |
| `josh` | `TxGEqnHWrfWFTfGW9XjX` | Young, energetic male |

---

## Stage 4: Visual Fetch (`pipeline/visual.go`)

**Purpose:** Download stock video clips or generate AI images for each segment's visual cues.

**Two code paths:**

### Sub-visual mode (preferred)
When segments have `SubVisuals`, each sub-visual is fetched individually:
- Key format: `"{segment_id}_{sub_index}"`
- The `video_style` setting **overrides** the AI's type assignment:
  - `stock` → all sub_visuals become clips
  - `ai_images` → all become images
  - `mixed` → respects the AI's `type` field

### Legacy mode (fallback)
When no `SubVisuals` exist, falls back to one visual per segment using `VisualQuery`/`VisualCue`.

**Stock clips (Pexels):**
- Endpoint: `https://api.pexels.com/videos/search`
- Auth: `Authorization: {PEXELS_API_KEY}`
- **Orientation** is derived from `payload.aspect_ratio` — `landscape` / `portrait` / `square` are passed directly to Pexels' `orientation` query param so portrait Shorts source from portrait footage (no zoom-crop quality loss).
- **Source resolution** is governed by the `output_quality` profile (`pipeline/quality.go::ProfileFor`):
  - `draft` — `size=medium`, minimum width 1280
  - `standard` — `size=large`, minimum width 1280
  - `high` — `size=large`, minimum width 1920 (FHD master)
- **File selection (`pickBestVideoFile`)**: candidates are sorted by `width` ascending; we pick the smallest file with `width >= target_output_width` (1920 for landscape, 1080 for portrait). This avoids upscaling 720p → 1080p **and** wastes no bandwidth on 4K when our render frame is only 1080p. If no file meets the target, falls back to the largest available.
- **Dedup:** Tracks used queries + video IDs + URLs across the whole job (`usedVideoTracker`) so the same asset never appears twice. When all top results are already used, falls back gracefully (relax min-width gate, then last resort allows reuse).
- **Retry:** Broadens query to first 3 words on failure.

**AI Images (Together AI → HuggingFace fallback):**

Together AI:
- Model: `black-forest-labs/FLUX.1-schnell-Free`
- Endpoint: `https://api.together.xyz/v1/images/generations`
- Resolution: derived from `payload.aspect_ratio` — 1792×1024 (landscape), 1024×1792 (portrait), or 1280×1280 (square)
- Steps: 4

HuggingFace (fallback):
- Model: `black-forest-labs/FLUX.1-schnell`
- Endpoint: `https://api-inference.huggingface.co/models/...`
- Returns raw image bytes
- Handles 503 (model loading) with progressive waits

**Prompt enhancement:** `buildAIPrompt(visualCue, tone)` adds tone-specific style suffixes:
| Tone | Style Keywords |
|------|---------------|
| dramatic | cinematic lighting, high contrast, epic atmosphere |
| suspenseful | dark moody lighting, shadows, thriller atmosphere |
| educational | clean bright lighting, documentary style |
| conversational | natural soft lighting, warm tones |
| motivational | golden hour lighting, uplifting, vibrant colors |
| humorous | bright vivid colors, playful composition |

**Cross-fallback:** If the primary source fails (stock or AI), it tries the alternative.

---

## Stage 5: Music (`pipeline/music.go`)

**Purpose:** Fetch royalty-free background music from Jamendo.

**Modes:**
- `skip` → No music, stage completes immediately
- `manual` → Downloads from `MusicUrl`, optionally crops with FFmpeg using `MusicStart`/`MusicEnd`
- `auto` → Searches Jamendo by script tone

**Jamendo auto search:**
- Endpoint: `https://api.jamendo.com/v3.0/tracks/`
- Auth: `client_id={JAMENDO_CLIENT_ID}`
- Filters: instrumental only, duration range matching video, popularity boost
- Tone → tag mapping:
  | Tone | Jamendo Tags | Speed |
  |------|-------------|-------|
  | dramatic | cinematic dramatic epic | medium high |
  | suspenseful | suspense dark mysterious thriller | low medium |
  | educational | calm ambient relaxing background | low medium |
  | conversational | acoustic light happy positive | medium |
  | motivational | uplifting motivational energetic | high veryhigh |
  | humorous | fun playful comedy upbeat | medium high |

**Non-fatal:** Music failure does not stop the pipeline — logs an error and continues.

---

## Stage 6: Video Render (`pipeline/renderer.go`)

**Purpose:** Combine all assets into the final video using FFmpeg.

**Sub-steps:**

### 6a: Build Segment Videos
For each segment:
1. If sub-visuals exist, process each sub-clip:
   - **Images** → converted to video with Ken Burns zoom effect (`zoompan` filter)
   - **Clips** → trimmed to `duration / num_sub_visuals` seconds, scaled/padded to target resolution
2. Concatenate sub-clips into one segment video (re-encoded to prevent freeze artifacts)
3. Overlay voiceover audio onto the segment video (`-shortest` flag syncs to audio length)

### 6b: Concatenate Segments
Uses FFmpeg `concat` demuxer with `-c copy` to join all segment videos.

### 6c: Generate Captions
Runs Whisper CLI: `whisper {video} --model base --output_format srt --output_dir {dir}`
- Non-fatal: if Whisper fails, continues without captions.

### 6d: Final Render
Combines video + music + captions:
- Music is looped (`-stream_loop -1`) to cover the full video duration
- Music volume: 12% (`volume=0.12`), voice volume: 100%
- Caption styles: `bold_white` (bold + outline + shadow) or `subtitle` (clean outline)
- Output: `final_output.mp4` with `libx264`, `crf 21`, `faststart`

**Resolution (derived from `payload.aspect_ratio`):**
- `landscape` → 1920×1080 (16:9, default for long-form / YouTube)
- `portrait` → 1080×1920 (9:16, default for Shorts / TikTok / Reels)
- `square` → 1080×1080 (1:1, Instagram feed / LinkedIn)

Aspect ratio is independent of duration `format`, so any combination is valid
(e.g. a long-form video in portrait for TikTok, or a Short in landscape for X).
The renderer's `resolveResolution()` helper centralises this mapping so all
filter graphs (segment scale/pad, AI image gen, final mux) stay consistent.

**Fit mode (`payload.fit_mode`):**
- `fill` (default) — `scale=W:H:force_original_aspect_ratio=increase, crop=W:H`.
  Frame is fully covered with content; mismatched aspect ratios get center-cropped.
- `fit` — `scale=W:H:force_original_aspect_ratio=decrease, pad=W:H:...:color=black`.
  The entire source is preserved; black bars fill any leftover space.

The `fitFilter()` helper in `pipeline/renderer.go` centralises this so the
sub-clip prep step and the segment sync filter chain both apply the same
behavior consistently.

**Output Quality (`payload.output_quality`):**

The renderer reads the user-selected quality once via `ProfileFor()` (`pipeline/quality.go`) and applies it consistently across every encode pass:

| Quality | x264 `-preset` | `-crf` | `-r` (fps) | Pexels source |
| --- | --- | --- | --- | --- |
| `draft` | `ultrafast` | `28` | `30` | `size=medium`, ≥1280w |
| `standard` (default) | `fast` | `23` | `30` | `size=large`, ≥1280w |
| `high` | `medium` | `18` | `30` | `size=large`, ≥1920w |

**Where the profile applies:**
- **Master encode passes** — segment sync (per-segment audio-synced master), `raw_combined.mp4` concat (full video master), and the captions burn-in pass in `finalRender` — all use the profile's `preset`/`crf`/`fps`.
- **Intermediate prep passes** (sub-clip trims, sub-clip concats) intentionally stay at `fast`/`crf=23` even in `high` mode — they get re-encoded again at segment-sync time, so spending CPU on them is wasted.
- **Image-loop ken-burns passes** scale the `zoompan` frame count by `profile.FPS` so the motion remains smooth at the chosen framerate.
- **Trim handler** (`/api/jobs/{id}/trim`) is a one-off post-render edit and stays at `fast`/`crf=21`; it doesn't carry the original render's profile.

**Why intermediate stays cheap:** Each pipeline pass loses a tiny bit of detail. The expensive `medium`/`crf=18` pass is reserved for the *last* re-encode each frame goes through — that's where the user-visible quality is set. Earlier passes at `fast`/`crf=23` produce fine source for that final encode without inflating render time.

---

## Stage 7: Upload (`pipeline/uploader.go`)

**Purpose:** Upload the final video to YouTube via the Data API v3.

**Prerequisites:**
- `client_secret.json` — YouTube OAuth client credentials
- `token.json` — Saved OAuth access/refresh token

**If OAuth files are missing:** Stage succeeds gracefully with `youtube_url = "local://{path}"`.

**Upload metadata:**
- Title: First option from `ScriptDocument.TitleOptions`
- Description: From `ScriptDocument.Description`
- Tags: From `ScriptDocument.Tags`
- Category: `27` (Education)
- Privacy: `private` (always)

**Scheduling:**
- `immediate` → Upload as private
- `HH:MM` (e.g., `19:00`) → Sets `publishAt` for today (or tomorrow if time has passed)

**Approval gate (in Orchestrator):**
Before Stage 7 runs, if `auto_upload = false` and `approved = false`:
- Sets status to `pending_approval`
- Sends WebSocket event with status `"pending_approval"`
- Pipeline returns (pauses)
- User approves via `POST /api/jobs/{id}/approve`
- Orchestrator re-enqueues from Stage 7

---

## Error Handling

- **Fatal errors:** Stage function returns an error → pipeline stops, status set to `failed`, error recorded in DB
- **Non-fatal errors:** Logged via `job.AddError()` but pipeline continues (e.g., music failure, caption failure)
- **Retry:** Currently `RunFrom(startStage)` just calls `Run()` (full re-run). True stage resumption is not implemented.

## Worker Queue (`worker/queue.go`)

- Buffered Go channel (capacity: 100 jobs)
- Configurable goroutine count via `MAX_CONCURRENT_JOBS` (default: 1)
- 1-second delay before starting to allow WebSocket connection
- Calls `Orchestrator.Run()` or `Orchestrator.RunFrom()` based on `IsRetry`
