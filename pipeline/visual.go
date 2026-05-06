package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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

	if payload.VideoMode == "manual" {
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 4, StageName: "Visual Fetch",
			ProgressPct: 100,
			Message:     fmt.Sprintf("Manual mode — place MP4 clips in: %s", jobDir),
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})
		for _, seg := range segments {
			clipPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_clip.mp4", seg.SegmentID))
			job.ClipFiles[fmt.Sprintf("%d", seg.SegmentID)] = clipPath
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

				aspect := payload.AspectRatio
				if effectiveType == "image" {
					imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_sub_%02d.jpg", seg.SegmentID, j))
					if err := generateAIImage(sv.Description, imgPath, payload.ScriptTone, aspect); err != nil {
						log.Printf("⚠️ AI image failed for seg %d sub %d (%v), falling back to stock", seg.SegmentID, j, err)
						clipPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_sub_%02d.mp4", seg.SegmentID, j))
						if err2 := fetchPexelsClipTracked(sv.Query, clipPath, tracker); err2 != nil {
							return fmt.Errorf("visual fetch seg %d sub %d: AI failed (%v), stock failed (%v)", seg.SegmentID, j, err, err2)
						}
						job.ClipFiles[key] = clipPath
					} else {
						job.ClipFiles[key] = imgPath
					}
				} else {
					// Stock clip
					clipPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_sub_%02d.mp4", seg.SegmentID, j))
					if err := fetchPexelsClipTracked(sv.Query, clipPath, tracker); err != nil {
						log.Printf("⚠️ Stock clip failed for seg %d sub %d (%v), trying AI image", seg.SegmentID, j, err)
						imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_sub_%02d.jpg", seg.SegmentID, j))
						if err2 := generateAIImage(sv.Description, imgPath, payload.ScriptTone, aspect); err2 != nil {
							return fmt.Errorf("visual fetch seg %d sub %d: stock failed (%v), AI failed (%v)", seg.SegmentID, j, err, err2)
						}
						job.ClipFiles[key] = imgPath
					} else {
						job.ClipFiles[key] = clipPath
					}
				}
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

			clipPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_clip.mp4", seg.SegmentID))
			key := fmt.Sprintf("%d", seg.SegmentID)

			aspect := payload.AspectRatio
			switch payload.VideoStyle {
			case "ai_images":
				imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_img.jpg", seg.SegmentID))
				if err := generateAIImage(seg.VisualCue, imgPath, payload.ScriptTone, aspect); err != nil {
					if err2 := fetchPexelsClipTracked(seg.VisualQuery, clipPath, tracker); err2 != nil {
						return fmt.Errorf("visual fetch for segment %d: AI failed (%v), stock failed (%v)", seg.SegmentID, err, err2)
					}
				} else {
					clipPath = imgPath
				}
			case "mixed":
				if seg.SegmentID%2 == 1 {
					if err := fetchPexelsClipTracked(seg.VisualQuery, clipPath, tracker); err != nil {
						imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_img.jpg", seg.SegmentID))
						if err2 := generateAIImage(seg.VisualCue, imgPath, payload.ScriptTone, aspect); err2 == nil {
							clipPath = imgPath
						}
					}
				} else {
					imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_img.jpg", seg.SegmentID))
					if err := generateAIImage(seg.VisualCue, imgPath, payload.ScriptTone, aspect); err != nil {
						_ = fetchPexelsClipTracked(seg.VisualQuery, clipPath, tracker)
					} else {
						clipPath = imgPath
					}
				}
			default: // "stock"
				if err := fetchPexelsClipTracked(seg.VisualQuery, clipPath, tracker); err != nil {
					return fmt.Errorf("visual fetch for segment %d: %w", seg.SegmentID, err)
				}
			}

			job.ClipFiles[key] = clipPath
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

// generateAIImage handles generation with fallback (Together AI -> HuggingFace).
// `aspect` is one of "landscape", "portrait", or "square" and selects the
// generated image dimensions to match the final video orientation.
func generateAIImage(prompt, outputPath, tone, aspect string) error {
	err := generateTogetherImage(prompt, outputPath, tone, aspect)
	if err != nil {
		log.Printf("⚠️ Together AI failed: %v — falling back to HuggingFace", err)
		return generateHFImage(prompt, outputPath, tone)
	}
	return nil
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

// generateHFImage uses Hugging Face's free Inference API as a fallback
func generateHFImage(prompt, outputPath, tone string) error {
	apiKey := config.App.HFAPIKey
	if apiKey == "" {
		return fmt.Errorf("HF_API_KEY not configured")
	}

	enhancedPrompt := buildAIPrompt(prompt, tone)
	hfURL := "https://api-inference.huggingface.co/models/black-forest-labs/FLUX.1-schnell"

	reqBody := map[string]interface{}{
		"inputs": enhancedPrompt,
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	retries := 3

	for attempt := 1; attempt <= retries; attempt++ {
		req, err := http.NewRequest("POST", hfURL, bytes.NewReader(jsonBody))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("⚠️ HF request failed attempt %d: %v", attempt, err)
			time.Sleep(10 * time.Second)
			continue
		}
		
		if resp.StatusCode == 503 {
			// Model is loading
			resp.Body.Close()
			wait := time.Duration(20*attempt) * time.Second
			log.Printf("⚠️ HF model loading, waiting %v (attempt %d/%d)", wait, attempt, retries)
			time.Sleep(wait)
			continue
		}
		
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("HF API returned %d: %s", resp.StatusCode, string(body))
		}

		// HF returns raw image bytes directly
		outFile, err := os.Create(outputPath)
		if err != nil {
			resp.Body.Close()
			return fmt.Errorf("create file: %w", err)
		}
		
		_, err = io.Copy(outFile, resp.Body)
		outFile.Close()
		resp.Body.Close()
		
		if err != nil {
			return fmt.Errorf("save HF image: %w", err)
		}
		return nil
	}

	return fmt.Errorf("HuggingFace failed after %d retries", retries)
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
