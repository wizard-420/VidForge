# Database Schema & Storage

VidForge uses **SQLite** via the pure-Go driver `modernc.org/sqlite`. The database file is stored at `storage/jobs.db`.

All database operations are in `storage/db.go`.

## Configuration

- **WAL mode** is enabled at startup for better concurrent read performance
- Database path: `filepath.Join("storage", "jobs.db")` (hardcoded in `main.go`)
- Connection is opened once at startup and closed on shutdown via `defer storage.Close()`

## Schema

### Table: `jobs`

```sql
CREATE TABLE IF NOT EXISTS jobs (
    id            TEXT PRIMARY KEY,        -- UUID
    status        TEXT NOT NULL DEFAULT 'pending',
    format        TEXT,                    -- long | short | both
    input_type    TEXT,                    -- category | topic | event
    raw_input     TEXT,                    -- Original user input
    title         TEXT,                    -- Generated video title (set on completion)
    youtube_url   TEXT,                    -- YouTube URL (set on upload)
    duration_sec  INTEGER DEFAULT 0,       -- Final video duration
    voice_mode    TEXT,                    -- ai | manual
    video_mode    TEXT,                    -- auto | manual
    auto_upload   INTEGER DEFAULT 0,       -- 0=false, 1=true
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at  DATETIME,                -- Set on completion or failure
    error_msg     TEXT                     -- Set on failure
);
```

### Indexes

```sql
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at DESC);
```

### Migrations

```sql
-- Adds auto_upload column for older databases (runs blindly, ignores error if column exists)
ALTER TABLE jobs ADD COLUMN auto_upload INTEGER DEFAULT 0;
```

## CRUD Operations

| Function | SQL | Notes |
|----------|-----|-------|
| `InsertJob(payload)` | `INSERT INTO jobs (id, status, format, input_type, raw_input, voice_mode, video_mode, auto_upload, created_at) VALUES (...)` | Status initialized as `'queued'` |
| `UpdateJobStatus(jobID, status)` | `UPDATE jobs SET status = ? WHERE id = ?` | Called throughout pipeline |
| `UpdateJobCompleted(jobID, title, youtubeURL, durationSec)` | `UPDATE jobs SET status='completed', title=?, youtube_url=?, duration_sec=?, completed_at=? WHERE id=?` | Called at pipeline end |
| `UpdateJobFailed(jobID, errorMsg)` | `UPDATE jobs SET status='failed', error_msg=?, completed_at=? WHERE id=?` | Called on fatal stage failure |
| `GetJob(jobID)` | `SELECT ... FROM jobs WHERE id = ?` | Returns `*JobDBRecord` |
| `ListJobs(limit)` | `SELECT ... FROM jobs ORDER BY created_at DESC LIMIT ?` | Default limit: 50 |
| `DeleteJob(jobID)` | `DELETE FROM jobs WHERE id = ?` | Also removed from `activeJobs` in-memory map by handler |

## Important Notes

- **Only metadata is persisted to SQLite.** The full `JobContext` (script, file paths, etc.) lives only in-memory while the job is active.
- **No foreign keys or related tables.** The schema is flat — one row per job.
- **COALESCE used in queries** for nullable fields (`title`, `youtube_url`, `error_msg`) to prevent nil scan errors.
- **No data archiving or cleanup** is implemented despite `CLEANUP_AFTER_DAYS` config.
- **Boolean storage:** `auto_upload` is stored as INTEGER (0/1) since SQLite has no native boolean type.
