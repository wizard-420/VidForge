package storage

import (
	"database/sql"
	"log"
	"time"

	"yt-automation-studio/models"

	_ "modernc.org/sqlite"
)

// DB is the global database handle
var DB *sql.DB

// InitDB opens the SQLite database and creates tables if needed
func InitDB(path string) error {
	var err error
	DB, err = sql.Open("sqlite", path)
	if err != nil {
		return err
	}

	// Enable WAL mode for better concurrent read performance
	if _, err := DB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		log.Printf("⚠️  Could not set WAL mode: %v", err)
	}

	// Create jobs table
	createSQL := `
	CREATE TABLE IF NOT EXISTS jobs (
		id            TEXT PRIMARY KEY,
		status        TEXT NOT NULL DEFAULT 'pending',
		format        TEXT,
		input_type    TEXT,
		raw_input     TEXT,
		title         TEXT,
		youtube_url   TEXT,
		duration_sec  INTEGER DEFAULT 0,
		voice_mode    TEXT,
		video_mode    TEXT,
		auto_upload   INTEGER DEFAULT 0,
		created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
		completed_at  DATETIME,
		error_msg     TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
	CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at DESC);
	`
	if _, err := DB.Exec(createSQL); err != nil {
		return err
	}

	// Migration: Add auto_upload column if it doesn't exist (from previous versions)
	_, _ = DB.Exec("ALTER TABLE jobs ADD COLUMN auto_upload INTEGER DEFAULT 0")

	log.Println("✅ Database initialized at", path)
	return nil
}

// InsertJob creates a new job record
func InsertJob(payload *models.InputPayload) error {
	_, err := DB.Exec(`
		INSERT INTO jobs (id, status, format, input_type, raw_input, voice_mode, video_mode, auto_upload, created_at)
		VALUES (?, 'queued', ?, ?, ?, ?, ?, ?, ?)
	`, payload.JobID, payload.Format, payload.InputType, payload.RawInput,
		payload.VoiceoverMode, payload.VideoMode, payload.AutoUpload, payload.CreatedAt)
	return err
}

// UpdateJobStatus updates a job's status
func UpdateJobStatus(jobID string, status models.JobStatus) error {
	_, err := DB.Exec(`UPDATE jobs SET status = ? WHERE id = ?`, string(status), jobID)
	return err
}

// UpdateJobCompleted marks a job as completed with final details
func UpdateJobCompleted(jobID, title, youtubeURL string, durationSec int) error {
	now := time.Now().UTC()
	_, err := DB.Exec(`
		UPDATE jobs SET status = 'completed', title = ?, youtube_url = ?, 
		duration_sec = ?, completed_at = ? WHERE id = ?
	`, title, youtubeURL, durationSec, now, jobID)
	return err
}

// UpdateJobFailed marks a job as failed with an error message
func UpdateJobFailed(jobID, errorMsg string) error {
	now := time.Now().UTC()
	_, err := DB.Exec(`
		UPDATE jobs SET status = 'failed', error_msg = ?, completed_at = ? WHERE id = ?
	`, errorMsg, now, jobID)
	return err
}

// GetJob retrieves a single job by ID
func GetJob(jobID string) (*models.JobDBRecord, error) {
	row := DB.QueryRow(`SELECT id, status, format, input_type, raw_input, 
		COALESCE(title,''), COALESCE(youtube_url,''), duration_sec, 
		voice_mode, video_mode, auto_upload, created_at, completed_at, COALESCE(error_msg,'')
		FROM jobs WHERE id = ?`, jobID)

	j := &models.JobDBRecord{}
	err := row.Scan(&j.ID, &j.Status, &j.Format, &j.InputType, &j.RawInput,
		&j.Title, &j.YouTubeURL, &j.DurationSec,
		&j.VoiceMode, &j.VideoMode, &j.AutoUpload, &j.CreatedAt, &j.CompletedAt, &j.ErrorMsg)
	if err != nil {
		return nil, err
	}
	return j, nil
}

// ListJobs retrieves all jobs ordered by creation time (newest first)
func ListJobs(limit int) ([]models.JobDBRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := DB.Query(`
		SELECT id, status, format, input_type, raw_input,
		COALESCE(title,''), COALESCE(youtube_url,''), duration_sec,
		voice_mode, video_mode, auto_upload, created_at, completed_at, COALESCE(error_msg,'')
		FROM jobs ORDER BY created_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []models.JobDBRecord
	for rows.Next() {
		j := models.JobDBRecord{}
		if err := rows.Scan(&j.ID, &j.Status, &j.Format, &j.InputType, &j.RawInput,
			&j.Title, &j.YouTubeURL, &j.DurationSec,
			&j.VoiceMode, &j.VideoMode, &j.AutoUpload, &j.CreatedAt, &j.CompletedAt, &j.ErrorMsg); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// DeleteJob removes a job record
func DeleteJob(jobID string) error {
	_, err := DB.Exec(`DELETE FROM jobs WHERE id = ?`, jobID)
	return err
}

// Close closes the database connection
func Close() {
	if DB != nil {
		DB.Close()
	}
}
