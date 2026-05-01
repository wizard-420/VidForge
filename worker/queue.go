package worker

import (
	"log"
	"time"
	"yt-automation-studio/config"
	"yt-automation-studio/models"
	"yt-automation-studio/pipeline"
)

var jobQueue chan *JobRequest

type JobRequest struct {
	Job        *models.JobContext
	OnProgress pipeline.ProgressFunc
	IsRetry    bool
	StartStage int
}

// InitQueue initializes the worker pool
func InitQueue() {
	maxWorkers := config.App.MaxConcurrent
	if maxWorkers <= 0 {
		maxWorkers = 1
	}

	jobQueue = make(chan *JobRequest, 100) // Buffer up to 100 jobs

	for i := 1; i <= maxWorkers; i++ {
		go worker(i, jobQueue)
	}

	log.Printf("👷 Started %d background workers", maxWorkers)
}

func worker(id int, queue <-chan *JobRequest) {
	for req := range queue {
		log.Printf("Worker %d starting job %s", id, req.Job.JobID[:8])
		
		orch := pipeline.NewOrchestrator(req.Job, req.OnProgress)
		
		// Wait a small amount before starting to give the UI a chance to connect WS
		time.Sleep(1 * time.Second)
		
		if req.IsRetry {
			orch.RunFrom(req.StartStage)
		} else {
			orch.Run()
		}
		
		log.Printf("Worker %d finished job %s", id, req.Job.JobID[:8])
	}
}

// Enqueue adds a job to the queue to be processed
func Enqueue(req *JobRequest) {
	jobQueue <- req
}
