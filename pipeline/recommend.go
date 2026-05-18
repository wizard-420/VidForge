package pipeline

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"yt-automation-studio/config"
	"yt-automation-studio/models"
)

// llmAvailable reports whether Groq is configured. Used by both recommenders
// to short-circuit the LLM tier without attempting a network call when the
// Groq key is missing (or, defensively, when the config hasn't been loaded
// yet — unit tests can hit this path).
func llmAvailable() bool {
	if config.App == nil {
		return false
	}
	return strings.TrimSpace(config.App.GroqAPIKey) != ""
}

// This file holds the "what should we suggest to the user?" helpers used by
// the recommendation API endpoints. Each recommender has the same shape:
//
//   1) Build a heuristic answer from script fields we already have
//      (script_tone, per-segment MusicMood, etc.). This is deterministic
//      and runs without any network calls.
//   2) Optionally upgrade the heuristic answer with a Groq LLM call. When
//      Groq is unavailable or returns garbage, we fall back to the
//      heuristic so the UI never breaks.
//
// All exported types here are serialised straight to JSON for the API
// responses — keep field names stable.

// ---------- Music recommendation ----------

// MusicRecommendation is the API response shape for
// POST /api/music/recommend-search.
type MusicRecommendation struct {
	DominantMood   string   `json:"dominant_mood"`              // most frequent per-segment MusicMood
	SecondaryMood  string   `json:"secondary_mood,omitempty"`   // second most frequent (if any)
	MoodVariance   bool     `json:"mood_variance"`              // true when no single mood dominates
	Queries        []string `json:"queries"`                    // ranked Jamendo search strings to try
	Keywords       []string `json:"keywords,omitempty"`         // mood/genre keywords for chips/badges
	Avoid          []string `json:"avoid,omitempty"`            // music styles that would clash
	Explanation    string   `json:"explanation,omitempty"`      // human-readable "why" (LLM only)
	Source         string   `json:"source"`                     // "heuristic" | "llm" | "llm+heuristic"
}

// mood -> ranked Jamendo-friendly search keywords. These are tuned for the
// kinds of YouTube videos this app produces: faceless narration over
// stock/AI visuals. We prefer instrumental keywords (Jamendo's search +
// our vocalinstrumental=instrumental filter does most of the work).
var musicMoodKeywords = map[string][]string{
	"dramatic":    {"cinematic orchestral", "epic strings drama", "dramatic trailer", "emotional orchestral"},
	"calm":        {"ambient piano", "peaceful meditation", "soft acoustic", "minimal calm"},
	"upbeat":      {"uplifting corporate", "motivational pop", "happy acoustic", "feel good indie"},
	"mysterious":  {"dark ambient", "tension drone", "suspenseful underscore", "mystery cinematic"},
	"suspenseful": {"tension cinematic", "dark suspense", "thriller score", "anxious ambient"},
	"motivational": {"epic motivational", "uplifting strings", "heroic orchestral", "anthemic"},
	"educational": {"corporate background", "documentary underscore", "thoughtful ambient", "neutral acoustic"},
	"conversational": {"lo-fi calm", "warm acoustic", "easy listening", "chill background"},
	"humorous":    {"playful ukulele", "quirky indie", "comedic light", "whimsical"},
	"epic":        {"epic cinematic", "powerful orchestral", "anthemic", "grand drama"},
	"sad":         {"emotional piano", "melancholic strings", "somber ambient", "reflective"},
}

// moods that should be avoided when the dominant mood is X (anti-pairs).
// Surfacing these in the UI helps the user understand why we didn't suggest
// e.g. "happy ukulele" for a dramatic-mystery script.
var musicMoodAvoid = map[string][]string{
	"dramatic":     {"happy", "comedic", "playful"},
	"calm":         {"epic", "aggressive", "fast tempo"},
	"upbeat":       {"sad", "dark ambient", "horror"},
	"mysterious":   {"happy", "uplifting", "playful"},
	"suspenseful":  {"happy", "comedic"},
	"motivational": {"sad", "melancholic"},
	"educational":  {"epic trailer", "aggressive"},
	"conversational": {"epic trailer", "aggressive"},
	"humorous":     {"sad", "dark ambient"},
	"sad":          {"happy", "comedic", "upbeat"},
}

// RecommendMusicSearch produces ranked Jamendo search queries + a short
// explanation for the script. It first builds a heuristic answer from the
// per-segment moods, then asks Groq for a refined version. The heuristic
// remains the authoritative shape if Groq fails or returns invalid JSON.
//
// `tone` is the user-selected script tone (state.script_tone). It can be
// empty.
func RecommendMusicSearch(script *models.ScriptDocument, tone string) *MusicRecommendation {
	rec := heuristicMusicRecommendation(script, tone)

	// Try LLM polish on top of the heuristic. If it fails, the heuristic
	// answer stands on its own.
	if llm := llmMusicRecommendation(script, tone, rec); llm != nil {
		// Merge: LLM-provided fields override heuristic, but we keep the
		// heuristic queries as fallbacks if the LLM didn't return any.
		if len(llm.Queries) == 0 {
			llm.Queries = rec.Queries
		}
		if len(llm.Keywords) == 0 {
			llm.Keywords = rec.Keywords
		}
		if llm.DominantMood == "" {
			llm.DominantMood = rec.DominantMood
			llm.SecondaryMood = rec.SecondaryMood
			llm.MoodVariance = rec.MoodVariance
		}
		llm.Source = "llm+heuristic"
		return llm
	}
	return rec
}

func heuristicMusicRecommendation(script *models.ScriptDocument, tone string) *MusicRecommendation {
	rec := &MusicRecommendation{Source: "heuristic"}
	if script == nil {
		return rec
	}

	counts := map[string]int{}
	for _, seg := range script.Segments {
		m := strings.ToLower(strings.TrimSpace(seg.MusicMood))
		if m == "" {
			continue
		}
		counts[m]++
	}

	type moodCount struct {
		mood  string
		count int
	}
	var ranked []moodCount
	for m, c := range counts {
		ranked = append(ranked, moodCount{m, c})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].mood < ranked[j].mood
	})

	if len(ranked) > 0 {
		rec.DominantMood = ranked[0].mood
	}
	if len(ranked) > 1 {
		rec.SecondaryMood = ranked[1].mood
		// "Variance" means no single mood is dominant — useful UI hint.
		if ranked[0].count <= len(script.Segments)/2 {
			rec.MoodVariance = true
		}
	}

	// Fall back to user tone when the script has no labelled moods (rare).
	if rec.DominantMood == "" {
		rec.DominantMood = strings.ToLower(strings.TrimSpace(tone))
	}

	// Build queries: dominant mood first, then secondary, then tone-flavoured
	// variants. Dedup while preserving order.
	seen := map[string]bool{}
	push := func(q string) {
		q = strings.TrimSpace(q)
		if q == "" || seen[q] {
			return
		}
		seen[q] = true
		rec.Queries = append(rec.Queries, q)
	}
	for _, q := range musicMoodKeywords[rec.DominantMood] {
		push(q)
	}
	for _, q := range musicMoodKeywords[rec.SecondaryMood] {
		push(q)
	}
	// Tone-blended query — gives the user one obviously "matches my script"
	// option that pairs mood with tone.
	if tone != "" && rec.DominantMood != "" && tone != rec.DominantMood {
		push(strings.ToLower(tone) + " " + rec.DominantMood)
	}

	// Last-resort generic options so the UI never shows zero chips.
	if len(rec.Queries) == 0 {
		rec.Queries = []string{"cinematic background", "ambient corporate", "uplifting acoustic"}
	}
	if len(rec.Queries) > 5 {
		rec.Queries = rec.Queries[:5]
	}

	// Keywords are the dedup'd mood labels for badges.
	if rec.DominantMood != "" {
		rec.Keywords = append(rec.Keywords, rec.DominantMood)
	}
	if rec.SecondaryMood != "" && rec.SecondaryMood != rec.DominantMood {
		rec.Keywords = append(rec.Keywords, rec.SecondaryMood)
	}
	if tone != "" {
		rec.Keywords = append(rec.Keywords, strings.ToLower(tone))
	}
	rec.Avoid = musicMoodAvoid[rec.DominantMood]
	return rec
}

func llmMusicRecommendation(script *models.ScriptDocument, tone string, heuristic *MusicRecommendation) *MusicRecommendation {
	if !llmAvailable() {
		return nil
	}
	digest := scriptDigest(script, 1200)
	if digest == "" {
		return nil
	}
	systemPrompt := "You are a music supervisor picking background music for a YouTube voiceover video. " +
		"You return STRICT JSON only — no markdown, no commentary."
	userPrompt := fmt.Sprintf(`Script tone: %q
Per-segment moods (most frequent first): %s
Script excerpt:
%s

Pick 3-5 ranked instrumental Jamendo search queries that would work as the SINGLE background music track for this video. The narration is dominant; the music must support, not compete. Prefer 2-3 word queries.

Respond with JSON of shape:
{
  "queries": ["...", "..."],
  "keywords": ["mood1", "genre2"],
  "avoid": ["style we don't want", "..."],
  "explanation": "One short paragraph (max 60 words) explaining why these fit the script."
}`,
		tone, strings.Join(heuristic.Keywords, ", "), digest)

	resp, err := callGroq(systemPrompt, userPrompt)
	if err != nil {
		return nil
	}
	jsonStr := extractJSON(resp)
	var parsed struct {
		Queries     []string `json:"queries"`
		Keywords    []string `json:"keywords"`
		Avoid       []string `json:"avoid"`
		Explanation string   `json:"explanation"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return nil
	}
	if len(parsed.Queries) == 0 && parsed.Explanation == "" {
		return nil
	}
	// Trim, dedup, cap.
	out := &MusicRecommendation{
		Queries:     dedupTrim(parsed.Queries, 5),
		Keywords:    dedupTrim(parsed.Keywords, 6),
		Avoid:       dedupTrim(parsed.Avoid, 5),
		Explanation: strings.TrimSpace(parsed.Explanation),
		Source:      "llm",
	}
	return out
}

// ---------- Voice recommendation ----------

// VoiceCandidate is one ranked voice in a VoiceRecommendation.
type VoiceCandidate struct {
	Name   string `json:"name"`             // exact voice name from /api/gcp-tts/voices
	Gender string `json:"gender,omitempty"` // "MALE" | "FEMALE" | "NEUTRAL"
	Family string `json:"family,omitempty"` // "Chirp3-HD" | "Studio" | "Neural2" | ...
	Reason string `json:"reason,omitempty"` // short "why this voice"
}

// VoiceRecommendation is the API response shape for
// POST /api/tts/recommend-voice.
type VoiceRecommendation struct {
	Character    string           `json:"character"`     // "Deep, authoritative", "Warm, conversational", ...
	GenderHint   string           `json:"gender_hint"`   // "male" | "female" | "either"
	Voices       []VoiceCandidate `json:"voices"`        // top 3 picks, best first
	Reason       string           `json:"reason"`        // overall explanation
	Source       string           `json:"source"`        // "heuristic" | "llm+heuristic"
	NoteForUser  string           `json:"note,omitempty"` // optional caveat (e.g. premium-only)
}

type toneVoicePreference struct {
	character  string
	genderHint string // "male" | "female" | "either"
	// preferredNameTokens are case-insensitive substrings we score voices on.
	// Google's voice naming gives hints — e.g. "Studio-O" tends to be deep,
	// "Wavenet-D" warmer, "Neural2-J" more friendly. The list is curated;
	// it doesn't need to be exhaustive because the LLM tier handles nuance.
	preferredNameTokens []string
}

var tonePrefs = map[string]toneVoicePreference{
	"dramatic":      {character: "Deep, authoritative narrator", genderHint: "male", preferredNameTokens: []string{"Studio-Q", "Studio-O", "Chirp3-HD-Charon", "Wavenet-D", "Neural2-D", "Neural2-J"}},
	"educational":   {character: "Warm, clear, neutral", genderHint: "either", preferredNameTokens: []string{"Neural2-F", "Neural2-C", "Studio-O", "Wavenet-F", "Chirp3-HD-Aoede"}},
	"conversational": {character: "Friendly, natural, mid-range", genderHint: "either", preferredNameTokens: []string{"Neural2-J", "Neural2-G", "Wavenet-C", "Chirp3-HD-Puck"}},
	"suspenseful":   {character: "Whispery, dark, controlled", genderHint: "either", preferredNameTokens: []string{"Studio-Q", "Wavenet-B", "Neural2-D", "Chirp3-HD-Charon"}},
	"motivational":  {character: "Strong, confident, present", genderHint: "either", preferredNameTokens: []string{"Studio-O", "Neural2-J", "Wavenet-D", "Chirp3-HD-Charon", "Chirp3-HD-Kore"}},
	"humorous":      {character: "Animated, energetic, expressive", genderHint: "either", preferredNameTokens: []string{"Neural2-G", "Wavenet-A", "Chirp3-HD-Puck"}},
}

func familyOfVoice(name string) string {
	switch {
	case strings.Contains(name, "Chirp3-HD"):
		return "Chirp3-HD"
	case strings.Contains(name, "Studio"):
		return "Studio"
	case strings.Contains(name, "Chirp-HD"):
		return "Chirp-HD"
	case strings.Contains(name, "Neural2"):
		return "Neural2"
	case strings.Contains(name, "News"):
		return "News"
	case strings.Contains(name, "Casual"):
		return "Casual"
	case strings.Contains(name, "Polyglot"):
		return "Polyglot"
	case strings.Contains(name, "Wavenet"):
		return "Wavenet"
	case strings.Contains(name, "Standard"):
		return "Standard"
	}
	return ""
}

// familyRank ranks voice families from "best for narration" to "fallback".
// Lower number = better. Premium families (Chirp3-HD, Studio) only get the
// top ranks when the deployment actually has SA configured — the caller
// passes saAvailable to keep ranking honest.
func familyRank(family string, saAvailable bool) int {
	switch family {
	case "Chirp3-HD":
		if saAvailable {
			return 0
		}
		return 99
	case "Studio":
		if saAvailable {
			return 1
		}
		return 99
	case "Chirp-HD":
		return 2
	case "Neural2":
		return 3
	case "Wavenet":
		return 4
	case "Casual", "News", "Polyglot":
		return 5
	case "Standard":
		return 6
	}
	return 8
}

// RecommendTTSVoice scores the supplied voice catalog against the script's
// tone and (optionally) content, then returns the top 3 picks. The LLM tier
// is consulted opportunistically — the heuristic alone always produces a
// usable answer.
func RecommendTTSVoice(script *models.ScriptDocument, tone string, voices []GCPVoice, saAvailable bool) *VoiceRecommendation {
	rec := heuristicVoiceRecommendation(script, tone, voices, saAvailable)

	if llm := llmVoiceRecommendation(script, tone, voices, saAvailable); llm != nil {
		// LLM gives us character/gender hint + ordered voice names. We
		// re-validate each name against the catalog (LLM might hallucinate)
		// and fall back to the heuristic's picks for any unknown ones.
		validated := pickValidatedVoices(llm.Voices, voices, saAvailable, 3)
		if len(validated) > 0 {
			llm.Voices = validated
			// Backfill missing fields from heuristic for safety.
			if llm.Character == "" {
				llm.Character = rec.Character
			}
			if llm.GenderHint == "" {
				llm.GenderHint = rec.GenderHint
			}
			if llm.NoteForUser == "" {
				llm.NoteForUser = rec.NoteForUser
			}
			llm.Source = "llm+heuristic"
			return llm
		}
	}
	return rec
}

func heuristicVoiceRecommendation(script *models.ScriptDocument, tone string, voices []GCPVoice, saAvailable bool) *VoiceRecommendation {
	rec := &VoiceRecommendation{Source: "heuristic"}
	if len(voices) == 0 {
		rec.Reason = "No voices available to rank."
		return rec
	}

	tn := strings.ToLower(strings.TrimSpace(tone))
	pref, ok := tonePrefs[tn]
	if !ok {
		pref = tonePrefs["conversational"]
	}
	rec.Character = pref.character
	rec.GenderHint = pref.genderHint

	// Score every voice; lower score wins.
	type scored struct {
		voice GCPVoice
		score float64
	}
	all := make([]scored, 0, len(voices))
	for _, v := range voices {
		family := familyOfVoice(v.Name)
		score := float64(familyRank(family, saAvailable)) * 10

		// Gender match bonus
		gender := strings.ToUpper(v.SSMLGender)
		if pref.genderHint == "male" && gender == "MALE" {
			score -= 3
		} else if pref.genderHint == "female" && gender == "FEMALE" {
			score -= 3
		} else if pref.genderHint == "male" && gender == "FEMALE" {
			score += 1
		} else if pref.genderHint == "female" && gender == "MALE" {
			score += 1
		}

		// Token match bonus — best matches stack
		matched := 0
		for i, tok := range pref.preferredNameTokens {
			if tok != "" && strings.Contains(v.Name, tok) {
				score -= 5 - float64(i)*0.3
				matched++
				if matched >= 2 {
					break
				}
			}
		}

		// Premium voices without SA: heavily penalise so they only show as
		// last resorts and the UI can warn the user.
		if v.Premium && !saAvailable {
			score += 100
		}

		all = append(all, scored{v, score})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score < all[j].score
		}
		// Stable tie-break by name for deterministic results.
		return all[i].voice.Name < all[j].voice.Name
	})

	picked := 0
	usedFamilies := map[string]bool{}
	for _, s := range all {
		family := familyOfVoice(s.voice.Name)
		// Prefer at most 2 voices per family so the user sees variety.
		if usedFamilies[family] && picked > 0 && len(rec.Voices) >= 1 && family == familyOfVoice(rec.Voices[0].Name) {
			continue
		}
		reason := voiceHeuristicReason(s.voice, family, pref)
		rec.Voices = append(rec.Voices, VoiceCandidate{
			Name:   s.voice.Name,
			Gender: s.voice.SSMLGender,
			Family: family,
			Reason: reason,
		})
		usedFamilies[family] = true
		picked++
		if picked >= 3 {
			break
		}
	}

	rec.Reason = fmt.Sprintf("Based on your %q tone, we suggest a %s voice. "+
		"Top picks come from %s families.",
		tone, strings.ToLower(pref.character), describePickedFamilies(rec.Voices))

	if !saAvailable {
		// All premium-only voices got penalised — surface the caveat once.
		hasPremiumDowngrade := false
		for _, v := range voices {
			if v.Premium {
				hasPremiumDowngrade = true
				break
			}
		}
		if hasPremiumDowngrade {
			rec.NoteForUser = "Premium voice families (Chirp 3 HD, Studio) are excluded because no Google Cloud service account is configured. Add credentials to .env to unlock them."
		}
	}
	return rec
}

func voiceHeuristicReason(v GCPVoice, family string, pref toneVoicePreference) string {
	parts := []string{}
	if family != "" {
		parts = append(parts, family+" voice")
	}
	gender := strings.ToLower(v.SSMLGender)
	if gender != "" && gender != "ssml_voice_gender_unspecified" {
		parts = append(parts, strings.ToLower(gender))
	}
	for _, tok := range pref.preferredNameTokens {
		if tok != "" && strings.Contains(v.Name, tok) {
			parts = append(parts, "known to suit "+strings.ToLower(pref.character))
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	out := strings.Join(parts, " · ")
	return strings.ToUpper(out[:1]) + out[1:]
}

func describePickedFamilies(picked []VoiceCandidate) string {
	families := map[string]bool{}
	var ordered []string
	for _, v := range picked {
		if v.Family == "" || families[v.Family] {
			continue
		}
		families[v.Family] = true
		ordered = append(ordered, v.Family)
	}
	if len(ordered) == 0 {
		return "available"
	}
	return strings.Join(ordered, " / ")
}

func llmVoiceRecommendation(script *models.ScriptDocument, tone string, voices []GCPVoice, saAvailable bool) *VoiceRecommendation {
	if !llmAvailable() {
		return nil
	}
	digest := scriptDigest(script, 1000)
	if digest == "" || len(voices) == 0 {
		return nil
	}

	// Build a compact catalog so we don't blow the context window on
	// hundreds of voices. We give the LLM enough hints (family + gender)
	// to pick well.
	type catEntry struct {
		Name    string `json:"name"`
		Gender  string `json:"gender"`
		Family  string `json:"family"`
		Premium bool   `json:"premium"`
	}
	catalog := make([]catEntry, 0, len(voices))
	for _, v := range voices {
		if v.Premium && !saAvailable {
			continue // skip voices the user can't actually use
		}
		catalog = append(catalog, catEntry{
			Name:    v.Name,
			Gender:  v.SSMLGender,
			Family:  familyOfVoice(v.Name),
			Premium: v.Premium,
		})
		if len(catalog) >= 80 {
			break // cap to keep prompt short
		}
	}
	catalogJSON, _ := json.Marshal(catalog)

	systemPrompt := "You are a casting director picking a voiceover voice for a YouTube video. " +
		"Return STRICT JSON only — no commentary, no markdown."
	userPrompt := fmt.Sprintf(`Script tone: %q
Available voices (subset of Google Cloud TTS catalog):
%s

Script excerpt:
%s

Rules:
- Pick exactly 3 voice names from the AVAILABLE list. Do NOT invent voice names.
- Order them best-first.
- Briefly explain why each fits.
- Suggest a gender_hint ("male"|"female"|"either") that best suits the script.

Return JSON of shape:
{
  "character": "Short adjective phrase, e.g. 'Deep, authoritative narrator'",
  "gender_hint": "male|female|either",
  "voices": [
    {"name": "exact-voice-name", "reason": "why this voice (max 18 words)"},
    {"name": "...", "reason": "..."},
    {"name": "...", "reason": "..."}
  ],
  "reason": "One-sentence overall reasoning (max 30 words)"
}`,
		tone, string(catalogJSON), digest)

	resp, err := callGroq(systemPrompt, userPrompt)
	if err != nil {
		return nil
	}
	jsonStr := extractJSON(resp)
	var parsed struct {
		Character  string `json:"character"`
		GenderHint string `json:"gender_hint"`
		Voices     []struct {
			Name   string `json:"name"`
			Reason string `json:"reason"`
		} `json:"voices"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return nil
	}
	rec := &VoiceRecommendation{
		Character:  strings.TrimSpace(parsed.Character),
		GenderHint: strings.ToLower(strings.TrimSpace(parsed.GenderHint)),
		Reason:     strings.TrimSpace(parsed.Reason),
		Source:     "llm",
	}
	for _, v := range parsed.Voices {
		rec.Voices = append(rec.Voices, VoiceCandidate{
			Name:   strings.TrimSpace(v.Name),
			Reason: strings.TrimSpace(v.Reason),
		})
	}
	return rec
}

// pickValidatedVoices keeps only candidates whose name actually exists in
// the live voice catalog, fills in Family/Gender from the catalog entry,
// and caps to `limit`. Drops premium voices when SA is unavailable so the
// LLM can't recommend something the user can't use.
func pickValidatedVoices(candidates []VoiceCandidate, voices []GCPVoice, saAvailable bool, limit int) []VoiceCandidate {
	byName := make(map[string]GCPVoice, len(voices))
	for _, v := range voices {
		byName[v.Name] = v
	}
	var out []VoiceCandidate
	for _, c := range candidates {
		v, ok := byName[c.Name]
		if !ok {
			continue
		}
		if v.Premium && !saAvailable {
			continue
		}
		c.Gender = v.SSMLGender
		c.Family = familyOfVoice(v.Name)
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// ---------- Shared helpers ----------

// scriptDigest builds a compact text representation of the script for the
// LLM. We include the hook + first few segments + tone — enough signal,
// short enough to keep prompts cheap.
func scriptDigest(script *models.ScriptDocument, maxChars int) string {
	if script == nil {
		return ""
	}
	var b strings.Builder
	if script.Hook != "" {
		b.WriteString("HOOK: ")
		b.WriteString(script.Hook)
		b.WriteString("\n\n")
	}
	for i, seg := range script.Segments {
		if i >= 6 {
			break
		}
		fmt.Fprintf(&b, "SEG %d (%s, mood=%s): ", seg.SegmentID, seg.Type, seg.MusicMood)
		text := seg.Text
		if len(text) > 240 {
			text = text[:240] + "…"
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	s := strings.TrimSpace(b.String())
	if len(s) > maxChars {
		s = s[:maxChars] + "…"
	}
	return s
}

// dedupTrim removes duplicates (case-insensitive) and caps the slice length.
func dedupTrim(in []string, max int) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		k := strings.ToLower(strings.TrimSpace(s))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, strings.TrimSpace(s))
		if len(out) >= max {
			break
		}
	}
	return out
}
