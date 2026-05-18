package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yt-automation-studio/config"
	"yt-automation-studio/models"
)

// RunScriptGenerator executes Stage 2: generate a full ScriptDocument using Groq AI
func RunScriptGenerator(job *models.JobContext, progress ProgressFunc) error {
	payload := job.Payload

	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 2, StageName: "Script Generation",
		ProgressPct: 10, Message: "Crafting script prompt...",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	// Calculate target word count
	wordCount := payload.DurationMin * 130 // 130 wpm average TTS pace
	if payload.Format == "short" {
		wordCount = 120 // fixed for shorts
	}

	var script *models.ScriptDocument
	var err error

	if payload.PreGeneratedScript != nil {
		// Manual voiceover mode: use the pre-generated script from the preview step
		script = payload.PreGeneratedScript
		script.JobID = job.JobID
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 2, StageName: "Script Generation",
			ProgressPct: 90, Message: "Using pre-generated script from manual recording...",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	} else {
		// Generate long-form script
		if payload.Format == "long" || payload.Format == "both" {
			progress(models.ProgressEvent{
				JobID: job.JobID, Stage: 2, StageName: "Script Generation",
				ProgressPct: 30, Message: "Generating long-form script with Groq AI...",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})

			script, err = generateLongScript(payload.RawInput, payload.DurationMin, wordCount, payload.ScriptTone, payload.Language, payload.ClipCount, payload.ImageCount, payload.SecondsPerVisual)
			if err != nil {
				return fmt.Errorf("long script generation: %w", err)
			}
			script.JobID = job.JobID
			script.Format = "long"
		}

		// Generate short-form script
		if payload.Format == "short" || payload.Format == "both" {
			progress(models.ProgressEvent{
				JobID: job.JobID, Stage: 2, StageName: "Script Generation",
				ProgressPct: 70, Message: "Generating Shorts version...",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})

			shortScript, err := generateShortScript(payload.RawInput, payload.ScriptTone, payload.Language, payload.ClipCount, payload.ImageCount, payload.SecondsPerVisual)
			if err != nil {
				// Non-fatal: log and continue with long only
				job.AddError(fmt.Sprintf("Short script generation failed: %v", err))
			} else {
				if payload.Format == "short" {
					script = shortScript
					script.JobID = job.JobID
					script.Format = "short"
				} else {
					if script == nil {
						script = &models.ScriptDocument{JobID: job.JobID, Format: "both"}
					}
					script.ShortVersion = &models.ShortScript{
						Hook:          shortScript.Hook,
						Segments:      shortScript.Segments,
						TotalDuration: shortScript.TotalDuration,
					}
				}
			}
		}
	}

	if script == nil {
		return fmt.Errorf("no script generated")
	}

	// Pacing safety net: even though we instruct the LLM to follow the pacing
	// guideline, it doesn't always obey perfectly. Enforce it here so the
	// user's "seconds per visual" choice is respected regardless of LLM output.
	enforceVisualPacing(script, payload.SecondsPerVisual)

	// Compute totals
	script.TotalSegments = len(script.Segments)
	totalDur := 0
	for _, seg := range script.Segments {
		totalDur += seg.DurationSec
	}
	script.TotalDuration = totalDur

	// Store in job context
	job.Script = script

	// Save script to workspace
	jobDir := filepath.Join(config.App.WorkspaceDir, fmt.Sprintf("job_%s", job.JobID))
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		return fmt.Errorf("create job directory: %w", err)
	}

	scriptJSON, _ := json.MarshalIndent(script, "", "  ")
	if err := os.WriteFile(filepath.Join(jobDir, "script.json"), scriptJSON, 0644); err != nil {
		return fmt.Errorf("save script: %w", err)
	}

	progress(models.ProgressEvent{
		JobID: job.JobID, Stage: 2, StageName: "Script Generation",
		ProgressPct: 100, Message: fmt.Sprintf("Script generated: %d segments, ~%d min", script.TotalSegments, script.TotalDuration/60),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	return nil
}

func generateLongScript(topic string, durationMin, wordCount int, tone, language string, clipCount, imageCount, secondsPerVisual int) (*models.ScriptDocument, error) {
	totalVisuals := clipCount + imageCount
	if secondsPerVisual <= 0 {
		secondsPerVisual = 6
	}
	systemPrompt := fmt.Sprintf(`You are an expert YouTube scriptwriter for faceless channels. You write scripts that:
- Open with a 5-second hook that creates immediate curiosity
- Use short, punchy sentences optimised for text-to-speech voiceover
- Maintain a %s tone throughout
- Include dramatic pauses (marked as [PAUSE])
- End with a strong CTA (like, subscribe, comment)
- For each segment, provide fine-grained sub_visuals at a calm, natural pace — visuals should change roughly once every %d seconds, NOT faster.

IMPORTANT: Return ONLY valid JSON, no markdown code blocks, no explanations.`, tone, secondsPerVisual)

	userPrompt := fmt.Sprintf(`Write a complete YouTube script for this topic: "%s"
Target duration: %d minutes (~%d words at 130 words/minute)
Tone: %s
Language: %s

Return ONLY valid JSON matching this exact schema:
{
  "title_options": ["Title A", "Title B", "Title C"],
  "description": "Full YouTube description with timestamps and links",
  "tags": ["tag1", "tag2", ...up to 15 tags],
  "thumbnail_text": "Short punchy text for thumbnail",
  "hook": "Opening 5-second line to grab attention",
  "segments": [
    {
      "segment_id": 1,
      "type": "hook",
      "text": "Full narration text for this segment",
      "word_count": 120,
      "duration_sec": 45,
      "visual_cue": "Primary visual description",
      "visual_query": "primary search query",
      "music_mood": "dramatic",
      "transition": "fade",
      "sub_visuals": [
        {"index": 0, "query": "ancient rome colosseum aerial", "description": "Aerial view of the Roman Colosseum", "type": "clip"},
        {"index": 1, "query": "roman soldiers marching", "description": "Roman soldiers in formation", "type": "clip"},
        {"index": 2, "query": "ancient rome painting dramatic", "description": "Dramatic painting of ancient Rome", "type": "image"}
      ]
    }
  ]
}

VISUAL PACING RULES (most important):
- Aim for ONE sub_visual roughly every %d seconds of narration. Do NOT cram in more.
- For a segment of duration D seconds, generate approximately ceil(D / %d) sub_visuals (e.g. a 30s segment → ~%d sub_visuals at this pacing).
- Total budget across the entire script: about %d sub_visuals (%d clips + %d images). NEVER exceed this; fewer is fine if it serves the narrative.
- Hook and CTA segments should have FEWER sub_visuals (1-2 each) to let the message land.
- Body segments scale with their length using the pacing rule above.

OTHER VISUAL RULES:
- Each sub_visual query MUST be 2-4 words optimized for Pexels stock video search.
- Each sub_visual MUST semantically match the SPECIFIC words being narrated at that point in the segment text.
  Example: If the text mentions "Mars has a red surface", the sub_visual at that point must be "mars red surface planet".
- Set type to "clip" for stock footage, "image" for AI-generated images.
- NEVER repeat the same query across sub_visuals. Each must be unique.
- ALWAYS include the main subject in every query (e.g., "mars red surface" not just "red surface").

Generate at least %d segments to fill %d minutes. Each segment should be 30-90 seconds.
The first segment type must be "hook", the last must be "cta", and all others "body".`,
		topic, durationMin, wordCount, tone, language,
		secondsPerVisual, secondsPerVisual, max(30/secondsPerVisual, 1),
		totalVisuals, clipCount, imageCount,
		max(durationMin/2, 4), durationMin)

	return callGroqForScript(systemPrompt, userPrompt)
}

func generateShortScript(topic, tone, language string, clipCount, imageCount, secondsPerVisual int) (*models.ScriptDocument, error) {
	totalVisuals := clipCount + imageCount
	if secondsPerVisual <= 0 {
		secondsPerVisual = 4 // shorts default a bit faster than long-form
	}
	systemPrompt := fmt.Sprintf(`You are an expert YouTube Shorts scriptwriter for faceless channels. You write viral short-form scripts that:
- Hook must be in the FIRST 3 words (no slow intros)
- Maximum 130 words total
- One single revelation or fact that makes the viewer share it
- End with a direct question to drive comments
- Maintain a %s tone
- Provide sub_visuals at a deliberate pace — visuals change roughly every %d seconds, NOT every word.

IMPORTANT: Return ONLY valid JSON, no markdown code blocks, no explanations.`, tone, secondsPerVisual)

	userPrompt := fmt.Sprintf(`Write a 45-60 second YouTube Shorts script for: "%s"
Tone: %s
Language: %s

Rules:
- Hook must be in the FIRST 3 words (no slow intros)
- Maximum 130 words total
- One single revelation or fact that makes the viewer share it
- End with a direct question to drive comments

Return ONLY valid JSON matching this schema:
{
  "title_options": ["Title A", "Title B", "Title C"],
  "description": "Short YouTube description",
  "tags": ["tag1", "tag2", ...up to 15 tags],
  "thumbnail_text": "Short punchy text",
  "hook": "Opening line",
  "segments": [
    {
      "segment_id": 1,
      "type": "hook",
      "text": "...",
      "word_count": 30,
      "duration_sec": 12,
      "visual_cue": "...",
      "visual_query": "...",
      "music_mood": "dramatic",
      "transition": "cut",
      "sub_visuals": [
        {"index": 0, "query": "space nebula galaxy colorful", "description": "Colorful space nebula", "type": "clip"},
        {"index": 1, "query": "earth rotating planet blue", "description": "Earth rotating in space", "type": "clip"}
      ]
    }
  ]
}

VISUAL PACING RULES (most important):
- Aim for ONE sub_visual roughly every %d seconds. Do NOT cram in more.
- For a segment of duration D seconds, generate approximately ceil(D / %d) sub_visuals.
- Total budget: about %d sub_visuals (%d clips + %d images). NEVER exceed this.

OTHER VISUAL RULES:
- Each sub_visual query MUST be 2-4 words optimized for Pexels stock video search.
- Each sub_visual MUST semantically match the SPECIFIC words being narrated at that point.
- Set type to "clip" for stock footage, "image" for AI-generated images.
- NEVER repeat the same query. Each must be unique.
- ALWAYS include the main subject in every query.

Generate 4-6 segments. Each segment should be 8-15 seconds. Total duration 45-60 seconds.`, topic, tone, language,
		secondsPerVisual, secondsPerVisual,
		totalVisuals, clipCount, imageCount)

	return callGroqForScript(systemPrompt, userPrompt)
}

// enforceVisualPacing trims each segment's sub_visuals so the user's
// "seconds per visual" choice is respected even when the LLM ignores the
// pacing instruction. For each segment, the maximum number of sub_visuals
// allowed is ceil(segDuration / secondsPerVisual), tweaked per segment type
// (hook/CTA get fewer, body uses the raw rule). When the LLM produced more
// than the cap, we keep an evenly-spaced subset to preserve narrative
// coverage rather than just slicing off the tail.
func enforceVisualPacing(script *models.ScriptDocument, secondsPerVisual int) {
	if script == nil || secondsPerVisual <= 0 {
		return
	}
	for i, seg := range script.Segments {
		if len(seg.SubVisuals) == 0 {
			continue
		}
		segDur := seg.DurationSec
		if segDur <= 0 {
			segDur = 30 // sensible fallback
		}

		// Per-segment-type pacing tweak: hooks and CTAs deserve breathing room.
		segPace := secondsPerVisual
		switch seg.Type {
		case "hook", "cta":
			segPace = secondsPerVisual * 3 / 2 // ~50% slower
		}
		if segPace < 1 {
			segPace = 1
		}

		maxAllowed := segDur / segPace
		if segDur%segPace != 0 {
			maxAllowed++
		}
		if maxAllowed < 1 {
			maxAllowed = 1
		}
		// Hard ceiling for hook/CTA: at most 2 visuals each.
		if (seg.Type == "hook" || seg.Type == "cta") && maxAllowed > 2 {
			maxAllowed = 2
		}

		if len(seg.SubVisuals) <= maxAllowed {
			continue
		}

		// Pick maxAllowed items at evenly-spaced indices so we cover beginning,
		// middle, and end of the segment narrative.
		original := seg.SubVisuals
		kept := make([]models.SubVisual, 0, maxAllowed)
		n := len(original)
		for k := 0; k < maxAllowed; k++ {
			idx := k * n / maxAllowed
			if idx >= n {
				idx = n - 1
			}
			sv := original[idx]
			sv.Index = k
			kept = append(kept, sv)
		}
		script.Segments[i].SubVisuals = kept
	}
}

func callGroqForScript(systemPrompt, userPrompt string) (*models.ScriptDocument, error) {
	// Retry up to 3 times with back-off
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := callGroq(systemPrompt, userPrompt)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt*5) * time.Second)
			continue
		}

		// Extract JSON from response
		jsonStr := extractJSON(resp)

		// Try to parse as ScriptDocument
		var script models.ScriptDocument
		if err := json.Unmarshal([]byte(jsonStr), &script); err != nil {
			lastErr = fmt.Errorf("invalid JSON from Groq (attempt %d): %w\nRaw: %s", attempt, err, truncate(resp, 200))
			time.Sleep(time.Duration(attempt*5) * time.Second)
			continue
		}

		// Validate minimum requirements
		if len(script.Segments) == 0 {
			lastErr = fmt.Errorf("script has no segments (attempt %d)", attempt)
			continue
		}
		if len(script.TitleOptions) == 0 {
			lastErr = fmt.Errorf("script has no title options (attempt %d)", attempt)
			continue
		}

		return &script, nil
	}

	return nil, fmt.Errorf("script generation failed after 3 attempts: %w", lastErr)
}

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// RefineScript takes an existing script and a user prompt, and asks Groq to modify it.
func RefineScript(currentScript *models.ScriptDocument, userPrompt string, config map[string]interface{}) (*models.ScriptDocument, error) {
	scriptJSON, _ := json.MarshalIndent(currentScript, "", "  ")

	systemPrompt := fmt.Sprintf(`You are an expert YouTube scriptwriter and AI editor.
The user has an existing YouTube script and wants to make modifications.
Your job is to read the existing script and the user's instructions, and output a NEW, updated script that perfectly incorporates their requests while maintaining the strict JSON schema.

IMPORTANT: Return ONLY valid JSON, no markdown code blocks, no explanations.
The output MUST follow the EXACT same schema as the input script.`)

	schemaExample := ""
	if config["format"] == "short" {
		schemaExample = `{
  "title_options": ["Title A", "Title B", "Title C"],
  "description": "Short YouTube description",
  "tags": ["tag1", "tag2"],
  "thumbnail_text": "Short punchy text",
  "hook": "Opening line",
  "segments": [ ... ]
}`
	} else {
		schemaExample = `{
  "title_options": ["Title A", "Title B", "Title C"],
  "description": "Full YouTube description with timestamps and links",
  "tags": ["tag1", "tag2"],
  "thumbnail_text": "Short punchy text for thumbnail",
  "hook": "Opening 5-second line to grab attention",
  "segments": [ ... ]
}`
	}

	userPromptFull := fmt.Sprintf(`Here is the current script:
%s

Here are the user's modification instructions:
"%s"

Additional context for the video:
- Topic: %v
- Target duration: %v minutes
- Tone: %v
- Language: %v

Please rewrite the script incorporating the user's instructions.
CRITICAL VISUAL RULES (DO NOT BREAK THESE):
- If the text changes significantly, you MUST update the sub_visuals to match the new text.
- Each sub_visual query MUST be 2-4 words optimized for Pexels stock video search.
- Set type to "clip" for stock footage, "image" for AI-generated images.
- NEVER repeat the same query across sub_visuals. Each must be unique.
- ALWAYS include the main subject in every query.

Ensure your JSON output includes ALL required fields like "title_options", "description", etc. as shown in this schema format:
%s

Return the FULL updated script as valid JSON matching the original schema.`,
		string(scriptJSON), userPrompt, config["raw_input"], config["duration_min"], config["script_tone"], config["language"], schemaExample)

	return callGroqForScript(systemPrompt, userPromptFull)
}
