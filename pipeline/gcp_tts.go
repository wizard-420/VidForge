package pipeline

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const gcpTTSBaseURL = "https://texttospeech.googleapis.com/v1"

// GCPVoice represents a voice returned by the Google Cloud TTS voices.list endpoint
type GCPVoice struct {
	Name              string   `json:"name"`
	LanguageCodes     []string `json:"languageCodes"`
	SSMLGender        string   `json:"ssmlGender"`
	NaturalSampleRate int      `json:"naturalSampleRateHertz"`
}

type gcpVoicesResponse struct {
	Voices []GCPVoice `json:"voices"`
}

type gcpSynthesizeRequest struct {
	Input       gcpInput       `json:"input"`
	Voice       gcpVoiceParams `json:"voice"`
	AudioConfig gcpAudioConfig `json:"audioConfig"`
}

type gcpInput struct {
	Text string `json:"text"`
}

type gcpVoiceParams struct {
	LanguageCode string `json:"languageCode"`
	Name         string `json:"name"`
}

type gcpAudioConfig struct {
	AudioEncoding string `json:"audioEncoding"`
}

type gcpSynthesizeResponse struct {
	AudioContent string `json:"audioContent"`
}

// ListGCPVoices fetches available voices from Google Cloud TTS, optionally filtered by language.
func ListGCPVoices(apiKey, languageCode string) ([]GCPVoice, error) {
	params := url.Values{"key": {apiKey}}
	if languageCode != "" {
		params.Set("languageCode", languageCode)
	}

	reqURL := fmt.Sprintf("%s/voices?%s", gcpTTSBaseURL, params.Encode())
	client := &http.Client{Timeout: 15 * time.Second}

	resp, err := client.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("GCP TTS voices request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GCP TTS voices API returned %d: %s", resp.StatusCode, string(body))
	}

	var result gcpVoicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode GCP voices response: %w", err)
	}

	return result.Voices, nil
}

// SynthesizeGCPTTS calls Google Cloud TTS to synthesize text and writes the audio to outputPath as MP3.
func SynthesizeGCPTTS(text, voiceName, languageCode, apiKey, outputPath string) error {
	reqBody := gcpSynthesizeRequest{
		Input: gcpInput{Text: text},
		Voice: gcpVoiceParams{
			LanguageCode: languageCode,
			Name:         voiceName,
		},
		AudioConfig: gcpAudioConfig{AudioEncoding: "MP3"},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal GCP TTS request: %w", err)
	}

	reqURL := fmt.Sprintf("%s/text:synthesize?key=%s", gcpTTSBaseURL, url.QueryEscape(apiKey))
	req, err := http.NewRequest("POST", reqURL, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create GCP TTS request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GCP TTS API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GCP TTS API returned %d: %s", resp.StatusCode, string(body))
	}

	var result gcpSynthesizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode GCP TTS response: %w", err)
	}

	audioBytes, err := base64.StdEncoding.DecodeString(result.AudioContent)
	if err != nil {
		return fmt.Errorf("decode GCP TTS audio base64: %w", err)
	}

	if err := os.WriteFile(outputPath, audioBytes, 0644); err != nil {
		return fmt.Errorf("write GCP TTS audio file: %w", err)
	}

	return nil
}

// SynthesizeGCPTTSToBytes is like SynthesizeGCPTTS but returns the MP3 bytes directly (used for preview).
func SynthesizeGCPTTSToBytes(text, voiceName, languageCode, apiKey string) ([]byte, error) {
	reqBody := gcpSynthesizeRequest{
		Input: gcpInput{Text: text},
		Voice: gcpVoiceParams{
			LanguageCode: languageCode,
			Name:         voiceName,
		},
		AudioConfig: gcpAudioConfig{AudioEncoding: "MP3"},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal GCP TTS request: %w", err)
	}

	reqURL := fmt.Sprintf("%s/text:synthesize?key=%s", gcpTTSBaseURL, url.QueryEscape(apiKey))
	req, err := http.NewRequest("POST", reqURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create GCP TTS request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GCP TTS API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GCP TTS API returned %d: %s", resp.StatusCode, string(body))
	}

	var result gcpSynthesizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode GCP TTS response: %w", err)
	}

	audioBytes, err := base64.StdEncoding.DecodeString(result.AudioContent)
	if err != nil {
		return nil, fmt.Errorf("decode GCP TTS audio base64: %w", err)
	}

	return audioBytes, nil
}
