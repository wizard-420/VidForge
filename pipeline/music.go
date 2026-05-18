package pipeline

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
	"strings"
	"time"

	"yt-automation-studio/config"
	"yt-automation-studio/models"
)

// RunMusicGenerator executes Stage 5: fetch background music.
// Uses the user's selected script_tone to ensure the music matches the theme.
func RunMusicGenerator(job *models.JobContext, progress ProgressFunc) error {
	if job.Script == nil {
		return fmt.Errorf("no script available")
	}

	payload := job.Payload
	jobDir := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", job.JobID))
	os.MkdirAll(jobDir, 0755)

	if payload.MusicMode == "skip" {
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 5, StageName: "Music",
			ProgressPct: 100, Message: "Music skipped.",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		return nil
	}

	if payload.MusicMode == "manual" {
		if payload.MusicUrl == "" && payload.MusicFileBase64 == "" {
			return fmt.Errorf("manual music mode selected but no URL or uploaded file provided")
		}

		musicPath := filepath.Join(jobDir, "music.mp3")
		tempPath := filepath.Join(jobDir, "music_temp")
		needsCrop := payload.MusicStart > 0 || payload.MusicEnd > 0

		// 1) Materialise the source audio onto disk (either from base64 upload or remote URL).
		switch {
		case payload.MusicFileBase64 != "":
			progress(models.ProgressEvent{
				JobID: job.JobID, Stage: 5, StageName: "Music",
				ProgressPct: 30, Message: "Decoding uploaded audio file...",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})

			if err := writeBase64Audio(payload.MusicFileBase64, tempPath); err != nil {
				return fmt.Errorf("failed to decode uploaded music: %v", err)
			}
		default:
			progress(models.ProgressEvent{
				JobID: job.JobID, Stage: 5, StageName: "Music",
				ProgressPct: 30, Message: "Downloading selected music track...",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})

			if err := downloadFile(payload.MusicUrl, tempPath); err != nil {
				return fmt.Errorf("failed to download manual music: %v", err)
			}
		}

		// 2) Normalise to MP3 (re-encoding via ffmpeg also handles wav/m4a/ogg
		//    uploads transparently) and apply crop in the same pass when requested.
		args := []string{"-i", tempPath}
		if needsCrop {
			duration := payload.MusicEnd - payload.MusicStart
			if duration <= 0 {
				duration = 60 // fallback if invalid
			}
			args = append(args,
				"-ss", fmt.Sprintf("%d", payload.MusicStart),
				"-t", fmt.Sprintf("%d", duration),
			)
		}
		args = append(args, "-vn", "-c:a", "libmp3lame", "-b:a", "192k", "-y", musicPath)

		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			os.Remove(tempPath)
			return fmt.Errorf("failed to process manual music: %v - %s", err, string(out))
		}
		os.Remove(tempPath)

		job.MusicFile = musicPath

		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 5, StageName: "Music",
			ProgressPct: 100, Message: "Manual music track ready.",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		return nil
	}

	musicPath := filepath.Join(jobDir, "music.mp3")
	durationSec := job.Script.TotalDuration
	if durationSec < 60 {
		durationSec = 60
	}

	if payload.MusicMode == "ai_generated" {
		// Fast path: the user pre-generated the track via the wizard's
		// "Generate Preview" button (POST /api/music/ai/generate). We already
		// have the bytes — decode, optionally crop, done. No second slow
		// HuggingFace round-trip, and no second ambience-layering pass since
		// the preview already includes the layers.
		if payload.AIMusicAudioBase64 != "" {
			progress(models.ProgressEvent{
				JobID: job.JobID, Stage: 5, StageName: "Music",
				ProgressPct: 50, Message: "Using pre-generated AI music track...",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})

			tempPath := filepath.Join(jobDir, "music_ai_preview")
			if err := writeBase64Audio(payload.AIMusicAudioBase64, tempPath); err != nil {
				return fmt.Errorf("decode pre-generated AI music: %v", err)
			}

			args := []string{"-i", tempPath}
			if payload.AIMusicStart > 0 || payload.AIMusicEnd > 0 {
				cropDur := payload.AIMusicEnd - payload.AIMusicStart
				if cropDur <= 0 {
					cropDur = 30
				}
				args = append(args,
					"-ss", fmt.Sprintf("%d", payload.AIMusicStart),
					"-t", fmt.Sprintf("%d", cropDur),
				)
			}
			args = append(args, "-vn", "-c:a", "libmp3lame", "-b:a", "192k", "-y", musicPath)

			if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
				os.Remove(tempPath)
				return fmt.Errorf("process pre-generated AI music: %v - %s", err, string(out))
			}
			os.Remove(tempPath)

			job.MusicFile = musicPath
			progress(models.ProgressEvent{
				JobID: job.JobID, Stage: 5, StageName: "Music",
				ProgressPct: 100, Message: "AI-generated music ready (from preview).",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
			return nil
		}

		// Slow path: no preview was generated, run the provider chain inline.
		prompt := buildAIMusicPrompt(payload.MusicPrompt, payload.MusicPreset, payload.ScriptTone, payload.MusicAmbience)

		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 5, StageName: "Music",
			ProgressPct: 20, Message: "Generating AI music — this may take 30-90 seconds...",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})

		err := generateAIMusic(prompt, payload.MusicProvider, durationSec, musicPath, jobDir)
		if err != nil {
			// Provider chain already includes Jamendo as the last leg, so any
			// error here means even the curated fallback failed. Try the
			// auto-mode Jamendo flow one more time as a safety net.
			log.Printf("⚠️ AI music chain failed (%v) — falling back to tone-based Jamendo", err)
			job.AddError(fmt.Sprintf("AI music providers failed: %v", err))
			tone := payload.ScriptTone
			if tone == "" {
				tone = "dramatic"
			}
			if err2 := fetchJamendoMusicAuto(tone, musicPath, durationSec); err2 != nil {
				job.AddError(fmt.Sprintf("Jamendo fallback also failed: %v", err2))
				return nil // Non-fatal — pipeline continues without music.
			}
		}

		// Optional ambience layering (birds, rain, wind, waves, ...). Mixed
		// after the base track is on disk so renderer.go still sees a single
		// music.mp3 with consistent volume levels for voice ducking.
		if len(payload.MusicAmbience) > 0 {
			progress(models.ProgressEvent{
				JobID: job.JobID, Stage: 5, StageName: "Music",
				ProgressPct: 70, Message: fmt.Sprintf("Layering ambience: %s...", strings.Join(payload.MusicAmbience, ", ")),
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
			if err := layerAmbienceTracks(musicPath, payload.MusicAmbience, jobDir); err != nil {
				log.Printf("⚠️ ambience layering failed (%v) — keeping base track", err)
				job.AddError(fmt.Sprintf("Ambience layering skipped: %v", err))
			}
		}

		job.MusicFile = musicPath
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 5, StageName: "Music",
			ProgressPct: 100, Message: "AI-generated music ready.",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		return nil
	}

	mood := payload.ScriptTone
	if mood == "" {
		mood = "dramatic"
	}

	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 5, StageName: "Music",
		ProgressPct: 30, Message: fmt.Sprintf("Fetching %s background music...", mood),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	if err := fetchJamendoMusicAuto(mood, musicPath, durationSec); err != nil {
		log.Printf("⚠️ Jamendo music fetch failed (%v)", err)
		job.AddError(fmt.Sprintf("Music fetch failed completely: %v", err))
		return nil // Non-fatal
	}

	job.MusicFile = musicPath

	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 5, StageName: "Music",
		ProgressPct: 100, Message: fmt.Sprintf("Background music ready (%s theme).", mood),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	return nil
}

func fetchJamendoMusicAuto(mood, outputPath string, durationSec int) error {
	clientID := config.App.JamendoClientID
	if clientID == "" {
		clientID = "b6747d04"
	}

	toneToTags := map[string]string{
		"dramatic":       "cinematic dramatic epic",
		"suspenseful":    "suspense dark mysterious thriller",
		"educational":    "calm ambient relaxing background",
		"conversational": "acoustic light happy positive",
		"motivational":   "uplifting motivational energetic",
		"humorous":       "fun playful comedy upbeat",
	}

	toneToSpeed := map[string]string{
		"dramatic":       "medium high",
		"suspenseful":    "low medium",
		"educational":    "low medium",
		"conversational": "medium",
		"motivational":   "high veryhigh",
		"humorous":       "medium high",
	}

	tags := toneToTags[mood]
	if tags == "" {
		tags = "cinematic"
	}
	speed := toneToSpeed[mood]
	if speed == "" {
		speed = "medium"
	}

	apiURL, _ := url.Parse("https://api.jamendo.com/v3.0/tracks/")
	q := apiURL.Query()
	q.Add("client_id", clientID)
	q.Add("format", "json")
	q.Add("limit", "5")
	q.Add("audioformat", "mp32")
	q.Add("audiodlformat", "mp32")
	q.Add("durationbetween", fmt.Sprintf("%d_%d", durationSec, durationSec+120))
	q.Add("vocalinstrumental", "instrumental")
	q.Add("boost", "popularity_total")
	q.Add("type", "albumtrack")
	q.Add("fuzzytags", strings.ReplaceAll(tags, " ", "+"))
	q.Add("speed", speed)
	
	apiURL.RawQuery = q.Encode()

	req, _ := http.NewRequest("GET", apiURL.String(), nil)
	req.Header.Set("User-Agent", "YoutubeAutomationStudio/1.0")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("Jamendo HTTP %d", resp.StatusCode)
	}

	var r struct {
		Results []map[string]interface{} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}

	var downloadURL string
	for _, track := range r.Results {
		if allowed, _ := track["audiodownload_allowed"].(bool); allowed {
			if dl, ok := track["audiodownload"].(string); ok && dl != "" {
				downloadURL = dl
				break
			}
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("no downloadable Jamendo tracks found for tone: %s", mood)
	}

	return downloadFile(downloadURL, outputPath)
}

// writeBase64Audio decodes a data-URL or raw base64 audio payload from the
// frontend file upload and writes the binary contents to outputPath. The
// resulting file is in its original format (mp3/wav/m4a/ogg/...) and is later
// normalised to MP3 by ffmpeg in the manual-mode flow.
func writeBase64Audio(payload, outputPath string) error {
	data := payload
	// Strip "data:audio/...;base64," prefix when present.
	if idx := strings.Index(data, ","); idx != -1 && strings.HasPrefix(data, "data:") {
		data = data[idx+1:]
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fmt.Errorf("invalid base64 audio: %v", err)
	}
	if len(decoded) == 0 {
		return fmt.Errorf("decoded audio is empty")
	}
	return os.WriteFile(outputPath, decoded, 0644)
}
