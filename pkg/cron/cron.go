// Package cron provides scheduled job execution for hermes-go.
// Jobs are stored in ~/.hermes/cron/jobs.json and output in ~/.hermes/cron/output/{job_id}/.
package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Schedule kinds
// ---------------------------------------------------------------------------

// ScheduleKind is the type of schedule.
type ScheduleKind string

const (
	ScheduleOnce     ScheduleKind = "once"
	ScheduleInterval ScheduleKind = "interval"
	ScheduleCron     ScheduleKind = "cron"
)

// Schedule describes when a job should run.
type Schedule struct {
	Kind    ScheduleKind `json:"kind"`              // once | interval | cron
	Minutes int          `json:"minutes,omitempty"` // for interval
	RunAt   string       `json:"runAt,omitempty"`   // ISO timestamp for once
	Cron    string        `json:"cron,omitempty"`    // cron expression for cron
	Display string       `json:"display"`           // human-readable
}

// RepeatConfig tracks how many times a job should run.
type RepeatConfig struct {
	Times     *int `json:"times"`      // nil = forever
	Completed int  `json:"completed"`
}

// Job represents a scheduled job.
type Job struct {
	ID       string `json:"id"`                // 12-char hex ID
	Name     string `json:"name"`              // friendly name
	Prompt   string `json:"prompt"`            // the task prompt
	Skills   []string `json:"skills"`          // skills to load before running
	Schedule Schedule `json:"schedule"`        // parsed schedule

	Repeat RepeatConfig `json:"repeat"` // repeat configuration

	Enabled bool   `json:"enabled"` // whether job can fire
	State   string `json:"state"`   // scheduled | running | done | paused

	CreatedAt string `json:"createdAt"` // ISO timestamp
	NextRunAt string `json:"nextRunAt"` // ISO timestamp
	LastRunAt string `json:"lastRunAt"` // ISO timestamp

	LastStatus        string `json:"lastStatus"`         // "success" | "error"
	LastError         string `json:"lastError,omitempty"` // last execution error
	LastDeliveryError string `json:"lastDeliveryError,omitempty"`

	// Delivery: "origin", "local", or "platform:chat_id:thread_id"
	Deliver string `json:"deliver"`
	// For "origin" delivery - tracks the source channel
	Origin *DeliveryOrigin `json:"origin,omitempty"`
	// Per-job model/provider overrides
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Script: path to a Python script whose stdout is injected as context
	Script string `json:"script,omitempty"`
}

// DeliveryOrigin describes where a job was created for "origin" delivery.
type DeliveryOrigin struct {
	Platform  string `json:"platform"`  // e.g. "qqbot"
	ChatID    string `json:"chatId"`    // e.g. "B37D6F61D..."
	ThreadID  string `json:"threadId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

// ParseSchedule parses a human-readable schedule string.
// Supports:
//   - "30m", "2h", "1d"        → one-shot in N minutes/hours/days
//   - "every 30m", "every 2h"  → recurring interval
//   - "0 9 * * *"             → cron expression (5 fields: min hour day month weekday)
//   - "2026-02-03T14:00"       → one-shot at specific time
func ParseSchedule(s string) (Schedule, error) {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)

	// "every X" pattern → recurring interval
	if strings.HasPrefix(lower, "every ") {
		durationStr := strings.TrimPrefix(s, "every ")
		durationStr = strings.TrimPrefix(strings.ToLower(s), "every ")
		minutes, err := parseDuration(durationStr)
		if err != nil {
			return Schedule{}, err
		}
		return Schedule{
			Kind:    ScheduleInterval,
			Minutes: minutes,
			Display: fmt.Sprintf("every %dm", minutes),
		}, nil
	}

	// Check for cron expression (5 space-separated fields)
	parts := strings.Fields(s)
	if len(parts) == 5 && isCronField(parts[0]) && isCronField(parts[1]) &&
		isCronField(parts[2]) && isCronField(parts[3]) && isCronField(parts[4]) {
		// Validate it looks like a cron expression
		cronRegex := regexp.MustCompile(`^[\d\*\-\,\/]+$`)
		valid := true
		for _, p := range parts {
			if !cronRegex.MatchString(p) {
				valid = false
				break
			}
		}
		if valid {
			return Schedule{
				Kind:    ScheduleCron,
				Cron:    s,
				Display: s,
			}, nil
		}
	}

	// ISO timestamp
	if strings.Contains(s, "T") || regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`).MatchString(s) {
		// Try parsing as ISO timestamp
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			// Try date-only
			t, err = time.Parse("2006-01-02", s)
			if err != nil {
				return Schedule{}, fmt.Errorf("invalid timestamp %q: %w", s, err)
			}
		}
		return Schedule{
			Kind:    ScheduleOnce,
			RunAt:   t.In(time.Local).Format(time.RFC3339),
			Display: fmt.Sprintf("once at %s", t.Format("2006-01-02 15:04")),
		}, nil
	}

	// Duration like "30m", "2h", "1d" → one-shot from now
	minutes, err := parseDuration(s)
	if err == nil {
		runAt := time.Now().Add(time.Duration(minutes) * time.Minute)
		return Schedule{
			Kind:    ScheduleOnce,
			RunAt:   runAt.Format(time.RFC3339),
			Display: fmt.Sprintf("once in %dm", minutes),
		}, nil
	}

	return Schedule{}, fmt.Errorf(
		"invalid schedule %q. Use:\n"+
			"  - Duration: '30m', '2h', '1d' (one-shot)\n"+
			"  - Interval: 'every 30m', 'every 2h' (recurring)\n"+
			"  - Cron: '0 9 * * *' (daily at 9am)\n"+
			"  - Timestamp: '2026-02-03T14:00' (one-shot at time)", s)
}

// parseDuration parses "30m", "2h", "1d" etc into minutes.
func parseDuration(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	re := regexp.MustCompile(`^(\d+)\s*(m|min|mins|h|hr|hrs|d|day|days)$`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	val, _ := strconv.Atoi(m[1])
	unit := m[2][0] // first char
	multipliers := map[byte]int{'m': 1, 'h': 60, 'd': 1440}
	return val * multipliers[unit], nil
}

func isCronField(s string) bool {
	return regexp.MustCompile(`^[\d\*\-\,\/]+$`).MatchString(s)
}

// ComputeNextRun returns the ISO timestamp of the next run, or "" if no more runs.
func ComputeNextRun(schedule Schedule, lastRunAt string) string {
	now := time.Now()

	switch schedule.Kind {
	case ScheduleOnce:
		// One-shot: only run if we haven't run yet and time has arrived
		if lastRunAt != "" {
			return "" // already ran
		}
		if schedule.RunAt == "" {
			return ""
		}
		runAt, err := time.Parse(time.RFC3339, schedule.RunAt)
		if err != nil {
			return ""
		}
		// 2-hour grace window for missed ticks
		if runAt.Before(now.Add(-2 * time.Hour)) {
			return "" // too late
		}
		return schedule.RunAt

	case ScheduleInterval:
		interval := time.Duration(schedule.Minutes) * time.Minute
		if lastRunAt == "" {
			// First run: now + interval
			return now.Add(interval).Format(time.RFC3339)
		}
		last, err := time.Parse(time.RFC3339, lastRunAt)
		if err != nil {
			return now.Add(interval).Format(time.RFC3339)
		}
		return last.Add(interval).Format(time.RFC3339)

	case ScheduleCron:
		// Simple cron: validate and compute next occurrence
		// Use robfig/cron if available, otherwise approximate
		next := computeCronNext(schedule.Cron, now)
		if next.IsZero() {
			return ""
		}
		return next.Format(time.RFC3339)
	}

	return ""
}

// computeCronNext approximates the next cron occurrence.
// For accurate cron, install robfig/cron v3.
// This simple version handles common cases: "* * * * *" (every minute),
// specific minutes, and daily at hour:minute.
func computeCronNext(expr string, base time.Time) time.Time {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return time.Time{}
	}
	minuteStr, hourStr := parts[0], parts[1]

	// Handle "*" (every minute)
	if minuteStr == "*" && hourStr == "*" {
		return base.Add(1 * time.Minute)
	}

	// Parse specific minute and hour
	var minute, hour int
	if minuteStr == "*" {
		minute = base.Minute()
	} else {
		fmt.Sscanf(minuteStr, "%d", &minute)
	}
	if hourStr == "*" {
		hour = base.Hour()
	} else {
		fmt.Sscanf(hourStr, "%d", &hour)
	}

	// Build next occurrence
	next := time.Date(base.Year(), base.Month(), base.Day(), hour, minute, 0, 0, base.Location())
	if next.Before(base) || next.Equal(base) {
		// Move to next day
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// GenerateID generates a short random hex ID.
func GenerateID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// Store manages job persistence.
type Store struct {
	mu    sync.RWMutex
	path  string
	jobs  []*Job
	dirty bool
}

const jobsFile = "jobs.json"

// NewStore creates a cron store, loading existing jobs from disk.
func NewStore(hermesHome string) (*Store, error) {
	if hermesHome == "" {
		hermesHome = filepath.Join(os.Getenv("HOME"), ".hermes")
	}
	cronDir := filepath.Join(hermesHome, "cron")
	if err := os.MkdirAll(cronDir, 0700); err != nil {
		return nil, fmt.Errorf("create cron dir: %w", err)
	}
	// Set permissions on cron dir
	os.Chmod(cronDir, 0700)

	s := &Store{path: filepath.Join(cronDir, jobsFile)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		s.jobs = []*Job{}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read jobs: %w", err)
	}

	var wrapper struct {
		Jobs      []*Job `json:"jobs"`
		UpdatedAt string `json:"updatedAt"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		// Try lenient parse
		var raw map[string]any
		if json.Unmarshal(data, &raw) == nil {
			if jobsRaw, ok := raw["jobs"].([]any); ok {
				jobs := make([]*Job, 0, len(jobsRaw))
				for _, j := range jobsRaw {
					if jm, ok := j.(map[string]any); ok {
						jb, _ := json.Marshal(jm)
						var job Job
						if json.Unmarshal(jb, &job) == nil {
							jobs = append(jobs, &job)
						}
					}
				}
				s.jobs = jobs
				return nil
			}
		}
		return fmt.Errorf("parse jobs: %w", err)
	}
	s.jobs = wrapper.Jobs
	if s.jobs == nil {
		s.jobs = []*Job{}
	}
	return nil
}

func (s *Store) save() error {
	// Write atomically via temp file
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".jobs_*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	wrapper := struct {
		Jobs      []*Job `json:"jobs"`
		UpdatedAt string `json:"updatedAt"`
	}{
		Jobs:      s.jobs,
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	if err := enc.Encode(wrapper); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("encode: %w", err)
	}
	tmp.Close()
	if err := os.Chmod(tmpPath, 0600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	os.Chmod(s.path, 0600)
	return nil
}

// List returns all jobs.
func (s *Store) List() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Job, len(s.jobs))
	copy(out, s.jobs)
	return out
}

// Get returns a job by ID.
func (s *Store) Get(id string) *Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, j := range s.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// Add adds a new job.
func (s *Store) Add(job *Job) error {
	s.mu.Lock()
	s.jobs = append(s.jobs, job)
	s.mu.Unlock()
	return s.save()
}

// Update updates an existing job.
func (s *Store) Update(id string, fn func(*Job) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, j := range s.jobs {
		if j.ID == id {
			if err := fn(j); err != nil {
				return err
			}
			s.jobs[i] = j
			s.dirty = true
			return s.save()
		}
	}
	return fmt.Errorf("job not found: %s", id)
}

// Remove deletes a job by ID.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, j := range s.jobs {
		if j.ID == id {
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			return s.save()
		}
	}
	return fmt.Errorf("job not found: %s", id)
}

// DueJobs returns all enabled jobs that are due to run.
func (s *Store) DueJobs() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	var due []*Job
	for _, j := range s.jobs {
		if !j.Enabled || j.State == "running" {
			continue
		}
		if j.NextRunAt == "" {
			continue
		}
		nextRun, err := time.Parse(time.RFC3339, j.NextRunAt)
		if err != nil {
			continue
		}
		if !now.After(nextRun) {
			continue
		}
		// Check repeat limit
		if j.Repeat.Times != nil && j.Repeat.Completed >= *j.Repeat.Times {
			continue
		}
		due = append(due, j)
	}
	return due
}
