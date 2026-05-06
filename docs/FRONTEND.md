# Frontend Architecture

The frontend is a vanilla HTML/CSS/JS single-page application served as static files from the `ui/` directory. No build step, no framework, no bundler.

## Files

| File | Purpose |
|------|---------|
| `ui/index.html` | Complete HTML structure with three "pages" (toggled via JS) |
| `ui/app.js` | All application logic: API calls, state, WebSocket, recording |
| `ui/styles.css` | Full styling with CSS custom properties, responsive layout |

## Page Structure

The app has three "pages" toggled by showing/hiding div elements:

### 1. Create Video (`#page-create`) — Wizard Flow
The main workflow uses a **6-step wizard** where each step is shown one at a time. Users navigate with Next/Back buttons, and can click completed stepper steps to jump back.

| Step | Name | What It Contains |
|------|------|-----------------|
| 0 | Content | Input type tabs (category/topic/event), textarea, hint chips |
| 1 | Format | Long/Short/Both cards, duration slider, aspect ratio chips, fit mode chips, **output quality chips** |
| 2 | Script | Voiceover mode toggle (AI / Google TTS / Manual / 🎵 Music Only), voice selection, Google TTS language+voice picker with preview, script generation + AI chat refinement, manual recording, tone chips |
| 3 | Visuals | Video mode (auto/manual), style (stock/AI/mixed), clip/image count sliders |
| 4 | Music | Music mode (auto/manual/skip), Jamendo search, track selection + crop |
| 5 | Finalize | Language, upload schedule, caption style, auto-upload toggle, summary card, Generate button |

**Wizard navigation:**
- `wizardNext()` / `wizardBack()` — Sequential navigation with validation
- `wizardJumpTo(step)` — Click a completed stepper step to jump back
- Step 0 validates that `raw_input` is non-empty before allowing forward navigation
- The final step renders a summary of all selected options

**After clicking Generate**, the wizard body is replaced by the Pipeline Progress panel with WebSocket updates and the approval flow.

### 2. Job History (`#page-jobs`)
- Table of past jobs with status, title, format, dates
- Actions: download, retry, delete

### 3. Settings (`#page-settings`)
- Displays API key status (configured/not configured) from `GET /api/status`
- System tool availability (FFmpeg, Whisper)

## State Management

A single global `state` object mirrors the `InputPayload` fields:

```javascript
const state = {
    raw_input: '',
    input_type: 'topic',
    format: 'long',
    duration_min: 8,
    voiceover_mode: 'ai',
    voice_id: 'adam',
    video_mode: 'auto',
    video_style: 'stock',
    music_mode: 'auto',
    script_tone: 'dramatic',
    language: 'english',
    upload_schedule: 'immediate',
    caption_style: 'bold_white',
    auto_upload: false,
    clip_count: 0,
    image_count: 0,
    music_url: '',
    music_start: 0,
    music_end: 0,
    pre_generated_script: null,
    manual_audio_base64: {}
};
```

Additional globals: `currentJobId`, `ws` (WebSocket), `scriptChatHistory`, `currentDraftScript`, recording state per segment.

## API Communication

- **Base API URL:** Hardcoded as `const API = 'http://localhost:8000'`
- **REST calls:** Standard `fetch()` with JSON body
- **Jamendo search:** Uses relative URL `/api/music/jamendo/search` (same-origin)
- **WebSocket:** `ws://localhost:8000/ws/{job_id}` for progress updates

## Key Workflows

### Script Preview & Chat
1. User fills in form fields
2. `generateScript()` → `POST /api/preview-script` with `InputPayload`
3. Script displayed in preview panel
4. User can chat to refine: `submitScriptRefinement()` → `POST /api/refine-script`
5. User approves script → stored in `state.pre_generated_script`

### Google Cloud TTS
1. When `voiceover_mode = "gcp_tts"`, shows language/voice selection panel
2. Language dropdown (curated BCP-47 list: en-US, en-GB, hi-IN, etc.)
3. Voice dropdown populated dynamically from `GET /api/gcp-tts/voices?language=X`
4. After script approval, per-segment "Play" buttons preview audio via `POST /api/gcp-tts/synthesize`
5. State fields: `state.gcp_voice_name`, `state.gcp_language_code`
6. Voices are cached client-side per language to avoid repeated API calls
7. The dropdown's contents depend on which GCP auth is configured server-side:
   - **API key only** → Standard, Wavenet, Neural2, News, Casual, Polyglot, regular Chirp HD.
   - **Service account configured** (`GOOGLE_APPLICATION_CREDENTIALS_JSON` or `GOOGLE_APPLICATION_CREDENTIALS`) → all of the above **plus** Chirp 3 HD and Studio voices, marked with a "★ Premium" badge in the dropdown.
8. The `/api/gcp-tts/voices` response includes `service_account_configured` and `premium_voices_available` flags. The UI mirrors them in `gcpServiceAccountConfigured` to show a warning panel and block "Next" on the wizard if a premium voice is somehow selected without auth (e.g. stale cached state).

### Music-Only Mode
1. When `voiceover_mode = "none"`, `setVoiceMode('none')` hides every voice-related panel (voice cards, GCP TTS panel, manual recording panel) and shows a `#no-voice-panel` info card explaining the mode.
2. The script generation panel stays visible — the script is still needed to plan visuals and segment durations.
3. Wizard validation skips voice-specific checks (no API keys / voice IDs / recordings required).
4. The summary card on Step 5 shows "🎵 Music only (no narration)" for the Voice row.
5. Backend renders silent placeholder MP3s and boosts music to near-full volume; captions are skipped server-side.

### Output Quality
1. Step 1 (Format) has a third chip group `#quality-chips` with Draft / Standard / High options.
2. `setOutputQuality()` updates `state.output_quality` (default `'standard'`).
3. The value flows to the backend via `JSON.stringify(state)` in `createJob`. No special wiring beyond the state field.
4. The Step 5 summary appends the quality (e.g. "long (8 min) • landscape • fill • high quality") so users can verify their choice before generating.
5. Server-side, `pipeline/quality.go::ProfileFor` maps the string to (Pexels `size`, min clip width, x264 `preset`, `crf`, `fps`) and the pipeline applies it consistently.

### Manual Voiceover Recording
1. When `voiceover_mode = "manual"`, shows recording section per segment
2. Uses `MediaRecorder` API to capture audio
3. Converts to base64, stored in `state.manual_audio_base64[segment_id]`

### Job Creation & Progress
1. `createJob()` → `POST /api/jobs` with full state
2. Opens WebSocket to `ws://localhost:8000/ws/{job_id}`
3. `ProgressEvent` messages update the UI pipeline tracker in real-time
4. On `pending_approval` status, shows video player and approve/reject buttons
5. On `completed`, shows download link and YouTube URL

### Jamendo Music Browser
1. `searchJamendo()` → `GET /api/music/jamendo/search?q=...&mood=...`
2. Results shown as cards with play/pause, duration, genre
3. User selects a track → populates `state.music_url`, `music_start`, `music_end`

## Styling Notes

- Uses CSS custom properties (variables) for theming
- Google Fonts loaded in CSS
- Responsive design with media queries
- Animation for pipeline stage progress
- Dark-themed design

## Known Quirks

- API base URL is hardcoded to `localhost:8000` — would need changing for deployment
- Wizard stepper step labels are hidden on mobile (< 900px) to save space; only numbered circles are shown
