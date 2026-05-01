package pipeline

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"yt-automation-studio/config"
	"yt-automation-studio/models"
)

// RunMusicGenerator executes Stage 5: generate mood-matched background music.
// Uses FFmpeg audio synthesis to create ambient tracks that match the user's
// selected tone — no external API required, guaranteed to work every time.
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

	mood := payload.ScriptTone
	if mood == "" {
		mood = "dramatic"
	}

	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 5, StageName: "Music",
		ProgressPct: 20, Message: fmt.Sprintf("Generating %s background music...", mood),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	musicPath := filepath.Join(jobDir, "music.mp3")

	// Calculate duration: script total + 30s buffer (renderer loops with -stream_loop anyway)
	durationSec := 180 // default 3 min
	if job.Script.TotalDuration > 0 {
		durationSec = job.Script.TotalDuration + 30
	}

	if err := generateAmbientMusic(mood, musicPath, durationSec); err != nil {
		log.Printf("⚠️ Music generation failed: %v", err)
		job.AddError(fmt.Sprintf("Music generation failed: %v", err))
		return nil // Non-fatal
	}

	job.MusicFile = musicPath

	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 5, StageName: "Music",
		ProgressPct: 100, Message: fmt.Sprintf("Background music ready (%s, %ds).", mood, durationSec),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	return nil
}

// generateAmbientMusic creates mood-specific ambient background music using
// FFmpeg's audio synthesis filters. Each mood has a carefully tuned combination
// of noise colors, sine frequencies, tremolo rates, and filtering to create
// a distinct atmosphere. This runs 100% locally with zero API dependencies.
func generateAmbientMusic(mood string, outputPath string, durationSec int) error {
	dur := fmt.Sprintf("%d", durationSec)

	// Each mood uses a different combination of:
	// - anoisesrc: colored noise (brown=deep rumble, pink=warm, white=bright)
	// - sine: tonal drones at specific musical frequencies
	// - tremolo: slow modulation for movement and atmosphere
	// - lowpass/highpass: shape the frequency spectrum
	// - amix: blend all layers together

	var filterComplex string

	switch mood {
	case "suspenseful":
		// Dark, tense atmosphere: deep brown noise rumble + low dissonant drones
		// Frequencies: 55Hz (A1) + 82.4Hz (E2) = dark power chord with tension
		filterComplex = fmt.Sprintf(
			"anoisesrc=color=brown:duration=%s:seed=42[n];"+
				"sine=frequency=55:duration=%s[s1];"+
				"sine=frequency=82.4:duration=%s[s2];"+
				"sine=frequency=116.5:duration=%s[s3];"+
				"[s1]volume=0.15,tremolo=f=0.3:d=0.7[ts1];"+
				"[s2]volume=0.10,tremolo=f=0.5:d=0.5[ts2];"+
				"[s3]volume=0.06,tremolo=f=0.8:d=0.4[ts3];"+
				"[n]volume=0.25,lowpass=f=180[fn];"+
				"[fn][ts1][ts2][ts3]amix=inputs=4:duration=first,"+
				"highpass=f=30,lowpass=f=800,volume=1.5[out]",
			dur, dur, dur, dur)

	case "dramatic":
		// Cinematic intensity: warm pink noise + power drones with presence
		// Frequencies: 110Hz (A2) + 146.8Hz (D3) + 165Hz (E3)
		filterComplex = fmt.Sprintf(
			"anoisesrc=color=pink:duration=%s:seed=77[n];"+
				"sine=frequency=110:duration=%s[s1];"+
				"sine=frequency=146.8:duration=%s[s2];"+
				"sine=frequency=165:duration=%s[s3];"+
				"[s1]volume=0.18,tremolo=f=0.15:d=0.5[ts1];"+
				"[s2]volume=0.12,tremolo=f=0.2:d=0.4[ts2];"+
				"[s3]volume=0.08,tremolo=f=0.25:d=0.3[ts3];"+
				"[n]volume=0.18,lowpass=f=300[fn];"+
				"[fn][ts1][ts2][ts3]amix=inputs=4:duration=first,"+
				"highpass=f=40,lowpass=f=1200,volume=1.4[out]",
			dur, dur, dur, dur)

	case "educational":
		// Calm, focused atmosphere: gentle ambient pad, non-distracting
		// Frequencies: 220Hz (A3) + 329.6Hz (E4) = open, airy fifth
		filterComplex = fmt.Sprintf(
			"anoisesrc=color=pink:duration=%s:seed=55[n];"+
				"sine=frequency=220:duration=%s[s1];"+
				"sine=frequency=329.6:duration=%s[s2];"+
				"[s1]volume=0.08,tremolo=f=0.1:d=0.3[ts1];"+
				"[s2]volume=0.05,tremolo=f=0.08:d=0.2[ts2];"+
				"[n]volume=0.10,lowpass=f=500,highpass=f=100[fn];"+
				"[fn][ts1][ts2]amix=inputs=3:duration=first,"+
				"highpass=f=80,lowpass=f=2000,volume=1.2[out]",
			dur, dur, dur)

	case "conversational":
		// Warm, gentle background: soft and inviting
		// Frequencies: 196Hz (G3) + 261.6Hz (C4) + 329.6Hz (E4) = major chord
		filterComplex = fmt.Sprintf(
			"anoisesrc=color=pink:duration=%s:seed=33[n];"+
				"sine=frequency=196:duration=%s[s1];"+
				"sine=frequency=261.6:duration=%s[s2];"+
				"sine=frequency=329.6:duration=%s[s3];"+
				"[s1]volume=0.06,tremolo=f=0.12:d=0.25[ts1];"+
				"[s2]volume=0.04,tremolo=f=0.1:d=0.2[ts2];"+
				"[s3]volume=0.03,tremolo=f=0.08:d=0.15[ts3];"+
				"[n]volume=0.08,lowpass=f=400,highpass=f=120[fn];"+
				"[fn][ts1][ts2][ts3]amix=inputs=4:duration=first,"+
				"highpass=f=100,lowpass=f=1800,volume=1.3[out]",
			dur, dur, dur, dur)

	case "motivational":
		// Energetic, uplifting: brighter tones with rhythmic pulse
		// Frequencies: 220Hz (A3) + 277.2Hz (C#4) + 329.6Hz (E4) = A major chord
		filterComplex = fmt.Sprintf(
			"anoisesrc=color=pink:duration=%s:seed=88[n];"+
				"sine=frequency=220:duration=%s[s1];"+
				"sine=frequency=277.2:duration=%s[s2];"+
				"sine=frequency=329.6:duration=%s[s3];"+
				"sine=frequency=440:duration=%s[s4];"+
				"[s1]volume=0.12,tremolo=f=0.4:d=0.5[ts1];"+
				"[s2]volume=0.08,tremolo=f=0.35:d=0.4[ts2];"+
				"[s3]volume=0.06,tremolo=f=0.3:d=0.35[ts3];"+
				"[s4]volume=0.04,tremolo=f=0.5:d=0.3[ts4];"+
				"[n]volume=0.12,lowpass=f=600,highpass=f=150[fn];"+
				"[fn][ts1][ts2][ts3][ts4]amix=inputs=5:duration=first,"+
				"highpass=f=60,lowpass=f=2500,volume=1.4[out]",
			dur, dur, dur, dur, dur)

	case "humorous":
		// Light, playful: higher frequencies with bouncy modulation
		// Frequencies: 329.6Hz (E4) + 392Hz (G4) + 523.3Hz (C5) = C major (bright)
		filterComplex = fmt.Sprintf(
			"anoisesrc=color=white:duration=%s:seed=99[n];"+
				"sine=frequency=329.6:duration=%s[s1];"+
				"sine=frequency=392:duration=%s[s2];"+
				"sine=frequency=523.3:duration=%s[s3];"+
				"[s1]volume=0.07,tremolo=f=0.6:d=0.5[ts1];"+
				"[s2]volume=0.05,tremolo=f=0.8:d=0.4[ts2];"+
				"[s3]volume=0.04,tremolo=f=1.0:d=0.35[ts3];"+
				"[n]volume=0.06,lowpass=f=800,highpass=f=200[fn];"+
				"[fn][ts1][ts2][ts3]amix=inputs=4:duration=first,"+
				"highpass=f=150,lowpass=f=3000,volume=1.2[out]",
			dur, dur, dur, dur)

	default:
		// Fallback to dramatic
		return generateAmbientMusic("dramatic", outputPath, durationSec)
	}

	log.Printf("🎵 Generating %s ambient music (%ds) with FFmpeg...", mood, durationSec)

	args := []string{
		"-f", "lavfi",
		"-i", filterComplex,
		"-map", "[out]",
		"-c:a", "libmp3lame", "-b:a", "128k",
		"-y", outputPath,
	}

	cmd := exec.Command("ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg music gen: %w — output: %s", err, string(output))
	}

	// Verify the file was created and has content
	info, err := os.Stat(outputPath)
	if err != nil || info.Size() < 1000 {
		return fmt.Errorf("generated music file is empty or missing")
	}

	log.Printf("✅ %s music generated: %s (%d KB)", mood, outputPath, info.Size()/1024)
	return nil
}
