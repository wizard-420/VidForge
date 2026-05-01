package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"yt-automation-studio/config"
	"yt-automation-studio/models"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

// RunYouTubeUploader executes Stage 7: upload to YouTube
func RunYouTubeUploader(job *models.JobContext, progress ProgressFunc) error {
	if job.FinalVideo == "" {
		return fmt.Errorf("no final video available — Stage 6 must complete first")
	}

	if job.Payload.UploadSchedule == "" {
		job.Payload.UploadSchedule = "immediate"
	}

	progress(models.ProgressEvent{
		JobID:       job.JobID,
		Stage:       7,
		StageName:   "Upload",
		ProgressPct: 20,
		Message:     "Preparing YouTube upload...",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	})

	// 1. Read the client_secret.json
	b, err := os.ReadFile(config.App.YouTubeClientSecretFile)
	if err != nil {
		job.SetStageLog(7, "Upload stage: video saved locally. YouTube OAuth not configured (client_secret.json missing).")
		job.YouTubeURL = fmt.Sprintf("local://%s", job.FinalVideo)
		progress(models.ProgressEvent{
			JobID:       job.JobID,
			Stage:       7,
			StageName:   "Upload",
			ProgressPct: 100,
			Message:     fmt.Sprintf("Video ready at: %s — YouTube upload skipped (no client secret)", job.FinalVideo),
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})
		return nil
	}

	// 2. Parse OAuth Config
	oauthConfig, err := google.ConfigFromJSON(b, youtube.YoutubeUploadScope)
	if err != nil {
		return fmt.Errorf("unable to parse client secret file: %w", err)
	}

	// 3. Load existing token
	token, err := tokenFromFile(config.App.YouTubeTokenFile)
	if err != nil {
		job.SetStageLog(7, "Upload stage: video saved locally. YouTube token.json missing. Run setup wizard.")
		job.YouTubeURL = fmt.Sprintf("local://%s", job.FinalVideo)
		progress(models.ProgressEvent{
			JobID:       job.JobID,
			Stage:       7,
			StageName:   "Upload",
			ProgressPct: 100,
			Message:     fmt.Sprintf("Video ready at: %s — YouTube upload skipped (needs auth setup)", job.FinalVideo),
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		})
		return nil
	}

	// 4. Build YouTube client
	client := oauthConfig.Client(context.Background(), token)
	youtubeService, err := youtube.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return fmt.Errorf("error creating YouTube client: %w", err)
	}

	progress(models.ProgressEvent{
		JobID:       job.JobID,
		Stage:       7,
		StageName:   "Upload",
		ProgressPct: 40,
		Message:     "Uploading video to YouTube...",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	})

	// 5. Setup upload metadata
	uploadTitle := job.Payload.RawInput
	if job.Script != nil && len(job.Script.TitleOptions) > 0 {
		uploadTitle = job.Script.TitleOptions[0]
	}

	uploadDesc := ""
	if job.Script != nil {
		uploadDesc = job.Script.Description
	}

	var tags []string
	if job.Script != nil {
		tags = job.Script.Tags
	}

	video := &youtube.Video{
		Snippet: &youtube.VideoSnippet{
			Title:       uploadTitle,
			Description: uploadDesc,
			Tags:        tags,
			CategoryId:  "27", // Education
		},
		Status: &youtube.VideoStatus{
			PrivacyStatus:           "private", // Defaults to private for safety, can be "public"
			SelfDeclaredMadeForKids: false,
		},
	}

	// Handle scheduling
	if job.Payload.UploadSchedule != "immediate" {
		// schedule should be ISO 8601 string, but user input is "19:00", "20:00", "21:00" etc.
		// Parse it for today
		now := time.Now()
		layout := "15:04"
		t, err := time.Parse(layout, job.Payload.UploadSchedule)
		if err == nil {
			publishAt := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
			if publishAt.Before(now) {
				publishAt = publishAt.AddDate(0, 0, 1) // Next day if time already passed
			}
			video.Status.PrivacyStatus = "private" // Required when publishAt is set
			video.Status.PublishAt = publishAt.Format(time.RFC3339)
		}
	}

	// 6. Upload file
	file, err := os.Open(job.FinalVideo)
	if err != nil {
		return fmt.Errorf("error opening final video file: %w", err)
	}
	defer file.Close()

	call := youtubeService.Videos.Insert([]string{"snippet", "status"}, video)
	response, err := call.Media(file).Do()
	if err != nil {
		return fmt.Errorf("error uploading video: %w", err)
	}

	videoURL := fmt.Sprintf("https://youtube.com/watch?v=%s", response.Id)
	job.YouTubeURL = videoURL
	job.SetStageLog(7, fmt.Sprintf("Successfully uploaded video to YouTube: %s", videoURL))

	progress(models.ProgressEvent{
		JobID:       job.JobID,
		Stage:       7,
		StageName:   "Upload",
		ProgressPct: 100,
		Message:     fmt.Sprintf("Video successfully uploaded to YouTube! URL: %s", videoURL),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	})

	return nil
}

// tokenFromFile retrieves a Token from a given file path.
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}
