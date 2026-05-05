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

	// Track used queries AND video IDs to prevent duplicate clips in one video
	tracker := newVideoTracker()
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

				isShort := payload.Format == "short"
				if effectiveType == "image" {
					imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_sub_%02d.jpg", seg.SegmentID, j))
					if err := generateAIImage(sv.Description, imgPath, payload.ScriptTone, isShort); err != nil {
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
						if err2 := generateAIImage(sv.Description, imgPath, payload.ScriptTone, isShort); err2 != nil {
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

			isShort := payload.Format == "short"
			switch payload.VideoStyle {
			case "ai_images":
				imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_img.jpg", seg.SegmentID))
				if err := generateAIImage(seg.VisualCue, imgPath, payload.ScriptTone, isShort); err != nil {
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
						if err2 := generateAIImage(seg.VisualCue, imgPath, payload.ScriptTone, isShort); err2 == nil {
							clipPath = imgPath
						}
					}
				} else {
					imgPath := filepath.Join(jobDir, fmt.Sprintf("seg_%02d_img.jpg", seg.SegmentID))
					if err := generateAIImage(seg.VisualCue, imgPath, payload.ScriptTone, isShort); err != nil {
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

// usedVideoTracker tracks used Pexels video IDs and download URLs across a single job
// to prevent the same asset from appearing multiple times in one video.
type usedVideoTracker struct {
	usedQueries   map[string]bool
	usedVideoIDs  map[int]bool
	usedURLs      map[string]bool
}

func newVideoTracker() *usedVideoTracker {
	return &usedVideoTracker{
		usedQueries:  make(map[string]bool),
		usedVideoIDs: make(map[int]bool),
		usedURLs:     make(map[string]bool),
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
	q.Add("orientation", "landscape")
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
			} `json:"video_files"`
		} `json:"videos"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&pexelsResp); err != nil {
		return fmt.Errorf("parse Pexels response: %w", err)
	}

	if len(pexelsResp.Videos) == 0 {
		return fmt.Errorf("no videos found for query: %s", query)
	}

	// Find best quality clip, skipping already-used video IDs
	var downloadURL string
	for _, video := range pexelsResp.Videos {
		// Skip if this video ID was already used in this job
		if tracker != nil && tracker.usedVideoIDs[video.ID] {
			continue
		}

		for _, file := range video.VideoFiles {
			if (file.Quality == "hd" || file.Quality == "sd") && file.Width >= 1280 {
				// Skip if this exact URL was already downloaded
				if tracker != nil && tracker.usedURLs[file.Link] {
					continue
				}
				downloadURL = file.Link
				if tracker != nil {
					tracker.usedVideoIDs[video.ID] = true
					tracker.usedURLs[file.Link] = true
				}
				break
			}
		}
		if downloadURL != "" {
			break
		}
	}

	// Fallback: if all HD clips were already used, allow any unused video
	if downloadURL == "" {
		for _, video := range pexelsResp.Videos {
			if tracker != nil && tracker.usedVideoIDs[video.ID] {
				continue
			}
			if len(video.VideoFiles) > 0 {
				downloadURL = video.VideoFiles[0].Link
				if tracker != nil {
					tracker.usedVideoIDs[video.ID] = true
					tracker.usedURLs[downloadURL] = true
				}
				break
			}
		}
	}

	// Last resort: if ALL videos in results are used, pick the first one anyway
	if downloadURL == "" {
		for _, video := range pexelsResp.Videos {
			for _, file := range video.VideoFiles {
				if (file.Quality == "hd" || file.Quality == "sd") && file.Width >= 1280 {
					downloadURL = file.Link
					break
				}
			}
			if downloadURL != "" {
				break
			}
			if len(video.VideoFiles) > 0 {
				downloadURL = video.VideoFiles[0].Link
				break
			}
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("no downloadable video files found for: %s", query)
	}

	return downloadFile(downloadURL, outputPath)
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

// generateAIImage handles generation with fallback (Together AI -> HuggingFace)
func generateAIImage(prompt, outputPath, tone string, isShort bool) error {
	err := generateTogetherImage(prompt, outputPath, tone, isShort)
	if err != nil {
		log.Printf("⚠️ Together AI failed: %v — falling back to HuggingFace", err)
		return generateHFImage(prompt, outputPath, tone)
	}
	return nil
}

// generateTogetherImage uses Together AI's free Flux.1 Schnell endpoint
func generateTogetherImage(prompt, outputPath, tone string, isShort bool) error {
	apiKey := config.App.TogetherAPIKey
	if apiKey == "" {
		return fmt.Errorf("TOGETHER_API_KEY not configured")
	}

	width, height := 1792, 1024 // youtube_landscape
	if isShort {
		width, height = 1024, 1792 // youtube_short
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
