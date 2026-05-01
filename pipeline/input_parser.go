package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"yt-automation-studio/config"
	"yt-automation-studio/models"
)

// RunInputParser executes Stage 1: normalise raw user input into a clean InputPayload
func RunInputParser(job *models.JobContext, progress ProgressFunc) error {
	payload := job.Payload

	switch payload.InputType {
	case "category":
		// Use Claude to generate a trending topic from the category
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 1, StageName: "Input Parsing",
			ProgressPct: 30, Message: "Generating trending topic from category...",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})

		topic, err := categoryToTopic(payload.RawInput)
		if err != nil {
			return fmt.Errorf("category to topic: %w", err)
		}
		payload.RawInput = topic
		job.SetStageLog(1, fmt.Sprintf("Category '%s' → Topic: '%s'", payload.RawInput, topic))

	case "topic":
		// Clean whitespace, use as-is
		payload.RawInput = strings.TrimSpace(payload.RawInput)
		job.SetStageLog(1, fmt.Sprintf("Topic passthrough: '%s'", payload.RawInput))

	case "event":
		// Use Claude to extract narrative from event description
		progress(models.ProgressEvent{
			JobID: job.JobID, Stage: 1, StageName: "Input Parsing",
			ProgressPct: 30, Message: "Extracting narrative from event description...",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})

		narrative, err := eventToNarrative(payload.RawInput)
		if err != nil {
			return fmt.Errorf("event to narrative: %w", err)
		}
		payload.RawInput = narrative
		job.SetStageLog(1, fmt.Sprintf("Event extracted → Narrative: '%s'", narrative))
	}

	// Validate duration constraints
	if payload.Format == "short" {
		payload.DurationMin = 1
	} else if payload.DurationMin < 5 {
		payload.DurationMin = 5
	} else if payload.DurationMin > 20 {
		payload.DurationMin = 20
	}

	return nil
}

// categoryToTopic calls Claude to generate a trending video topic from a category
func categoryToTopic(category string) (string, error) {
	systemPrompt := "You are a YouTube content strategist specialising in faceless channels."
	userPrompt := fmt.Sprintf(
		`Given the category "%s", generate ONE specific, compelling video topic `+
			`that is currently trending, evergreen, and optimised for a general audience. `+
			`Return ONLY the topic title, no explanation. Max 12 words.`, category)

	resp, err := callGroq(systemPrompt, userPrompt)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp), nil
}

// eventToNarrative calls Claude to extract a narrative from a raw event description
func eventToNarrative(rawInput string) (string, error) {
	systemPrompt := "You are a YouTube scriptwriter."
	userPrompt := fmt.Sprintf(
		`The user has described this event or story: "%s"
Extract the core narrative in one sentence, identify 3 key facts, and suggest a dramatic
video title. Return JSON: {"narrative": "...", "key_facts": [...], "title": "..."}`, rawInput)

	resp, err := callGroq(systemPrompt, userPrompt)
	if err != nil {
		return "", err
	}

	// Parse the JSON to extract the title as the topic
	var result struct {
		Narrative string   `json:"narrative"`
		KeyFacts  []string `json:"key_facts"`
		Title     string   `json:"title"`
	}

	// Try to extract JSON from response (Claude sometimes wraps in markdown)
	jsonStr := extractJSON(resp)
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		// Fallback: use the raw input trimmed
		return strings.TrimSpace(rawInput), nil
	}

	return result.Title, nil
}

// callGroq makes a request to the Groq Llama 3.3 70B API
func callGroq(systemPrompt, userPrompt string) (string, error) {
	apiKey := config.App.GroqAPIKey
	if apiKey == "" {
		return "", fmt.Errorf("GROQ_API_KEY not configured")
	}

	reqBody := map[string]interface{}{
		"model": "llama-3.3-70b-versatile",
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Groq API returned %d: %s", resp.StatusCode, string(body))
	}

	// Parse Groq response
	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("empty response from Groq")
	}

	return apiResp.Choices[0].Message.Content, nil
}

// extractJSON attempts to extract a JSON object from text that may contain markdown
func extractJSON(text string) string {
	// Find the first { and last }
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return text[start : end+1]
	}
	return text
}
