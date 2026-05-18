package pipeline

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"yt-automation-studio/models"
)

// emotion_tagger.go takes a ScriptSegment and produces per-sentence emotion
// + pacing hints. Two tiers in the same shape pattern used by recommend.go:
//
//   1) Sentence splitting + heuristic tagging — deterministic, no network
//      calls. Produces "good enough" results from music_mood + tone +
//      simple cues (exclamations → excited, ellipses → dramatic, etc.).
//   2) One Groq call per segment that refines the heuristic answer. When
//      Groq is unavailable or returns invalid JSON, the heuristic stands.
//
// The tagger writes the result directly back into seg.Sentences so the
// rendering layer doesn't need to know which tier produced the tags.

// commonAbbreviations holds tokens ending in "." that should NOT break a
// sentence even when followed by a capitalised word. Lower-cased for
// comparison. Add to this list as the LLM surfaces new edge cases.
var commonAbbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true,
	"sr": true, "jr": true, "st": true,
	"mt": true, "rd": true, "blvd": true, "ave": true,
	"jan": true, "feb": true, "mar": true, "apr": true, "jun": true,
	"jul": true, "aug": true, "sep": true, "sept": true, "oct": true,
	"nov": true, "dec": true,
	"etc": true, "vs": true, "approx": true, "no": true,
	"u.s": true, "u.k": true, "u.n": true,
}

// splitSentences splits a paragraph of narration into individual sentences,
// preserving terminators. Go's regexp engine (RE2) lacks lookahead, so we
// do this with a hand-rolled scan rather than a regex — it's faster anyway.
//
// Algorithm:
//   1) Walk the text character by character.
//   2) On `.`, `!`, `?`, `…`, or `...`, consume all consecutive terminators.
//   3) If the token before the terminator is a known abbreviation
//      ("Mt", "Dr", "etc"), DON'T split — keep accumulating.
//   4) If followed by whitespace + (uppercase letter | quote | open-paren |
//      end-of-string), treat as a sentence break.
//   5) Otherwise (e.g. odd mid-sentence punctuation), keep accumulating.
//
// This is heuristic-grade and good enough for narration scripts; we don't
// need perfect NLP sentence segmentation.
func splitSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	var sentences []string
	var cur strings.Builder
	runes := []rune(text)
	n := len(runes)

	isTerminator := func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '…'
	}

	for i := 0; i < n; i++ {
		r := runes[i]
		cur.WriteRune(r)

		if !isTerminator(r) {
			continue
		}

		// Single "." only — `?`, `!`, `…` and multi-char terminators are
		// strong signals and shouldn't be suppressed by the abbreviation
		// check. Capture whether this terminator is just a lone period
		// BEFORE we consume any consecutive terminators.
		isLonePeriod := r == '.' && (i+1 >= n || !isTerminator(runes[i+1]))

		// Consume consecutive terminators (e.g. "..." or "!?").
		for i+1 < n && isTerminator(runes[i+1]) {
			i++
			cur.WriteRune(runes[i])
		}

		// Skip following whitespace and check the next non-space char.
		j := i + 1
		for j < n && (runes[j] == ' ' || runes[j] == '\t' || runes[j] == '\n') {
			j++
		}

		// End-of-text → close the sentence.
		if j >= n {
			s := strings.TrimSpace(cur.String())
			if s != "" {
				sentences = append(sentences, s)
			}
			cur.Reset()
			i = j // exit loop
			continue
		}

		next := runes[j]
		// Sentence boundary if next char looks like the start of a new
		// sentence: uppercase letter, opening quote/paren, or digit.
		isBoundary := (next >= 'A' && next <= 'Z') ||
			next == '"' || next == '\'' || next == '“' || next == '‘' ||
			next == '(' || next == '[' ||
			(next >= '0' && next <= '9')

		if !isBoundary {
			// Mid-sentence punctuation like "version 1.5 of" — keep going.
			continue
		}

		// Don't split on lone periods that follow a known abbreviation.
		// We look back at the buffered chars to find the token before the
		// period.
		if isLonePeriod && endsWithAbbreviation(cur.String()) {
			continue
		}

		// Real sentence boundary.
		s := strings.TrimSpace(cur.String())
		if s != "" {
			sentences = append(sentences, s)
		}
		cur.Reset()
		i = j - 1 // outer loop's i++ will land on `next`
	}

	// Trailing fragment (text without final terminator).
	if tail := strings.TrimSpace(cur.String()); tail != "" {
		sentences = append(sentences, tail)
	}

	if len(sentences) == 0 {
		sentences = []string{text}
	}
	return sentences
}

// endsWithAbbreviation returns true when `buffer` ends in something like
// "Mt." or "U.S." where the last `.` is part of an abbreviation rather than
// a sentence terminator. The buffer ALWAYS ends with a `.` when this is
// called (the splitter only invokes it on lone periods).
func endsWithAbbreviation(buffer string) bool {
	// Strip the trailing period before extracting the last token.
	if !strings.HasSuffix(buffer, ".") {
		return false
	}
	body := buffer[:len(buffer)-1]

	// Walk backwards to find the start of the last whitespace-delimited
	// token. We accept letters, digits, and inner periods (so "U.S" counts
	// as one token for the abbreviation lookup).
	i := len(body) - 1
	for i >= 0 {
		ch := body[i]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '(' || ch == '[' || ch == '"' || ch == '\'' {
			break
		}
		i--
	}
	token := strings.ToLower(body[i+1:])
	return commonAbbreviations[token]
}

// moodToDefaultEmotion maps the segment's MusicMood to a sensible default
// per-sentence emotion. This anchors the heuristic so the whole segment
// has a consistent feel even before LLM refinement.
var moodToDefaultEmotion = map[string]string{
	"dramatic":    "dramatic",
	"mysterious":  "dramatic",
	"suspenseful": "dramatic",
	"sad":         "somber",
	"calm":        "neutral",
	"educational": "neutral",
	"upbeat":      "excited",
	"motivational": "excited",
	"humorous":    "playful",
	"epic":        "dramatic",
}

// toneToBias provides a secondary nudge based on the user-selected
// script_tone. Tone wins over mood when they conflict.
var toneToBias = map[string]string{
	"dramatic":      "dramatic",
	"suspenseful":   "dramatic",
	"motivational":  "excited",
	"educational":   "neutral",
	"conversational": "neutral",
	"humorous":      "playful",
}

// reQuestion matches sentences ending with a question mark.
var reQuestion = regexp.MustCompile(`\?\s*$`)

// reExclaim matches sentences ending with one or more exclamation marks.
var reExclaim = regexp.MustCompile(`!+\s*$`)

// reEllipsis matches trailing ellipses (… or ...) — strong dramatic-pause cue.
var reEllipsis = regexp.MustCompile(`(\.{3}|…)\s*$`)

// heuristicTagSegment applies a fast, deterministic emotion + pause + pace
// labelling for each sentence. The LLM tier can override the result.
//
// Rules (in priority order):
//   - Ellipsis at end → emotion = dramatic, pre_pause = 600ms
//   - Question mark → emotion = curious
//   - Exclamation → emotion = excited
//   - Otherwise → mood-derived default (mood beats tone for body segments,
//     tone wins for hook/cta where the tone is the editorial intent)
//   - First sentence of a hook → +pre_pause 100ms (gentle lead-in)
//   - Last sentence of a CTA → emotion = excited (call-to-action energy)
func heuristicTagSegment(seg *models.ScriptSegment, tone string) []models.ScriptSentence {
	rawSentences := splitSentences(seg.Text)
	if len(rawSentences) == 0 {
		return nil
	}

	mood := strings.ToLower(strings.TrimSpace(seg.MusicMood))
	tn := strings.ToLower(strings.TrimSpace(tone))

	moodDefault, moodOK := moodToDefaultEmotion[mood]
	toneDefault, toneOK := toneToBias[tn]

	defaultEmotion := "neutral"
	switch {
	case seg.Type == "hook" && toneOK:
		defaultEmotion = toneDefault
	case moodOK:
		defaultEmotion = moodDefault
	case toneOK:
		defaultEmotion = toneDefault
	}

	tagged := make([]models.ScriptSentence, 0, len(rawSentences))
	for i, s := range rawSentences {
		st := models.ScriptSentence{Text: s, Emotion: defaultEmotion}

		// Punctuation overrides — these signal intent strongly enough to
		// trump the mood/tone default.
		switch {
		case reEllipsis.MatchString(s):
			st.Emotion = "dramatic"
			st.PrePauseMs = 600
		case reQuestion.MatchString(s):
			st.Emotion = "curious"
		case reExclaim.MatchString(s):
			st.Emotion = "excited"
		}

		// Last sentence of a CTA → call-to-action energy.
		if seg.Type == "cta" && i == len(rawSentences)-1 && st.Emotion == defaultEmotion {
			st.Emotion = "excited"
		}

		tagged = append(tagged, st)
	}
	return tagged
}

// TagSegmentEmotions populates seg.Sentences with per-sentence emotion +
// pacing hints. Runs the heuristic tier first, then optionally refines with
// a Groq call. Safe to call on a segment that already has Sentences — it
// will re-tag (use case: regenerate after a script edit).
//
// `tone` is payload.ScriptTone. Pass "" if unknown.
func TagSegmentEmotions(seg *models.ScriptSegment, tone string) {
	if seg == nil {
		return
	}
	tagged := heuristicTagSegment(seg, tone)
	if len(tagged) == 0 {
		seg.Sentences = nil
		return
	}

	// Try LLM refinement when available. If it fails or returns nothing
	// useful, the heuristic answer stands.
	if refined := llmTagSegment(seg, tone, tagged); len(refined) > 0 {
		tagged = refined
	}
	seg.Sentences = tagged
}

// TagScriptEmotions tags every segment in a script. Idempotent — if the
// script already has sentence tagging it gets refreshed (useful after
// script-refinement loops in the UI).
func TagScriptEmotions(script *models.ScriptDocument, tone string) {
	if script == nil {
		return
	}
	for i := range script.Segments {
		TagSegmentEmotions(&script.Segments[i], tone)
	}
}

// llmTagSegment asks Groq to refine the heuristic tags. The LLM sees both
// the raw sentences AND the heuristic guesses so it acts as a "second pass"
// rather than starting from scratch — this dramatically improves accuracy
// and lets us validate output against the heuristic count.
func llmTagSegment(seg *models.ScriptSegment, tone string, heuristic []models.ScriptSentence) []models.ScriptSentence {
	if !llmAvailable() {
		return nil
	}
	if len(heuristic) == 0 {
		return nil
	}

	// Build the prompt context. We send the heuristic guess inline so the
	// LLM has a starting point — much more reliable than asking it to label
	// from scratch.
	type sentenceForPrompt struct {
		Index            int      `json:"index"`
		Text             string   `json:"text"`
		HeuristicEmotion string   `json:"heuristic_emotion"`
	}
	prompt := make([]sentenceForPrompt, 0, len(heuristic))
	for i, s := range heuristic {
		prompt = append(prompt, sentenceForPrompt{
			Index:            i,
			Text:             s.Text,
			HeuristicEmotion: s.Emotion,
		})
	}
	promptJSON, _ := json.Marshal(prompt)

	systemPrompt := "You are a voice director tagging emotional delivery for a YouTube voiceover. " +
		"You return STRICT JSON only — no markdown, no commentary."
	userPrompt := fmt.Sprintf(`Segment type: %q
Segment music mood: %q
Script tone: %q
Sentences (with heuristic emotion guesses):
%s

For EACH sentence, return:
- emotion: ONE of [neutral, excited, dramatic, somber, curious, whispered, playful]
- pace: ONE of [slow, normal, fast] (optional — only when sentence calls for a non-default pace)
- emphasis: 0-2 words from the sentence to stress (must appear in the sentence text)
- pre_pause_ms: 0 | 200 | 400 | 600 | 800 — silence BEFORE this sentence

Rules:
- Vary the emotion across sentences. A whole segment of "dramatic" sounds flat.
- Reserve "whispered" for genuinely conspiratorial / secret moments.
- Use "curious" sparingly — usually only for rhetorical questions.
- Emphasis words must be SINGLE words copied verbatim from the sentence.

Return JSON array of shape:
[
  {"index": 0, "emotion": "...", "pace": "...", "emphasis": ["word"], "pre_pause_ms": 200},
  ...
]
Return EXACTLY %d items, one per input sentence, in the same order.`,
		seg.Type, seg.MusicMood, tone, string(promptJSON), len(heuristic))

	resp, err := callGroq(systemPrompt, userPrompt)
	if err != nil {
		log.Printf("emotion tagger: Groq call failed (using heuristic): %v", err)
		return nil
	}

	jsonStr := extractJSONArray(resp)
	if jsonStr == "" {
		return nil
	}

	var parsed []struct {
		Index      int      `json:"index"`
		Emotion    string   `json:"emotion"`
		Pace       string   `json:"pace"`
		Emphasis   []string `json:"emphasis"`
		PrePauseMs int      `json:"pre_pause_ms"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		log.Printf("emotion tagger: invalid Groq JSON (using heuristic): %v", err)
		return nil
	}

	// Re-key by index and rebuild in heuristic order so missing/duplicate
	// indices don't corrupt the output.
	byIdx := make(map[int]int, len(parsed))
	for i, p := range parsed {
		byIdx[p.Index] = i
	}

	out := make([]models.ScriptSentence, len(heuristic))
	for i, h := range heuristic {
		out[i] = h
		j, ok := byIdx[i]
		if !ok {
			continue
		}
		p := parsed[j]
		emotion := NormalizeEmotion(p.Emotion)
		if emotion != "" {
			out[i].Emotion = emotion
		}
		pace := strings.ToLower(strings.TrimSpace(p.Pace))
		if pace == "slow" || pace == "normal" || pace == "fast" {
			out[i].Pace = pace
		}
		if p.PrePauseMs > 0 {
			out[i].PrePauseMs = boundedPauseMs(p.PrePauseMs)
		}
		// Emphasis: drop anything that doesn't actually appear in the
		// sentence (LLMs occasionally invent words).
		if len(p.Emphasis) > 0 {
			lowText := strings.ToLower(h.Text)
			var keep []string
			for _, w := range p.Emphasis {
				w = strings.TrimSpace(w)
				if w == "" {
					continue
				}
				if strings.Contains(lowText, strings.ToLower(w)) {
					keep = append(keep, w)
					if len(keep) >= 2 {
						break
					}
				}
			}
			out[i].Emphasis = keep
		}
	}
	return out
}

// extractJSONArray finds the first [...] block in text. The LLM sometimes
// wraps responses in markdown despite being told not to; this strips it
// without panicking. Returns "" if no array can be found.
func extractJSONArray(text string) string {
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start >= 0 && end > start {
		return text[start : end+1]
	}
	return ""
}
