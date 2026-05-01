package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	mux.HandleFunc("GET /api/settings", handleGetSettings)
	mux.HandleFunc("PUT /api/settings", handleUpdateSettings)
	mux.HandleFunc("GET /api/voices", handleGetVoices)
	mux.HandleFunc("POST /api/preview-script", handlePreviewScript)
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
