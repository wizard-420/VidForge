package models

import (
	"sync"
	"time"
)

// JobStatus represents the current state of a pipeline job
type JobStatus string

const (
	StatusPending             JobStatus = "pending"
	StatusQueued              JobStatus = "queued"
	StatusRunning             JobStatus = "running"
	StatusCompleted           JobStatus = "completed"
	StatusFailed              JobStatus = "failed"
	StatusPaused              JobStatus = "paused"
	StatusPendingApproval     JobStatus = "pending_approval"
	StatusPendingVisualReview JobStatus = "pending_visual_review"
)

// JobContext is the shared mutable state passed between all pipeline stages.
// It accumulates files and metadata as each stage completes.
type JobContext struct {
	mu sync.RWMutex

	JobID        string                     `json:"job_id"`
	Status       JobStatus                  `json:"status"`
	CurrentStage int                        `json:"current_stage"`
	Payload      *InputPayload              `json:"payload"`
	Script       *ScriptDocument            `json:"script,omitempty"`
	VoiceFiles   map[string]string          `json:"voice_files,omitempty"`   // segment_id -> file path
	ClipFiles    map[string]string          `json:"clip_files,omitempty"`    // segment_id -> file path
	ClipReview   map[string]*ClipReviewItem `json:"clip_review,omitempty"`   // segID_subID -> review state (overlay, regen count, approval)
	MusicFile    string                     `json:"music_file,omitempty"`
	CaptionsFile string                     `json:"captions_file,omitempty"`
	FinalVideo   string                     `json:"final_video,omitempty"`
	YouTubeURL   string                     `json:"youtube_url,omitempty"`
	Errors       []string                   `json:"errors"`
	StageLogs    map[int]string             `json:"stage_logs"`
	StartedAt    string                     `json:"started_at"`
	CompletedAt  string                     `json:"completed_at,omitempty"`
	Approved     bool                       `json:"approved,omitempty"`
	// VisualsApproved is set to true once the user clicks "Continue to render"
	// on the per-clip review screen. Pipeline checks this when resuming after
	// StatusPendingVisualReview to know it can proceed past Stage 4.
	VisualsApproved bool `json:"visuals_approved,omitempty"`

	// visualTracker is preserved across the pause/resume so per-clip
	// regenerations continue to dedup against the originally-fetched set
	// (no repeats with the rest of the video). Not serialized — rebuilt on
	// resume from job state if needed. Held as `any` to avoid an import
	// cycle: pipeline (which defines the concrete tracker type) imports
	// models, not the other way around.
	visualTracker any
}

// ClipReviewItem captures everything the user can change about a single
// generated visual on the review screen: its current source file, what
// segment/sub-visual it belongs to, the search query that produced it (so
// regeneration can override it), how many times it has been regenerated,
// whether the user has explicitly approved it, and an optional text
// overlay to burn into the clip during render.
type ClipReviewItem struct {
	Key         string       `json:"key"`           // "segID" (legacy) or "segID_subID"
	SegmentID   int          `json:"segment_id"`
	SubIndex    int          `json:"sub_index"`     // -1 for legacy single-visual segments
	FilePath    string       `json:"file_path"`     // absolute path inside workspace
	SourceType  string       `json:"source_type"`   // "clip" | "image"
	Query       string       `json:"query"`         // current search query / AI prompt for this visual
	Description string       `json:"description"`   // narration context shown to user
	NarrationText string     `json:"narration_text"` // segment narration so the user can match clip to script
	RegenCount  int          `json:"regen_count"`
	Approved    bool         `json:"approved"`
	Overlay     *TextOverlay `json:"overlay,omitempty"`
}

// TextOverlay describes an Instagram-stories-style burnt-in caption for one
// clip. All fields are optional; the renderer applies sensible defaults when
// fields are blank/zero. Position is a 9-cell grid:
//
//	top-left    top-center    top-right
//	mid-left    mid-center    mid-right
//	bot-left    bot-center    bot-right
type TextOverlay struct {
	Text      string `json:"text"`
	Position  string `json:"position,omitempty"`   // top-left | top-center | top-right | mid-* | bot-* (default: bot-center)
	FontSize  int    `json:"font_size,omitempty"`  // px in source coordinates (default: 48)
	FontColor string `json:"font_color,omitempty"` // any FFmpeg color (default: white)
	BoxColor  string `json:"box_color,omitempty"`  // background box color, e.g. "black@0.5" (empty = no box)
	FadeIn    bool   `json:"fade_in,omitempty"`    // brief fade-in animation

	// Typography. FontFamily picks a TTF from assets/fonts/; the renderer
	// falls back to a sensible default when blank or unknown. Bold/Italic
	// pick a different file from the same family when available.
	FontFamily string `json:"font_family,omitempty"` // e.g. "Inter", "Roboto", "Montserrat", "PlayfairDisplay", "Bebas"
	Bold       bool   `json:"bold,omitempty"`
	Italic     bool   `json:"italic,omitempty"`

	// Drop shadow. Empty ShadowColor disables the shadow. When Glow is true,
	// the shadow color is rendered as a soft halo via multiple offset passes
	// regardless of ShadowX/ShadowY.
	ShadowColor string `json:"shadow_color,omitempty"` // e.g. "black@0.7" — empty disables
	ShadowX     int    `json:"shadow_x,omitempty"`     // px offset; default 2 when shadow enabled
	ShadowY     int    `json:"shadow_y,omitempty"`     // px offset; default 2 when shadow enabled
	Glow        bool   `json:"glow,omitempty"`         // soft halo (multi-pass) variant
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
		ClipReview:   make(map[string]*ClipReviewItem),
		Errors:       []string{},
		StageLogs:    make(map[int]string),
		StartedAt:    time.Now().UTC().Format(time.RFC3339),
	}
}

// SetVisualTracker stores the per-job dedup tracker in a thread-safe way.
// Pipeline package types it as *pipeline.usedVideoTracker.
func (j *JobContext) SetVisualTracker(t any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.visualTracker = t
}

// GetVisualTracker returns the per-job dedup tracker (may be nil if not yet
// set or after a process restart). Pipeline package re-asserts the type.
func (j *JobContext) GetVisualTracker() any {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.visualTracker
}

// SetClipReview stores or replaces the review item for a given key
// (thread-safe). Used by visual-fetch when populating, and by the API
// handlers when the user updates an overlay or approves a clip.
func (j *JobContext) SetClipReview(key string, item *ClipReviewItem) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ClipReview == nil {
		j.ClipReview = make(map[string]*ClipReviewItem)
	}
	j.ClipReview[key] = item
}

// GetClipReview returns a single review item by key, or nil.
func (j *JobContext) GetClipReview(key string) *ClipReviewItem {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.ClipReview == nil {
		return nil
	}
	return j.ClipReview[key]
}

// ListClipReviews returns a snapshot of all review items in deterministic
// order (segment, then sub-index). The returned slice is safe to mutate
// because it contains pointers to the underlying items — callers should
// not mutate the items concurrently with the pipeline.
func (j *JobContext) ListClipReviews() []*ClipReviewItem {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]*ClipReviewItem, 0, len(j.ClipReview))
	for _, v := range j.ClipReview {
		out = append(out, v)
	}
	// sort by segment id then sub index for stable UI ordering
	for i := 1; i < len(out); i++ {
		for k := i; k > 0; k-- {
			a, b := out[k-1], out[k]
			if a.SegmentID < b.SegmentID || (a.SegmentID == b.SegmentID && a.SubIndex <= b.SubIndex) {
				break
			}
			out[k-1], out[k] = b, a
		}
	}
	return out
}

// SetVisualsApproved marks the per-clip review as accepted (thread-safe).
func (j *JobContext) SetVisualsApproved(approved bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.VisualsApproved = approved
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
