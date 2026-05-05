package pipeline

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"yt-automation-studio/config"
	"yt-automation-studio/models"
)

// segmentTailPad returns the silence-pad after a segment based on its type.
// This produces a podcast-like cadence: a tiny breath after the hook,
// brief pauses between body segments, and a longer outro after the CTA.
func segmentTailPad(segType string) float64 {
	switch segType {
	case "hook":
		return 0.5
	case "cta":
		return 1.0
	default: // "body" and anything else
		return 0.4
	}
}

// RunVideoRenderer executes Stage 6: FFmpeg video rendering pipeline.
// Handles multi-clip segments (sub-visuals) where each segment may contain
// multiple visual assets that need to be stitched together.
func RunVideoRenderer(job *models.JobContext, progress ProgressFunc) error {
	if job.Script == nil {
		return fmt.Errorf("no script available")
	}

	jobDir := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", job.JobID))
	segDir := filepath.Join(jobDir, "segments")
	os.MkdirAll(segDir, 0755)

	segments := job.Script.Segments
	isShort := job.Payload.Format == "short"
	resolution := "1920:1080"
	width, height := 1920, 1080
	if isShort {
		resolution = "1080:1920"
		width, height = 1080, 1920
	}

	// Step 6a — Build segment videos from sub-visuals + voice
	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 6, StageName: "Video Render",
		ProgressPct: 10, Message: "Syncing visuals with voiceover...",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	var syncedFiles []string
	for i, seg := range segments {
		pct := 10 + int(float64(i+1)/float64(len(segments))*30)
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 6, StageName: "Video Render",
			ProgressPct: pct,
			Message:     fmt.Sprintf("Processing segment %d of %d...", i+1, len(segments)),
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})

		voicePath := job.VoiceFiles[fmt.Sprintf("%d", seg.SegmentID)]
		if voicePath == "" {
			continue
		}

		// VOICE DRIVES TIMING. The actual TTS duration is the source of truth
		// for this segment. We add a small natural breath pad (varies by type)
		// and use that as the target for both video and audio.
		voiceDur := getMediaDuration(voicePath)
		if voiceDur <= 0 {
			log.Printf("⚠️ Could not determine voice duration for seg %d, falling back to script estimate", seg.SegmentID)
			voiceDur = float64(seg.DurationSec)
		}
		tailPad := segmentTailPad(seg.Type)
		segTarget := voiceDur + tailPad
		segTargetStr := fmt.Sprintf("%.3f", segTarget)
		log.Printf("📐 Job %s — seg %d (%s): voice=%.3fs + pad=%.2fs → target=%.3fs",
			job.JobID[:8], seg.SegmentID, seg.Type, voiceDur, tailPad, segTarget)

		var segmentVideoPath string

		if len(seg.SubVisuals) > 0 {
			// ---- Multi-clip segment: stitch sub-visuals together ----
			// Sub-clip duration is derived from the actual voice timing, not the
			// LLM's optimistic plan. Each sub-clip gets an even share of segTarget.
			subClipDuration := segTarget / float64(len(seg.SubVisuals))
			if subClipDuration < 2 {
				subClipDuration = 2
			}

			var subClipPaths []string
			for j := range seg.SubVisuals {
				key := fmt.Sprintf("%d_%d", seg.SegmentID, j)
				clipPath := job.ClipFiles[key]
				if clipPath == "" {
					continue
				}

				preparedPath := filepath.Join(segDir, fmt.Sprintf("seg_%02d_sub_%02d_prep.mp4", seg.SegmentID, j))

				if strings.HasSuffix(clipPath, ".jpg") || strings.HasSuffix(clipPath, ".png") {
					frames := int(subClipDuration * 30)
					vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,zoompan=z='min(zoom+0.0015,1.1)':d=%d:s=%dx%d:fps=30", width, height, width, height, frames, width, height)
					args := []string{
						"-loop", "1", "-i", clipPath,
						"-c:v", "libx264", "-t", fmt.Sprintf("%.3f", subClipDuration),
						"-pix_fmt", "yuv420p", "-r", "30",
						"-vf", vf,
						"-y", preparedPath,
					}
					if err := runFFmpeg(args); err != nil {
						return fmt.Errorf("image to video seg %d sub %d: %w", seg.SegmentID, j, err)
					}
				} else {
					args := []string{
						"-i", clipPath,
						"-t", fmt.Sprintf("%.3f", subClipDuration),
						"-vf", fmt.Sprintf("scale=%s:force_original_aspect_ratio=decrease,pad=%s:(ow-iw)/2:(oh-ih)/2:color=black", resolution, resolution),
						"-c:v", "libx264", "-preset", "fast", "-crf", "23",
						"-pix_fmt", "yuv420p", "-an", "-r", "30",
						"-y", preparedPath,
					}
					if err := runFFmpeg(args); err != nil {
						return fmt.Errorf("trim clip seg %d sub %d: %w", seg.SegmentID, j, err)
					}
				}
				subClipPaths = append(subClipPaths, preparedPath)
			}

			if len(subClipPaths) == 0 {
				continue
			}

			if len(subClipPaths) == 1 {
				segmentVideoPath = subClipPaths[0]
			} else {
				concatPath := filepath.Join(segDir, fmt.Sprintf("seg_%02d_subconcat.txt", seg.SegmentID))
				var lines []string
				for _, p := range subClipPaths {
					lines = append(lines, fmt.Sprintf("file '%s'", filepath.ToSlash(p)))
				}
				os.WriteFile(concatPath, []byte(strings.Join(lines, "\n")), 0644)

				segmentVideoPath = filepath.Join(segDir, fmt.Sprintf("seg_%02d_subcombined.mp4", seg.SegmentID))
				args := []string{
					"-f", "concat", "-safe", "0", "-i", concatPath,
					"-c:v", "libx264", "-preset", "fast", "-crf", "23",
					"-pix_fmt", "yuv420p", "-r", "30",
					"-y", segmentVideoPath,
				}
				if err := runFFmpeg(args); err != nil {
					return fmt.Errorf("concat sub-clips seg %d: %w", seg.SegmentID, err)
				}
			}
		} else {
			// ---- Legacy: single clip per segment ----
			clipPath := job.ClipFiles[fmt.Sprintf("%d", seg.SegmentID)]
			if clipPath == "" {
				continue
			}

			if strings.HasSuffix(clipPath, ".jpg") || strings.HasSuffix(clipPath, ".png") {
				imgVideoPath := filepath.Join(segDir, fmt.Sprintf("seg_%02d_imgvid.mp4", seg.SegmentID))
				frames := int(segTarget * 30)
				vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,zoompan=z='min(zoom+0.0015,1.1)':d=%d:s=%dx%d:fps=30", width, height, width, height, frames, width, height)
				args := []string{
					"-loop", "1", "-i", clipPath,
					"-c:v", "libx264", "-t", segTargetStr,
					"-pix_fmt", "yuv420p", "-r", "30",
					"-vf", vf,
					"-y", imgVideoPath,
				}
				if err := runFFmpeg(args); err != nil {
					return fmt.Errorf("image to video seg %d: %w", seg.SegmentID, err)
				}
				segmentVideoPath = imgVideoPath
			} else {
				segmentVideoPath = clipPath
			}
		}

		// Sync filter: video gets padded (clone last frame) and trimmed to segTarget.
		// Audio (voice) gets padded with silence (apad) to segTarget. The voice plays
		// for voiceDur seconds, then exactly tailPad seconds of silence — that's the
		// natural breath that makes segment transitions sound human.
		syncedPath := filepath.Join(segDir, fmt.Sprintf("seg_%02d_synced.mp4", seg.SegmentID))
		filterComplex := fmt.Sprintf(
			"[0:v]scale=%s:force_original_aspect_ratio=decrease,pad=%s:(ow-iw)/2:(oh-ih)/2:color=black,tpad=stop_mode=clone:stop_duration=%s,trim=duration=%s,setpts=PTS-STARTPTS[v];"+
				"[1:a]apad=whole_dur=%s,atrim=duration=%s,asetpts=PTS-STARTPTS[a]",
			resolution, resolution, segTargetStr, segTargetStr, segTargetStr, segTargetStr,
		)
		args := []string{
			"-i", segmentVideoPath, "-i", voicePath,
			"-filter_complex", filterComplex,
			"-map", "[v]", "-map", "[a]",
			"-c:v", "libx264", "-preset", "fast", "-crf", "23",
			"-pix_fmt", "yuv420p", "-r", "30",
			"-c:a", "aac", "-b:a", "192k", "-ar", "48000",
			"-y", syncedPath,
		}
		if err := runFFmpeg(args); err != nil {
			return fmt.Errorf("sync seg %d: %w", seg.SegmentID, err)
		}
		syncedFiles = append(syncedFiles, syncedPath)
	}

	if len(syncedFiles) == 0 {
		return fmt.Errorf("no segments were rendered")
	}

	// Step 6b — Concatenate all segments
	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 6, StageName: "Video Render",
		ProgressPct: 50, Message: "Concatenating segments...",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	concatListPath := filepath.Join(jobDir, "concat_list.txt")
	var concatLines []string
	for _, f := range syncedFiles {
		concatLines = append(concatLines, fmt.Sprintf("file '%s'", filepath.ToSlash(f)))
	}
	os.WriteFile(concatListPath, []byte(strings.Join(concatLines, "\n")), 0644)

	rawCombined := filepath.Join(jobDir, "raw_combined.mp4")
	// Re-encode during concat to prevent AAC priming/padding gaps from
	// accumulating between segments. -c copy preserves these gaps and
	// causes audio-shorter-than-video issues downstream.
	args := []string{
		"-f", "concat", "-safe", "0", "-i", concatListPath,
		"-c:v", "libx264", "-preset", "fast", "-crf", "23",
		"-pix_fmt", "yuv420p", "-r", "30",
		"-c:a", "aac", "-b:a", "192k", "-ar", "48000",
		"-fflags", "+genpts",
		"-y", rawCombined,
	}
	if err := runFFmpeg(args); err != nil {
		return fmt.Errorf("concat: %w", err)
	}

	// Log durations to verify audio matches video at concat stage
	rawVideoDur := GetStreamDuration(rawCombined, "v")
	rawAudioDur := GetStreamDuration(rawCombined, "a")
	log.Printf("📊 Job %s — raw_combined.mp4: video=%.3fs, audio=%.3fs", job.JobID[:8], rawVideoDur, rawAudioDur)

	// Step 6c — Generate captions with Whisper
	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 6, StageName: "Video Render",
		ProgressPct: 65, Message: "Generating captions...",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	captionsPath := filepath.Join(jobDir, "captions.srt")
	if err := generateCaptions(rawCombined, captionsPath); err != nil {
		job.AddError(fmt.Sprintf("Caption generation failed: %v — continuing without captions", err))
	}
	job.CaptionsFile = captionsPath

	// Step 6d — Final render with music + captions
	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 6, StageName: "Video Render",
		ProgressPct: 80, Message: "Final render with music and captions...",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	finalOutput := filepath.Join(jobDir, "final_output.mp4")
	if err := finalRender(rawCombined, job.MusicFile, captionsPath, finalOutput, job.Payload.CaptionStyle, isShort); err != nil {
		return fmt.Errorf("final render: %w", err)
	}

	job.FinalVideo = finalOutput
	return nil
}

// finalRender uses a 2-pass approach for maximum reliability:
//  Pass 1: prepare the final audio track of EXACTLY video duration in a separate file.
//  Pass 2: mux video + prepared audio + captions, then verify final durations.
func finalRender(videoPath, musicPath, captionsPath, outputPath, captionStyle string, isShort bool) error {
	hasCaptions := fileExists(captionsPath) && captionStyle != "none"
	hasMusic := musicPath != "" && fileExists(musicPath)

	// Get exact video duration; this becomes the master length for the entire output.
	videoDuration := getMediaDuration(videoPath)
	if videoDuration <= 0 {
		return fmt.Errorf("could not determine video duration for %s", videoPath)
	}
	durStr := fmt.Sprintf("%.3f", videoDuration)
	jobDir := filepath.Dir(outputPath)

	// Pre-loop music to cover the video duration with buffer.
	loopedMusicPath := ""
	if hasMusic {
		loopedMusicPath = filepath.Join(jobDir, "music_looped.mp3")
		if err := loopMusicToFitDuration(musicPath, loopedMusicPath, videoDuration); err != nil {
			log.Printf("⚠️ Music loop failed: %v — proceeding without music", err)
			loopedMusicPath = ""
			hasMusic = false
		}
	}

	// Pass 1: Prepare the final audio track to a separate file
	preparedAudioPath := filepath.Join(jobDir, "final_audio.m4a")
	if err := prepareFinalAudio(videoPath, loopedMusicPath, preparedAudioPath, videoDuration, hasMusic); err != nil {
		return fmt.Errorf("prepare final audio: %w", err)
	}

	// Verify the prepared audio duration matches video duration
	preparedAudioDur := getMediaDuration(preparedAudioPath)
	log.Printf("📊 finalRender: video=%.3fs, prepared audio=%.3fs (diff=%.3fs)",
		videoDuration, preparedAudioDur, preparedAudioDur-videoDuration)

	// Pass 2: Mux video + prepared audio (+ captions if applicable)
	var args []string
	if hasCaptions {
		fontSize := "14"
		if isShort {
			fontSize = "22"
		}
		fontStyle := fmt.Sprintf("FontName=Arial,FontSize=%s,Bold=1,PrimaryColour=&HFFFFFF,OutlineColour=&H000000,Outline=2,Shadow=1,Alignment=2", fontSize)
		if captionStyle == "subtitle" {
			fontStyle = fmt.Sprintf("FontName=Arial,FontSize=%s,PrimaryColour=&HFFFFFF,OutlineColour=&H000000,Outline=1,Shadow=0,Alignment=2", fontSize)
		}
		args = []string{
			"-i", videoPath,
			"-i", preparedAudioPath,
			"-vf", fmt.Sprintf("subtitles=%s:force_style='%s'", filepath.ToSlash(captionsPath), fontStyle),
			"-map", "0:v", "-map", "1:a",
			"-t", durStr,
			"-c:v", "libx264", "-preset", "medium", "-crf", "21",
			"-c:a", "aac", "-b:a", "192k",
			"-shortest",
			"-avoid_negative_ts", "make_zero",
			"-movflags", "+faststart", "-y", outputPath,
		}
	} else {
		args = []string{
			"-i", videoPath,
			"-i", preparedAudioPath,
			"-map", "0:v", "-map", "1:a",
			"-t", durStr,
			"-c:v", "copy",
			"-c:a", "aac", "-b:a", "192k",
			"-shortest",
			"-avoid_negative_ts", "make_zero",
			"-movflags", "+faststart", "-y", outputPath,
		}
	}

	err := runFFmpeg(args)

	// Cleanup
	os.Remove(preparedAudioPath)
	if loopedMusicPath != "" {
		os.Remove(loopedMusicPath)
	}

	if err == nil {
		// Verify final output durations
		finalVideoDur := GetStreamDuration(outputPath, "v")
		finalAudioDur := GetStreamDuration(outputPath, "a")
		log.Printf("✅ finalRender output: video=%.3fs, audio=%.3fs (diff=%.3fs)",
			finalVideoDur, finalAudioDur, finalAudioDur-finalVideoDur)
	}

	return err
}

// prepareFinalAudio generates a complete audio track of exactly targetDuration seconds.
// It pads the voice with silence to match duration, optionally mixes in music.
func prepareFinalAudio(videoPath, musicPath, outputPath string, targetDuration float64, hasMusic bool) error {
	durStr := fmt.Sprintf("%.3f", targetDuration)

	var args []string
	if hasMusic {
		// Mix voice (padded to duration) + music (already pre-looped to >= duration)
		filterComplex := fmt.Sprintf(
			"[0:a]apad=whole_dur=%s,atrim=duration=%s,asetpts=PTS-STARTPTS,volume=1.0[voice];"+
				"[1:a]atrim=duration=%s,asetpts=PTS-STARTPTS,volume=0.12[music];"+
				"[voice][music]amix=inputs=2:duration=longest:dropout_transition=0[aout]",
			durStr, durStr, durStr,
		)
		args = []string{
			"-i", videoPath,
			"-i", musicPath,
			"-filter_complex", filterComplex,
			"-map", "[aout]",
			"-t", durStr,
			"-c:a", "aac", "-b:a", "192k", "-ar", "48000",
			"-y", outputPath,
		}
	} else {
		// Just pad voice to duration
		filterComplex := fmt.Sprintf(
			"[0:a]apad=whole_dur=%s,atrim=duration=%s,asetpts=PTS-STARTPTS[aout]",
			durStr, durStr,
		)
		args = []string{
			"-i", videoPath,
			"-filter_complex", filterComplex,
			"-map", "[aout]",
			"-t", durStr,
			"-c:a", "aac", "-b:a", "192k", "-ar", "48000",
			"-y", outputPath,
		}
	}

	return runFFmpeg(args)
}

// GetStreamDuration returns the duration of a specific stream type ("v" or "a") in seconds.
// Exported for use by API handlers that need to verify stream durations.
func GetStreamDuration(filePath, streamType string) float64 {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", streamType+":0",
		"-show_entries", "stream=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	var dur float64
	fmt.Sscanf(strings.TrimSpace(string(output)), "%f", &dur)
	if dur > 0 {
		return dur
	}
	// Fallback to format duration if stream duration is missing
	return getMediaDuration(filePath)
}

// getMediaDuration returns the duration of a media file in seconds using ffprobe.
func getMediaDuration(filePath string) float64 {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	var dur float64
	fmt.Sscanf(strings.TrimSpace(string(output)), "%f", &dur)
	return dur
}

// loopMusicToFitDuration creates a new audio file by concatenating the music enough times
// to guarantee it covers the target duration. This avoids -stream_loop reliability issues.
func loopMusicToFitDuration(musicPath, outputPath string, targetDuration float64) error {
	musicDuration := getMediaDuration(musicPath)
	if musicDuration <= 0 {
		return fmt.Errorf("cannot determine music duration")
	}

	// If music is already long enough, just copy it
	if musicDuration >= targetDuration+5 {
		args := []string{"-i", musicPath, "-c", "copy", "-y", outputPath}
		return runFFmpeg(args)
	}

	// Calculate how many times we need to repeat
	repeats := int(targetDuration/musicDuration) + 2

	// Build a concat list file
	concatPath := strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + "_concat.txt"
	var lines []string
	for i := 0; i < repeats; i++ {
		lines = append(lines, fmt.Sprintf("file '%s'", filepath.ToSlash(musicPath)))
	}
	if err := os.WriteFile(concatPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		return fmt.Errorf("write concat list: %w", err)
	}
	defer os.Remove(concatPath)

	// Concatenate and trim to target duration + small buffer
	args := []string{
		"-f", "concat", "-safe", "0", "-i", concatPath,
		"-t", fmt.Sprintf("%.1f", targetDuration+10),
		"-c:a", "libmp3lame", "-b:a", "192k",
		"-y", outputPath,
	}
	return runFFmpeg(args)
}

// generateCaptions uses Whisper to create SRT captions
func generateCaptions(videoPath, srtPath string) error {
	// Try using whisper CLI (pip install openai-whisper)
	cmd := exec.Command("whisper", videoPath,
		"--model", "base",
		"--output_format", "srt",
		"--output_dir", filepath.Dir(srtPath),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("whisper: %w — output: %s", err, string(output))
	}
	return nil
}

func runFFmpeg(args []string) error {
	cmd := exec.Command("ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w — output: %s", err, string(output))
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
