package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all application configuration loaded from .env
type Config struct {
	// API Keys
	GroqAPIKey           string
	ElevenLabsAPIKey     string
	GoogleCloudTTSAPIKey string
	PexelsAPIKey         string
	PixabayAPIKey        string
	OpenAIAPIKey         string
	TogetherAPIKey       string
	HFAPIKey             string
	JamendoClientID      string

	// Cloudflare Workers AI — free tier (10,000 neurons/day) is used as the
	// FLUX image-generation fallback between Together AI (paid quality) and
	// Pollinations.ai (last-resort). Requires BOTH the account ID and an
	// API token with "Workers AI" read permission.
	CloudflareAccountID string
	CloudflareAPIToken  string

	// Google Cloud Service Account (for premium TTS voices like Chirp 3 HD,
	// Studio). Either may be set; JSON env var takes precedence over file path.
	// When neither is set, GCP TTS falls back to API-key auth which only
	// supports Standard / Wavenet / Neural2 / News / Casual / Polyglot /
	// regular Chirp HD voices.
	GoogleApplicationCredentialsJSON string
	GoogleApplicationCredentialsFile string

	// YouTube OAuth
	YouTubeClientSecretFile string
	YouTubeTokenFile        string

	// App Settings
	WorkspaceDir    string
	ExportDir       string
	LogLevel        string
	MaxConcurrent   int
	ServerPort      int
	CleanupDays     int
}

// Global application config — initialized once at startup
var App *Config

// Load reads .env file and populates the global Config
func Load() {
	// Load .env if it exists (won't error if missing)
	_ = godotenv.Load()

	App = &Config{
		GroqAPIKey:           getEnv("GROQ_API_KEY", ""),
		ElevenLabsAPIKey:     getEnv("ELEVENLABS_API_KEY", ""),
		GoogleCloudTTSAPIKey: getEnv("GOOGLE_CLOUD_TTS_API_KEY", ""),
		PexelsAPIKey:         getEnv("PEXELS_API_KEY", ""),
		PixabayAPIKey:    getEnv("PIXABAY_API_KEY", ""),
		OpenAIAPIKey:     getEnv("OPENAI_API_KEY", ""),
		TogetherAPIKey:   getEnv("TOGETHER_API_KEY", ""),
		HFAPIKey:         getEnv("HF_API_KEY", ""),
		JamendoClientID:  getEnv("JAMENDO_CLIENT_ID", "b6747d04"), // default if empty

		CloudflareAccountID: getEnv("CLOUDFLARE_ACCOUNT_ID", ""),
		CloudflareAPIToken:  getEnv("CLOUDFLARE_API_TOKEN", ""),

		GoogleApplicationCredentialsJSON: getEnv("GOOGLE_APPLICATION_CREDENTIALS_JSON", ""),
		GoogleApplicationCredentialsFile: getEnv("GOOGLE_APPLICATION_CREDENTIALS", ""),

		YouTubeClientSecretFile: getEnv("YOUTUBE_CLIENT_SECRET_FILE", "client_secret.json"),
		YouTubeTokenFile:        getEnv("YOUTUBE_TOKEN_FILE", "token.json"),

		WorkspaceDir:  getEnv("WORKSPACE_DIR", "./workspace"),
		ExportDir:     getEnv("EXPORT_DIR", "./exports"),
		LogLevel:      strings.ToUpper(getEnv("LOG_LEVEL", "INFO")),
		MaxConcurrent: getEnvInt("MAX_CONCURRENT_JOBS", 1),
		ServerPort:    getEnvInt("SERVER_PORT", 8000),
		CleanupDays:   getEnvInt("CLEANUP_AFTER_DAYS", 7),
	}

	// Create required directories
	ensureDir(App.WorkspaceDir)
	ensureDir(App.ExportDir)
	ensureDir("./logs")
	ensureDir("./storage")

	log.Printf("✅ Config loaded — Server will run on port %d", App.ServerPort)
}

// GetMaskedSettings returns config with API keys masked for the frontend
func (c *Config) GetMaskedSettings() map[string]interface{} {
	return map[string]interface{}{
		"groq_api_key":           maskKey(c.GroqAPIKey),
		"elevenlabs_api_key":     maskKey(c.ElevenLabsAPIKey),
		"google_cloud_tts_key":   maskKey(c.GoogleCloudTTSAPIKey),
		"pexels_api_key":         maskKey(c.PexelsAPIKey),
		"pixabay_api_key":        maskKey(c.PixabayAPIKey),
		"openai_api_key":         maskKey(c.OpenAIAPIKey),
		"together_api_key":       maskKey(c.TogetherAPIKey),
		"hf_api_key":             maskKey(c.HFAPIKey),
		"cloudflare_account_id":  maskKey(c.CloudflareAccountID),
		"cloudflare_api_token":   maskKey(c.CloudflareAPIToken),
		"workspace_dir":          c.WorkspaceDir,
		"export_dir":             c.ExportDir,
		"log_level":              c.LogLevel,
		"max_concurrent":         c.MaxConcurrent,
		"server_port":            c.ServerPort,
		"cleanup_days":           c.CleanupDays,
	}
}

// HasRequiredKeys checks if minimum API keys are configured
func (c *Config) HasRequiredKeys() map[string]bool {
	return map[string]bool{
		"groq":               c.GroqAPIKey != "",
		"elevenlabs":         c.ElevenLabsAPIKey != "",
		"google_tts":         c.GoogleCloudTTSAPIKey != "",
		"google_tts_premium": c.HasGCPServiceAccount(),
		"pexels":             c.PexelsAPIKey != "",
		"pixabay":            c.PixabayAPIKey != "",
		"openai":             c.OpenAIAPIKey != "",
		"together":           c.TogetherAPIKey != "",
		"hf":                 c.HFAPIKey != "",
		"cloudflare":         c.CloudflareAccountID != "" && c.CloudflareAPIToken != "",
	}
}

// HasGCPServiceAccount reports whether a service-account credential is
// configured (either as raw JSON or a file path). When true, GCP TTS premium
// voice families (Chirp 3 HD, Studio) become available.
func (c *Config) HasGCPServiceAccount() bool {
	return strings.TrimSpace(c.GoogleApplicationCredentialsJSON) != "" ||
		strings.TrimSpace(c.GoogleApplicationCredentialsFile) != ""
}

// --- Helpers ---

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			return n
		}
	}
	return fallback
}

func maskKey(key string) string {
	if len(key) <= 8 {
		if key == "" {
			return "(not set)"
		}
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func ensureDir(path string) {
	if err := os.MkdirAll(path, 0755); err != nil {
		log.Printf("⚠️  Could not create directory %s: %v", path, err)
	}
}
