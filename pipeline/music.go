package pipeline

import (
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
		if payload.MusicUrl == "" {
			return fmt.Errorf("manual music mode selected but no music URL provided")
		}

		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 5, StageName: "Music",
			ProgressPct: 30, Message: "Downloading selected Jamendo track...",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})

		musicPath := filepath.Join(jobDir, "music.mp3")
		
		// If cropping is needed
		if payload.MusicStart > 0 || payload.MusicEnd > 0 {
			tempPath := filepath.Join(jobDir, "music_temp.mp3")
			if err := downloadFile(payload.MusicUrl, tempPath); err != nil {
				return fmt.Errorf("failed to download manual music: %v", err)
			}
			
			duration := payload.MusicEnd - payload.MusicStart
			if duration <= 0 {
				duration = 60 // fallback if invalid
			}
			
			args := []string{
				"-i", tempPath,
				"-ss", fmt.Sprintf("%d", payload.MusicStart),
				"-t", fmt.Sprintf("%d", duration),
				"-c:a", "libmp3lame", "-b:a", "192k",
				"-y", musicPath,
			}
			cmd := exec.Command("ffmpeg", args...)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to crop music: %v - %s", err, string(out))
			}
			os.Remove(tempPath)
		} else {
			if err := downloadFile(payload.MusicUrl, musicPath); err != nil {
				return fmt.Errorf("failed to download manual music: %v", err)
			}
		}

		job.MusicFile = musicPath

		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 5, StageName: "Music",
			ProgressPct: 100, Message: "Manual music track ready.",
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

	musicPath := filepath.Join(jobDir, "music.mp3")
	durationSec := job.Script.TotalDuration
	if durationSec < 60 {
		durationSec = 60
	}

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
