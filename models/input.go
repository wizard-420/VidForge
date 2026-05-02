package models

import "time"

// InputPayload is the primary object created from user input in the dashboard.
// It is the starting point for every pipeline run.
type InputPayload struct {
	JobID         string `json:"job_id"`
	RawInput      string `json:"raw_input"`
	InputType     string `json:"input_type"`      // category | topic | event
	Format        string `json:"format"`           // long | short | both
	DurationMin   int    `json:"duration_min"`     // 5-20 for long, forced 1 for short
	VoiceoverMode string `json:"voiceover_mode"`   // ai | manual
	VoiceID       string `json:"voice_id"`         // adam | rachel | domi | josh
	VideoMode     string `json:"video_mode"`       // auto | manual
	VideoStyle    string `json:"video_style"`      // stock | ai_images | mixed
	MusicMode     string `json:"music_mode"`       // auto | skip
	ScriptTone    string `json:"script_tone"`      // dramatic | educational | conversational | suspenseful | motivational | humorous
	Language      string `json:"language"`          // english | hindi | hinglish
	UploadSchedule string `json:"upload_schedule"` // immediate | 19:00 | 20:00 | 21:00
	CaptionStyle  string `json:"caption_style"`    // bold_white | subtitle | none
	AutoUpload    bool   `json:"auto_upload"`      // if false, pause before stage 7
	ClipCount          int                  `json:"clip_count"`       // number of stock video clips to fetch
	ImageCount         int                  `json:"image_count"`      // number of AI images to generate
	MusicUrl           string               `json:"music_url"`        // Jamendo track URL
	MusicStart         int                  `json:"music_start"`      // crop start in seconds
	MusicEnd           int                  `json:"music_end"`        // crop end in seconds
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

	if p.Format == "long" || p.Format == "both" {
		if p.DurationMin < 5 || p.DurationMin > 20 {
			errs = append(errs, "duration_min must be between 5 and 20 for long-form")
		}
	}

	validVoiceModes := map[string]bool{"ai": true, "manual": true}
	if !validVoiceModes[p.VoiceoverMode] {
		errs = append(errs, "voiceover_mode must be one of: ai, manual")
	}

	validVoices := map[string]bool{"adam": true, "rachel": true, "domi": true, "josh": true}
	if p.VoiceoverMode == "ai" && !validVoices[p.VoiceID] {
		errs = append(errs, "voice_id must be one of: adam, rachel, domi, josh")
	}

	validVideoModes := map[string]bool{"auto": true, "manual": true}
	if !validVideoModes[p.VideoMode] {
		errs = append(errs, "video_mode must be one of: auto, manual")
	}

	validStyles := map[string]bool{"stock": true, "ai_images": true, "mixed": true}
	if !validStyles[p.VideoStyle] {
		errs = append(errs, "video_style must be one of: stock, ai_images, mixed")
	}

	if p.ClipCount < 0 || p.ClipCount > 30 {
		errs = append(errs, "clip_count must be between 0 and 30")
	}
	if p.ImageCount < 0 || p.ImageCount > 20 {
		errs = append(errs, "image_count must be between 0 and 20")
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
	// Default clip/image counts based on format and style
	if p.ClipCount == 0 && p.ImageCount == 0 {
		switch p.VideoStyle {
		case "stock":
			if p.Format == "short" {
				p.ClipCount = 6
			} else {
				p.ClipCount = max(p.DurationMin*2, 8)
			}
		case "ai_images":
			if p.Format == "short" {
				p.ImageCount = 6
			} else {
				p.ImageCount = max(p.DurationMin*2, 8)
			}
		case "mixed":
			if p.Format == "short" {
				p.ClipCount = 4
				p.ImageCount = 2
			} else {
				p.ClipCount = max(p.DurationMin, 4)
				p.ImageCount = max(p.DurationMin, 4)
			}
		}
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
