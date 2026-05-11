package pipeline

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"yt-automation-studio/config"
	"yt-automation-studio/models"
)

// RunVisualFetcher executes Stage 4: fetch video clips or generate AI images for each segment.
// Supports sub_visuals for fine-grained visual-to-narration alignment.
func RunVisualFetcher(job *models.JobContext, progress ProgressFunc) error {
	if job.Script == nil {
		return fmt.Errorf("no script available — Stage 2 must complete first")
	}

	payload := job.Payload
	segments := job.Script.Segments
	jobDir := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", job.JobID), "segments")

	if err := os.MkdirAll(jobDir, 0755); err != nil {
		return fmt.Errorf("create segments directory: %w", err)
	}

	// Ensure the ClipReview map exists; we populate it as we generate each
	// visual so the post-Stage-4 review screen has everything it needs.
	if job.ClipReview == nil {
		job.ClipReview = make(map[string]*models.ClipReviewItem)
	}

	if payload.VideoMode == "manual" {
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 4, StageName: "Visual Fetch",
			ProgressPct: 100,
			Message:     fmt.Sprintf("Manual mode — place MP4 clips in: %s", jobDir),
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})
		for _, seg := range segments {
			key := fmt.Sprintf("%d", seg.SegmentID)
			clipPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_clip.mp4", seg.SegmentID))
			job.ClipFiles[key] = clipPath
			job.SetClipReview(key, &models.ClipReviewItem{
				Key:           key,
				SegmentID:     seg.SegmentID,
				SubIndex:      -1,
				FilePath:      clipPath,
				SourceType:    "clip",
				Query:         seg.VisualQuery,
				Description:   seg.VisualCue,
				NarrationText: seg.Text,
			})
		}
		return nil
	}

	// Count total visuals for progress tracking
	totalVisuals := 0
	for _, seg := range segments {
		if len(seg.SubVisuals) > 0 {
			totalVisuals += len(seg.SubVisuals)
		} else {
			totalVisuals++ // legacy fallback: 1 visual per segment
		}
	}

	// Track used queries AND video IDs to prevent duplicate clips in one video.
	// Source-selection settings come from the user's chosen output_quality plus
	// the requested aspect ratio so we ask Pexels for the right shape and
	// resolution.
	profile := ProfileFor(payload.OutputQuality)
	targetW, _, _ := resolveResolution(payload.AspectRatio)
	tracker := newVideoTracker()
	tracker.orientation = pexelsOrientation(payload.AspectRatio)
	tracker.pexelsSize = profile.PexelsSize
	tracker.minClipWidth = profile.MinClipWidth
	tracker.targetWidth = targetW
	log.Printf("🎯 Visual fetch — quality=%s orientation=%s pexels_size=%s min_width=%d target_width=%d",
		profile.Quality, tracker.orientation, tracker.pexelsSize, tracker.minClipWidth, tracker.targetWidth)
	// Stash the tracker on the job so per-clip regenerations triggered from
	// the review UI dedup against the rest of this video's clips.
	job.SetVisualTracker(tracker)
	visualsDone := 0

	for _, seg := range segments {
		if len(seg.SubVisuals) > 0 {
			// ---- New: multi-visual per segment ----
			for j, sv := range seg.SubVisuals {
				visualsDone++
				pct := int(float64(visualsDone) / float64(totalVisuals) * 90)
				progress(models.ProgressEvent{
					JobID: job.JobID, Stage: 4, StageName: "Visual Fetch",
					ProgressPct: pct,
					Message:     fmt.Sprintf("Segment %d, visual %d/%d: %s", seg.SegmentID, j+1, len(seg.SubVisuals), sv.Query),
					Timestamp:   time.Now().UTC().Format(time.RFC3339),
				})

				key := fmt.Sprintf("%d_%d", seg.SegmentID, j)

				// FIX #3: Override the AI's type based on user's video_style.
				// The AI might label everything as "clip" even when user chose "ai_images".
				effectiveType := sv.Type
				switch payload.VideoStyle {
				case "stock":
					effectiveType = "clip"
				case "ai_images":
					effectiveType = "image"
					// "mixed" — respect the AI's choice
				}

				outPath, srcType, err := fetchOneVisual(jobDir, seg.SegmentID, j, effectiveType, sv.Query, sv.Description, payload, tracker)
				if err != nil {
					return fmt.Errorf("visual fetch seg %d sub %d: %w", seg.SegmentID, j, err)
				}
				job.ClipFiles[key] = outPath
				job.SetClipReview(key, &models.ClipReviewItem{
					Key:           key,
					SegmentID:     seg.SegmentID,
					SubIndex:      j,
					FilePath:      outPath,
					SourceType:    srcType,
					Query:         sv.Query,
					Description:   sv.Description,
					NarrationText: seg.Text,
				})
			}
		} else {
			// ---- Legacy fallback: 1 visual per segment ----
			visualsDone++
			pct := int(float64(visualsDone) / float64(totalVisuals) * 90)
			progress(models.ProgressEvent{
				JobID: job.JobID, Stage: 4, StageName: "Visual Fetch",
				ProgressPct: pct,
				Message:     fmt.Sprintf("Fetching visual for segment %d: %s", seg.SegmentID, seg.VisualQuery),
				Timestamp:   time.Now().UTC().Format(time.RFC3339),
			})

			key := fmt.Sprintf("%d", seg.SegmentID)
			clipPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_clip.mp4", seg.SegmentID))

			aspect := payload.AspectRatio
			srcType := "clip"
			switch payload.VideoStyle {
			case "ai_images":
				imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_img.jpg", seg.SegmentID))
				if err := generateAIImage(seg.VisualCue, imgPath, payload.ScriptTone, aspect); err != nil {
					if err2 := fetchPexelsClipTracked(seg.VisualQuery, clipPath, tracker); err2 != nil {
						return fmt.Errorf("visual fetch for segment %d: AI failed (%v), stock failed (%v)", seg.SegmentID, err, err2)
					}
				} else {
					clipPath = imgPath
					srcType = "image"
				}
			case "mixed":
				if seg.SegmentID%2 == 1 {
					if err := fetchPexelsClipTracked(seg.VisualQuery, clipPath, tracker); err != nil {
						imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_img.jpg", seg.SegmentID))
						if err2 := generateAIImage(seg.VisualCue, imgPath, payload.ScriptTone, aspect); err2 == nil {
							clipPath = imgPath
							srcType = "image"
						}
					}
				} else {
					imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_img.jpg", seg.SegmentID))
					if err := generateAIImage(seg.VisualCue, imgPath, payload.ScriptTone, aspect); err != nil {
						_ = fetchPexelsClipTracked(seg.VisualQuery, clipPath, tracker)
					} else {
						clipPath = imgPath
						srcType = "image"
					}
				}
			default: // "stock"
				if err := fetchPexelsClipTracked(seg.VisualQuery, clipPath, tracker); err != nil {
					return fmt.Errorf("visual fetch for segment %d: %w", seg.SegmentID, err)
				}
			}

			job.ClipFiles[key] = clipPath
			job.SetClipReview(key, &models.ClipReviewItem{
				Key:           key,
				SegmentID:     seg.SegmentID,
				SubIndex:      -1,
				FilePath:      clipPath,
				SourceType:    srcType,
				Query:         seg.VisualQuery,
				Description:   seg.VisualCue,
				NarrationText: seg.Text,
			})
		}
	}

	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 4, StageName: "Visual Fetch",
		ProgressPct: 100,
		Message:     fmt.Sprintf("All visuals fetched: %d assets downloaded", visualsDone),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	})

	return nil
}

// fetchOneVisual produces a single sub-visual file (image or clip) at a
// canonical path, with the same fallback chain the original loop used:
// requested type first, then the other type if it fails. Returns the
// resulting file path and the actual source type written ("clip" / "image").
//
// This helper is reused by RegenerateOneVisual so per-clip regeneration
// from the review UI gets the same dedup + fallback behavior as the
// initial fetch.
func fetchOneVisual(
	jobDir string, segID, subIdx int,
	requestedType, query, description string,
	payload *models.InputPayload,
	tracker *usedVideoTracker,
) (string, string, error) {
	aspect := payload.AspectRatio
	imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_sub_%02d.jpg", segID, subIdx))
	clipPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_sub_%02d.mp4", segID, subIdx))

	if requestedType == "image" {
		if err := generateAIImage(description, imgPath, payload.ScriptTone, aspect); err != nil {
			log.Printf("⚠️ AI image failed for seg %d sub %d (%v), falling back to stock", segID, subIdx, err)
			if err2 := fetchPexelsClipTracked(query, clipPath, tracker); err2 != nil {
				return "", "", fmt.Errorf("AI failed (%v), stock failed (%v)", err, err2)
			}
			_ = os.Remove(imgPath) // remove the leftover failed-image placeholder
			return clipPath, "clip", nil
		}
		_ = os.Remove(clipPath) // make sure no stale clip lingers
		return imgPath, "image", nil
	}

	// "clip" (or empty) — try stock first
	if err := fetchPexelsClipTracked(query, clipPath, tracker); err != nil {
		log.Printf("⚠️ Stock clip failed for seg %d sub %d (%v), trying AI image", segID, subIdx, err)
		if err2 := generateAIImage(description, imgPath, payload.ScriptTone, aspect); err2 != nil {
			return "", "", fmt.Errorf("stock failed (%v), AI failed (%v)", err, err2)
		}
		_ = os.Remove(clipPath)
		return imgPath, "image", nil
	}
	_ = os.Remove(imgPath)
	return clipPath, "clip", nil
}

// RegenerateOneVisual regenerates a single visual identified by key. Used by
// the review-screen API to refresh a clip the user didn't like. Optionally
// overrides the search query and/or source type ("clip" | "image"); empty
// strings keep the existing values.
//
// On success the JobContext's ClipFiles, ClipReview, and on-disk file are
// all updated. The per-job dedup tracker (if present) is reused so the
// regenerated clip won't duplicate any other visual in the same video.
func RegenerateOneVisual(job *models.JobContext, key, newQuery, newSourceType string) (*models.ClipReviewItem, error) {
	item := job.GetClipReview(key)
	if item == nil {
		return nil, fmt.Errorf("no review item for key %q", key)
	}
	if job.Payload.VideoMode == "manual" {
		return nil, fmt.Errorf("regeneration not supported in manual video mode")
	}

	if newQuery != "" {
		item.Query = newQuery
	}
	requestedType := item.SourceType
	if newSourceType == "clip" || newSourceType == "image" {
		requestedType = newSourceType
	}

	jobDir := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", job.JobID), "segments")
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		return nil, fmt.Errorf("ensure segments dir: %w", err)
	}

	// Recover (or rebuild) the per-job dedup tracker. If we're resuming a
	// long-paused job in a fresh process, the tracker may be nil — rebuild
	// a minimal one from the current settings so regeneration still works.
	tracker, _ := job.GetVisualTracker().(*usedVideoTracker)
	if tracker == nil {
		profile := ProfileFor(job.Payload.OutputQuality)
		targetW, _, _ := resolveResolution(job.Payload.AspectRatio)
		tracker = newVideoTracker()
		tracker.orientation = pexelsOrientation(job.Payload.AspectRatio)
		tracker.pexelsSize = profile.PexelsSize
		tracker.minClipWidth = profile.MinClipWidth
		tracker.targetWidth = targetW
		job.SetVisualTracker(tracker)
	}

	// Use the same canonical filename slot as the original. fetchOneVisual
	// will overwrite it (and remove the alternate-extension leftover).
	subIdx := item.SubIndex
	if subIdx < 0 {
		// Legacy single-visual segments don't have a sub index. We still
		// route through fetchOneVisual using subIdx=0 so the file naming
		// stays consistent — but legacy segments stored at seg_NN_clip.mp4
		// need their own handling. Fall back to direct stock/AI calls.
		clipPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_clip.mp4", item.SegmentID))
		imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_img.jpg", item.SegmentID))
		if requestedType == "image" {
			if err := generateAIImage(item.Description, imgPath, job.Payload.ScriptTone, job.Payload.AspectRatio); err != nil {
				return nil, fmt.Errorf("regenerate image: %w", err)
			}
			_ = os.Remove(clipPath)
			item.FilePath = imgPath
			item.SourceType = "image"
		} else {
			if err := fetchPexelsClipTracked(item.Query, clipPath, tracker); err != nil {
				return nil, fmt.Errorf("regenerate clip: %w", err)
			}
			_ = os.Remove(imgPath)
			item.FilePath = clipPath
			item.SourceType = "clip"
		}
	} else {
		path, srcType, err := fetchOneVisual(jobDir, item.SegmentID, subIdx, requestedType, item.Query, item.Description, job.Payload, tracker)
		if err != nil {
			return nil, err
		}
		item.FilePath = path
		item.SourceType = srcType
	}

	item.RegenCount++
	item.Approved = false // regenerating implicitly un-approves; user must re-confirm
	job.ClipFiles[key] = item.FilePath
	job.SetClipReview(key, item)
	return item, nil
}

// usedVideoTracker tracks used Pexels video IDs and download URLs across a
// single job to prevent the same asset from appearing multiple times in one
// video. It also carries the per-job source-selection settings (orientation,
// minimum file width, Pexels size filter) so the fetch chain doesn't need to
// thread them through every function signature.
type usedVideoTracker struct {
	usedQueries  map[string]bool
	usedVideoIDs map[int]bool
	usedURLs     map[string]bool

	// Source-selection inputs derived from the InputPayload at job start.
	// Zero values fall back to the prior default behavior, so anything
	// constructing a tracker without setting these still works.
	orientation  string // "landscape" | "portrait" | "square"
	pexelsSize   string // "" / "medium" / "large" — passed as &size= to Pexels
	minClipWidth int    // minimum acceptable VideoFile.Width
	targetWidth  int    // output frame width (1920/1080) — used to pick the smallest file ≥ this
}

func newVideoTracker() *usedVideoTracker {
	return &usedVideoTracker{
		usedQueries:  make(map[string]bool),
		usedVideoIDs: make(map[int]bool),
		usedURLs:     make(map[string]bool),
	}
}

// pexelsOrientation maps the InputPayload aspect ratio to Pexels' orientation
// query parameter. Pexels supports "landscape" / "portrait" / "square".
func pexelsOrientation(aspect string) string {
	switch aspect {
	case "portrait":
		return "portrait"
	case "square":
		return "square"
	default:
		return "landscape"
	}
}

// fetchPexelsClipDedup fetches a clip while tracking used queries AND video IDs to avoid duplicates.
func fetchPexelsClipDedup(query, outputPath string, used map[string]bool) error {
	// Legacy wrapper for backward compatibility — uses global-ish query map only.
	// The stronger dedup uses usedVideoTracker via fetchPexelsClipTracked.
	originalQuery := query
	if used[query] {
		words := strings.Fields(query)
		if len(words) > 1 {
			query = strings.Join(words[1:], " ") + " cinematic"
		} else {
			query = query + " cinematic"
		}
	}
	used[originalQuery] = true

	if err := fetchPexelsClipWithRetry(query, outputPath, nil); err != nil {
		if query != originalQuery {
			return fetchPexelsClipWithRetry(originalQuery, outputPath, nil)
		}
		return err
	}
	return nil
}

// fetchPexelsClipTracked fetches a clip with full video-ID dedup and randomized selection.
func fetchPexelsClipTracked(query, outputPath string, tracker *usedVideoTracker) error {
	originalQuery := query
	if tracker.usedQueries[query] {
		words := strings.Fields(query)
		if len(words) > 1 {
			query = strings.Join(words[1:], " ") + " cinematic"
		} else {
			query = query + " cinematic"
		}
	}
	tracker.usedQueries[originalQuery] = true

	if err := fetchPexelsClipWithRetry(query, outputPath, tracker); err != nil {
		if query != originalQuery {
			return fetchPexelsClipWithRetry(originalQuery, outputPath, tracker)
		}
		return err
	}
	return nil
}

// fetchPexelsClipWithRetry tries the original query, then a broadened query
func fetchPexelsClipWithRetry(query, outputPath string, tracker *usedVideoTracker) error {
	if err := fetchPexelsClip(query, outputPath, tracker); err != nil {
		words := strings.Fields(query)
		if len(words) > 3 {
			broadQuery := strings.Join(words[:3], " ")
			return fetchPexelsClip(broadQuery, outputPath, tracker)
		}
		return err
	}
	return nil
}

// fetchPexelsClip downloads a video clip from Pexels matching the query.
// When tracker is non-nil, skips videos whose ID was already used (prevents same asset in one video).
// Randomizes page offset to avoid always getting the same top results.
func fetchPexelsClip(query, outputPath string, tracker *usedVideoTracker) error {
	apiKey := config.App.PexelsAPIKey
	if apiKey == "" {
		return fmt.Errorf("PEXELS_API_KEY not configured")
	}

	req, err := http.NewRequest("GET", "https://api.pexels.com/videos/search", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	q := req.URL.Query()
	q.Add("query", query)
	q.Add("per_page", "15")
	// Orientation matches the user's chosen aspect ratio so portrait Shorts
	// don't get sourced from landscape footage (which would then need to be
	// zoom-cropped, losing detail).
	orientation := "landscape"
	if tracker != nil && tracker.orientation != "" {
		orientation = tracker.orientation
	}
	q.Add("orientation", orientation)
	// Bias toward higher-res sources when the quality profile asks for it.
	if tracker != nil && tracker.pexelsSize != "" {
		q.Add("size", tracker.pexelsSize)
	}
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Authorization", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Pexels API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Pexels API returned %d: %s", resp.StatusCode, string(body))
	}

	var pexelsResp struct {
		Videos []struct {
			ID         int `json:"id"`
			VideoFiles []struct {
				Link    string `json:"link"`
				Quality string `json:"quality"`
				Width   int    `json:"width"`
				Height  int    `json:"height"`
			} `json:"video_files"`
		} `json:"videos"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&pexelsResp); err != nil {
		return fmt.Errorf("parse Pexels response: %w", err)
	}

	if len(pexelsResp.Videos) == 0 {
		return fmt.Errorf("no videos found for query: %s", query)
	}

	minWidth := 1280
	targetWidth := 1920
	if tracker != nil {
		if tracker.minClipWidth > 0 {
			minWidth = tracker.minClipWidth
		}
		if tracker.targetWidth > 0 {
			targetWidth = tracker.targetWidth
		}
	}

	// Iterate videos in the order Pexels returned them (relevance-sorted).
	// For each candidate video, pick the best file using pickBestVideoFile,
	// which prefers the smallest file >= targetWidth (to avoid both upscaling
	// from 720p and wasting bandwidth on 4K when we only render to 1080p).
	var downloadURL string
	for _, video := range pexelsResp.Videos {
		if tracker != nil && tracker.usedVideoIDs[video.ID] {
			continue
		}
		files := make([]pexelsFile, 0, len(video.VideoFiles))
		for _, f := range video.VideoFiles {
			files = append(files, pexelsFile{Link: f.Link, Quality: f.Quality, Width: f.Width, Height: f.Height})
		}
		picked := pickBestVideoFile(files, targetWidth, minWidth, tracker)
		if picked == "" {
			continue
		}
		downloadURL = picked
		if tracker != nil {
			tracker.usedVideoIDs[video.ID] = true
			tracker.usedURLs[picked] = true
		}
		break
	}

	// Fallback 1: relax the minimum-width gate (some niches only have 720p
	// content). Still prefer larger files; still skip already-used videos.
	if downloadURL == "" {
		for _, video := range pexelsResp.Videos {
			if tracker != nil && tracker.usedVideoIDs[video.ID] {
				continue
			}
			files := make([]pexelsFile, 0, len(video.VideoFiles))
			for _, f := range video.VideoFiles {
				files = append(files, pexelsFile{Link: f.Link, Quality: f.Quality, Width: f.Width, Height: f.Height})
			}
			picked := pickBestVideoFile(files, targetWidth, 0, tracker)
			if picked == "" {
				continue
			}
			downloadURL = picked
			if tracker != nil {
				tracker.usedVideoIDs[video.ID] = true
				tracker.usedURLs[picked] = true
			}
			break
		}
	}

	// Last resort: ALL videos are already used in this job — reuse one rather
	// than fail. Still pick the best available file from it.
	if downloadURL == "" {
		for _, video := range pexelsResp.Videos {
			files := make([]pexelsFile, 0, len(video.VideoFiles))
			for _, f := range video.VideoFiles {
				files = append(files, pexelsFile{Link: f.Link, Quality: f.Quality, Width: f.Width, Height: f.Height})
			}
			if picked := pickBestVideoFile(files, targetWidth, 0, nil); picked != "" {
				downloadURL = picked
				break
			}
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("no downloadable video files found for: %s", query)
	}

	return downloadFile(downloadURL, outputPath)
}

// pexelsFile is a copy of the relevant subset of Pexels' video_file payload,
// kept separate so pickBestVideoFile can be unit-tested without depending on
// the full search-response struct.
type pexelsFile struct {
	Link    string
	Quality string
	Width   int
	Height  int
}

// pickBestVideoFile chooses the best file from a Pexels video's available
// renditions for our target output width. Strategy:
//
//  1. Skip files marked "hls" / streaming variants — Pexels sometimes mixes
//     HLS playlists into video_files and we want a single MP4 to download.
//  2. Skip files with zero width (broken metadata).
//  3. Skip files already used in this job (by URL) when a tracker is given.
//  4. Sort the remaining candidates by width ascending.
//  5. Pick the smallest file with width >= targetWidth (avoids upscaling without
//     wasting bandwidth on 4K when we render to 1080p).
//  6. If no file meets target, fall back to the largest available — better to
//     upscale a little than to fail.
//
// minWidth is a hard floor: when set (>0), files below it are excluded entirely.
func pickBestVideoFile(files []pexelsFile, targetWidth, minWidth int, tracker *usedVideoTracker) string {
	candidates := make([]pexelsFile, 0, len(files))
	for _, f := range files {
		if f.Width <= 0 {
			continue
		}
		if f.Quality == "hls" {
			continue
		}
		if minWidth > 0 && f.Width < minWidth {
			continue
		}
		if tracker != nil && tracker.usedURLs[f.Link] {
			continue
		}
		candidates = append(candidates, f)
	}
	if len(candidates) == 0 {
		return ""
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Width < candidates[j].Width
	})

	// Smallest file >= target width (no upscale, no needless 4K download).
	for _, f := range candidates {
		if f.Width >= targetWidth {
			return f.Link
		}
	}
	// Nothing meets target — return the largest available.
	return candidates[len(candidates)-1].Link
}

// buildAIPrompt enhances the prompt for Flux.1 Schnell based on the selected tone.
func buildAIPrompt(visualCue, tone string) string {
	style := "cinematic lighting, high contrast, epic atmosphere, photorealistic"
	switch tone {
	case "suspenseful":
		style = "dark moody lighting, shadows, thriller atmosphere, photorealistic"
	case "educational":
		style = "clean bright lighting, documentary style, informative, photorealistic"
	case "conversational":
		style = "natural soft lighting, warm tones, approachable, photorealistic"
	case "motivational":
		style = "golden hour lighting, uplifting, vibrant colors, photorealistic"
	case "humorous":
		style = "bright vivid colors, playful composition, lighthearted, photorealistic"
	}
	return fmt.Sprintf("%s, %s, no text, no watermark, 4K quality, wide angle shot", visualCue, style)
}

// generateAIImage handles generation with a three-tier fallback chain:
//
//	1. Together AI               — primary, paid FLUX.1-schnell-Free quality
//	2. Cloudflare Workers AI     — free tier (10k neurons/day ≈ 50–100 imgs)
//	3. Pollinations.ai           — keyless, last-resort public FLUX endpoint
//
// `aspect` is one of "landscape", "portrait", or "square" and selects the
// generated image dimensions to match the final video orientation. Cloudflare
// currently emits ~1024×1024 only, so aspect is honoured best-effort there
// and downstream FFmpeg crops/resizes as needed.
//
// HuggingFace's legacy Inference API for FLUX was removed in 2025/2026, so
// it's no longer in the chain. Providers without configured credentials are
// skipped silently rather than counted as failures.
func generateAIImage(prompt, outputPath, tone, aspect string) error {
	// Tier 1 — Together AI (skip if not configured).
	if config.App.TogetherAPIKey != "" {
		if err := generateTogetherImage(prompt, outputPath, tone, aspect); err == nil {
			return nil
		} else {
			log.Printf("⚠️ Together AI failed: %v — falling back to Cloudflare Workers AI", err)
		}
	}

	// Tier 2 — Cloudflare Workers AI (skip if not configured).
	if config.App.CloudflareAccountID != "" && config.App.CloudflareAPIToken != "" {
		if err := generateCloudflareImage(prompt, outputPath, tone, aspect); err == nil {
			return nil
		} else {
			log.Printf("⚠️ Cloudflare Workers AI failed: %v — falling back to Pollinations", err)
		}
	}

	// Tier 3 — Pollinations.ai (always available, no key required).
	return generatePollinationsImage(prompt, outputPath, tone, aspect)
}

// generateTogetherImage uses Together AI's free Flux.1 Schnell endpoint
func generateTogetherImage(prompt, outputPath, tone, aspect string) error {
	apiKey := config.App.TogetherAPIKey
	if apiKey == "" {
		return fmt.Errorf("TOGETHER_API_KEY not configured")
	}

	// Choose generation resolution from aspect ratio — matches the final
	// video output so we don't waste pixels on letterboxing/cropping.
	var width, height int
	switch aspect {
	case "portrait":
		width, height = 1024, 1792
	case "square":
		width, height = 1280, 1280
	default: // landscape
		width, height = 1792, 1024
	}

	enhancedPrompt := buildAIPrompt(prompt, tone)

	reqBody := map[string]interface{}{
		"model":  "black-forest-labs/FLUX.1-schnell-Free",
		"prompt": enhancedPrompt,
		"width":  width,
		"height": height,
		"steps":  4,
		"n":      1,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.together.xyz/v1/images/generations", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Together API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Together API returned %d: %s", resp.StatusCode, string(body))
	}

	var imgResp struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&imgResp); err != nil {
		return fmt.Errorf("parse Together response: %w", err)
	}

	if len(imgResp.Data) == 0 {
		return fmt.Errorf("no image generated by Together AI")
	}

	return downloadFile(imgResp.Data[0].URL, outputPath)
}

// generateCloudflareImage uses Cloudflare Workers AI's free FLUX.1-schnell
// endpoint as the middle tier between paid Together AI and keyless
// Pollinations. Workers AI gives 10,000 neurons/day on the free plan and
// FLUX.1-schnell consumes ~100–200 neurons per image, so the budget
// comfortably covers ~50–100 images/day with no credit card required.
//
// API quirk: flux-1-schnell currently outputs at ~1024×1024 only — `aspect`
// is captured for forward compatibility (Cloudflare has signalled
// width/height support is coming) but ignored for now. Downstream FFmpeg
// handles the resize/crop to the final video aspect.
//
// Response shape is the Cloudflare wrapper:
//
//	{ "result": { "image": "<base64 PNG>" }, "success": bool, "errors": [...] }
func generateCloudflareImage(prompt, outputPath, tone, aspect string) error {
	accountID := config.App.CloudflareAccountID
	apiToken := config.App.CloudflareAPIToken
	if accountID == "" || apiToken == "" {
		return fmt.Errorf("CLOUDFLARE_ACCOUNT_ID or CLOUDFLARE_API_TOKEN not configured")
	}
	_ = aspect // see doc comment — reserved for upstream width/height support

	enhancedPrompt := buildAIPrompt(prompt, tone)
	cfURL := fmt.Sprintf(
		"https://api.cloudflare.com/client/v4/accounts/%s/ai/run/@cf/black-forest-labs/flux-1-schnell",
		accountID,
	)

	reqBody := map[string]interface{}{
		"prompt": enhancedPrompt,
		"steps":  4, // 4 hits the sweet spot for FLUX.1-schnell (max 8 on free tier)
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", cfURL, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiToken)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Cloudflare AI request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Cloudflare AI returned %d: %s", resp.StatusCode, string(body))
	}

	var cfResp struct {
		Result struct {
			Image string `json:"image"`
		} `json:"result"`
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return fmt.Errorf("parse Cloudflare response: %w", err)
	}
	if !cfResp.Success {
		msg := "unknown error"
		if len(cfResp.Errors) > 0 {
			msg = cfResp.Errors[0].Message
		}
		return fmt.Errorf("Cloudflare AI error: %s", msg)
	}
	if cfResp.Result.Image == "" {
		return fmt.Errorf("Cloudflare returned empty image payload")
	}

	imgBytes, err := base64.StdEncoding.DecodeString(cfResp.Result.Image)
	if err != nil {
		return fmt.Errorf("decode Cloudflare image: %w", err)
	}
	if err := os.WriteFile(outputPath, imgBytes, 0644); err != nil {
		return fmt.Errorf("save Cloudflare image: %w", err)
	}

	if info, err := os.Stat(outputPath); err == nil && info.Size() < 5*1024 {
		return fmt.Errorf("Cloudflare returned suspiciously small image (%d bytes)", info.Size())
	}
	return nil
}

// generatePollinationsImage uses Pollinations.ai's free public FLUX endpoint
// as the AI image fallback. The API is keyless: a simple GET request to
// https://image.pollinations.ai/prompt/<encoded prompt>?width=W&height=H&model=flux
// streams back JPEG bytes directly. We pass nologo=true so the result is
// clean for monetised YouTube content, and use the same buildAIPrompt() the
// Together AI path uses so the artistic style stays consistent across
// providers.
//
// Retries handle the occasional cold-start / 502 the public endpoint
// produces under load. A small sanity check rejects suspiciously tiny
// payloads (placeholder images returned when the upstream service is
// degraded).
func generatePollinationsImage(prompt, outputPath, tone, aspect string) error {
	width, height := pollinationsDimensionsFor(aspect)
	enhancedPrompt := buildAIPrompt(prompt, tone)

	// Pollinations encodes the prompt in the path — must escape it.
	pollURL := fmt.Sprintf(
		"https://image.pollinations.ai/prompt/%s?width=%d&height=%d&model=flux&nologo=true&enhance=false",
		url.PathEscape(enhancedPrompt), width, height,
	)

	client := &http.Client{Timeout: 90 * time.Second}
	const retries = 3

	for attempt := 1; attempt <= retries; attempt++ {
		req, err := http.NewRequest("GET", pollURL, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("User-Agent", "VidForge/1.0")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("⚠️ Pollinations request failed (attempt %d/%d): %v", attempt, retries, err)
			time.Sleep(time.Duration(5*attempt) * time.Second)
			continue
		}

		if resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			// Cold start / overloaded upstream — back off and retry.
			resp.Body.Close()
			wait := time.Duration(10*attempt) * time.Second
			log.Printf("⚠️ Pollinations %d (attempt %d/%d), waiting %v", resp.StatusCode, attempt, retries, wait)
			time.Sleep(wait)
			continue
		}

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("Pollinations API returned %d: %s", resp.StatusCode, string(body))
		}

		out, err := os.Create(outputPath)
		if err != nil {
			resp.Body.Close()
			return fmt.Errorf("create output file: %w", err)
		}
		_, copyErr := io.Copy(out, resp.Body)
		out.Close()
		resp.Body.Close()
		if copyErr != nil {
			return fmt.Errorf("save Pollinations image: %w", copyErr)
		}

		// Sanity check: anything under ~5 KB is almost certainly a placeholder
		// or HTML error page that slipped through with status 200.
		if info, err := os.Stat(outputPath); err == nil && info.Size() < 5*1024 {
			return fmt.Errorf("Pollinations returned suspiciously small image (%d bytes)", info.Size())
		}
		return nil
	}

	return fmt.Errorf("Pollinations failed after %d retries", retries)
}

// pollinationsDimensionsFor returns image dimensions matching the requested
// aspect ratio. Pollinations is more forgiving than Together AI about exact
// pixel counts but we keep the same proportions so swapping providers
// doesn't change perceived framing.
func pollinationsDimensionsFor(aspect string) (int, int) {
	switch aspect {
	case "portrait":
		return 1024, 1792
	case "square":
		return 1280, 1280
	default: // landscape
		return 1792, 1024
	}
}

// downloadFile downloads a file from URL to local path
func downloadFile(url, outputPath string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	return err
}
