# Data Models

All Go structs are defined in the `models/` package. This document covers every type and its fields.

---

## InputPayload (`models/input.go`)

The primary object created from user input. Starting point for every pipeline run.

| Field | JSON Key | Type | Valid Values | Default | Description |
|-------|----------|------|--------------|---------|-------------|
| JobID | `job_id` | string | UUID | (auto-generated) | Unique job identifier |
| RawInput | `raw_input` | string | non-empty | — | **Required.** User's topic, category name, or event description |
| InputType | `input_type` | string | `category`, `topic`, `event` | — | How to interpret RawInput |
| Format | `format` | string | `long`, `short`, `both` | `long` | Video format |
| DurationMin | `duration_min` | int | 5–20 (long), forced 1 (short) | 8 (long), 1 (short) | Target duration in minutes |
| VoiceoverMode | `voiceover_mode` | string | `ai`, `manual` | `ai` | TTS mode |
| VoiceID | `voice_id` | string | `adam`, `rachel`, `domi`, `josh` | `adam` | ElevenLabs voice (only for ai mode) |
| VideoMode | `video_mode` | string | `auto`, `manual` | `auto` | Visual asset fetching mode |
| VideoStyle | `video_style` | string | `stock`, `ai_images`, `mixed` | `stock` | Type of visuals to use |
| MusicMode | `music_mode` | string | `auto`, `skip`, `manual` | `auto` | Background music mode |
| ScriptTone | `script_tone` | string | `dramatic`, `educational`, `conversational`, `suspenseful`, `motivational`, `humorous` | `dramatic` | Script writing tone |
| Language | `language` | string | `english`, `hindi`, `hinglish` | `english` | Script language |
| UploadSchedule | `upload_schedule` | string | `immediate`, `HH:MM` | `immediate` | When to publish on YouTube |
| CaptionStyle | `caption_style` | string | `bold_white`, `subtitle`, `none` | `bold_white` | Caption rendering style |
| AutoUpload | `auto_upload` | bool | — | `false` | If false, pause before upload for approval |
| ClipCount | `clip_count` | int | 0–30 | auto-calculated from format/style | Number of stock video clips |
| ImageCount | `image_count` | int | 0–20 | auto-calculated from format/style | Number of AI images |
| MusicUrl | `music_url` | string | URL | — | Jamendo track URL (manual music mode) |
| MusicStart | `music_start` | int | seconds | 0 | Crop start point for manual music |
| MusicEnd | `music_end` | int | seconds | 0 | Crop end point for manual music |
| PreGeneratedScript | `pre_generated_script` | *ScriptDocument | — | nil | Pre-approved script from preview step |
| ManualAudioBase64 | `manual_audio_base64` | map[int]string | segment_id → base64 | nil | Recorded audio per segment (manual voiceover) |
| CreatedAt | `created_at` | string | RFC3339 | (auto) | Job creation timestamp |

### Default ClipCount/ImageCount Calculation

When both are 0, defaults are computed based on `format` and `video_style`:

| Style | Short Format | Long Format |
|-------|-------------|-------------|
| `stock` | clips=6 | clips=max(duration*2, 8) |
| `ai_images` | images=6 | images=max(duration*2, 8) |
| `mixed` | clips=4, images=2 | clips=max(duration, 4), images=max(duration, 4) |

---

## ScriptDocument (`models/script.go`)

Output of Stage 2 (Script Generator). Contains the full video script.

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| JobID | `job_id` | string | Parent job ID |
| Format | `format` | string | `long` or `short` |
| TitleOptions | `title_options` | []string | 3 YouTube title suggestions |
| Description | `description` | string | YouTube video description |
| Tags | `tags` | []string | Up to 15 YouTube tags |
| ThumbnailText | `thumbnail_text` | string | Short punchy thumbnail text |
| Hook | `hook` | string | Opening 5-second attention grabber |
| Segments | `segments` | []ScriptSegment | Ordered script segments |
| TotalSegments | `total_segments` | int | Count of segments |
| TotalDuration | `total_duration_sec` | int | Sum of all segment durations |
| ShortVersion | `short_version` | *ShortScript | Present when format="both" |

## ScriptSegment (`models/script.go`)

One unit of the script (~30-90 seconds for long, 8-15 seconds for short).

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| SegmentID | `segment_id` | int | Sequential ID (1-based) |
| Type | `type` | string | `hook`, `body`, or `cta` |
| Text | `text` | string | Full narration text |
| WordCount | `word_count` | int | Word count |
| DurationSec | `duration_sec` | int | Estimated duration in seconds |
| VisualCue | `visual_cue` | string | Natural language visual description (legacy) |
| VisualQuery | `visual_query` | string | Pexels search term (legacy) |
| MusicMood | `music_mood` | string | `dramatic`, `calm`, `upbeat`, `mysterious` |
| Transition | `transition` | string | `fade`, `cut`, `slide` |
| SubVisuals | `sub_visuals` | []SubVisual | Fine-grained visual cues within this segment |

## SubVisual (`models/script.go`)

One visual asset shown within a segment. Multiple sub-visuals create a "B-roll" effect.

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| Index | `index` | int | Order within segment (0-based) |
| Query | `query` | string | Pexels/image search term (2-4 words) |
| Description | `description` | string | Natural language description |
| Type | `type` | string | `clip` (stock video) or `image` (AI-generated) |

## ShortScript (`models/script.go`)

YouTube Shorts version (present when `format = "both"`).

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| Hook | `hook` | string | Opening line |
| Segments | `segments` | []ScriptSegment | Short-form segments |
| TotalDuration | `total_duration_sec` | int | Total duration |

---

## JobContext (`models/job.go`)

Runtime state object passed between all pipeline stages. Mutex-protected.

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| JobID | `job_id` | string | UUID |
| Status | `status` | JobStatus | Current pipeline status |
| CurrentStage | `current_stage` | int | Currently executing stage (1-7) |
| Payload | `payload` | *InputPayload | Original input |
| Script | `script` | *ScriptDocument | Generated script (after Stage 2) |
| VoiceFiles | `voice_files` | map[string]string | segment_id → voice MP3 file path |
| ClipFiles | `clip_files` | map[string]string | key → video/image file path |
| MusicFile | `music_file` | string | Background music file path |
| CaptionsFile | `captions_file` | string | SRT captions file path |
| FinalVideo | `final_video` | string | Final output MP4 path |
| YouTubeURL | `youtube_url` | string | YouTube video URL after upload |
| Errors | `errors` | []string | Non-fatal error messages |
| StageLogs | `stage_logs` | map[int]string | Per-stage completion logs |
| StartedAt | `started_at` | string | RFC3339 timestamp |
| CompletedAt | `completed_at` | string | RFC3339 timestamp |
| Approved | `approved` | bool | Whether user approved upload |

### ClipFiles Key Format

- **Sub-visual mode:** `"{segment_id}_{sub_index}"` (e.g., `"1_0"`, `"1_1"`, `"2_0"`)
- **Legacy mode:** `"{segment_id}"` (e.g., `"1"`, `"2"`)

---

## JobStatus (`models/job.go`)

Enum of possible job statuses:

| Value | Description |
|-------|-------------|
| `pending` | Initial state |
| `queued` | Waiting in worker queue |
| `running` | Pipeline actively executing |
| `completed` | All stages finished successfully |
| `failed` | A stage failed fatally |
| `paused` | (Reserved, not currently used) |
| `pending_approval` | Rendered, waiting for user to approve upload |

---

## JobDBRecord (`models/job.go`)

SQLite row model for persisting job history.

| Field | JSON Key | DB Column | Type |
|-------|----------|-----------|------|
| ID | `id` | `id` | TEXT PK |
| Status | `status` | `status` | TEXT |
| Format | `format` | `format` | TEXT |
| InputType | `input_type` | `input_type` | TEXT |
| RawInput | `raw_input` | `raw_input` | TEXT |
| Title | `title` | `title` | TEXT |
| YouTubeURL | `youtube_url` | `youtube_url` | TEXT |
| DurationSec | `duration_sec` | `duration_sec` | INTEGER |
| VoiceMode | `voice_mode` | `voice_mode` | TEXT |
| VideoMode | `video_mode` | `video_mode` | TEXT |
| AutoUpload | `auto_upload` | `auto_upload` | INTEGER (0/1) |
| CreatedAt | `created_at` | `created_at` | DATETIME |
| CompletedAt | `completed_at` | `completed_at` | DATETIME (nullable) |
| ErrorMsg | `error_msg` | `error_msg` | TEXT |

---

## ProgressEvent (`models/job.go`)

Sent via WebSocket to the frontend during pipeline execution.

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| JobID | `job_id` | string | Job UUID |
| Stage | `stage` | int | Stage number (1-7) |
| StageName | `stage_name` | string | Human-readable stage name |
| ProgressPct | `progress_pct` | int | 0-100 progress within stage |
| Message | `message` | string | Human-readable status message |
| Status | `status` | string | `""`, `"failed"`, `"completed"`, `"pending_approval"` |
| YouTubeURL | `youtube_url` | string | Set on completion if uploaded |
| Duration | `duration` | string | (reserved) |
| Timestamp | `timestamp` | string | RFC3339 |

---

## StageNames (`models/job.go`)

Static map of stage numbers to names:

| Stage | Name |
|-------|------|
| 1 | Input Parsing |
| 2 | Script Generation |
| 3 | Voiceover |
| 4 | Visual Fetch |
| 5 | Music |
| 6 | Video Render |
| 7 | Upload |
