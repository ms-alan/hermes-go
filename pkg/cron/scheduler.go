package cron

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Runner executes a job's prompt and delivers the result.
type Runner interface {
	Run(ctx context.Context, job *Job) (string, error)
}

// Deliverer sends job output to a destination.
type Deliverer interface {
	Deliver(ctx context.Context, jobID string, content string, origin *DeliveryOrigin) error
}

// Scheduler runs the cron tick loop.
type Scheduler struct {
	store    *Store
	runner   Runner
	deliverer Deliverer
	logger   *slog.Logger
	mu       sync.RWMutex
	stopCh   chan struct{}
	wg       sync.WaitGroup
	running  bool
	tickInterval time.Duration
}

// NewScheduler creates a new scheduler.
func NewScheduler(store *Store, runner Runner, deliverer Deliverer, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		store:        store,
		runner:       runner,
		deliverer:    deliverer,
		logger:       logger,
		stopCh:       make(chan struct{}),
		tickInterval: 60 * time.Second, // tick every 60s
	}
}

// Start begins the scheduler loop in a goroutine.
// Returns immediately.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.mu.Unlock()

	s.wg.Add(1)
	go s.runLoop()
	s.logger.Info("cron scheduler started")
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stopCh)
	s.mu.Unlock()

	s.wg.Wait()
	s.logger.Info("cron scheduler stopped")
}

func (s *Scheduler) runLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()

	// Run once immediately on start (catch up)
	s.tick()

	for {
		select {
		case <-ticker.C:
			s.tick()
		case <-s.stopCh:
			return
		}
	}
}

func (s *Scheduler) tick() {
	jobs := s.store.DueJobs()
	if len(jobs) == 0 {
		return
	}

	s.logger.Info("cron tick", "due_jobs", len(jobs))
	for _, job := range jobs {
		s.runJob(job)
	}
}

func (s *Scheduler) runJob(job *Job) {
	// Mark as running
	if err := s.store.Update(job.ID, func(j *Job) error {
		j.State = "running"
		return nil
	}); err != nil {
		s.logger.Error("mark job running", "job", job.ID, "err", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	start := time.Now()
	var lastErr string
	var output string

	if s.runner != nil {
		result, err := s.runner.Run(ctx, job)
		if err != nil {
			lastErr = err.Error()
			s.logger.Error("job run failed", "job", job.ID, "err", err)
		} else {
			output = result
		}
	} else {
		lastErr = "no runner configured"
	}

	elapsed := time.Since(start)

	// Compute next run
	nextRun := ComputeNextRun(job.Schedule, job.LastRunAt)

	// Update repeat completed count
	newCompleted := job.Repeat.Completed + 1
	isDone := job.Repeat.Times != nil && newCompleted >= *job.Repeat.Times

	if err := s.store.Update(job.ID, func(j *Job) error {
		j.State = "scheduled"
		if isDone {
			j.State = "done"
			j.Enabled = false
		}
		j.LastRunAt = time.Now().Format(time.RFC3339)
		j.NextRunAt = nextRun
		j.Repeat.Completed = newCompleted
		if lastErr != "" {
			j.LastStatus = "error"
			j.LastError = lastErr
		} else {
			j.LastStatus = "success"
			j.LastError = ""
		}
		return nil
	}); err != nil {
		s.logger.Error("update job after run", "job", job.ID, "err", err)
	}

	// Deliver output
	if output != "" && s.deliverer != nil {
		deliverCtx, delCancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := s.deliverer.Deliver(deliverCtx, job.ID, output, job.Origin); err != nil {
			s.logger.Error("delivery failed", "job", job.ID, "err", err)
			// Record delivery error
			s.store.Update(job.ID, func(j *Job) error {
				j.LastDeliveryError = err.Error()
				return nil
			})
		}
		delCancel()
	}

	s.logger.Info("job completed",
		"job", job.ID,
		"name", job.Name,
		"elapsed", elapsed,
		"status", map[bool]string{true: "error", false: "success"}[lastErr != ""],
		"next_run", nextRun)
}

// RunOnce runs a specific job immediately (for manual trigger).
func (s *Scheduler) RunNow(jobID string) error {
	job := s.store.Get(jobID)
	if job == nil {
		return fmt.Errorf("job not found: %s", jobID)
	}
	s.runJob(job)
	return nil
}
