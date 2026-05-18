package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"yt-automation-studio/config"
	"yt-automation-studio/models"
	"yt-automation-studio/pipeline"
	"yt-automation-studio/storage"
	"yt-automation-studio/worker"

	"github.com/google/uuid"
)

// activeJobs stores running job contexts in memory
var (
	activeJobs = make(map[string]*models.JobContext)
	jobsMu     sync.RWMutex
)

// RegisterRoutes sets up all API endpoints on the given mux
func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/jobs", handleCreateJob)
	mux.HandleFunc("GET /api/jobs", handleListJobs)
	mux.HandleFunc("GET /api/jobs/{id}", handleGetJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", handleDeleteJob)
	mux.HandleFunc("GET /api/jobs/{id}/script", handleGetScript)
	mux.HandleFunc("GET /api/jobs/{id}/download", handleDownload)
	mux.HandleFunc("POST /api/jobs/{id}/retry", handleRetryJob)
	mux.HandleFunc("POST /api/jobs/{id}/approve", handleApproveJob)
	mux.HandleFunc("POST /api/jobs/{id}/trim", handleTrimJob)
	// Per-clip visual review (paused after Stage 4)
	mux.HandleFunc("GET /api/jobs/{id}/clips", handleListClips)
	mux.HandleFunc("GET /api/jobs/{id}/clips/{key}/preview", handleClipPreview)
	mux.HandleFunc("POST /api/jobs/{id}/clips/{key}/regenerate", handleRegenerateClip)
	mux.HandleFunc("POST /api/jobs/{id}/clips/{key}/overlay", handleSetClipOverlay)
	mux.HandleFunc("POST /api/jobs/{id}/clips/{key}/overlay-preview", handleOverlayPreview)
	mux.HandleFunc("GET /api/jobs/{id}/clips/{key}/overlay-preview", handleOverlayPreviewFetch)
	mux.HandleFunc("POST /api/jobs/{id}/clips/approve-all", handleApproveVisuals)
	mux.HandleFunc("GET /api/settings", handleGetSettings)
	mux.HandleFunc("PUT /api/settings", handleUpdateSettings)
	mux.HandleFunc("GET /api/voices", handleGetVoices)
	mux.HandleFunc("GET /api/gcp-tts/voices", handleGCPTTSVoices)
	mux.HandleFunc("POST /api/gcp-tts/synthesize", handleGCPTTSSynthesize)
	mux.HandleFunc("GET /api/music/jamendo/search", handleJamendoSearch)
	mux.HandleFunc("POST /api/music/ai/generate", handleAIMusicGenerate)
	mux.HandleFunc("POST /api/music/recommend-search", handleMusicRecommend)
	mux.HandleFunc("POST /api/tts/recommend-voice", handleTTSRecommend)
	mux.HandleFunc("POST /api/preview-script", handlePreviewScript)
	mux.HandleFunc("POST /api/refine-script", handleRefineScript)
	mux.HandleFunc("GET /api/status", handleHealthCheck)
	mux.HandleFunc("/ws/{id}", handleWebSocket)
}

// POST /api/jobs — Create and queue a new video job
func handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var payload models.InputPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	payload.SetDefaults()
	payload.JobID = uuid.New().String()
	payload.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	if errs := payload.Validate(); len(errs) > 0 {
		writeError(w, http.StatusBadRequest, strings.Join(errs, "; "))
		return
	}

	// Insert into DB
	if err := storage.InsertJob(&payload); err != nil {
		writeError(w, http.StatusInternalServerError, "Database error: "+err.Error())
		return
	}

	// Create job context
	job := models.NewJobContext(&payload)

	jobsMu.Lock()
	activeJobs[job.JobID] = job
	jobsMu.Unlock()

	// Enqueue job for background processing
	progressFn := func(event models.ProgressEvent) {
		BroadcastProgress(job.JobID, event)
	}
	worker.Enqueue(&worker.JobRequest{
		Job:        job,
		OnProgress: progressFn,
	})

	// Estimate ETA
	etaSec := 300 // ~5 min default
	if payload.Format == "short" {
		etaSec = 120
	}

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"job_id":  job.JobID,
		"status":  "queued",
		"message": "Job queued successfully. Pipeline starting.",
		"eta_sec": etaSec,
	})
}

// GET /api/jobs — List all jobs
func handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := storage.ListJobs(50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Database error: "+err.Error())
		return
	}
	if jobs == nil {
		jobs = []models.JobDBRecord{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

// GET /api/jobs/{id} — Get full job status
func handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Try active job first (has full context)
	jobsMu.RLock()
	job, exists := activeJobs[id]
	jobsMu.RUnlock()

	if exists {
		writeJSON(w, http.StatusOK, job)
		return
	}

	// Fall back to DB
	dbJob, err := storage.GetJob(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "Job not found")
		return
	}
	writeJSON(w, http.StatusOK, dbJob)
}

// DELETE /api/jobs/{id} — Cancel and delete a job
func handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	jobsMu.Lock()
	delete(activeJobs, id)
	jobsMu.Unlock()

	if err := storage.DeleteJob(id); err != nil {
		writeError(w, http.StatusInternalServerError, "Delete failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Job deleted"})
}

// GET /api/jobs/{id}/script — Get generated script
func handleGetScript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Try active job
	jobsMu.RLock()
	job, exists := activeJobs[id]
	jobsMu.RUnlock()

	if exists && job.Script != nil {
		writeJSON(w, http.StatusOK, job.Script)
		return
	}

	// Try loading from disk
	scriptPath := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", id), "script.json")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "Script not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// GET /api/jobs/{id}/download — Download final MP4
func handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	videoPath := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", id), "final_output.mp4")

	if _, err := os.Stat(videoPath); os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, "Video not found")
		return
	}

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=video_%s.mp4", id[:8]))
	http.ServeFile(w, r, videoPath)
}

// POST /api/jobs/{id}/retry — Retry a failed job
func handleRetryJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	jobsMu.RLock()
	job, exists := activeJobs[id]
	jobsMu.RUnlock()

	if !exists {
		// Try to load from DB
		dbJob, err := storage.GetJob(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "Job not found")
			return
		}
		// Reconstruct basic context
		job = models.NewJobContext(&models.InputPayload{
			JobID:         dbJob.ID,
			RawInput:      dbJob.RawInput,
			InputType:     dbJob.InputType,
			Format:        dbJob.Format,
			VoiceoverMode: dbJob.VoiceMode,
			VideoMode:     dbJob.VideoMode,
			AutoUpload:    dbJob.AutoUpload,
			CreatedAt:     dbJob.CreatedAt.Format(time.RFC3339),
		})
		jobsMu.Lock()
		activeJobs[id] = job
		jobsMu.Unlock()
	}

	progressFn := func(event models.ProgressEvent) {
		BroadcastProgress(job.JobID, event)
	}
	worker.Enqueue(&worker.JobRequest{
		Job:        job,
		OnProgress: progressFn,
		IsRetry:    true,
		StartStage: job.CurrentStage,
	})

	writeJSON(w, http.StatusAccepted, map[string]string{
		"message": fmt.Sprintf("Retrying job from stage %d", job.CurrentStage),
		"job_id":  job.JobID,
	})
}

// POST /api/jobs/{id}/approve — Approve a pending job for upload
func handleApproveJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	jobsMu.RLock()
	job, ok := activeJobs[id]
	jobsMu.RUnlock()

	if !ok {
		// Try to load from DB and reconstruct context
		dbJob, err := storage.GetJob(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "Job not found")
			return
		}
		if dbJob.Status != string(models.StatusPendingApproval) {
			writeError(w, http.StatusBadRequest, "Job is not pending approval")
			return
		}
		// Reconstruct basic context
		job = models.NewJobContext(&models.InputPayload{
			JobID:         dbJob.ID,
			RawInput:      dbJob.RawInput,
			InputType:     dbJob.InputType,
			Format:        dbJob.Format,
			VoiceoverMode: dbJob.VoiceMode,
			VideoMode:     dbJob.VideoMode,
			AutoUpload:    dbJob.AutoUpload,
			CreatedAt:     dbJob.CreatedAt.Format(time.RFC3339),
		})
		// We might need to load the script too if it exists
		// For now we assume the files are in the workspace
		jobsMu.Lock()
		activeJobs[id] = job
		jobsMu.Unlock()
	}

	if job.Status != models.StatusPendingApproval {
		writeError(w, http.StatusBadRequest, "Job is not pending approval")
		return
	}

	job.Approved = true
	job.SetStatus(models.StatusQueued)
	_ = storage.UpdateJobStatus(job.JobID, models.StatusQueued)

	progressFn := func(event models.ProgressEvent) {
		BroadcastProgress(job.JobID, event)
	}
	worker.Enqueue(&worker.JobRequest{
		Job:        job,
		OnProgress: progressFn,
		IsRetry:    true,
		StartStage: 7, // Stage 7 is Upload
	})

	writeJSON(w, http.StatusAccepted, map[string]string{
		"message": "Job approved and queued for upload",
		"job_id":  job.JobID,
	})
}

// GET /api/jobs/{id}/clips — Return all visual review items for a job.
// Used by the UI's per-clip review screen that opens automatically when a
// job enters StatusPendingVisualReview.
func handleListClips(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	jobsMu.RLock()
	job, ok := activeJobs[id]
	jobsMu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Job not found or no longer active")
		return
	}

	items := job.ListClipReviews()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"job_id":           id,
		"status":           job.Status,
		"visuals_approved": job.VisualsApproved,
		"clips":            items,
	})
}

// GET /api/jobs/{id}/clips/{key}/preview — Stream the raw clip/image bytes.
// Lets the browser play the generated MP4 / display the JPG without exposing
// the workspace folder publicly.
func handleClipPreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	key := r.PathValue("key")

	jobsMu.RLock()
	job, ok := activeJobs[id]
	jobsMu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Job not found or no longer active")
		return
	}

	item := job.GetClipReview(key)
	if item == nil || item.FilePath == "" {
		writeError(w, http.StatusNotFound, "Clip not found")
		return
	}

	// Defense in depth: the file must live inside the workspace dir for
	// this job. Reject any path that tries to escape it.
	expectedPrefix := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", id))
	absPath, err := filepath.Abs(item.FilePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Bad clip path")
		return
	}
	absExpected, _ := filepath.Abs(expectedPrefix)
	if !strings.HasPrefix(absPath, absExpected) {
		writeError(w, http.StatusForbidden, "Clip path outside workspace")
		return
	}

	if _, err := os.Stat(absPath); err != nil {
		writeError(w, http.StatusNotFound, "Clip file missing on disk")
		return
	}

	// Set MIME by extension so the browser can pick the right player.
	ext := strings.ToLower(filepath.Ext(absPath))
	switch ext {
	case ".mp4":
		w.Header().Set("Content-Type", "video/mp4")
	case ".jpg", ".jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	}
	w.Header().Set("Cache-Control", "no-store") // regenerated clips reuse the same path
	http.ServeFile(w, r, absPath)
}

// POST /api/jobs/{id}/clips/{key}/regenerate — Regenerate one clip in place.
// Body (optional): {"query": "...", "source_type": "clip"|"image", "description": "..."}
//   - When body is empty, regenerates with the same query and type but the
//     dedup tracker forces a different Pexels asset / new AI roll.
//   - When `query` is provided it overrides the stored Pexels search term
//     AND the AI-image prompt (the UI uses a single input for both).
//   - When `description` is also provided it takes precedence over the
//     query→description sync (intended for future advanced UI; the current
//     single-input UI doesn't need to send it).
//   - When `source_type` is provided the clip is forcibly regenerated as a
//     stock clip or AI image regardless of how it was originally produced.
func handleRegenerateClip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	key := r.PathValue("key")

	jobsMu.RLock()
	job, ok := activeJobs[id]
	jobsMu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Job not found or no longer active")
		return
	}

	if job.Status != models.StatusPendingVisualReview {
		writeError(w, http.StatusBadRequest, "Job is not in visual-review state")
		return
	}

	var body struct {
		Query       string `json:"query"`
		SourceType  string `json:"source_type"`
		Description string `json:"description"`
	}
	// An empty body is fine — clients can POST with no payload to "give me
	// a different one" without changing anything else.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
			return
		}
	}

	item, err := pipeline.RegenerateOneVisual(job, key, body.Query, body.SourceType, body.Description)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Regenerate failed: "+err.Error())
		return
	}

	// Notify any open WebSocket clients so the UI can refresh its tile
	// without polling. We piggyback on the existing ProgressEvent shape.
	BroadcastProgress(id, models.ProgressEvent{
		JobID:       id,
		Stage:       4,
		StageName:   "Visual Fetch",
		ProgressPct: 100,
		Message:     fmt.Sprintf("Regenerated clip %s (attempt #%d)", key, item.RegenCount),
		Status:      "clip_regenerated",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	})

	writeJSON(w, http.StatusOK, item)
}

// POST /api/jobs/{id}/clips/{key}/overlay — Set or clear the text overlay
// burnt into one clip during the render stage. POST with an empty `text`
// field to remove an existing overlay.
//
// Body: { text, position, font_size, font_color, box_color, fade_in }
func handleSetClipOverlay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	key := r.PathValue("key")

	jobsMu.RLock()
	job, ok := activeJobs[id]
	jobsMu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Job not found or no longer active")
		return
	}

	item := job.GetClipReview(key)
	if item == nil {
		writeError(w, http.StatusNotFound, "Clip not found")
		return
	}

	var body models.TextOverlay
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if strings.TrimSpace(body.Text) == "" {
		item.Overlay = nil
	} else {
		item.Overlay = &body
	}
	job.SetClipReview(key, item)
	writeJSON(w, http.StatusOK, item)
}

// POST /api/jobs/{id}/clips/{key}/overlay-preview — Render a short preview
// of the clip with the supplied overlay burnt in (without persisting the
// overlay to the job). The response is JSON with a URL the UI can swap into
// its preview <video>. Use this when the user wants pixel-accurate
// confirmation before saving (the in-modal HTML preview is a close
// approximation but not exact).
//
// Body: full TextOverlay JSON.
func handleOverlayPreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	key := r.PathValue("key")

	jobsMu.RLock()
	job, ok := activeJobs[id]
	jobsMu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Job not found or no longer active")
		return
	}

	item := job.GetClipReview(key)
	if item == nil || item.FilePath == "" {
		writeError(w, http.StatusNotFound, "Clip not found")
		return
	}

	var body models.TextOverlay
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeError(w, http.StatusBadRequest, "Overlay text is empty")
		return
	}

	// Defense in depth: source must live inside the job's workspace.
	expectedPrefix := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", id))
	absSrc, err := filepath.Abs(item.FilePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Bad clip path")
		return
	}
	absExpected, _ := filepath.Abs(expectedPrefix)
	if !strings.HasPrefix(absSrc, absExpected) {
		writeError(w, http.StatusForbidden, "Clip path outside workspace")
		return
	}
	if _, err := os.Stat(absSrc); err != nil {
		writeError(w, http.StatusNotFound, "Clip file missing on disk")
		return
	}

	width, height := pipeline.ResolveResolution(job.Payload.AspectRatio)

	previewDir := filepath.Join(expectedPrefix, "segments")
	if err := os.MkdirAll(previewDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "Cannot create preview dir: "+err.Error())
		return
	}
	previewName := fmt.Sprintf("%s_overlay_preview.mp4", sanitizeKeyForFilename(key))
	previewPath := filepath.Join(previewDir, previewName)

	if err := pipeline.BuildOverlayPreview(absSrc, &body, width, height, 2.0, previewPath); err != nil {
		writeError(w, http.StatusInternalServerError, "Preview render failed: "+err.Error())
		return
	}

	// Return a fetch URL — the client appends a cache-buster to force the
	// browser to re-load the file when the preview is regenerated.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"url": fmt.Sprintf("/api/jobs/%s/clips/%s/overlay-preview", id, url.PathEscape(key)),
		"ts":  time.Now().UnixMilli(),
	})
}

// GET /api/jobs/{id}/clips/{key}/overlay-preview — Stream the most recent
// preview bytes produced by handleOverlayPreview.
func handleOverlayPreviewFetch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	key := r.PathValue("key")

	jobsMu.RLock()
	_, ok := activeJobs[id]
	jobsMu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Job not found or no longer active")
		return
	}

	jobDir := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", id))
	previewName := fmt.Sprintf("%s_overlay_preview.mp4", sanitizeKeyForFilename(key))
	previewPath := filepath.Join(jobDir, "segments", previewName)

	if _, err := os.Stat(previewPath); err != nil {
		writeError(w, http.StatusNotFound, "Preview not generated yet")
		return
	}

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, previewPath)
}

// sanitizeKeyForFilename turns a clip review key (e.g. "1_0") into a safe
// filename fragment by stripping anything outside [A-Za-z0-9_-].
func sanitizeKeyForFilename(key string) string {
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "preview"
	}
	return b.String()
}

// POST /api/jobs/{id}/clips/approve-all — Resume the pipeline at Stage 5.
// Called when the user clicks "Continue to render" on the review screen.
func handleApproveVisuals(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	jobsMu.RLock()
	job, ok := activeJobs[id]
	jobsMu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Job not found or no longer active")
		return
	}
	if job.Status != models.StatusPendingVisualReview {
		writeError(w, http.StatusBadRequest, "Job is not pending visual review")
		return
	}

	// Mark every clip approved (in case the UI didn't explicitly approve
	// each one — a global "looks good, continue" still implies acceptance).
	for _, item := range job.ListClipReviews() {
		item.Approved = true
		job.SetClipReview(item.Key, item)
	}
	job.SetVisualsApproved(true)
	job.SetStatus(models.StatusQueued)
	_ = storage.UpdateJobStatus(job.JobID, models.StatusQueued)

	progressFn := func(event models.ProgressEvent) {
		BroadcastProgress(job.JobID, event)
	}
	worker.Enqueue(&worker.JobRequest{
		Job:        job,
		OnProgress: progressFn,
		IsRetry:    true,
		StartStage: 5, // resume at Music
	})

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"message": "Visuals approved, resuming pipeline at Music + Render",
		"job_id":  job.JobID,
	})
}

// POST /api/jobs/{id}/trim — Trim the final video to a specified end time
func handleTrimJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		EndTime float64 `json:"end_time"` // seconds
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if req.EndTime <= 0 {
		writeError(w, http.StatusBadRequest, "end_time must be greater than 0")
		return
	}

	jobDir := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", id))
	inputPath := filepath.Join(jobDir, "final_output.mp4")

	if _, err := os.Stat(inputPath); os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, "Video not found for this job")
		return
	}

	// Log input file durations BEFORE trim to diagnose any issues
	inV := pipeline.GetStreamDuration(inputPath, "v")
	inA := pipeline.GetStreamDuration(inputPath, "a")
	log.Printf("📊 Trim input: video=%.3fs, audio=%.3fs (diff=%.3fs)", inV, inA, inA-inV)

	// Filter-based trim: produces exactly endStr seconds for both streams.
	// -t alone causes audio to lose ~1.5s due to AAC encoder flush behavior;
	// filters bypass this by feeding the encoder pre-trimmed frames.
	// apad is added to ensure audio is at least endStr long even if input audio is short.
	trimmedPath := filepath.Join(jobDir, "final_output_trimmed.mp4")
	endStr := fmt.Sprintf("%.3f", req.EndTime)
	filterComplex := fmt.Sprintf(
		"[0:v]trim=duration=%s,setpts=PTS-STARTPTS[v];"+
			"[0:a]apad=whole_dur=%s,atrim=duration=%s,asetpts=PTS-STARTPTS[a]",
		endStr, endStr, endStr,
	)
	cmd := exec.Command("ffmpeg",
		"-i", inputPath,
		"-filter_complex", filterComplex,
		"-map", "[v]", "-map", "[a]",
		"-c:v", "libx264", "-preset", "fast", "-crf", "21",
		"-c:a", "aac", "-b:a", "192k", "-ar", "48000",
		"-movflags", "+faststart",
		"-y", trimmedPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("❌ Trim failed for job %s: %v — output: %s", id, err, string(output))
		writeError(w, http.StatusInternalServerError, "Failed to trim video: "+err.Error())
		return
	}

	// Log output file durations AFTER trim
	outV := pipeline.GetStreamDuration(trimmedPath, "v")
	outA := pipeline.GetStreamDuration(trimmedPath, "a")
	log.Printf("📊 Trim output: video=%.3fs, audio=%.3fs (diff=%.3fs)", outV, outA, outA-outV)

	// Replace original with trimmed version
	if err := os.Rename(trimmedPath, inputPath); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to save trimmed video")
		return
	}

	log.Printf("✂️ Trimmed job %s to %.3fs (requested) — actual: video=%.3fs audio=%.3fs", id, req.EndTime, outV, outA)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":  "Video trimmed successfully",
		"end_time": req.EndTime,
	})
}

// GET /api/settings — Get current config
func handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, config.App.GetMaskedSettings())
}

// PUT /api/settings — Update settings
func handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	// Settings update is limited for security
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "Settings update not yet implemented — edit .env directly",
	})
}

// GET /api/voices — List available voices
func handleGetVoices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, pipeline.GetAvailableVoices())
}

// POST /api/preview-script — Generate script only
func handlePreviewScript(w http.ResponseWriter, r *http.Request) {
	var payload models.InputPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	payload.SetDefaults()
	payload.JobID = uuid.New().String()

	job := models.NewJobContext(&payload)

	// Run only stages 1-2
	noopProgress := func(event models.ProgressEvent) {}

	if err := pipeline.RunInputParser(job, noopProgress); err != nil {
		writeError(w, http.StatusInternalServerError, "Input parsing failed: "+err.Error())
		return
	}

	if err := pipeline.RunScriptGenerator(job, noopProgress); err != nil {
		writeError(w, http.StatusInternalServerError, "Script generation failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, job.Script)
}

// POST /api/refine-script — Refine an existing script
func handleRefineScript(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		CurrentScript *models.ScriptDocument `json:"current_script"`
		UserPrompt    string                 `json:"user_prompt"`
		RawInput      string                 `json:"raw_input"`
		Format        string                 `json:"format"`
		DurationMin   int                    `json:"duration_min"`
		ScriptTone    string                 `json:"script_tone"`
		Language      string                 `json:"language"`
		ClipCount     int                    `json:"clip_count"`
		ImageCount    int                    `json:"image_count"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if payload.CurrentScript == nil {
		writeError(w, http.StatusBadRequest, "current_script is required")
		return
	}
	if payload.UserPrompt == "" {
		writeError(w, http.StatusBadRequest, "user_prompt is required")
		return
	}

	config := map[string]interface{}{
		"raw_input":    payload.RawInput,
		"format":       payload.Format,
		"duration_min": payload.DurationMin,
		"script_tone":  payload.ScriptTone,
		"language":     payload.Language,
		"clip_count":   payload.ClipCount,
		"image_count":  payload.ImageCount,
	}

	updatedScript, err := pipeline.RefineScript(payload.CurrentScript, payload.UserPrompt, config)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Script refinement failed: "+err.Error())
		return
	}

	// Preserve the job ID if it existed
	updatedScript.JobID = payload.CurrentScript.JobID

	writeJSON(w, http.StatusOK, updatedScript)
}

// GET /api/status — Health check
func handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	keys := config.App.HasRequiredKeys()

	// Check FFmpeg
	ffmpegAvail := checkCommand("ffmpeg", "-version")
	whisperAvail := checkCommand("whisper", "--help")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "healthy",
		"version":   "1.0.0",
		"api_keys":  keys,
		"ffmpeg":    ffmpegAvail,
		"whisper":   whisperAvail,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

func checkCommand(name string, args ...string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	log.Printf("⚠️  API Error %d: %s", status, message)
	writeJSON(w, status, map[string]string{"error": message})
}

// handleJamendoSearch proxies search requests to Jamendo API to bypass CORS/Adblock issues
// Matches the Python snippet requirements exactly.
func handleJamendoSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	mood := r.URL.Query().Get("mood")
	speed := r.URL.Query().Get("speed")
	minDur := r.URL.Query().Get("min_dur")
	maxDur := r.URL.Query().Get("max_dur")
	limit := r.URL.Query().Get("limit")

	if minDur == "" {
		minDur = "60"
	}
	if maxDur == "" {
		maxDur = "600"
	}
	if limit == "" {
		limit = "10"
	}
	if mood == "" && query == "" {
		mood = "cinematic"
	}

	clientID := config.App.JamendoClientID
	if clientID == "" {
		clientID = "b6747d04"
	}

	apiURL, _ := url.Parse("https://api.jamendo.com/v3.0/tracks/")
	q := apiURL.Query()
	q.Add("client_id", clientID)
	q.Add("format", "json")
	q.Add("limit", limit)
	q.Add("audioformat", "mp32")
	q.Add("audiodlformat", "mp32")
	q.Add("imagesize", "300")
	q.Add("include", "musicinfo")
	q.Add("durationbetween", fmt.Sprintf("%s_%s", minDur, maxDur))
	q.Add("vocalinstrumental", "instrumental")
	q.Add("boost", "popularity_total")
	q.Add("type", "albumtrack")

	if query != "" {
		q.Add("search", query)
	}
	if mood != "" {
		q.Add("fuzzytags", strings.ReplaceAll(mood, " ", "+"))
	}
	if speed != "" {
		q.Add("speed", speed)
	}
	apiURL.RawQuery = q.Encode()

	req, _ := http.NewRequest("GET", apiURL.String(), nil)
	req.Header.Set("User-Agent", "YoutubeAutomationStudio/1.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Failed to reach Jamendo: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		http.Error(w, fmt.Sprintf("Jamendo returned %d", resp.StatusCode), resp.StatusCode)
		return
	}

	var rawData struct {
		Results []map[string]interface{} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rawData); err != nil {
		http.Error(w, "Failed to parse Jamendo JSON", http.StatusInternalServerError)
		return
	}

	type Track struct {
		ID           string      `json:"id"`
		Name         string      `json:"name"`
		Artist       string      `json:"artist"`
		Album        string      `json:"album"`
		Duration     int         `json:"duration"`
		Cover        string      `json:"cover"`
		StreamURL    string      `json:"stream_url"`
		DownloadURL  string      `json:"download_url"`
		Waveform     []int       `json:"waveform"`
		Genre        []string    `json:"genre"`
		Speed        string      `json:"speed"`
		LicenseURL   string      `json:"license_url"`
		Downloadable bool        `json:"downloadable"`
	}

	var tracks []Track
	for _, raw := range rawData.Results {
		// Only downloadable
		allowed, _ := raw["audiodownload_allowed"].(bool)
		if !allowed {
			continue
		}

		duration := 0
		if dStr, ok := raw["duration"].(string); ok {
			duration, _ = strconv.Atoi(dStr)
		} else if dFloat, ok := raw["duration"].(float64); ok {
			duration = int(dFloat)
		}

		t := Track{
			ID:           fmt.Sprintf("%v", raw["id"]),
			Name:         strings.TrimSpace(fmt.Sprintf("%v", raw["name"])),
			Artist:       fmt.Sprintf("%v", raw["artist_name"]),
			Album:        strings.TrimSpace(fmt.Sprintf("%v", raw["album_name"])),
			Duration:     duration,
			Cover:        fmt.Sprintf("%v", raw["album_image"]),
			StreamURL:    fmt.Sprintf("%v", raw["audio"]),
			DownloadURL:  fmt.Sprintf("%v", raw["audiodownload"]),
			LicenseURL:   fmt.Sprintf("%v", raw["license_ccurl"]),
			Downloadable: allowed,
		}

		if mInfo, ok := raw["musicinfo"].(map[string]interface{}); ok {
			t.Speed, _ = mInfo["speed"].(string)
			if tags, ok := mInfo["tags"].(map[string]interface{}); ok {
				if genres, ok := tags["genres"].([]interface{}); ok {
					for _, g := range genres {
						t.Genre = append(t.Genre, fmt.Sprintf("%v", g))
					}
				}
			}
		}

		if waveStr, ok := raw["waveform"].(string); ok && waveStr != "" {
			var wData struct {
				Peaks []int `json:"peaks"`
			}
			json.Unmarshal([]byte(waveStr), &wData)
			t.Waveform = wData.Peaks
		}

		tracks = append(tracks, t)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"tracks": tracks})
}

// --- Google Cloud TTS Handlers ---

var (
	gcpVoiceCache      []pipeline.GCPVoice
	gcpVoiceCacheLang  string
	gcpVoiceCacheTime  time.Time
	gcpVoiceCacheMu    sync.RWMutex
)

func handleGCPTTSVoices(w http.ResponseWriter, r *http.Request) {
	apiKey := config.App.GoogleCloudTTSAPIKey
	hasSA := pipeline.HasGCPServiceAccount()
	if apiKey == "" && !hasSA {
		http.Error(w, `{"error":"GCP TTS auth not configured — set GOOGLE_CLOUD_TTS_API_KEY (basic voices) and/or GOOGLE_APPLICATION_CREDENTIALS_JSON (premium voices)"}`, http.StatusServiceUnavailable)
		return
	}

	lang := r.URL.Query().Get("language")
	cacheKey := lang
	if hasSA {
		cacheKey += "|sa"
	}

	// Check cache (1 hour TTL, per-language + per-auth-mode)
	gcpVoiceCacheMu.RLock()
	if gcpVoiceCache != nil && gcpVoiceCacheLang == cacheKey && time.Since(gcpVoiceCacheTime) < time.Hour {
		cached := gcpVoiceCache
		gcpVoiceCacheMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"voices":                     cached,
			"service_account_configured": hasSA,
			"premium_voices_available":   hasSA,
		})
		return
	}
	gcpVoiceCacheMu.RUnlock()

	voices, err := pipeline.ListGCPVoices(apiKey, lang)
	if err != nil {
		log.Printf("❌ GCP TTS voices error: %v", err)
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}

	// Update cache (keyed on language + auth mode)
	gcpVoiceCacheMu.Lock()
	gcpVoiceCache = voices
	gcpVoiceCacheLang = cacheKey
	gcpVoiceCacheTime = time.Now()
	gcpVoiceCacheMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"voices":                       voices,
		"service_account_configured":   hasSA,
		"premium_voices_available":     hasSA,
	})
}

func handleGCPTTSSynthesize(w http.ResponseWriter, r *http.Request) {
	apiKey := config.App.GoogleCloudTTSAPIKey
	hasSA := pipeline.HasGCPServiceAccount()
	if apiKey == "" && !hasSA {
		http.Error(w, `{"error":"GCP TTS auth not configured — set GOOGLE_CLOUD_TTS_API_KEY and/or GOOGLE_APPLICATION_CREDENTIALS_JSON"}`, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Text         string `json:"text"`
		VoiceName    string `json:"voice_name"`
		LanguageCode string `json:"language_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.Text == "" || req.VoiceName == "" || req.LanguageCode == "" {
		http.Error(w, `{"error":"text, voice_name, and language_code are required"}`, http.StatusBadRequest)
		return
	}

	audioBytes, err := pipeline.SynthesizeGCPTTSToBytes(req.Text, req.VoiceName, req.LanguageCode, apiKey)
	if err != nil {
		log.Printf("❌ GCP TTS synthesize error: %v", err)
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"audio_base64": base64.StdEncoding.EncodeToString(audioBytes),
		"content_type": "audio/mpeg",
	})
}

// POST /api/music/recommend-search — Suggest Jamendo search queries that
// match the supplied script. Used by the Manual Music step to remove the
// "blank search box" cold-start. Body shape:
//
//	{
//	  "script": <ScriptDocument>,
//	  "tone":   "dramatic"      // user-selected script tone (state.script_tone)
//	}
//
// Returns a MusicRecommendation. Always produces a usable response even
// when Groq is unavailable — the heuristic tier carries everything.
func handleMusicRecommend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Script *models.ScriptDocument `json:"script"`
		Tone   string                 `json:"tone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if body.Script == nil {
		writeError(w, http.StatusBadRequest, "script is required")
		return
	}
	rec := pipeline.RecommendMusicSearch(body.Script, body.Tone)
	writeJSON(w, http.StatusOK, rec)
}

// POST /api/tts/recommend-voice — Suggest the top 3 Google Cloud TTS voices
// for the supplied script + language. Body:
//
//	{
//	  "script":   <ScriptDocument>,
//	  "tone":     "educational",
//	  "language": "en-US"
//	}
//
// We fetch the live voice catalog for `language`, rank it heuristically,
// optionally polish via Groq, and return the top picks plus a short reason.
func handleTTSRecommend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Script   *models.ScriptDocument `json:"script"`
		Tone     string                 `json:"tone"`
		Language string                 `json:"language"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if body.Script == nil {
		writeError(w, http.StatusBadRequest, "script is required")
		return
	}
	if strings.TrimSpace(body.Language) == "" {
		body.Language = "en-US"
	}

	apiKey := config.App.GoogleCloudTTSAPIKey
	hasSA := pipeline.HasGCPServiceAccount()
	if apiKey == "" && !hasSA {
		writeError(w, http.StatusServiceUnavailable,
			"GCP TTS auth not configured — set GOOGLE_CLOUD_TTS_API_KEY (basic voices) and/or GOOGLE_APPLICATION_CREDENTIALS_JSON (premium voices)")
		return
	}

	// Re-use the same cache the /api/gcp-tts/voices endpoint uses so we
	// don't double-hit Google when the user opens the GCP TTS panel right
	// after we cached the catalog.
	cacheKey := body.Language
	if hasSA {
		cacheKey += "|sa"
	}
	gcpVoiceCacheMu.RLock()
	cached := gcpVoiceCache
	cachedLang := gcpVoiceCacheLang
	cachedAt := gcpVoiceCacheTime
	gcpVoiceCacheMu.RUnlock()

	var voices []pipeline.GCPVoice
	if cached != nil && cachedLang == cacheKey && time.Since(cachedAt) < time.Hour {
		voices = cached
	} else {
		fetched, err := pipeline.ListGCPVoices(apiKey, body.Language)
		if err != nil {
			writeError(w, http.StatusBadGateway, "Failed to list voices: "+err.Error())
			return
		}
		voices = fetched
		gcpVoiceCacheMu.Lock()
		gcpVoiceCache = voices
		gcpVoiceCacheLang = cacheKey
		gcpVoiceCacheTime = time.Now()
		gcpVoiceCacheMu.Unlock()
	}

	rec := pipeline.RecommendTTSVoice(body.Script, body.Tone, voices, hasSA)
	writeJSON(w, http.StatusOK, rec)
}

// handleAIMusicGenerate is the preview endpoint behind the "Generate Preview"
// button in the AI Music wizard step. It mirrors what the Stage 5 pipeline
// does (build prompt → run provider chain → optionally layer ambience) but
// returns the resulting MP3 bytes inline as base64 so the browser can play
// the track and let the user iterate ("Regenerate") before committing to a
// full video job. The same audio is then sent back as part of the job
// payload (AIMusicAudioBase64) so the pipeline reuses it instead of
// re-running the slow provider chain.
func handleAIMusicGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Preset      string   `json:"preset"`
		Prompt      string   `json:"prompt"`
		Provider    string   `json:"provider"`
		ScriptTone  string   `json:"script_tone"`
		Ambience    []string `json:"ambience"`
		DurationSec int      `json:"duration_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	// Preview length is capped at the providers' practical free-tier max
	// (Stable Audio Open: 47s; MusicGen Large: 30s). The full pipeline loops
	// the result to fit the actual video duration, so a short preview is
	// representative of what the final video will sound like.
	dur := req.DurationSec
	if dur <= 0 {
		dur = 30
	}
	if dur > 47 {
		dur = 47
	}

	audioBytes, providerUsed, err := pipeline.GenerateAIMusicToBytes(
		req.Prompt, req.Preset, req.ScriptTone, req.Provider, dur, req.Ambience,
	)
	if err != nil {
		log.Printf("❌ AI music preview failed: %v", err)
		writeError(w, http.StatusBadGateway, "AI music generation failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"audio_base64":   base64.StdEncoding.EncodeToString(audioBytes),
		"content_type":   "audio/mpeg",
		"duration_sec":   dur,
		"provider_used":  providerUsed,
		"audio_size":     len(audioBytes),
	})
}
