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
	mux.HandleFunc("GET /api/settings", handleGetSettings)
	mux.HandleFunc("PUT /api/settings", handleUpdateSettings)
	mux.HandleFunc("GET /api/voices", handleGetVoices)
	mux.HandleFunc("GET /api/gcp-tts/voices", handleGCPTTSVoices)
	mux.HandleFunc("POST /api/gcp-tts/synthesize", handleGCPTTSSynthesize)
	mux.HandleFunc("GET /api/music/jamendo/search", handleJamendoSearch)
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
	if apiKey == "" {
		http.Error(w, `{"error":"GOOGLE_CLOUD_TTS_API_KEY not configured"}`, http.StatusServiceUnavailable)
		return
	}

	lang := r.URL.Query().Get("language")

	// Check cache (1 hour TTL, per-language)
	gcpVoiceCacheMu.RLock()
	if gcpVoiceCache != nil && gcpVoiceCacheLang == lang && time.Since(gcpVoiceCacheTime) < time.Hour {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"voices": gcpVoiceCache})
		gcpVoiceCacheMu.RUnlock()
		return
	}
	gcpVoiceCacheMu.RUnlock()

	voices, err := pipeline.ListGCPVoices(apiKey, lang)
	if err != nil {
		log.Printf("❌ GCP TTS voices error: %v", err)
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}

	// Update cache
	gcpVoiceCacheMu.Lock()
	gcpVoiceCache = voices
	gcpVoiceCacheLang = lang
	gcpVoiceCacheTime = time.Now()
	gcpVoiceCacheMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"voices": voices})
}

func handleGCPTTSSynthesize(w http.ResponseWriter, r *http.Request) {
	apiKey := config.App.GoogleCloudTTSAPIKey
	if apiKey == "" {
		http.Error(w, `{"error":"GOOGLE_CLOUD_TTS_API_KEY not configured"}`, http.StatusServiceUnavailable)
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
