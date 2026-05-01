package models

import (
	"sync"
	"time"
)

// JobStatus represents the current state of a pipeline job
type JobStatus string

const (
	StatusPending   JobStatus = "pending"
	StatusQueued    JobStatus = "queued"
	StatusRunning   JobStatus = "running"
	StatusCompleted       JobStatus = "completed"
	StatusFailed          JobStatus = "failed"
	StatusPaused          JobStatus = "paused"
	StatusPendingApproval JobStatus = "pending_approval"
)

// JobContext is the shared mutable state passed between all pipeline stages.
// It accumulates files and metadata as each stage completes.
type JobContext struct {
	mu sync.RWMutex

	JobID        string            `json:"job_id"`
	Status       JobStatus         `json:"status"`
	CurrentStage int               `json:"current_stage"`
	Payload      *InputPayload     `json:"payload"`
	Script       *ScriptDocument   `json:"script,omitempty"`
	VoiceFiles   map[string]string `json:"voice_files,omitempty"`   // segment_id -> file path
	ClipFiles    map[string]string `json:"clip_files,omitempty"`    // segment_id -> file path
	MusicFile    string            `json:"music_file,omitempty"`
	CaptionsFile string            `json:"captions_file,omitempty"`
	FinalVideo   string            `json:"final_video,omitempty"`
	YouTubeURL   string            `json:"youtube_url,omitempty"`
	Errors       []string          `json:"errors"`
	StageLogs    map[int]string    `json:"stage_logs"`
	StartedAt    string            `json:"started_at"`
	CompletedAt  string            `json:"completed_at,omitempty"`
	Approved     bool              `json:"approved,omitempty"`
}

// NewJobContext creates a new JobContext from an InputPayload
func NewJobContext(payload *InputPayload) *JobContext {
	return &JobContext{
		JobID:        payload.JobID,
		Status:       StatusQueued,
		CurrentStage: 0,
		Payload:      payload,
		VoiceFiles:   make(map[string]string),
		ClipFiles:    make(map[string]string),
		Errors:       []string{},
		StageLogs:    make(map[int]string),
		StartedAt:    time.Now().UTC().Format(time.RFC3339),
	}
}

// SetStage updates the current pipeline stage (thread-safe)
func (j *JobContext) SetStage(stage int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.CurrentStage = stage
	j.Status = StatusRunning
}

// SetStatus updates the job status (thread-safe)
func (j *JobContext) SetStatus(status JobStatus) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = status
	if status == StatusCompleted || status == StatusFailed {
		j.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}
}

// AddError appends an error message (thread-safe)
func (j *JobContext) AddError(err string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Errors = append(j.Errors, err)
}

// SetStageLog stores a log message for a specific stage (thread-safe)
func (j *JobContext) SetStageLog(stage int, log string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.StageLogs[stage] = log
}

// GetStatus safely reads the current status
func (j *JobContext) GetStatus() JobStatus {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Status
}

// JobDBRecord is the SQLite row model for persisting job history
type JobDBRecord struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	Format      string    `json:"format"`
	InputType   string    `json:"input_type"`
	RawInput    string    `json:"raw_input"`
	Title       string    `json:"title"`
	YouTubeURL  string    `json:"youtube_url"`
	DurationSec int       `json:"duration_sec"`
	VoiceMode   string    `json:"voice_mode"`
	VideoMode   string    `json:"video_mode"`
	AutoUpload  bool      `json:"auto_upload"`
	CreatedAt   time.Time `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	ErrorMsg    string    `json:"error_msg,omitempty"`
}

// ProgressEvent is sent via WebSocket to the frontend
type ProgressEvent struct {
	JobID       string `json:"job_id"`
	Stage       int    `json:"stage"`
	StageName   string `json:"stage_name"`
	ProgressPct int    `json:"progress_pct"`
	Message     string `json:"message"`
	Status      string `json:"status,omitempty"`
	YouTubeURL  string `json:"youtube_url,omitempty"`
	Duration    string `json:"duration,omitempty"`
	Timestamp   string `json:"timestamp"`
}

// StageNames maps stage numbers to human-readable names
var StageNames = map[int]string{
	1: "Input Parsing",
	2: "Script Generation",
	3: "Voiceover",
	4: "Visual Fetch",
	5: "Music",
	6: "Video Render",
	7: "Upload",
}
