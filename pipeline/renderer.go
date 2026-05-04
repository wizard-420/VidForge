package pipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"yt-automation-studio/config"
	"yt-automation-studio/models"
)

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

		var segmentVideoPath string

		if len(seg.SubVisuals) > 0 {
			// ---- Multi-clip segment: stitch sub-visuals together ----
			subClipDuration := float64(seg.DurationSec) / float64(len(seg.SubVisuals))
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
					// Image → video of sub-clip duration with Ken Burns zoom
					frames := int(subClipDuration * 30)
					vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,zoompan=z='min(zoom+0.0015,1.1)':d=%d:s=%dx%d:fps=30", width, height, width, height, frames, width, height)
					args := []string{
						"-loop", "1", "-i", clipPath,
						"-c:v", "libx264", "-t", fmt.Sprintf("%.1f", subClipDuration),
						"-pix_fmt", "yuv420p", "-r", "30",
						"-vf", vf,
						"-y", preparedPath,
					}
					if err := runFFmpeg(args); err != nil {
						return fmt.Errorf("image to video seg %d sub %d: %w", seg.SegmentID, j, err)
					}
				} else {
					// FIX #4: No -stream_loop — clip plays once, trimmed to duration.
					// This prevents the same footage from visually repeating.
					args := []string{
						"-i", clipPath,
						"-t", fmt.Sprintf("%.1f", subClipDuration),
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

			// Concat sub-clips into one segment video (no audio yet)
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
				// FIX #5: Re-encode instead of -c copy to prevent freeze artifacts
				// from codec/framerate mismatches between different source clips.
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
				frames := seg.DurationSec * 30
				vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,zoompan=z='min(zoom+0.0015,1.1)':d=%d:s=%dx%d:fps=30", width, height, width, height, frames, width, height)
				args := []string{
					"-loop", "1", "-i", clipPath,
					"-c:v", "libx264", "-t", fmt.Sprintf("%d", seg.DurationSec),
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

		// Overlay voiceover audio onto the segment video
		syncedPath := filepath.Join(segDir, fmt.Sprintf("seg_%02d_synced.mp4", seg.SegmentID))
		args := []string{
			"-i", segmentVideoPath, "-i", voicePath,
			"-map", "0:v", "-map", "1:a",
			"-shortest",
			"-vf", fmt.Sprintf("scale=%s:force_original_aspect_ratio=decrease,pad=%s:(ow-iw)/2:(oh-ih)/2:color=black", resolution, resolution),
			"-c:v", "libx264", "-preset", "fast", "-crf", "23",
			"-pix_fmt", "yuv420p", "-r", "30",
			"-c:a", "aac", "-b:a", "192k",
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
	args := []string{"-f", "concat", "-safe", "0", "-i", concatListPath, "-c", "copy", "-y", rawCombined}
	if err := runFFmpeg(args); err != nil {
		return fmt.Errorf("concat: %w", err)
	}

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

// finalRender combines video, music (looped to full duration), and captions into the final MP4.
// Music is looped with -stream_loop -1 and cut to video length by amix duration=first.
func finalRender(videoPath, musicPath, captionsPath, outputPath, captionStyle string, isShort bool) error {
	// Build caption style
	fontSize := "14"
	if isShort {
		fontSize = "22"
	}
	fontStyle := fmt.Sprintf("FontName=Arial,FontSize=%s,Bold=1,PrimaryColour=&HFFFFFF,OutlineColour=&H000000,Outline=2,Shadow=1,Alignment=2", fontSize)

	if captionStyle == "subtitle" {
		fontStyle = fmt.Sprintf("FontName=Arial,FontSize=%s,PrimaryColour=&HFFFFFF,OutlineColour=&H000000,Outline=1,Shadow=0,Alignment=2", fontSize)
	}

	var args []string

	hasCaptions := fileExists(captionsPath) && captionStyle != "none"
	hasMusic := musicPath != "" && fileExists(musicPath)

	if hasMusic && hasCaptions {
		// Music is looped indefinitely with -stream_loop, amix duration=first cuts it at voice end
		args = []string{
			"-i", videoPath,
			"-stream_loop", "-1", "-i", musicPath,
			"-filter_complex",
			"[0:a]volume=1.0[voice];[1:a]volume=0.12[music];[voice][music]amix=inputs=2:duration=first[aout]",
			"-vf", fmt.Sprintf("subtitles=%s:force_style='%s'", filepath.ToSlash(captionsPath), fontStyle),
			"-map", "0:v", "-map", "[aout]",
			"-c:v", "libx264", "-preset", "medium", "-crf", "21",
			"-c:a", "aac", "-b:a", "192k",
			"-movflags", "+faststart", "-y", outputPath,
		}
	} else if hasCaptions {
		args = []string{
			"-i", videoPath,
			"-vf", fmt.Sprintf("subtitles=%s:force_style='%s'", filepath.ToSlash(captionsPath), fontStyle),
			"-c:v", "libx264", "-preset", "medium", "-crf", "21",
			"-c:a", "copy", "-movflags", "+faststart", "-y", outputPath,
		}
	} else if hasMusic {
		// Music looped to cover full video duration
		args = []string{
			"-i", videoPath,
			"-stream_loop", "-1", "-i", musicPath,
			"-filter_complex", "[0:a]volume=1.0[voice];[1:a]volume=0.12[music];[voice][music]amix=inputs=2:duration=first[aout]",
			"-map", "0:v", "-map", "[aout]",
			"-c:v", "copy", "-c:a", "aac", "-b:a", "192k",
			"-movflags", "+faststart", "-y", outputPath,
		}
	} else {
		// Just copy
		args = []string{"-i", videoPath, "-c", "copy", "-movflags", "+faststart", "-y", outputPath}
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
