package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"yt-automation-studio/config"
	"yt-automation-studio/models"
)

// ElevenLabs voice ID mapping
var voiceIDs = map[string]string{
	"adam":   "pNInz6obpgDQGcFmaJgB",
	"rachel": "21m00Tcm4TlvDq8ikWAM",
	"domi":   "AZnzlk1XvdvUeBnXmlld",
	"josh":   "TxGEqnHWrfWFTfGW9XjX",
}

// RunVoiceover executes Stage 3: generate voice audio for each script segment
func RunVoiceover(job *models.JobContext, progress ProgressFunc) error {
	if job.Script == nil {
		return fmt.Errorf("no script available — Stage 2 must complete first")
	}

	payload := job.Payload
	segments := job.Script.Segments
	jobDir := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", job.JobID), "segments")

	if err := os.MkdirAll(jobDir, 0755); err != nil {
		return fmt.Errorf("create segments directory: %w", err)
	}

	if payload.VoiceoverMode == "manual" {
		// Manual mode: user provides their own voice files
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 3, StageName: "Voiceover",
			ProgressPct: 100,
			Message:     fmt.Sprintf("Manual mode — place %d MP3 files in: %s", len(segments), jobDir),
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})

		for _, seg := range segments {
			voicePath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_voice.mp3", seg.SegmentID))

			// If file doesn't exist, create a silent dummy audio so FFmpeg doesn't crash in Stage 6
			if _, err := os.Stat(voicePath); os.IsNotExist(err) {
				log.Printf("⚠️ Manual voice file missing for segment %d, generating silent placeholder", seg.SegmentID)
				dur := seg.DurationSec
				if dur <= 0 {
					dur = 10
				}
				cmd := exec.Command("ffmpeg", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", fmt.Sprintf("%d", dur), "-q:a", "9", "-acodec", "libmp3lame", "-y", voicePath)
				_ = cmd.Run()
			}

			job.VoiceFiles[fmt.Sprintf("%d", seg.SegmentID)] = voicePath
		}
		return nil
	}

	// AI Mode: ElevenLabs
	apiKey := config.App.ElevenLabsAPIKey
	if apiKey == "" {
		return fmt.Errorf("ELEVENLABS_API_KEY not configured — set it in .env or switch to manual mode")
	}

	voiceID, ok := voiceIDs[payload.VoiceID]
	if !ok {
		voiceID = voiceIDs["adam"]
	}

	for i, seg := range segments {
		pct := int(float64(i+1) / float64(len(segments)) * 90)
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 3, StageName: "Voiceover",
			ProgressPct: pct,
			Message:     fmt.Sprintf("Generating voice for segment %d of %d...", i+1, len(segments)),
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})

		outputPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_voice.mp3", seg.SegmentID))

		// Retry up to 3 times with backoff
		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			if err := generateElevenLabsVoice(seg.Text, voiceID, apiKey, outputPath); err != nil {
				lastErr = err
				time.Sleep(time.Duration(attempt*10) * time.Second)
				continue
			}
			lastErr = nil
			break
		}
		if lastErr != nil {
			return fmt.Errorf("voiceover for segment %d after 3 retries: %w", seg.SegmentID, lastErr)
		}

		job.VoiceFiles[fmt.Sprintf("%d", seg.SegmentID)] = outputPath
	}

	return nil
}

// generateElevenLabsVoice calls the ElevenLabs TTS API for a single text segment
func generateElevenLabsVoice(text, voiceID, apiKey, outputPath string) error {
	url := fmt.Sprintf("https://api.elevenlabs.io/v1/text-to-speech/%s", voiceID)

	body := map[string]interface{}{
		"text":     text,
		"model_id": "eleven_multilingual_v2",
		"voice_settings": map[string]interface{}{
			"stability":         0.5,
			"similarity_boost":  0.75,
			"style":             0.4,
			"use_speaker_boost": true,
		},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("xi-api-key", apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ElevenLabs API returned %d: %s", resp.StatusCode, string(respBody))
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, resp.Body); err != nil {
		return fmt.Errorf("write audio: %w", err)
	}

	return nil
}

// GetAvailableVoices returns the list of supported voices
func GetAvailableVoices() []map[string]string {
	return []map[string]string{
		{"id": "adam", "name": "Adam", "description": "Deep, authoritative male voice", "accent": "American"},
		{"id": "rachel", "name": "Rachel", "description": "Warm, professional female voice", "accent": "American"},
		{"id": "domi", "name": "Domi", "description": "Strong, confident female voice", "accent": "American"},
		{"id": "josh", "name": "Josh", "description": "Young, energetic male voice", "accent": "American"},
	}
}
