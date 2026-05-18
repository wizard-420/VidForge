package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"yt-automation-studio/config"
)

// ----------------------------------------------------------------------------
// Vibe presets
// ----------------------------------------------------------------------------
//
// Each preset bundles together a music prompt (used by the AI provider chain),
// a default ambience stack (mixed on top of the music in a second pass), and
// a hint to the curated-library fallback (Jamendo) describing the tag/keyword
// that best matches this aesthetic. Presets are deliberately tuned for the
// short-form/long-form niches that perform best on YouTube + Shorts in 2026:
// peaceful aesthetic, lo-fi study, cinematic drama, motivational, etc.

// AIMusicPreset is the server-side representation of a vibe preset. The same
// preset IDs are used by the UI; the server is the source of truth for the
// prompt and ambience stack so old jobs stay reproducible.
type AIMusicPreset struct {
	ID            string   // stable identifier (e.g. "peaceful_aesthetic")
	Prompt        string   // natural-language prompt sent to the AI provider
	Ambience      []string // optional ambience layers (e.g. ["birds","wind"])
	JamendoTags   string   // fallback tags for Jamendo when AI providers fail
	JamendoSpeed  string   // fallback Jamendo speed hint
}

var aiMusicPresets = map[string]AIMusicPreset{
	"peaceful_aesthetic": {
		ID:           "peaceful_aesthetic",
		Prompt:       "Calm cinematic nature ambience, soft piano, airy vocal pad, light strings, distant reverb, peaceful sunrise mood, slow 70 BPM",
		Ambience:     []string{"birds", "wind"},
		JamendoTags:  "ambient calm peaceful piano relaxing",
		JamendoSpeed: "low medium",
	},
	"cinematic_drama": {
		ID:           "cinematic_drama",
		Prompt:       "Epic cinematic orchestral, deep cinematic drone, slow timpani, sweeping strings, emotional swell, hopeful but tense",
		JamendoTags:  "cinematic dramatic epic orchestral",
		JamendoSpeed: "medium high",
	},
	"lofi_study": {
		ID:           "lofi_study",
		Prompt:       "Chill lo-fi hip hop beat, vinyl crackle, soft Rhodes piano, mellow drums, jazzy chords, focused study mood, 80 BPM",
		Ambience:     []string{"vinyl"},
		JamendoTags:  "lofi chill study calm acoustic",
		JamendoSpeed: "low medium",
	},
	"sunrise_vlog": {
		ID:           "sunrise_vlog",
		Prompt:       "Warm acoustic guitar, gentle finger-picked melody, soft strings, hopeful uplifting mood, morning vlog feel",
		Ambience:     []string{"birds"},
		JamendoTags:  "acoustic uplifting warm light",
		JamendoSpeed: "medium",
	},
	"asmr_calm": {
		ID:           "asmr_calm",
		Prompt:       "Soft ambient drone, gentle synth pads, very slow evolving texture, sleep meditation mood, no drums",
		Ambience:     []string{"waves", "rain"},
		JamendoTags:  "ambient meditation sleep relaxing",
		JamendoSpeed: "low",
	},
	"tech_futuristic": {
		ID:           "tech_futuristic",
		Prompt:       "Modern electronic ambient, synth pads, soft glitch textures, motivational pulse, futuristic tech feel",
		JamendoTags:  "electronic futuristic technology motivational",
		JamendoSpeed: "medium high",
	},
	"mysterious": {
		ID:           "mysterious",
		Prompt:       "Suspenseful dark ambient, low drones, distant whispered textures, eerie tension build, no melody",
		Ambience:     []string{"wind"},
		JamendoTags:  "suspense dark mysterious thriller",
		JamendoSpeed: "low medium",
	},
	"motivational": {
		ID:           "motivational",
		Prompt:       "High-energy uplifting orchestral, driving drums, anthemic strings, triumphant climax, motivational mood",
		JamendoTags:  "uplifting motivational energetic epic",
		JamendoSpeed: "high veryhigh",
	},
}

// GetAIMusicPreset returns the preset definition for an ID; if the ID is
// "custom" or unknown, returns ok=false so the caller can fall back to the
// user-provided prompt.
func GetAIMusicPreset(id string) (AIMusicPreset, bool) {
	preset, ok := aiMusicPresets[id]
	return preset, ok
}

// buildAIMusicPrompt resolves the final prompt to feed to the AI provider.
// Resolution order: explicit MusicPrompt > preset prompt > script-tone fallback.
// We always append ambience hints into the prompt itself even though we mix
// them as a separate layer — the AI track sounds more cohesive when it
// "expects" those textures.
func buildAIMusicPrompt(promptOverride, presetID, scriptTone string, ambience []string) string {
	base := strings.TrimSpace(promptOverride)
	if base == "" {
		if preset, ok := aiMusicPresets[presetID]; ok {
			base = preset.Prompt
		}
	}
	if base == "" {
		// Last-resort: derive from script tone using the same map auto mode uses.
		fallback := map[string]string{
			"dramatic":       "Cinematic dramatic epic orchestral, emotional, hopeful tension",
			"suspenseful":    "Dark mysterious suspenseful ambient, low drones, eerie tension",
			"educational":    "Calm ambient relaxing background, soft piano, focus mood",
			"conversational": "Acoustic light happy positive, warm guitar, friendly mood",
			"motivational":   "Uplifting motivational energetic, driving drums, triumphant",
			"humorous":       "Fun playful comedy upbeat, light percussion, bouncy melody",
		}
		base = fallback[scriptTone]
		if base == "" {
			base = "Calm cinematic ambient background music, soft instrumentation"
		}
	}

	// Append ambience hints when the user requested layers — the AI track
	// itself sounds more natural when it knows it's sharing space with rain
	// or birds, even though those layers are mixed separately.
	if len(ambience) > 0 {
		base += ", with subtle " + strings.Join(ambience, " and ") + " atmosphere"
	}
	return base
}

// ----------------------------------------------------------------------------
// AI music provider chain
// ----------------------------------------------------------------------------

// GenerateAIMusicToBytes is the exported entry point used by the API layer
// for the "Generate Preview" feature in the UI. It runs the same provider
// chain as the pipeline-stage call but writes to a throwaway temp directory
// and returns the resulting MP3 bytes (plus which provider succeeded) so the
// browser can play the track and let the user iterate before committing to
// a full job. Ambience layering is included so the preview matches what the
// final video will actually sound like.
func GenerateAIMusicToBytes(promptOverride, presetID, scriptTone, provider string, durationSec int, ambience []string) (audioBytes []byte, providerUsed string, err error) {
	prompt := buildAIMusicPrompt(promptOverride, presetID, scriptTone, ambience)

	tempDir, err := os.MkdirTemp("", "ai_music_preview_*")
	if err != nil {
		return nil, "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	musicPath := filepath.Join(tempDir, "music.mp3")
	if err := generateAIMusicTracked(prompt, provider, durationSec, musicPath, tempDir, &providerUsed); err != nil {
		return nil, "", err
	}

	if len(ambience) > 0 {
		if err := layerAmbienceTracks(musicPath, ambience, tempDir); err != nil {
			// Non-fatal: preview falls back to base track without ambience.
			log.Printf("⚠️ preview ambience layering failed (%v) — returning base track", err)
		}
	}

	data, err := os.ReadFile(musicPath)
	if err != nil {
		return nil, "", fmt.Errorf("read preview file: %w", err)
	}
	return data, providerUsed, nil
}

// generateAIMusicTracked is identical to generateAIMusic but additionally
// reports which provider in the chain actually produced the track. We only
// need this nuance from the API/preview surface, so the regular pipeline
// keeps using the simpler signature.
func generateAIMusicTracked(prompt, provider string, durationSec int, outputPath, jobDir string, providerUsed *string) error {
	if prompt == "" {
		return fmt.Errorf("empty prompt")
	}
	if durationSec < 15 {
		durationSec = 15
	}

	chain := providerChainFor(provider)
	var lastErr error
	for _, p := range chain {
		log.Printf("🎵 AI music: trying provider %s (prompt=%q)", p, truncateForLog(prompt, 80))
		if err := runProvider(p, prompt, durationSec, outputPath, jobDir); err == nil {
			log.Printf("✅ AI music generated via %s", p)
			if providerUsed != nil {
				*providerUsed = p
			}
			return nil
		} else {
			log.Printf("⚠️ AI music provider %s failed: %v", p, err)
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no providers attempted")
	}
	return fmt.Errorf("all AI music providers failed: %w", lastErr)
}

// generateAIMusic runs the AI music provider chain and writes the final MP3
// to outputPath. Order: respect explicit provider when set, otherwise try
// HuggingFace MusicGen first (broader instrumentation), then Stable Audio
// Open (better cinematic/ambient), then Jamendo as the curated-library
// safety net. All three failures means the caller should fall back to the
// auto-mode Jamendo flow.
func generateAIMusic(prompt, provider string, durationSec int, outputPath, jobDir string) error {
	if prompt == "" {
		return fmt.Errorf("empty prompt")
	}
	if durationSec < 15 {
		durationSec = 15
	}

	chain := providerChainFor(provider)
	var lastErr error
	for _, p := range chain {
		log.Printf("🎵 AI music: trying provider %s (prompt=%q)", p, truncateForLog(prompt, 80))
		err := runProvider(p, prompt, durationSec, outputPath, jobDir)
		if err == nil {
			log.Printf("✅ AI music generated via %s", p)
			return nil
		}
		log.Printf("⚠️ AI music provider %s failed: %v", p, err)
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no providers attempted")
	}
	return fmt.Errorf("all AI music providers failed: %w", lastErr)
}

// providerChainFor returns the ordered list of providers to try for a given
// user selection. "auto" or "" means the full default chain.
func providerChainFor(selected string) []string {
	switch selected {
	case "huggingface_musicgen":
		return []string{"huggingface_musicgen", "huggingface_stable_audio", "jamendo"}
	case "huggingface_stable_audio":
		return []string{"huggingface_stable_audio", "huggingface_musicgen", "jamendo"}
	case "jamendo":
		return []string{"jamendo"}
	default:
		return []string{"huggingface_musicgen", "huggingface_stable_audio", "jamendo"}
	}
}

func runProvider(provider, prompt string, durationSec int, outputPath, jobDir string) error {
	switch provider {
	case "huggingface_musicgen":
		return fetchHuggingFaceMusicGen(prompt, durationSec, outputPath, jobDir)
	case "huggingface_stable_audio":
		return fetchHuggingFaceStableAudio(prompt, durationSec, outputPath, jobDir)
	case "jamendo":
		return fetchJamendoForPrompt(prompt, outputPath, durationSec)
	}
	return fmt.Errorf("unknown provider: %s", provider)
}

// ----------------------------------------------------------------------------
// HuggingFace: MusicGen
// ----------------------------------------------------------------------------
//
// MusicGen returns raw audio bytes (typically WAV/FLAC). We capture the bytes
// to a temp file then transcode to MP3 via ffmpeg so the renderer can treat
// AI-generated music identically to Jamendo / manual / uploaded sources.
//
// MusicGen practical limit is ~30s per request on the free Inference API; we
// rely on renderer.loopMusicToFitDuration() to cover longer videos. This is a
// deliberate trade-off: ambient/peaceful content loops well, and a single
// short generation keeps free-tier latency tolerable.

func fetchHuggingFaceMusicGen(prompt string, durationSec int, outputPath, jobDir string) error {
	apiKey := config.App.HFAPIKey
	if apiKey == "" {
		return fmt.Errorf("HF_API_KEY not configured")
	}

	// MusicGen free tier degrades quickly past ~30s; cap regardless of input.
	dur := durationSec
	if dur > 30 {
		dur = 30
	}

	hfURL := "https://api-inference.huggingface.co/models/facebook/musicgen-large"
	body, _ := json.Marshal(map[string]interface{}{
		"inputs": prompt,
		"parameters": map[string]interface{}{
			"duration":      dur,
			"do_sample":     true,
			"guidance_scale": 3,
		},
	})

	rawPath := filepath.Join(jobDir, "music_ai_raw")
	if err := postHFAudio(hfURL, apiKey, body, rawPath); err != nil {
		return fmt.Errorf("musicgen: %w", err)
	}
	defer os.Remove(rawPath)
	return transcodeToMP3(rawPath, outputPath)
}

// ----------------------------------------------------------------------------
// HuggingFace: Stable Audio Open
// ----------------------------------------------------------------------------
//
// Stable Audio Open generates higher-quality cinematic/ambient material than
// MusicGen and supports up to ~47s. Same byte-stream contract as MusicGen.

func fetchHuggingFaceStableAudio(prompt string, durationSec int, outputPath, jobDir string) error {
	apiKey := config.App.HFAPIKey
	if apiKey == "" {
		return fmt.Errorf("HF_API_KEY not configured")
	}

	dur := durationSec
	if dur > 47 {
		dur = 47
	}

	hfURL := "https://api-inference.huggingface.co/models/stabilityai/stable-audio-open-1.0"
	body, _ := json.Marshal(map[string]interface{}{
		"inputs": prompt,
		"parameters": map[string]interface{}{
			"audio_end_in_s": dur,
			"num_inference_steps": 100,
		},
	})

	rawPath := filepath.Join(jobDir, "music_ai_raw")
	if err := postHFAudio(hfURL, apiKey, body, rawPath); err != nil {
		return fmt.Errorf("stable_audio_open: %w", err)
	}
	defer os.Remove(rawPath)
	return transcodeToMP3(rawPath, outputPath)
}

// postHFAudio is the shared HTTP layer for the HuggingFace Inference API audio
// models. It handles model-loading 503s with exponential backoff (matching the
// pattern in pipeline/visual.go), captures raw bytes to disk, and surfaces
// useful error context on non-200 responses.
func postHFAudio(hfURL, apiKey string, body []byte, outputPath string) error {
	client := &http.Client{Timeout: 180 * time.Second}
	const retries = 3

	for attempt := 1; attempt <= retries; attempt++ {
		req, err := http.NewRequest("POST", hfURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "audio/wav")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("⚠️ HF music request failed (attempt %d/%d): %v", attempt, retries, err)
			time.Sleep(time.Duration(10*attempt) * time.Second)
			continue
		}

		if resp.StatusCode == 503 {
			// Cold-start: HF returns an estimated wait in the body.
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			wait := time.Duration(20*attempt) * time.Second
			log.Printf("⚠️ HF music model loading (attempt %d/%d), waiting %v: %s", attempt, retries, wait, string(b))
			time.Sleep(wait)
			continue
		}

		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("HF API returned %d: %s", resp.StatusCode, truncateForLog(string(b), 200))
		}

		// Stream raw audio bytes to disk.
		out, err := os.Create(outputPath)
		if err != nil {
			resp.Body.Close()
			return fmt.Errorf("create output: %w", err)
		}
		_, copyErr := io.Copy(out, resp.Body)
		out.Close()
		resp.Body.Close()
		if copyErr != nil {
			return fmt.Errorf("save audio: %w", copyErr)
		}

		// Sanity check: tiny payloads are usually error envelopes that slipped
		// through with a 200 status code.
		info, statErr := os.Stat(outputPath)
		if statErr != nil || info.Size() < 1024 {
			return fmt.Errorf("HF returned suspiciously small audio (%d bytes)", info.Size())
		}
		return nil
	}
	return fmt.Errorf("HuggingFace music inference failed after %d retries", retries)
}

// transcodeToMP3 normalises the raw HF audio payload (WAV/FLAC/etc.) into the
// MP3 format the renderer expects. We always re-encode rather than rename so
// downstream looping/mixing has consistent codec assumptions.
func transcodeToMP3(inputPath, outputPath string) error {
	args := []string{
		"-i", inputPath,
		"-vn",
		"-c:a", "libmp3lame",
		"-b:a", "192k",
		"-y", outputPath,
	}
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("transcode AI music to MP3: %v - %s", err, string(out))
	}
	return nil
}

// ----------------------------------------------------------------------------
// Jamendo fallback for AI mode
// ----------------------------------------------------------------------------
//
// fetchJamendoForPrompt is the curated-library leg of the AI provider chain.
// It extracts genre/mood keywords from the prompt and runs them through the
// existing Jamendo search API used by auto mode. This guarantees the pipeline
// always produces SOMETHING, even when HuggingFace is rate-limited or down.

func fetchJamendoForPrompt(prompt, outputPath string, durationSec int) error {
	clientID := config.App.JamendoClientID
	if clientID == "" {
		clientID = "b6747d04"
	}

	tags := extractJamendoTags(prompt)
	if tags == "" {
		tags = "cinematic"
	}

	apiURL, _ := url.Parse("https://api.jamendo.com/v3.0/tracks/")
	q := apiURL.Query()
	q.Add("client_id", clientID)
	q.Add("format", "json")
	q.Add("limit", "5")
	q.Add("audioformat", "mp32")
	q.Add("audiodlformat", "mp32")
	q.Add("durationbetween", fmt.Sprintf("%d_%d", durationSec, durationSec+180))
	q.Add("vocalinstrumental", "instrumental")
	q.Add("boost", "popularity_total")
	q.Add("type", "albumtrack")
	q.Add("fuzzytags", strings.ReplaceAll(tags, " ", "+"))
	apiURL.RawQuery = q.Encode()

	req, _ := http.NewRequest("GET", apiURL.String(), nil)
	req.Header.Set("User-Agent", "VidForge/1.0")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Jamendo HTTP %d", resp.StatusCode)
	}

	var r struct {
		Results []map[string]interface{} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}

	for _, track := range r.Results {
		if allowed, _ := track["audiodownload_allowed"].(bool); allowed {
			if dl, ok := track["audiodownload"].(string); ok && dl != "" {
				return downloadFile(dl, outputPath)
			}
		}
	}
	return fmt.Errorf("no downloadable Jamendo tracks for prompt")
}

// extractJamendoTags reduces a free-form prompt to a handful of keywords that
// Jamendo's fuzzy-tag search performs well on. We bias toward common
// instrument and mood words; the goal is "good enough" not "exact match".
func extractJamendoTags(prompt string) string {
	keywords := []string{
		"cinematic", "ambient", "calm", "peaceful", "epic", "dramatic",
		"acoustic", "piano", "guitar", "orchestral", "strings",
		"electronic", "synth", "uplifting", "motivational", "energetic",
		"dark", "suspense", "mysterious", "lofi", "chill",
		"happy", "sad", "emotional", "warm", "relaxing", "meditation",
	}
	lower := strings.ToLower(prompt)
	var matched []string
	for _, k := range keywords {
		if strings.Contains(lower, k) {
			matched = append(matched, k)
		}
		if len(matched) >= 4 {
			break
		}
	}
	return strings.Join(matched, " ")
}

// ----------------------------------------------------------------------------
// Ambience layering (Jamendo nature/SFX library)
// ----------------------------------------------------------------------------
//
// layerAmbienceTracks fetches one short track per requested ambience keyword
// from Jamendo (which has decent nature/wind/rain coverage), loops each to
// match the base music duration, then mixes them under the base track at a
// low volume using FFmpeg amix. The final result overwrites baseMusicPath.

func layerAmbienceTracks(baseMusicPath string, ambience []string, jobDir string) error {
	if len(ambience) == 0 {
		return nil
	}

	baseDur := getMediaDuration(baseMusicPath)
	if baseDur <= 0 {
		return fmt.Errorf("could not measure base music duration")
	}

	var ambiencePaths []string
	for i, term := range ambience {
		safeTerm := sanitizeFilename(term)
		path := filepath.Join(jobDir, fmt.Sprintf("ambience_%02d_%s.mp3", i, safeTerm))
		if err := fetchAmbienceTrack(term, path, int(baseDur)); err != nil {
			log.Printf("⚠️ ambience layer %s failed: %v (continuing without it)", term, err)
			continue
		}
		ambiencePaths = append(ambiencePaths, path)
	}

	if len(ambiencePaths) == 0 {
		log.Printf("ℹ️ no ambience layers fetched, keeping base music as-is")
		return nil
	}

	mixedPath := filepath.Join(jobDir, "music_layered.mp3")
	if err := mixAmbienceWithBase(baseMusicPath, ambiencePaths, baseDur, mixedPath); err != nil {
		return fmt.Errorf("mix ambience: %w", err)
	}

	// Replace base music with layered version; keep the file name stable so
	// renderer.go finds it via job.MusicFile.
	if err := os.Rename(mixedPath, baseMusicPath); err != nil {
		// Fall back to a copy + remove on filesystems where rename across
		// targets fails (rare but harmless).
		if cpErr := copyFile(mixedPath, baseMusicPath); cpErr != nil {
			return fmt.Errorf("replace base music: rename=%v copy=%v", err, cpErr)
		}
		os.Remove(mixedPath)
	}

	// Clean up per-layer temp files.
	for _, p := range ambiencePaths {
		os.Remove(p)
	}
	return nil
}

// fetchAmbienceTrack searches Jamendo for a nature/SFX track matching the
// given ambience term. Jamendo's library has good coverage of "rain", "wind",
// "birds", "waves", "fire", "crowd", "vinyl crackle", etc.
func fetchAmbienceTrack(term, outputPath string, minDurationSec int) error {
	clientID := config.App.JamendoClientID
	if clientID == "" {
		clientID = "b6747d04"
	}

	apiURL, _ := url.Parse("https://api.jamendo.com/v3.0/tracks/")
	q := apiURL.Query()
	q.Add("client_id", clientID)
	q.Add("format", "json")
	q.Add("limit", "5")
	q.Add("audioformat", "mp32")
	q.Add("audiodlformat", "mp32")
	q.Add("vocalinstrumental", "instrumental")
	q.Add("fuzzytags", term+"+nature+ambient")
	apiURL.RawQuery = q.Encode()

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(apiURL.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Jamendo HTTP %d", resp.StatusCode)
	}

	var r struct {
		Results []map[string]interface{} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}

	for _, track := range r.Results {
		if allowed, _ := track["audiodownload_allowed"].(bool); allowed {
			if dl, ok := track["audiodownload"].(string); ok && dl != "" {
				return downloadFile(dl, outputPath)
			}
		}
	}
	return fmt.Errorf("no Jamendo ambience track for: %s", term)
}

// mixAmbienceWithBase composes the final layered music track. Levels:
//   - base music: 1.0 (unchanged, since renderer ducks again later when mixing
//     with voice)
//   - each ambience layer: ~0.25 (audible texture, never fights the base)
//
// All layers are looped to cover baseDur via -stream_loop -1; the output is
// trimmed to baseDur exactly so renderer math still lines up.
func mixAmbienceWithBase(basePath string, ambiencePaths []string, baseDur float64, outputPath string) error {
	args := []string{"-i", basePath}
	for _, p := range ambiencePaths {
		args = append(args, "-stream_loop", "-1", "-i", p)
	}

	// Build filter graph: tag base [a0]; tag each ambience [aN] at low volume;
	// then amix everything together.
	var filter strings.Builder
	filter.WriteString("[0:a]volume=1.0[a0];")
	mixInputs := []string{"[a0]"}
	for i := range ambiencePaths {
		idx := i + 1
		filter.WriteString(fmt.Sprintf("[%d:a]volume=0.25[a%d];", idx, idx))
		mixInputs = append(mixInputs, fmt.Sprintf("[a%d]", idx))
	}
	filter.WriteString(strings.Join(mixInputs, ""))
	filter.WriteString(fmt.Sprintf("amix=inputs=%d:duration=first:dropout_transition=0[aout]", len(mixInputs)))

	args = append(args,
		"-filter_complex", filter.String(),
		"-map", "[aout]",
		"-t", fmt.Sprintf("%.3f", baseDur),
		"-c:a", "libmp3lame", "-b:a", "192k",
		"-y", outputPath,
	)

	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg ambience mix: %v - %s", err, string(out))
	}
	return nil
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

func sanitizeFilename(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '_', r == '-':
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "x"
	}
	return out
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
