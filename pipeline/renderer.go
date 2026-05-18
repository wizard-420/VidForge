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

// escapeDrawtext escapes characters that have special meaning inside an
// FFmpeg drawtext filter's text='...' value (backslash, single quote, colon,
// percent), and collapses literal newlines to spaces.
func escapeDrawtext(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", `\'`)
	s = strings.ReplaceAll(s, ":", `\:`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// resolveFontFile picks a TTF inside assets/fonts/ that matches the requested
// family + bold/italic flags. Returns the slash-style path FFmpeg expects, or
// "" when no usable file is present (caller then omits fontfile= and lets
// FFmpeg fall back to its built-in font).
func resolveFontFile(family string, bold, italic bool) string {
	base := filepath.Join("assets", "fonts")
	pick := func(name string) string {
		p := filepath.Join(base, name)
		if fileExists(p) {
			abs, err := filepath.Abs(p)
			if err != nil {
				return filepath.ToSlash(p)
			}
			return filepath.ToSlash(abs)
		}
		return ""
	}

	fam := strings.ToLower(strings.TrimSpace(family))
	if fam == "" {
		fam = "inter"
	}

	var candidates []string
	switch fam {
	case "inter":
		switch {
		case bold && italic:
			candidates = []string{"Inter-BoldItalic.ttf", "Inter-Bold.ttf", "Inter-Regular.ttf"}
		case bold:
			candidates = []string{"Inter-Bold.ttf", "Inter-Regular.ttf"}
		case italic:
			candidates = []string{"Inter-Italic.ttf", "Inter-Regular.ttf"}
		default:
			candidates = []string{"Inter-Regular.ttf"}
		}
	case "roboto":
		switch {
		case bold && italic:
			candidates = []string{"Roboto-BoldItalic.ttf", "Roboto-Bold.ttf", "Roboto-Regular.ttf"}
		case bold:
			candidates = []string{"Roboto-Bold.ttf", "Roboto-Regular.ttf"}
		case italic:
			candidates = []string{"Roboto-Italic.ttf", "Roboto-Regular.ttf"}
		default:
			candidates = []string{"Roboto-Regular.ttf"}
		}
	case "montserrat":
		if bold {
			candidates = []string{"Montserrat-Bold.ttf", "Montserrat-Regular.ttf"}
		} else {
			candidates = []string{"Montserrat-Regular.ttf", "Montserrat-Bold.ttf"}
		}
	case "playfair", "playfairdisplay":
		if bold {
			candidates = []string{"PlayfairDisplay-Bold.ttf", "PlayfairDisplay-Regular.ttf"}
		} else {
			candidates = []string{"PlayfairDisplay-Regular.ttf", "PlayfairDisplay-Bold.ttf"}
		}
	case "bebas", "bebasneue":
		candidates = []string{"BebasNeue-Regular.ttf"}
	default:
		candidates = []string{"Inter-Regular.ttf"}
	}

	for _, c := range candidates {
		if p := pick(c); p != "" {
			return p
		}
	}
	// Last-resort fallback across all families
	for _, c := range []string{"Inter-Regular.ttf", "Roboto-Regular.ttf"} {
		if p := pick(c); p != "" {
			return p
		}
	}
	return ""
}

// positionExpr returns FFmpeg x/y expressions for the 9-cell position grid.
// "tw" / "th" are drawtext intrinsics that resolve to text dimensions at
// render time, so the same expression works regardless of text length.
func positionExpr(position string) (string, string) {
	switch position {
	case "top-left":
		return "w*0.05", "h*0.05"
	case "top-center":
		return "(w-tw)/2", "h*0.05"
	case "top-right":
		return "w-tw-w*0.05", "h*0.05"
	case "mid-left":
		return "w*0.05", "(h-th)/2"
	case "mid-center":
		return "(w-tw)/2", "(h-th)/2"
	case "mid-right":
		return "w-tw-w*0.05", "(h-th)/2"
	case "bot-left":
		return "w*0.05", "h-th-h*0.08"
	case "bot-right":
		return "w-tw-w*0.05", "h-th-h*0.08"
	default:
		return "(w-tw)/2", "h-th-h*0.08" // bot-center
	}
}

// buildDrawtextPass assembles a single drawtext= clause. Used for the main
// text and for each shadow pass. xExpr/yExpr are already-resolved position
// expressions (possibly with shadow offsets baked in).
func buildDrawtextPass(safeText, fontFile string, fontSize int, color, xExpr, yExpr string, opts drawtextOptions) string {
	parts := []string{
		fmt.Sprintf("text='%s'", safeText),
		fmt.Sprintf("fontsize=%d", fontSize),
		fmt.Sprintf("fontcolor=%s", color),
		fmt.Sprintf("x=%s", xExpr),
		fmt.Sprintf("y=%s", yExpr),
	}
	if fontFile != "" {
		// drawtext expects a filesystem path — colons (Windows drive letters)
		// must be escaped because ':' is the filter argument separator.
		fontFileEsc := strings.ReplaceAll(fontFile, `:`, `\:`)
		parts = append(parts, fmt.Sprintf("fontfile=%s", fontFileEsc))
	}
	if opts.withBorder {
		parts = append(parts, "borderw=2", "bordercolor=black@0.6")
	}
	if opts.boxColor != "" {
		parts = append(parts,
			"box=1",
			fmt.Sprintf("boxcolor=%s", opts.boxColor),
			"boxborderw=10",
		)
	}
	if opts.fadeIn {
		parts = append(parts, "alpha='if(lt(t,0.4),t/0.4,1)'")
	} else if opts.alphaExpr != "" {
		parts = append(parts, fmt.Sprintf("alpha='%s'", opts.alphaExpr))
	}
	return "drawtext=" + strings.Join(parts, ":")
}

type drawtextOptions struct {
	withBorder bool   // outline around glyphs (only the main pass)
	boxColor   string // background box (only the main pass)
	fadeIn     bool   // animate alpha 0→1 over 0.4s (only the main pass)
	alphaExpr  string // explicit alpha expression (shadow passes use this)
}

// drawtextFilter builds an FFmpeg filter chain (one or more drawtext= clauses
// joined by commas) for a single TextOverlay. Returns "" when the overlay is
// nil or has empty text. Position is mapped onto a 9-cell grid; font/box and
// shadow fields fall back to sensible defaults when blank.
//
// Layering: shadow passes are emitted FIRST (so they paint under the main
// text), then the main text pass with outline/border on top.
func drawtextFilter(overlay *models.TextOverlay, frameW, frameH int) string {
	if overlay == nil || strings.TrimSpace(overlay.Text) == "" {
		return ""
	}

	fontSize := overlay.FontSize
	if fontSize <= 0 {
		// Default to ~5% of frame height — looks good across landscape /
		// portrait without per-aspect tuning.
		fontSize = frameH / 20
		if fontSize < 24 {
			fontSize = 24
		}
	}
	fontColor := overlay.FontColor
	if fontColor == "" {
		fontColor = "white"
	}
	fontFile := resolveFontFile(overlay.FontFamily, overlay.Bold, overlay.Italic)

	xExpr, yExpr := positionExpr(overlay.Position)
	safeText := escapeDrawtext(overlay.Text)

	var passes []string

	// Shadow / glow layers — drawn underneath the main text.
	shadowColor := overlay.ShadowColor
	wantsGlow := overlay.Glow
	if wantsGlow && shadowColor == "" {
		shadowColor = "black@0.7"
	}
	if shadowColor != "" {
		if wantsGlow {
			// Faux glow: 8 low-alpha copies offset around the text at ~3px
			// radius. Cheaper than a real blur and renders crisply.
			glowAlpha := "0.35"
			offsets := [][2]int{{-3, 0}, {3, 0}, {0, -3}, {0, 3}, {-2, -2}, {2, -2}, {-2, 2}, {2, 2}}
			for _, off := range offsets {
				gx := fmt.Sprintf("(%s)+(%d)", xExpr, off[0])
				gy := fmt.Sprintf("(%s)+(%d)", yExpr, off[1])
				passes = append(passes, buildDrawtextPass(
					safeText, fontFile, fontSize, shadowColor, gx, gy,
					drawtextOptions{alphaExpr: glowAlpha},
				))
			}
		} else {
			sx := overlay.ShadowX
			sy := overlay.ShadowY
			if sx == 0 && sy == 0 {
				sx, sy = 2, 2
			}
			gx := fmt.Sprintf("(%s)+(%d)", xExpr, sx)
			gy := fmt.Sprintf("(%s)+(%d)", yExpr, sy)
			alphaExpr := ""
			if overlay.FadeIn {
				// Shadow fades in alongside the main text.
				alphaExpr = "if(lt(t,0.4),t/0.4,1)"
			}
			passes = append(passes, buildDrawtextPass(
				safeText, fontFile, fontSize, shadowColor, gx, gy,
				drawtextOptions{alphaExpr: alphaExpr},
			))
		}
	}

	// Main text pass — drawn on top of any shadow/glow.
	passes = append(passes, buildDrawtextPass(
		safeText, fontFile, fontSize, fontColor, xExpr, yExpr,
		drawtextOptions{
			withBorder: true,
			boxColor:   overlay.BoxColor,
			fadeIn:     overlay.FadeIn,
		},
	))

	return strings.Join(passes, ",")
}

// BuildOverlayPreview renders a short preview clip of `sourcePath` with the
// given overlay burnt in. It's used by the per-clip review UI to show what
// the FFmpeg-rendered text actually looks like before the user saves the
// overlay to the job. The output is written to `outputPath` at the same
// resolution as the source.
//
// `durationSec` is clamped to at most the source's intrinsic duration; when
// the source is a still image the preview is generated as a `durationSec`
// loop at 30fps. Encoded with ultrafast/CRF 28 because pixel-accuracy of the
// drawtext rendering is the goal, not transcode quality.
func BuildOverlayPreview(sourcePath string, overlay *models.TextOverlay, frameW, frameH int, durationSec float64, outputPath string) error {
	if overlay == nil {
		return fmt.Errorf("overlay is nil")
	}
	if durationSec <= 0 {
		durationSec = 2.0
	}

	filter := drawtextFilter(overlay, frameW, frameH)
	if filter == "" {
		return fmt.Errorf("overlay produced no filter (empty text?)")
	}

	ext := strings.ToLower(filepath.Ext(sourcePath))
	isImage := ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp"

	durStr := fmt.Sprintf("%.3f", durationSec)
	var args []string
	if isImage {
		// Still image: loop for the requested duration so the rendered
		// preview is a tiny video (consistent UI behaviour).
		args = []string{
			"-loop", "1", "-i", sourcePath,
			"-t", durStr,
			"-vf", filter,
			"-c:v", "libx264", "-preset", "ultrafast", "-crf", "28",
			"-pix_fmt", "yuv420p", "-r", "30",
			"-an", "-y", outputPath,
		}
	} else {
		args = []string{
			"-i", sourcePath,
			"-t", durStr,
			"-vf", filter,
			"-c:v", "libx264", "-preset", "ultrafast", "-crf", "28",
			"-pix_fmt", "yuv420p",
			"-an", "-y", outputPath,
		}
	}
	return runFFmpeg(args)
}

// ResolveResolution exposes resolveResolution for callers outside the
// pipeline package (e.g. the API's overlay-preview handler).
func ResolveResolution(aspect string) (int, int) {
	w, h, _ := resolveResolution(aspect)
	return w, h
}

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

				// Per-clip text overlay (Instagram-stories-style) is opt-in
				// via the post-Stage-4 review screen. When set, append it
				// to the video filter chain so it gets burnt in during this
				// prep pass — by the time we re-encode for segment sync it
				// is already part of the frame.
				var overlayFilter string
				if review := job.GetClipReview(key); review != nil {
					overlayFilter = drawtextFilter(review.Overlay, width, height)
				}

				if strings.HasSuffix(clipPath, ".jpg") || strings.HasSuffix(clipPath, ".png") {
					frames := int(subClipDuration * float64(profile.FPS))
					vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,zoompan=z='min(zoom+0.0015,1.1)':d=%d:s=%dx%d:fps=%d", width, height, width, height, frames, width, height, profile.FPS)
					if overlayFilter != "" {
						vf = vf + "," + overlayFilter
					}
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
					vf := scaleFilter
					if overlayFilter != "" {
						vf = vf + "," + overlayFilter
					}
					args := []string{
						"-i", clipPath,
						"-t", fmt.Sprintf("%.3f", subClipDuration),
						"-vf", vf,
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
			key := fmt.Sprintf("%d", seg.SegmentID)
			clipPath := job.ClipFiles[key]
			if clipPath == "" {
				continue
			}

			var overlayFilter string
			if review := job.GetClipReview(key); review != nil {
				overlayFilter = drawtextFilter(review.Overlay, width, height)
			}

			if strings.HasSuffix(clipPath, ".jpg") || strings.HasSuffix(clipPath, ".png") {
				imgVideoPath := filepath.Join(segDir, fmt.Sprintf("seg_%02d_imgvid.mp4", seg.SegmentID))
				frames := int(segTarget * float64(profile.FPS))
				vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,zoompan=z='min(zoom+0.0015,1.1)':d=%d:s=%dx%d:fps=%d", width, height, width, height, frames, width, height, profile.FPS)
				if overlayFilter != "" {
					vf = vf + "," + overlayFilter
				}
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
			} else if overlayFilter != "" {
				// Stock clip with an overlay — pre-bake the drawtext into a
				// scaled prep file so the segment-sync pass doesn't have to
				// know about overlays. Without overlay, we keep the existing
				// fast path of feeding the source straight into segment sync.
				prepPath := filepath.Join(segDir, fmt.Sprintf("seg_%02d_clip_prep.mp4", seg.SegmentID))
				vf := scaleFilter + "," + overlayFilter
				args := []string{
					"-i", clipPath,
					"-vf", vf,
					"-c:v", "libx264", "-preset", "fast", "-crf", "23",
					"-pix_fmt", "yuv420p", "-an", "-r", fpsStr,
					"-y", prepPath,
				}
				if err := runFFmpeg(args); err != nil {
					return fmt.Errorf("overlay seg %d: %w", seg.SegmentID, err)
				}
				segmentVideoPath = prepPath
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
