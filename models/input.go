package models

import "time"

// InputPayload is the primary object created from user input in the dashboard.
// It is the starting point for every pipeline run.
type InputPayload struct {
	JobID         string `json:"job_id"`
	RawInput      string `json:"raw_input"`
	InputType     string `json:"input_type"`      // category | topic | event
	Format        string `json:"format"`           // long | short | both
	AspectRatio   string `json:"aspect_ratio"`     // landscape (16:9) | portrait (9:16) | square (1:1)
	FitMode       string `json:"fit_mode"`         // fill (zoom-and-crop, default) | fit (letterbox/pillarbox with black bars)
	DurationMin   int    `json:"duration_min"`     // 5-20 for long, forced 1 for short
	VoiceoverMode string `json:"voiceover_mode"`   // ai | manual | gcp_tts | none
	VoiceID       string `json:"voice_id"`         // adam | rachel | domi | josh
	VideoMode     string `json:"video_mode"`       // auto | manual
	VideoStyle    string `json:"video_style"`      // stock | ai_images | mixed
	MusicMode     string `json:"music_mode"`       // auto | skip
	ScriptTone    string `json:"script_tone"`      // dramatic | educational | conversational | suspenseful | motivational | humorous
	Language      string `json:"language"`          // english | hindi | hinglish
	UploadSchedule string `json:"upload_schedule"` // immediate | 19:00 | 20:00 | 21:00
	CaptionStyle  string `json:"caption_style"`    // bold_white | subtitle | none
	AutoUpload    bool   `json:"auto_upload"`      // if false, pause before stage 7
	ClipCount          int                  `json:"clip_count"`       // number of stock video clips to fetch (auto-derived from SecondsPerVisual when 0)
	ImageCount         int                  `json:"image_count"`      // number of AI images to generate (auto-derived from SecondsPerVisual + AIImagePercent when 0)
	SecondsPerVisual   int                  `json:"seconds_per_visual"` // pacing: 1 visual per N seconds of narration (3-15, default 6)
	AIImagePercent     int                  `json:"ai_image_percent"` // % of visuals that should be AI images vs stock clips (0-100, default 0)
	MusicUrl           string               `json:"music_url"`        // Jamendo track URL
	MusicStart         int                  `json:"music_start"`      // crop start in seconds
	MusicEnd           int                  `json:"music_end"`        // crop end in seconds
	GCPVoiceName       string               `json:"gcp_voice_name,omitempty"`       // Google Cloud TTS voice name (e.g. "en-US-Neural2-D")
	GCPLanguageCode    string               `json:"gcp_language_code,omitempty"`    // BCP-47 language code (e.g. "en-US")
	OutputQuality      string               `json:"output_quality,omitempty"`       // draft | standard | high — controls Pexels source selection + FFmpeg preset/CRF/FPS
	PreGeneratedScript *ScriptDocument      `json:"pre_generated_script,omitempty"` // Used when script is generated via preview
	ManualAudioBase64  map[int]string       `json:"manual_audio_base64,omitempty"`  // Base64 audio per segment ID
	CreatedAt          string               `json:"created_at"`
}

// Validate checks the InputPayload for required fields and valid values
func (p *InputPayload) Validate() []string {
	var errs []string

	if p.RawInput == "" {
		errs = append(errs, "raw_input is required")
	}

	validInputTypes := map[string]bool{"category": true, "topic": true, "event": true}
	if !validInputTypes[p.InputType] {
		errs = append(errs, "input_type must be one of: category, topic, event")
	}

	validFormats := map[string]bool{"long": true, "short": true, "both": true}
	if !validFormats[p.Format] {
		errs = append(errs, "format must be one of: long, short, both")
	}

	if p.AspectRatio != "" {
		validAspects := map[string]bool{"landscape": true, "portrait": true, "square": true}
		if !validAspects[p.AspectRatio] {
			errs = append(errs, "aspect_ratio must be one of: landscape, portrait, square")
		}
	}

	if p.FitMode != "" {
		validFit := map[string]bool{"fill": true, "fit": true}
		if !validFit[p.FitMode] {
			errs = append(errs, "fit_mode must be one of: fill, fit")
		}
	}

	if p.OutputQuality != "" {
		validQuality := map[string]bool{"draft": true, "standard": true, "high": true}
		if !validQuality[p.OutputQuality] {
			errs = append(errs, "output_quality must be one of: draft, standard, high")
		}
	}

	if p.Format == "long" || p.Format == "both" {
		if p.DurationMin < 5 || p.DurationMin > 20 {
			errs = append(errs, "duration_min must be between 5 and 20 for long-form")
		}
	}

	validVoiceModes := map[string]bool{"ai": true, "manual": true, "gcp_tts": true, "none": true}
	if !validVoiceModes[p.VoiceoverMode] {
		errs = append(errs, "voiceover_mode must be one of: ai, manual, gcp_tts, none")
	}

	validVoices := map[string]bool{"adam": true, "rachel": true, "domi": true, "josh": true}
	if p.VoiceoverMode == "ai" && !validVoices[p.VoiceID] {
		errs = append(errs, "voice_id must be one of: adam, rachel, domi, josh")
	}

	if p.VoiceoverMode == "gcp_tts" {
		if p.GCPVoiceName == "" {
			errs = append(errs, "gcp_voice_name is required when voiceover_mode is gcp_tts")
		}
		if p.GCPLanguageCode == "" {
			errs = append(errs, "gcp_language_code is required when voiceover_mode is gcp_tts")
		}
	}

	validVideoModes := map[string]bool{"auto": true, "manual": true}
	if !validVideoModes[p.VideoMode] {
		errs = append(errs, "video_mode must be one of: auto, manual")
	}

	validStyles := map[string]bool{"stock": true, "ai_images": true, "mixed": true}
	if !validStyles[p.VideoStyle] {
		errs = append(errs, "video_style must be one of: stock, ai_images, mixed")
	}

	if p.ClipCount < 0 || p.ClipCount > 200 {
		errs = append(errs, "clip_count must be between 0 and 200")
	}
	if p.ImageCount < 0 || p.ImageCount > 200 {
		errs = append(errs, "image_count must be between 0 and 200")
	}
	if p.SecondsPerVisual != 0 && (p.SecondsPerVisual < 3 || p.SecondsPerVisual > 15) {
		errs = append(errs, "seconds_per_visual must be between 3 and 15")
	}
	if p.AIImagePercent < 0 || p.AIImagePercent > 100 {
		errs = append(errs, "ai_image_percent must be between 0 and 100")
	}

	validMusicModes := map[string]bool{"auto": true, "skip": true, "manual": true}
	if !validMusicModes[p.MusicMode] {
		errs = append(errs, "music_mode must be one of: auto, skip, manual")
	}

	validTones := map[string]bool{
		"dramatic": true, "educational": true, "conversational": true,
		"suspenseful": true, "motivational": true, "humorous": true,
	}
	if !validTones[p.ScriptTone] {
		errs = append(errs, "script_tone must be one of: dramatic, educational, conversational, suspenseful, motivational, humorous")
	}

	validLangs := map[string]bool{"english": true, "hindi": true, "hinglish": true}
	if !validLangs[p.Language] {
		errs = append(errs, "language must be one of: english, hindi, hinglish")
	}

	return errs
}

// SetDefaults fills in missing fields with sensible defaults
func (p *InputPayload) SetDefaults() {
	if p.Format == "" {
		p.Format = "long"
	}
	// Aspect ratio default follows the format unless caller specified otherwise:
	// shorts are vertical by convention, long-form is landscape.
	if p.AspectRatio == "" {
		if p.Format == "short" {
			p.AspectRatio = "portrait"
		} else {
			p.AspectRatio = "landscape"
		}
	}
	// Fit mode default: "fill" (zoom-and-crop) — modern Shorts/TikTok aesthetic.
	// Users who prefer the original "fit" behavior (letterbox/pillarbox preserving
	// the entire source frame with black bars) can opt in explicitly.
	if p.FitMode == "" {
		p.FitMode = "fill"
	}
	// Output quality picks Pexels source resolution + x264 preset/CRF/FPS.
	// "standard" is the existing behavior (CRF 23, fast, 30fps).
	if p.OutputQuality == "" {
		p.OutputQuality = "standard"
	}
	if p.DurationMin == 0 {
		if p.Format == "short" {
			p.DurationMin = 1
		} else {
			p.DurationMin = 8
		}
	}
	if p.VoiceoverMode == "" {
		p.VoiceoverMode = "ai"
	}
	if p.VoiceID == "" {
		p.VoiceID = "adam"
	}
	if p.VideoMode == "" {
		p.VideoMode = "auto"
	}
	if p.VideoStyle == "" {
		p.VideoStyle = "stock"
	}
	if p.SecondsPerVisual == 0 {
		p.SecondsPerVisual = 6
	}

	// Pacing-driven defaults: when ClipCount + ImageCount are both 0, derive
	// the total visual count from SecondsPerVisual and the estimated duration.
	// AIImagePercent (and VideoStyle as a fallback) decides the clip/image split.
	// If the caller explicitly set ClipCount or ImageCount, honor those values.
	if p.ClipCount == 0 && p.ImageCount == 0 {
		estDurationSec := p.DurationMin * 60
		if p.Format == "short" {
			estDurationSec = 50
		}
		totalVisuals := estDurationSec / p.SecondsPerVisual
		if estDurationSec%p.SecondsPerVisual != 0 {
			totalVisuals++
		}
		if totalVisuals < 3 {
			totalVisuals = 3
		}

		// Prefer the explicit AI/clip ratio; fall back to VideoStyle for old clients.
		aiPct := p.AIImagePercent
		if aiPct == 0 && p.AIImagePercent == 0 {
			switch p.VideoStyle {
			case "ai_images":
				aiPct = 100
			case "mixed":
				aiPct = 40
			}
		}

		p.ImageCount = totalVisuals * aiPct / 100
		p.ClipCount = totalVisuals - p.ImageCount
	}
	if p.MusicMode == "" {
		p.MusicMode = "auto"
	}
	if p.ScriptTone == "" {
		p.ScriptTone = "dramatic"
	}
	if p.Language == "" {
		p.Language = "english"
	}
	if p.UploadSchedule == "" {
		p.UploadSchedule = "immediate"
	}
	if p.CaptionStyle == "" {
		p.CaptionStyle = "bold_white"
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
}
