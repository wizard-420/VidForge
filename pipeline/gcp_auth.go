package pipeline

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// gcpScope is the OAuth scope required for Cloud Text-to-Speech.
const gcpScope = "https://www.googleapis.com/auth/cloud-platform"

// gcpAuth caches the parsed service-account credentials & token source so we
// don't re-parse the JSON / re-mint a JWT on every request. The underlying
// oauth2 library already caches access tokens internally and refreshes them
// automatically on expiry.
type gcpAuth struct {
	once       sync.Once
	httpClient *http.Client
	hasSA      bool
	loadErr    error
}

var globalGCPAuth = &gcpAuth{}

// loadServiceAccount tries to find service-account JSON via either:
//  1. GOOGLE_APPLICATION_CREDENTIALS_JSON — raw JSON in the env var
//     (Docker-friendly, no file mount needed).
//  2. GOOGLE_APPLICATION_CREDENTIALS — path to a JSON file on disk
//     (Google's standard convention).
//
// Returns ok=false if neither is configured. That is NOT an error — it just
// means we'll fall back to API-key auth, which is fine for non-premium
// voices.
func loadServiceAccount() (data []byte, source string, ok bool) {
	if raw := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS_JSON")); raw != "" {
		return []byte(raw), "GOOGLE_APPLICATION_CREDENTIALS_JSON env", true
	}
	if path := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			log.Printf("⚠️  GCP TTS: GOOGLE_APPLICATION_CREDENTIALS=%s but read failed: %v", path, err)
			return nil, "", false
		}
		return b, "GOOGLE_APPLICATION_CREDENTIALS file " + path, true
	}
	return nil, "", false
}

// init builds the authenticated HTTP client lazily on first use.
func (a *gcpAuth) init() {
	a.once.Do(func() {
		jsonBytes, source, ok := loadServiceAccount()
		if !ok {
			return
		}

		creds, err := google.CredentialsFromJSON(context.Background(), jsonBytes, gcpScope)
		if err != nil {
			a.loadErr = fmt.Errorf("parse GCP service-account JSON (%s): %w", source, err)
			log.Printf("⚠️  GCP TTS: %v", a.loadErr)
			return
		}

		// oauth2.NewClient returns an *http.Client that injects a fresh Bearer
		// token on every request via the credentials' TokenSource (which
		// already caches & refreshes tokens internally).
		a.httpClient = oauth2.NewClient(context.Background(), creds.TokenSource)
		a.hasSA = true
		log.Printf("✅ GCP TTS: service account loaded from %s — premium voices (Chirp 3 HD, Studio) unlocked", source)
	})
}

// hasServiceAccount reports whether an OAuth client is available.
func (a *gcpAuth) hasServiceAccount() bool {
	a.init()
	return a.hasSA
}

// client returns the authenticated *http.Client (Bearer token injected).
// Caller MUST check hasServiceAccount() first; otherwise this returns nil.
func (a *gcpAuth) client() *http.Client {
	a.init()
	return a.httpClient
}

// HasGCPServiceAccount is the package-level entry point used elsewhere.
func HasGCPServiceAccount() bool {
	return globalGCPAuth.hasServiceAccount()
}
