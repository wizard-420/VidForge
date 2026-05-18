package pipeline

import (
	"fmt"
	"regexp"
	"strings"

	"yt-automation-studio/models"
)

// This file owns the conversion from emotion-tagged ScriptSentence data into
// Google Cloud TTS SSML. The renderer is intentionally conservative — Neural2
// and Studio voices respond best to *subtle* prosody nudges; aggressive
// pitch/rate changes start to sound cartoonish.
//
// Cost note: Google bills the FULL ssml string including tags, so we keep
// per-sentence overhead tight (~50-70 bytes per sentence vs. the raw text).

// EmotionPreset captures the prosody knobs that define how a single emotion
// should be voiced. Rate/Pitch/Volume use SSML's native units; PreBreakMs is
// applied as a `<break>` BEFORE the sentence so emotional shifts feel
// deliberate rather than abrupt.
type EmotionPreset struct {
	Rate       string // SSML rate: "x-slow" | "slow" | "medium" | "fast" | "x-fast" | percentage like "92%"
	Pitch      string // SSML pitch: semitones like "+2st" / "-1.5st"
	Volume     string // SSML volume: "x-soft" | "soft" | "medium" | "loud" | "x-loud"
	PreBreakMs int    // pause inserted before this sentence
}

// emotionPresets is the canonical emotion → SSML prosody mapping. Values
// were tuned against Neural2 voices to be clearly distinguishable without
// sounding theatrical. If you add a new emotion, also update ValidEmotions
// and the emotion_tagger.go prompt enum.
var emotionPresets = map[string]EmotionPreset{
	"neutral":   {Rate: "100%", Pitch: "+0st", Volume: "medium", PreBreakMs: 250},
	"excited":   {Rate: "108%", Pitch: "+2st", Volume: "loud", PreBreakMs: 200},
	"dramatic":  {Rate: "92%", Pitch: "-1.5st", Volume: "medium", PreBreakMs: 500},
	"somber":    {Rate: "85%", Pitch: "-2st", Volume: "soft", PreBreakMs: 600},
	"curious":   {Rate: "100%", Pitch: "+1st", Volume: "medium", PreBreakMs: 300},
	"whispered": {Rate: "90%", Pitch: "-0.5st", Volume: "x-soft", PreBreakMs: 350},
	"playful":   {Rate: "105%", Pitch: "+1.5st", Volume: "medium", PreBreakMs: 250},
}

// ValidEmotions is the closed set of emotion labels the pipeline understands.
// Anything else gets normalised to "neutral".
var ValidEmotions = []string{
	"neutral", "excited", "dramatic", "somber", "curious", "whispered", "playful",
}

// validEmotionSet for O(1) lookup.
var validEmotionSet = func() map[string]bool {
	m := make(map[string]bool, len(ValidEmotions))
	for _, e := range ValidEmotions {
		m[e] = true
	}
	return m
}()

// NormalizeEmotion lowercases + validates an emotion label. Anything not in
// the closed set returns "neutral" so downstream rendering can't blow up on
// LLM hallucinations.
func NormalizeEmotion(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if validEmotionSet[s] {
		return s
	}
	return "neutral"
}

// paceToRate maps the optional "pace" hint to a percentage override. When
// pace is empty we fall back to the emotion's default rate. We keep the
// final rate value bounded so the LLM can't accidentally produce 200%.
func paceToRate(pace, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(pace)) {
	case "slow":
		return "88%"
	case "fast":
		return "112%"
	case "normal":
		return "100%"
	}
	return fallback
}

// boundedPauseMs clamps an LLM-suggested pause to a sensible range so a
// hallucinated 30000 doesn't insert a 30-second silence.
func boundedPauseMs(ms int) int {
	if ms < 0 {
		return 0
	}
	if ms > 1500 {
		return 1500
	}
	return ms
}

// xmlEscape escapes the five XML special characters. SSML is XML, so any
// stray & or < in narration text would invalidate the document and the
// GCP API would return 400.
var xmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"\"", "&quot;",
	"'", "&apos;",
)

// reYear matches 4-digit years 1000-2999, used to wrap them in <say-as> so
// "2026" doesn't get read as "two thousand twenty-six" inconsistently
// across voices. We accept whole-word years only (\b boundaries).
var reYear = regexp.MustCompile(`\b(1[0-9]{3}|2[0-9]{3})\b`)

// reLargeNumber matches plain integers ≥ 4 digits. Wrapping them in
// <say-as interpret-as="cardinal"> prevents misreads like "1996" → "nineteen
// ninety six" when it should be cardinal. The year regex runs first so true
// years win.
var reLargeNumber = regexp.MustCompile(`\b\d{4,}\b`)

// RenderSegmentSSML converts a tagged ScriptSegment into a single SSML
// document for one GCP TTS synthesis call. When seg.Sentences is empty the
// segment text is wrapped with neutral prosody so the caller still gets a
// valid SSML payload (useful for testing or when emotion tagging fails).
//
// The output is ALWAYS a complete <speak>…</speak> document, ready to ship
// as the `ssml` field of a SynthesizeRequest.
func RenderSegmentSSML(seg models.ScriptSegment) string {
	var b strings.Builder
	b.WriteString("<speak>")

	sentences := seg.Sentences
	if len(sentences) == 0 {
		// Synthesise a single neutral sentence from the raw segment text so
		// downstream consumers can always assume valid SSML.
		sentences = []models.ScriptSentence{{Text: seg.Text, Emotion: "neutral"}}
	}

	for i, s := range sentences {
		writeSentenceSSML(&b, s, i == 0)
	}

	b.WriteString("</speak>")
	return b.String()
}

// writeSentenceSSML appends one sentence's prosody-wrapped SSML to b. The
// `isFirst` flag suppresses the leading break for the very first sentence
// in a segment (we don't want videos to start with a silent half-second).
func writeSentenceSSML(b *strings.Builder, s models.ScriptSentence, isFirst bool) {
	emotion := NormalizeEmotion(s.Emotion)
	preset := emotionPresets[emotion]

	// Pre-break before the sentence. The first sentence gets a tiny lead-in
	// (~80ms) so the audio doesn't clip right at t=0, but no emotional pause.
	pauseMs := boundedPauseMs(s.PrePauseMs)
	if pauseMs == 0 {
		pauseMs = preset.PreBreakMs
	}
	if isFirst {
		pauseMs = 80
	}
	fmt.Fprintf(b, `<break time="%dms"/>`, pauseMs)

	// Resolve the actual rate — explicit pace overrides the emotion default.
	rate := paceToRate(s.Pace, preset.Rate)

	fmt.Fprintf(b, `<prosody rate="%s" pitch="%s" volume="%s">`,
		rate, preset.Pitch, preset.Volume)

	body := renderSentenceBody(s.Text, s.Emphasis)
	b.WriteString(body)

	b.WriteString(`</prosody>`)
}

// renderSentenceBody handles word-level emphasis + number normalisation
// inside a single sentence. Returns SSML-escaped fragment (no surrounding
// prosody tag — that's the caller's job).
func renderSentenceBody(text string, emphasis []string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Apply emphasis BEFORE escaping so we can recognise the words; we then
	// inject SSML tags that themselves stay un-escaped, and escape the rest.
	// To do this safely we tokenise via placeholders.
	type emphRun struct {
		start, end int
	}
	var runs []emphRun

	if len(emphasis) > 0 {
		for _, word := range emphasis {
			word = strings.TrimSpace(word)
			if word == "" {
				continue
			}
			// Match word boundaries case-insensitively. Use the first match
			// only per emphasis word — repeating emphasis on every occurrence
			// sounds robotic.
			pattern, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(word) + `\b`)
			if err != nil {
				continue
			}
			if loc := pattern.FindStringIndex(text); loc != nil {
				runs = append(runs, emphRun{loc[0], loc[1]})
			}
		}
	}

	// Sort runs by start, drop overlaps (keep earliest).
	if len(runs) > 1 {
		for i := 0; i < len(runs); i++ {
			for j := i + 1; j < len(runs); j++ {
				if runs[j].start < runs[i].start {
					runs[i], runs[j] = runs[j], runs[i]
				}
			}
		}
		cleaned := runs[:1]
		for _, r := range runs[1:] {
			if r.start >= cleaned[len(cleaned)-1].end {
				cleaned = append(cleaned, r)
			}
		}
		runs = cleaned
	}

	// Build the output by walking the original string and emitting either
	// escaped plain text or <emphasis>…</emphasis> wrapped escaped text.
	var b strings.Builder
	cursor := 0
	for _, r := range runs {
		if cursor < r.start {
			b.WriteString(escapeAndNormalize(text[cursor:r.start]))
		}
		b.WriteString(`<emphasis level="moderate">`)
		b.WriteString(escapeAndNormalize(text[r.start:r.end]))
		b.WriteString(`</emphasis>`)
		cursor = r.end
	}
	if cursor < len(text) {
		b.WriteString(escapeAndNormalize(text[cursor:]))
	}

	return b.String()
}

// escapeAndNormalize XML-escapes a fragment AND wraps obvious year /
// long-number patterns in <say-as>. The order is important: we substitute
// the say-as wrappers FIRST (since their inner content is digits with no
// XML-unsafe chars), then escape the surrounding text. The wrapped chunks
// themselves are emitted as raw SSML so the angle brackets survive.
func escapeAndNormalize(s string) string {
	if s == "" {
		return ""
	}

	// First pass: years (1000-2999) — wrap as date so Neural2 picks the
	// natural "nineteen ninety-six" reading.
	type repl struct {
		start, end int
		out        string
	}
	var repls []repl

	for _, loc := range reYear.FindAllStringIndex(s, -1) {
		repls = append(repls, repl{loc[0], loc[1],
			`<say-as interpret-as="date" format="y">` + s[loc[0]:loc[1]] + `</say-as>`})
	}

	// Second pass: long cardinals NOT already covered by year matches.
	for _, loc := range reLargeNumber.FindAllStringIndex(s, -1) {
		overlap := false
		for _, r := range repls {
			if loc[0] < r.end && loc[1] > r.start {
				overlap = true
				break
			}
		}
		if !overlap {
			repls = append(repls, repl{loc[0], loc[1],
				`<say-as interpret-as="cardinal">` + s[loc[0]:loc[1]] + `</say-as>`})
		}
	}

	// Sort by start ascending.
	for i := 0; i < len(repls); i++ {
		for j := i + 1; j < len(repls); j++ {
			if repls[j].start < repls[i].start {
				repls[i], repls[j] = repls[j], repls[i]
			}
		}
	}

	var b strings.Builder
	cursor := 0
	for _, r := range repls {
		if cursor < r.start {
			b.WriteString(xmlEscaper.Replace(s[cursor:r.start]))
		}
		b.WriteString(r.out) // already safe digits-only SSML
		cursor = r.end
	}
	if cursor < len(s) {
		b.WriteString(xmlEscaper.Replace(s[cursor:]))
	}
	return b.String()
}

// EstimateSSMLBilledChars approximates the billed character count for an
// SSML payload (everything in <speak>, tags included, EXCEPT <mark>). The
// pipeline can call this to log/warn about expected GCP TTS usage.
func EstimateSSMLBilledChars(ssml string) int {
	// Per Google's pricing docs, <mark> is the only excluded tag. We don't
	// emit <mark>, so the full length is the billable count.
	return len(ssml)
}
