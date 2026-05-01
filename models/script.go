package models

// ScriptDocument is the output of Stage 2 (Script Generator).
// Contains the full script split into segments, plus all YouTube metadata.
type ScriptDocument struct {
	JobID          string          `json:"job_id"`
	Format         string          `json:"format"`           // long | short
	TitleOptions   []string        `json:"title_options"`    // 3 title suggestions
	Description    string          `json:"description"`      // YouTube description
	Tags           []string        `json:"tags"`             // 15 tags
	ThumbnailText  string          `json:"thumbnail_text"`   // short punchy text
	Hook           string          `json:"hook"`             // opening 5-sec line
	Segments       []ScriptSegment `json:"segments"`
	TotalSegments  int             `json:"total_segments"`
	TotalDuration  int             `json:"total_duration_sec"`
	ShortVersion   *ShortScript    `json:"short_version,omitempty"` // present when format = "both"
}

// ScriptSegment represents one unit of the script (~30-90 seconds)
type ScriptSegment struct {
	SegmentID   int          `json:"segment_id"`
	Type        string       `json:"type"`          // hook | body | cta
	Text        string       `json:"text"`          // full narration text
	WordCount   int          `json:"word_count"`
	DurationSec int          `json:"duration_sec"`  // estimated
	VisualCue   string       `json:"visual_cue"`    // natural language description (legacy fallback)
	VisualQuery string       `json:"visual_query"`  // Pexels search term (legacy fallback)
	MusicMood   string       `json:"music_mood"`    // dramatic | calm | upbeat | mysterious
	Transition  string       `json:"transition"`    // fade | cut | slide
	SubVisuals  []SubVisual  `json:"sub_visuals,omitempty"` // fine-grained visual cues within this segment
}

// SubVisual is one visual asset shown within a segment.
// Multiple sub-visuals create a "B-roll" effect where footage changes
// every few seconds while the voiceover flows continuously.
type SubVisual struct {
	Index       int    `json:"index"`
	Query       string `json:"query"`       // Pexels/DALL-E search term (2-4 words)
	Description string `json:"description"` // natural language description of what to show
	Type        string `json:"type"`        // "clip" (stock video) or "image" (AI-generated)
}

// ShortScript is the YouTube Shorts version (present when format = "both")
type ShortScript struct {
	Hook            string          `json:"hook"`
	Segments        []ScriptSegment `json:"segments"`
	TotalDuration   int             `json:"total_duration_sec"`
}
