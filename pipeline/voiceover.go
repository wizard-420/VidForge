package pipeline

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"yt-automation-studio/config"
	"yt-automation-studio/models"
)

// reconcileScriptDurations updates job.Script.TotalDuration based on the
// ACTUAL voice durations (probed from the generated audio files) plus the
// per-segment tail pad. The LLM's duration estimates are inaccurate because
// TTS engines speak at a rate different from the assumed 130 wpm, which
// caused unnatural multi-second silences between segments. After this call,
// downstream stages (music fetch, video render) see a duration that matches
// what the viewer will actually experience.
func reconcileScriptDurations(job *models.JobContext) {
	if job.Script == nil {
		return
	}
	totalSec := 0.0
	for _, seg := range job.Script.Segments {
		voicePath := job.VoiceFiles[fmt.Sprintf("%d", seg.SegmentID)]
		if voicePath == "" {
			totalSec += float64(seg.DurationSec)
			continue
		}
		dur := getMediaDuration(voicePath)
		if dur <= 0 {
			dur = float64(seg.DurationSec)
		}
		totalSec += dur + segmentTailPad(seg.Type)
	}
	if totalSec <= 0 {
		return
	}
	newTotal := int(math.Ceil(totalSec))
	if job.Script.TotalDuration != newTotal {
		log.Printf("📐 Job %s — reconciled total duration: %ds (was estimate %ds)",
			job.JobID[:8], newTotal, job.Script.TotalDuration)
	}
	job.Script.TotalDuration = newTotal

	// Persist the updated script.json so the UI and downstream tooling see
	// the corrected duration rather than the LLM's estimate.
	jobDir := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", job.JobID))
	if scriptJSON, err := json.MarshalIndent(job.Script, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(jobDir, "script.json"), scriptJSON, 0644)
	}
}

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

	// "Music Only" mode: no narration. We synthesize silent MP3s sized to the
	// LLM-planned segment durations so the rest of the pipeline (segment sync,
	// concat, music mix) keeps working unchanged. Stage 6 then bumps the music
	// volume since there's no voice to duck under and skips Whisper captions.
	if payload.VoiceoverMode == "none" {
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 3, StageName: "Voiceover",
			ProgressPct: 50,
			Message:     "No-voice mode — generating silent placeholders for music-only video...",
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})

		for _, seg := range segments {
			voicePath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_voice.mp3", seg.SegmentID))

			dur := seg.DurationSec
			if dur < 2 {
				dur = 2 // safety floor for very short hooks
			}
			if dur > 600 {
				dur = 600 // sanity ceiling
			}

			// anullsrc generates a silent stereo track at 44.1kHz; we re-encode
			// to MP3 (libmp3lame) so the file is interchangeable with what the
			// other voiceover modes produce.
			cmd := exec.Command("ffmpeg",
				"-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo",
				"-t", fmt.Sprintf("%d", dur),
				"-q:a", "9", "-acodec", "libmp3lame",
				"-y", voicePath,
			)
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("generate silent placeholder for seg %d: %w", seg.SegmentID, err)
			}

			job.VoiceFiles[fmt.Sprintf("%d", seg.SegmentID)] = voicePath
		}

		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 3, StageName: "Voiceover",
			ProgressPct: 100,
			Message:     fmt.Sprintf("Generated %d silent placeholders (no narration).", len(segments)),
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})

		reconcileScriptDurations(job)
		return nil
	}

	if payload.VoiceoverMode == "manual" {
		// Manual mode: user provides their own voice files via Base64 or manual copy
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 3, StageName: "Voiceover",
			ProgressPct: 100,
			Message:     "Processing manual voiceover audio...",
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})

		for _, seg := range segments {
			voicePath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_voice.mp3", seg.SegmentID))

			// Check if we received base64 audio for this segment
			if payload.ManualAudioBase64 != nil {
				if b64Data, ok := payload.ManualAudioBase64[seg.SegmentID]; ok && b64Data != "" {
					// Format is typically: data:audio/mp3;base64,... or data:audio/webm;base64,...
					// We need to write this to a temp file, then convert it to the standardized mp3 format
					parts := strings.SplitN(b64Data, ",", 2)
					if len(parts) == 2 {
						b64Data = parts[1]
					}
					
					decodedAudio, err := base64.StdEncoding.DecodeString(b64Data)
					if err == nil {
						tempIn := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_raw.webm", seg.SegmentID))
						os.WriteFile(tempIn, decodedAudio, 0644)
						
						// Convert to standard MP3
						exec.Command("ffmpeg", "-i", tempIn, "-q:a", "2", "-acodec", "libmp3lame", "-y", voicePath).Run()
						os.Remove(tempIn)
					}
				}
			}

			// If file STILL doesn't exist, create a silent dummy audio so FFmpeg doesn't crash in Stage 6
			if _, err := os.Stat(voicePath); os.IsNotExist(err) {
				log.Printf("⚠️ Manual voice file missing for segment %d, generating silent placeholder", seg.SegmentID)
				dur := seg.DurationSec
				if dur <= 0 {
					dur = 10
				}
				cmd := exec.Command("ffmpeg", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", fmt.Sprintf("%d", dur), "-q:a", "9", "-acodec", "libmp3lame", "-y", voicePath)
				cmd.Run()
			}

			job.VoiceFiles[fmt.Sprintf("%d", seg.SegmentID)] = voicePath
		}
		reconcileScriptDurations(job)
		return nil
	}

	// Google Cloud TTS mode
	if payload.VoiceoverMode == "gcp_tts" {
		gcpKey := config.App.GoogleCloudTTSAPIKey
		hasSA := HasGCPServiceAccount()
		if gcpKey == "" && !hasSA {
			return fmt.Errorf("GCP TTS not configured — set GOOGLE_CLOUD_TTS_API_KEY (basic voices) and/or GOOGLE_APPLICATION_CREDENTIALS_JSON (premium voices like Chirp 3 HD), or switch to another voiceover mode")
		}

		// SSML emotion path is opt-out via VIDFORGE_DISABLE_SSML=1 so users
		// can fall back to flat text synthesis if the LLM tagging ever
		// misbehaves on a specific script. Defaults to ON.
		ssmlEnabled := strings.TrimSpace(os.Getenv("VIDFORGE_DISABLE_SSML")) == ""

		// Tag the whole script's emotions in one pass before we start
		// synthesising. Doing it up front means: (1) cost preview is
		// available before any audio is generated, (2) a single Groq call
		// can be batched per segment, and (3) the tags are persisted to
		// script.json so the UI can show them.
		if ssmlEnabled {
			progress(models.ProgressEvent{
				JobID: job.JobID, Stage: 3, StageName: "Voiceover",
				ProgressPct: 5,
				Message:     "Tagging per-sentence emotion for expressive delivery...",
				Timestamp:   time.Now().UTC().Format(time.RFC3339),
			})
			TagScriptEmotions(job.Script, payload.ScriptTone)
			// Persist the enriched script so the UI / debugging tools can
			// see the tags — same approach reconcileScriptDurations uses.
			jobRoot := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", job.JobID))
			if scriptJSON, mErr := json.MarshalIndent(job.Script, "", "  "); mErr == nil {
				_ = os.WriteFile(filepath.Join(jobRoot, "script.json"), scriptJSON, 0644)
			}
			// Re-read segments — TagScriptEmotions mutates in place but
			// `segments` here is a snapshot of the slice header.
			segments = job.Script.Segments
		}

		var totalBilledChars int

		for i, seg := range segments {
			pct := int(float64(i+1) / float64(len(segments)) * 90)
			progress(models.ProgressEvent{
				JobID: job.JobID, Stage: 3, StageName: "Voiceover",
				ProgressPct: pct,
				Message:     fmt.Sprintf("Google TTS: generating voice for segment %d of %d...", i+1, len(segments)),
				Timestamp:   time.Now().UTC().Format(time.RFC3339),
			})

			outputPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_voice.mp3", seg.SegmentID))

			// Pick the synthesis path: SSML when sentences are tagged AND
			// SSML hasn't been disabled, plain text otherwise.
			useSSML := ssmlEnabled && len(seg.Sentences) > 0
			var ssmlPayload string
			if useSSML {
				ssmlPayload = RenderSegmentSSML(seg)
				totalBilledChars += EstimateSSMLBilledChars(ssmlPayload)
			} else {
				totalBilledChars += len(seg.Text)
			}

			var lastErr error
			for attempt := 1; attempt <= 3; attempt++ {
				var err error
				if useSSML {
					err = SynthesizeGCPTTSSSML(ssmlPayload, payload.GCPVoiceName, payload.GCPLanguageCode, gcpKey, outputPath)
				} else {
					err = SynthesizeGCPTTS(seg.Text, payload.GCPVoiceName, payload.GCPLanguageCode, gcpKey, outputPath)
				}
				if err != nil {
					lastErr = err
					// SSML can occasionally fail with malformed-payload errors
					// (rare LLM output edge case). On the second retry, drop
					// down to plain text so the user still gets audio.
					if useSSML && attempt == 2 {
						log.Printf("⚠️ Job %s seg %d: SSML synth failed twice (%v) — falling back to plain text",
							job.JobID[:8], seg.SegmentID, err)
						useSSML = false
					}
					time.Sleep(time.Duration(attempt*10) * time.Second)
					continue
				}
				lastErr = nil
				break
			}
			if lastErr != nil {
				return fmt.Errorf("GCP TTS for segment %d after 3 retries: %w", seg.SegmentID, lastErr)
			}

			job.VoiceFiles[fmt.Sprintf("%d", seg.SegmentID)] = outputPath
		}

		if ssmlEnabled {
			log.Printf("📊 Job %s — GCP TTS billed ~%d chars (SSML included) for voice %q",
				job.JobID[:8], totalBilledChars, payload.GCPVoiceName)
		}

		reconcileScriptDurations(job)
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

	reconcileScriptDurations(job)
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
