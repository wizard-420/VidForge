# Known Issues & Technical Debt

This document tracks known bugs, incomplete features, and inconsistencies in the codebase.

---

## Incomplete Features

### 1. Job Retry Does Not Resume From Stage
**Location:** `pipeline/orchestrator.go` — `RunFrom()`
**Issue:** `RunFrom(startStage)` is supposed to resume a failed job from a specific stage, but it currently just calls `Run()` (full pipeline from Stage 1). The `worker/queue.go` passes `StartStage` correctly, but the orchestrator ignores it.
**Impact:** Retrying a job that failed at Stage 6 will re-run all 7 stages, wasting time and API credits.

### 2. Settings Update Not Implemented
**Location:** `api/handlers.go` — `handleUpdateSettings()`
**Issue:** Returns a stub message: "Settings update not yet implemented — edit .env directly".
**Impact:** Users cannot update API keys via the dashboard.

### 3. Job Cleanup Not Implemented
**Location:** `config/config.go` — `CleanupDays` field
**Issue:** `CLEANUP_AFTER_DAYS` is loaded from env but no background job or API endpoint exists to actually clean up old workspace directories or database records.
**Impact:** Disk space grows indefinitely.

---

## .env.example Inaccuracies

### 4. Wrong LLM Provider Listed
**Location:** `.env.example`
**Issue:** Lists `ANTHROPIC_API_KEY` (Anthropic/Claude) as required, but the codebase exclusively uses **Groq** (`GROQ_API_KEY`). There is no Anthropic integration anywhere.
**Impact:** New users will set up the wrong API key.

### 5. Missing Required Keys in .env.example
**Location:** `.env.example`
**Issue:** `GROQ_API_KEY` and `JAMENDO_CLIENT_ID` are not listed.

### 6. Wrong Description for Pixabay
**Location:** `.env.example`
**Issue:** Says "Pixabay — Required for background music", but music comes from Jamendo. Pixabay key is loaded into config but never used in any pipeline stage.

---

## Unused Configuration

### 7. PIXABAY_API_KEY Not Used
**Location:** `config/config.go`
**Issue:** Loaded into `config.App.PixabayAPIKey`, shown in health check and masked settings, but not referenced by any pipeline stage.

### 8. OPENAI_API_KEY Not Used
**Location:** `config/config.go`
**Issue:** Same as Pixabay — loaded and displayed but never used. AI images use Together AI and HuggingFace, not OpenAI.

---

## Frontend Issues

### 9. Hardcoded API URL
**Location:** `ui/app.js`
**Issue:** `const API = 'http://localhost:8000'` is hardcoded. The Jamendo search uses relative URL `/api/music/jamendo/search` (correct), but all other calls use the absolute localhost URL.
**Impact:** Won't work in Docker or any non-localhost deployment without modification.

---

## Branding Inconsistency

### 11. VidForge vs YouTube Automation Studio
**Issue:** The repository folder is named "VidForge", but the Go module is `yt-automation-studio`, the UI banner says "YouTube Automation Studio", and the Docker container is named `yt-automation-studio`.
**Impact:** Confusion about the project's canonical name.

---

## Potential Robustness Issues

### 12. In-Memory Job State Not Persisted
**Issue:** The full `JobContext` (script, file paths, etc.) only lives in the `activeJobs` map. If the server crashes mid-pipeline, this state is lost. Only the DB record (status, basic metadata) is recoverable.

### 13. No Request Rate Limiting
**Issue:** No rate limiting on API endpoints. A client could flood the job queue.

### 14. WebSocket Cleanup Race Condition
**Location:** `api/websocket.go`
**Issue:** `BroadcastProgress` acquires `RLock` twice (once to check existence, once to iterate). Between the two locks, the client map could change. Also, deleting from a map while iterating over it inside an RLock (not WLock) is not safe.
