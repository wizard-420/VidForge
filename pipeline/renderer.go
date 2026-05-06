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

// resolveResolution maps an aspect ratio name to (width, height, "W:H" string)
// suitable for FFmpeg scale/pad filters. Defaults to landscape 1920x1080.
func resolveResolution(aspect string) (int, int, string) {
	switch aspect {
	case "portrait":
		return 1080, 1920, "1080:1920"
	case "square":
		return 1080, 1080, "1080:1080"
	case "landscape":
		fallthrough
	default:
		return 1920, 1080, "1920:1080"
	}
}

// fitFilter returns the FFmpeg filter chain that scales an input video to
// exactly width×height according to the chosen fit mode:
//   - "fill" (default): zoom-and-crop (force_original_aspect_ratio=increase + crop).
//     The frame is fully filled with content; edges are cropped if aspect ratios differ.
//   - "fit": letterbox/pillarbox (force_original_aspect_ratio=decrease + black pad).
//     The entire source is preserved; black bars fill any leftover space.
func fitFilter(width, height int, fitMode string) string {
	if fitMode == "fit" {
		return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black",
			width, height, width, height)
	}
	// "fill" / default
	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d",
		width, height, width, height)
}

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
	// Resolution derives from AspectRatio (landscape | portrait | square),
	// independent of long/short format. This lets users render Shorts in
	// landscape or long-form videos in portrait/square as needed.
	width, height, resolution := resolveResolution(job.Payload.AspectRatio)
	isVertical := job.Payload.AspectRatio == "portrait" || job.Payload.AspectRatio == "square"
	fitMode := job.Payload.FitMode
	if fitMode == "" {
		fitMode = "fill"
	}
	scaleFilter := fitFilter(width, height, fitMode)
	voiceMuted := job.Payload.VoiceoverMode == "none"
	profile := ProfileFor(job.Payload.OutputQuality)
	fpsStr := fmt.Sprintf("%d", profile.FPS)
	log.Printf("📐 Job %s — render aspect=%q resolution=%s fit=%s (format=%s) voice_muted=%v quality=%s preset=%s crf=%s fps=%d",
		job.JobID[:8], job.Payload.AspectRatio, resolution, fitMode, job.Payload.Format,
		voiceMuted, profile.Quality, profile.Preset, profile.CRF, profile.FPS)

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
					frames := int(subClipDuration * float64(profile.FPS))
					vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,zoompan=z='min(zoom+0.0015,1.1)':d=%d:s=%dx%d:fps=%d", width, height, width, height, frames, width, height, profile.FPS)
					args := []string{
						"-loop", "1", "-i", clipPath,
						"-c:v", "libx264", "-t", fmt.Sprintf("%.3f", subClipDuration),
						"-pix_fmt", "yuv420p", "-r", fpsStr,
						"-vf", vf,
						"-y", preparedPath,
					}
					if err := runFFmpeg(args); err != nil {
						return fmt.Errorf("image to video seg %d sub %d: %w", seg.SegmentID, j, err)
					}
				} else {
					// Scale to target frame using the user's chosen fit mode:
					//   "fill" — zoom-and-crop, fully fills the frame (default).
					//   "fit"  — letterbox/pillarbox, preserves the entire source.
					// This pass is intermediate (re-encoded again at segment-sync
					// time) so we keep it fast/CRF-23 even in High quality mode —
					// the profile preset/CRF apply at the master encode passes
					// below where they matter.
					args := []string{
						"-i", clipPath,
						"-t", fmt.Sprintf("%.3f", subClipDuration),
						"-vf", scaleFilter,
						"-c:v", "libx264", "-preset", "fast", "-crf", "23",
						"-pix_fmt", "yuv420p", "-an", "-r", fpsStr,
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
				// Intermediate concat (re-encoded again at segment-sync). Keep fast.
				args := []string{
					"-f", "concat", "-safe", "0", "-i", concatPath,
					"-c:v", "libx264", "-preset", "fast", "-crf", "23",
					"-pix_fmt", "yuv420p", "-r", fpsStr,
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
				frames := int(segTarget * float64(profile.FPS))
				vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,zoompan=z='min(zoom+0.0015,1.1)':d=%d:s=%dx%d:fps=%d", width, height, width, height, frames, width, height, profile.FPS)
				args := []string{
					"-loop", "1", "-i", clipPath,
					"-c:v", "libx264", "-t", segTargetStr,
					"-pix_fmt", "yuv420p", "-r", fpsStr,
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
		// Apply the user's chosen fit mode (fill = zoom-crop, fit = letterbox).
		// When the source already matches the target frame, this is a no-op;
		// when it doesn't, it either crops or pads as configured. Sub-clips
		// usually arrive at target res from the prep step above, so this
		// final pass is mostly defensive against per-clip edge cases.
		filterComplex := fmt.Sprintf(
			"[0:v]%s,tpad=stop_mode=clone:stop_duration=%s,trim=duration=%s,setpts=PTS-STARTPTS[v];"+
				"[1:a]apad=whole_dur=%s,atrim=duration=%s,asetpts=PTS-STARTPTS[a]",
			scaleFilter, segTargetStr, segTargetStr, segTargetStr, segTargetStr,
		)
		// Segment sync — produces a per-segment master synced with voice. The
		// quality profile applies here so High mode actually reaches the final
		// output. Intermediate sub-clip prep above stays at fast/23 because
		// those files get re-encoded at this very step.
		args := []string{
			"-i", segmentVideoPath, "-i", voicePath,
			"-filter_complex", filterComplex,
			"-map", "[v]", "-map", "[a]",
			"-c:v", "libx264", "-preset", profile.Preset, "-crf", profile.CRF,
			"-pix_fmt", "yuv420p", "-r", fpsStr,
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
	// causes audio-shorter-than-video issues downstream. This is also the
	// master video encode — when finalRender skips captions burn-in it just
	// stream-copies this file's video, so the quality profile applied here
	// reaches the final output.
	args := []string{
		"-f", "concat", "-safe", "0", "-i", concatListPath,
		"-c:v", "libx264", "-preset", profile.Preset, "-crf", profile.CRF,
		"-pix_fmt", "yuv420p", "-r", fpsStr,
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

	// Step 6c — Generate captions with Whisper.
	// Skip in no-voice mode: the audio is silent so Whisper would either
	// produce empty output or hallucinate. The viewer doesn't expect captions
	// on a music-only video anyway.
	captionsPath := filepath.Join(jobDir, "captions.srt")
	if voiceMuted {
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 6, StageName: "Video Render",
			ProgressPct: 65, Message: "Skipping captions (no-voice mode)...",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		log.Printf("🎵 Job %s — voice_muted=true, skipping Whisper caption generation", job.JobID[:8])
	} else {
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 6, StageName: "Video Render",
			ProgressPct: 65, Message: "Generating captions...",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})

		if err := generateCaptions(rawCombined, captionsPath); err != nil {
			job.AddError(fmt.Sprintf("Caption generation failed: %v — continuing without captions", err))
		}
		job.CaptionsFile = captionsPath
	}

	// Step 6d — Final render with music + captions
	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 6, StageName: "Video Render",
		ProgressPct: 80, Message: "Final render with music and captions...",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	finalOutput := filepath.Join(jobDir, "final_output.mp4")
	if err := finalRender(rawCombined, job.MusicFile, captionsPath, finalOutput, job.Payload.CaptionStyle, isVertical, voiceMuted, profile); err != nil {
		return fmt.Errorf("final render: %w", err)
	}

	job.FinalVideo = finalOutput
	return nil
}

// finalRender uses a 2-pass approach for maximum reliability:
//   Pass 1: prepare the final audio track of EXACTLY video duration in a separate file.
//   Pass 2: mux video + prepared audio + captions, then verify final durations.
//
// isVertical = true for portrait/square aspect ratios — used to scale up the
// caption font size since vertical layouts have less horizontal text room.
//
// voiceMuted = true means the per-segment "voice" tracks are actually silent
// placeholders (no-voice / music-only mode). prepareFinalAudio uses this to
// boost music volume from a duck-under-voice level to near full.
//
// profile carries the user-selected output_quality so the captions burn-in
// pass (which re-encodes video) matches what the segment-sync + raw_combined
// passes produced. When captions are disabled, finalRender stream-copies the
// video and the profile only affects audio bitrate (which is fixed anyway).
func finalRender(videoPath, musicPath, captionsPath, outputPath, captionStyle string, isVertical, voiceMuted bool, profile EncodeProfile) error {
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
	if err := prepareFinalAudio(videoPath, loopedMusicPath, preparedAudioPath, videoDuration, hasMusic, voiceMuted); err != nil {
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
		if isVertical {
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
			"-c:v", "libx264", "-preset", profile.Preset, "-crf", profile.CRF,
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
//
// voiceMuted = true tells us the "voice" stream from videoPath is actually a
// silent placeholder (music-only mode). In that case music takes over as the
// primary audio at near-full volume, instead of being ducked under narration.
func prepareFinalAudio(videoPath, musicPath, outputPath string, targetDuration float64, hasMusic, voiceMuted bool) error {
	durStr := fmt.Sprintf("%.3f", targetDuration)

	var args []string
	if hasMusic {
		// Music volume — duck under voice in narrated modes, but take over the
		// soundtrack in music-only mode.
		musicVol := "0.12"
		if voiceMuted {
			musicVol = "0.9"
		}
		filterComplex := fmt.Sprintf(
			"[0:a]apad=whole_dur=%s,atrim=duration=%s,asetpts=PTS-STARTPTS,volume=1.0[voice];"+
				"[1:a]atrim=duration=%s,asetpts=PTS-STARTPTS,volume=%s[music];"+
				"[voice][music]amix=inputs=2:duration=longest:dropout_transition=0[aout]",
			durStr, durStr, durStr, musicVol,
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
