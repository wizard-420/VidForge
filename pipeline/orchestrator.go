package pipeline

import (
	"fmt"
	"log"
	"time"

	"yt-automation-studio/models"
	"yt-automation-studio/storage"
)

// ProgressFunc is a callback to send real-time progress updates
type ProgressFunc func(event models.ProgressEvent)

// Orchestrator runs all 7 pipeline stages in sequence
type Orchestrator struct {
	job      *models.JobContext
	onProgress ProgressFunc
}

// NewOrchestrator creates a new pipeline orchestrator for a job
func NewOrchestrator(job *models.JobContext, onProgress ProgressFunc) *Orchestrator {
	return &Orchestrator{
		job:        job,
		onProgress: onProgress,
	}
}

// Run executes the full pipeline from Stage 1 through Stage 7.
func (o *Orchestrator) Run() {
	o.runFrom(1)
}

// runFrom is the actual stage runner. When startStage > 1, earlier stages
// are skipped — used by RunFrom() to resume after a pause (visual review or
// upload approval) without re-running expensive stages whose outputs are
// already in the JobContext.
func (o *Orchestrator) runFrom(startStage int) {
	o.job.SetStatus(models.StatusRunning)
	_ = storage.UpdateJobStatus(o.job.JobID, models.StatusRunning)

	stages := []struct {
		num  int
		name string
		fn   func(*models.JobContext, ProgressFunc) error
	}{
		{1, "Input Parsing", RunInputParser},
		{2, "Script Generation", RunScriptGenerator},
		{3, "Voiceover", RunVoiceover},
		{4, "Visual Fetch", RunVisualFetcher},
		{5, "Music", RunMusicGenerator},
		{6, "Video Render", RunVideoRenderer},
		{7, "Upload", RunYouTubeUploader},
	}

	for _, stage := range stages {
		// Skip stages that already ran in a previous invocation.
		// Resumed jobs (post-review or post-approval) start at startStage > 1.
		if stage.num < startStage {
			continue
		}

		o.job.SetStage(stage.num)

		// Send "starting" progress event
		o.sendProgress(stage.num, stage.name, 0, fmt.Sprintf("Starting %s...", stage.name), "")

		log.Printf("🔄 Job %s — Stage %d: %s", o.job.JobID[:8], stage.num, stage.name)
		startTime := time.Now()

		// Check approval for Stage 7
		if stage.num == 7 && !o.job.Payload.AutoUpload && !o.job.Approved {
			o.job.SetStatus(models.StatusPendingApproval)
			storage.UpdateJobStatus(o.job.JobID, models.StatusPendingApproval)
			o.sendProgress(stage.num, stage.name, 0, "Video rendered! Pending your approval before upload.", "pending_approval")
			return
		}

		// Execute the stage
		if err := stage.fn(o.job, o.onProgress); err != nil {
			errMsg := fmt.Sprintf("Stage %d (%s) failed: %v", stage.num, stage.name, err)
			log.Printf("❌ Job %s — %s", o.job.JobID[:8], errMsg)
			o.job.AddError(errMsg)
			o.job.SetStatus(models.StatusFailed)
			_ = storage.UpdateJobFailed(o.job.JobID, errMsg)

			o.sendProgress(stage.num, stage.name, 0, errMsg, "failed")
			return
		}

		elapsed := time.Since(startTime)
		o.job.SetStageLog(stage.num, fmt.Sprintf("Completed in %s", elapsed.Round(time.Millisecond)))

		// Send "completed" progress event
		o.sendProgress(stage.num, stage.name, 100, fmt.Sprintf("%s completed in %s", stage.name, elapsed.Round(time.Second)), "")

		log.Printf("✅ Job %s — Stage %d completed in %s", o.job.JobID[:8], stage.num, elapsed.Round(time.Millisecond))

		// Pause after Stage 4 for per-clip visual review unless the user
		// opted out OR they've already approved on a prior pass. The
		// review screen lets them regenerate any clip they don't like and
		// optionally add Instagram-stories-style text overlays before the
		// video is rendered. Manual mode (where the user supplied their
		// own MP4s) is excluded — they already chose those clips.
		if stage.num == 4 &&
			!o.job.Payload.SkipVisualReview &&
			!o.job.VisualsApproved &&
			o.job.Payload.VideoMode != "manual" {
			o.job.SetStatus(models.StatusPendingVisualReview)
			_ = storage.UpdateJobStatus(o.job.JobID, models.StatusPendingVisualReview)
			o.sendProgress(stage.num, stage.name, 100,
				"Visuals fetched! Review and approve clips before rendering.",
				"pending_visual_review")
			log.Printf("⏸ Job %s — paused for visual review (%d clips)",
				o.job.JobID[:8], len(o.job.ClipReview))
			return
		}
	}

	// All stages complete
	o.job.SetStatus(models.StatusCompleted)

	title := ""
	duration := 0
	if o.job.Script != nil {
		if len(o.job.Script.TitleOptions) > 0 {
			title = o.job.Script.TitleOptions[0]
		}
		duration = o.job.Script.TotalDuration
	}
	_ = storage.UpdateJobCompleted(o.job.JobID, title, o.job.YouTubeURL, duration)

	// Send final completion event
	o.onProgress(models.ProgressEvent{
		JobID:      o.job.JobID,
		Stage:      7,
		StageName:  "Upload",
		ProgressPct: 100,
		Message:    "Pipeline completed successfully!",
		Status:     "completed",
		YouTubeURL: o.job.YouTubeURL,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	})

	log.Printf("🎉 Job %s — Pipeline completed successfully!", o.job.JobID[:8])
}

// RunFrom resumes a job from a specific stage. Used by the retry endpoint
// and by the post-review / post-approval resume flows. Stages numbered
// below startStage are skipped, so the JobContext must already contain the
// intermediate outputs (script, voice files, clip files, music, render).
func (o *Orchestrator) RunFrom(startStage int) {
	if startStage < 1 {
		startStage = 1
	}
	log.Printf("🔄 Resuming job %s from stage %d", o.job.JobID[:8], startStage)
	o.runFrom(startStage)
}

func (o *Orchestrator) sendProgress(stage int, name string, pct int, msg, status string) {
	o.onProgress(models.ProgressEvent{
		JobID:       o.job.JobID,
		Stage:       stage,
		StageName:   name,
		ProgressPct: pct,
		Message:     msg,
		Status:      status,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	})
}
