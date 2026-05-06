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
	"strings"
	"time"
)

const gcpTTSBaseURL = "https://texttospeech.googleapis.com/v1"

// GCPVoice represents a voice returned by the Google Cloud TTS voices.list endpoint.
// `Premium` is set true for voice families (Chirp 3 HD, Studio) that require
// service-account / OAuth auth — the UI uses this to badge them and to gate
// selection when only API-key auth is configured.
type GCPVoice struct {
	Name              string   `json:"name"`
	LanguageCodes     []string `json:"languageCodes"`
	SSMLGender        string   `json:"ssmlGender"`
	NaturalSampleRate int      `json:"naturalSampleRateHertz"`
	Premium           bool     `json:"premium,omitempty"`
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

// gcpPremiumVoiceMarkers lists voice-name substrings whose voice families
// require service-account / OAuth auth. The standard API-key + text:synthesize
// flow is rejected by Google for these with "This voice requires a model name
// to be specified." When a service account IS configured, we authenticate via
// Bearer token and the request succeeds normally.
var gcpPremiumVoiceMarkers = []string{
	"Chirp3-HD", // Chirp 3 HD voices (e.g. en-US-Chirp3-HD-Aoede)
	"Studio",    // Studio voices (e.g. en-US-Studio-O)
}

// isGCPVoicePremium reports whether the given voice belongs to a family that
// requires service-account auth.
func isGCPVoicePremium(name string) bool {
	for _, marker := range gcpPremiumVoiceMarkers {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// doGCPRequest performs an HTTP request against the TTS API, choosing auth
// based on availability:
//   - If a service account is configured, the OAuth-authenticated client is
//     used (Bearer token, no ?key= in URL).
//   - Otherwise the API key is appended as ?key= and a plain client is used.
//
// requireSA=true forces the service-account path (used when synthesizing a
// premium voice) and returns an error if no SA is configured.
func doGCPRequest(method, path string, body io.Reader, apiKey string, requireSA bool) (*http.Response, error) {
	saAvailable := HasGCPServiceAccount()

	if requireSA && !saAvailable {
		return nil, fmt.Errorf("this voice requires a Google Cloud service account; " +
			"set GOOGLE_APPLICATION_CREDENTIALS_JSON or GOOGLE_APPLICATION_CREDENTIALS in .env, " +
			"or pick a non-premium voice (Standard / Wavenet / Neural2 / News / Casual / Polyglot / Chirp HD)")
	}

	// Build URL — only add ?key= when we're using API-key auth.
	fullURL := gcpTTSBaseURL + path
	if !saAvailable {
		if apiKey == "" {
			return nil, fmt.Errorf("GCP TTS: no API key and no service account configured")
		}
		sep := "?"
		if strings.Contains(fullURL, "?") {
			sep = "&"
		}
		fullURL += sep + "key=" + url.QueryEscape(apiKey)
	}

	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, fmt.Errorf("create GCP TTS request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	var client *http.Client
	if saAvailable {
		client = globalGCPAuth.client()
		// The shared OAuth client doesn't have a per-request timeout, so wrap.
		// Cloning to keep token transport but apply a sensible deadline.
		client = &http.Client{Transport: client.Transport, Timeout: 120 * time.Second}
	} else {
		client = &http.Client{Timeout: 120 * time.Second}
	}

	return client.Do(req)
}

// ListGCPVoices fetches available voices from Google Cloud TTS, optionally
// filtered by language. Premium voices (Chirp 3 HD, Studio) are included only
// when a service account is configured — otherwise they would fail at
// synthesis time with the "requires a model name" 400.
func ListGCPVoices(apiKey, languageCode string) ([]GCPVoice, error) {
	path := "/voices"
	if languageCode != "" {
		path += "?languageCode=" + url.QueryEscape(languageCode)
	}

	resp, err := doGCPRequest("GET", path, nil, apiKey, false)
	if err != nil {
		return nil, err
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

	saAvailable := HasGCPServiceAccount()
	out := make([]GCPVoice, 0, len(result.Voices))
	for _, v := range result.Voices {
		premium := isGCPVoicePremium(v.Name)
		if premium && !saAvailable {
			// API-key-only mode: hide premium voices to spare users the 400.
			continue
		}
		v.Premium = premium
		out = append(out, v)
	}
	return out, nil
}

// SynthesizeGCPTTS calls Google Cloud TTS to synthesize text and writes the
// audio to outputPath as MP3.
func SynthesizeGCPTTS(text, voiceName, languageCode, apiKey, outputPath string) error {
	audioBytes, err := SynthesizeGCPTTSToBytes(text, voiceName, languageCode, apiKey)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, audioBytes, 0644); err != nil {
		return fmt.Errorf("write GCP TTS audio file: %w", err)
	}
	return nil
}

// SynthesizeGCPTTSToBytes is like SynthesizeGCPTTS but returns the MP3 bytes
// directly (used for preview).
func SynthesizeGCPTTSToBytes(text, voiceName, languageCode, apiKey string) ([]byte, error) {
	premium := isGCPVoicePremium(voiceName)

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

	resp, err := doGCPRequest("POST", "/text:synthesize", bytes.NewReader(jsonBody), apiKey, premium)
	if err != nil {
		return nil, err
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
