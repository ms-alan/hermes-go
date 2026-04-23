package tools

import (
	"fmt"
	"time"

	"github.com/nousresearch/hermes-go/pkg/cron"
)

var (
	globalCronStore      *cron.Store
	globalCronScheduler  *cron.Scheduler
)

// SetCronStore configures the global cron store.
func SetCronStore(store *cron.Store) {
	globalCronStore = store
}

// SetCronScheduler configures the global cron scheduler.
func SetCronScheduler(sched *cron.Scheduler) {
	globalCronScheduler = sched
}

// cronToolHandler is registered as the "cronjob" tool.
func cronToolHandler(args map[string]any) string {
	action, _ := args["action"].(string)

	if globalCronStore == nil {
		return toolError("cron store not configured")
	}

	switch action {
	case "create":
		return handleCronCreate(args)
	case "list":
		return handleCronList(args)
	case "get":
		return handleCronGet(args)
	case "remove":
		return handleCronRemove(args)
	case "pause":
		return handleCronPause(args)
	case "resume":
		return handleCronResume(args)
	case "run":
		return handleCronRun(args)
	default:
		return toolError("unknown action: "+action,
			"expected: create/list/get/remove/pause/resume/run")
	}
}

func handleCronCreate(args map[string]any) string {
	prompt, _ := args["prompt"].(string)
	scheduleStr, _ := args["schedule"].(string)
	name, _ := args["name"].(string)
	deliver, _ := args["deliver"].(string)
	repeat := args["repeat"]

	if prompt == "" {
		return toolError("prompt is required for create")
	}
	if scheduleStr == "" {
		return toolError("schedule is required for create")
	}

	// Parse schedule
	schedule, err := cron.ParseSchedule(scheduleStr)
	if err != nil {
		return toolError(fmt.Sprintf("invalid schedule: %v", err))
	}

	// Parse repeat
	var repeatTimes *int
	if repeat != nil {
		if f, ok := repeat.(float64); ok {
			t := int(f)
			if t > 0 {
				repeatTimes = &t
			}
		}
	}

	// Delivery default
	if deliver == "" {
		deliver = "local"
	}

	job := &cron.Job{
		ID:        cron.GenerateID(),
		Name:      name,
		Prompt:    prompt,
		Schedule:  schedule,
		Repeat: cron.RepeatConfig{
			Times: repeatTimes,
		},
		Enabled:    true,
		State:     "scheduled",
		CreatedAt: time.Now().Format(time.RFC3339),
		Deliver:   deliver,
		Skills:    parseSkillsArg(args),
	}

	// Compute first next run
	job.NextRunAt = cron.ComputeNextRun(schedule, "")

	if err := globalCronStore.Add(job); err != nil {
		return toolError(fmt.Sprintf("failed to save job: %v", err))
	}

	return toolResultData(map[string]any{
		"job":    job,
		"msg":    fmt.Sprintf("Created job %s (%s). Next run: %s", job.ID, job.Name, job.NextRunAt),
	})
}

func handleCronList(args map[string]any) string {
	allJobs := globalCronStore.List()

	// Optional filter by state/enabled
	filterEnabled := args["enabled"]
	filterState := args["state"]

	var filtered []*cron.Job
	for _, j := range allJobs {
		if filterEnabled != nil {
			wantEnabled := filterEnabled.(bool)
			if j.Enabled != wantEnabled {
				continue
			}
		}
		if filterState != nil {
			wantState := filterState.(string)
			if j.State != wantState {
				continue
			}
		}
		filtered = append(filtered, j)
	}

	type jobSummary struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Schedule     string `json:"schedule"`
		Enabled      bool   `json:"enabled"`
		State        string `json:"state"`
		NextRunAt    string `json:"nextRunAt"`
		LastRunAt    string `json:"lastRunAt"`
		LastStatus   string `json:"lastStatus"`
		Repeat       string `json:"repeat"`
		Deliver      string `json:"deliver"`
	}
	summaries := make([]jobSummary, len(filtered))
	for i, j := range filtered {
		repeatStr := "forever"
		if j.Repeat.Times != nil {
			repeatStr = fmt.Sprintf("%d/%d", j.Repeat.Completed, *j.Repeat.Times)
		}
		summaries[i] = jobSummary{
			ID:         j.ID,
			Name:       j.Name,
			Schedule:   j.Schedule.Display,
			Enabled:    j.Enabled,
			State:     j.State,
			NextRunAt:  j.NextRunAt,
			LastRunAt:  j.LastRunAt,
			LastStatus: j.LastStatus,
			Repeat:     repeatStr,
			Deliver:    j.Deliver,
		}
	}

	return toolResultData(map[string]any{
		"count": len(summaries),
		"jobs":   summaries,
	})
}

func handleCronGet(args map[string]any) string {
	id, _ := args["id"].(string)
	if id == "" {
		return toolError("id is required for get")
	}
	job := globalCronStore.Get(id)
	if job == nil {
		return toolError("job not found: " + id)
	}
	return toolResultData(map[string]any{"job": job})
}

func handleCronRemove(args map[string]any) string {
	id, _ := args["id"].(string)
	if id == "" {
		return toolError("id is required for remove")
	}
	if err := globalCronStore.Remove(id); err != nil {
		return toolError(fmt.Sprintf("remove failed: %v", err))
	}
	return toolResult("removed", id)
}

func handleCronPause(args map[string]any) string {
	id, _ := args["id"].(string)
	if id == "" {
		return toolError("id is required for pause")
	}
	if err := globalCronStore.Update(id, func(j *cron.Job) error {
		j.Enabled = false
		j.State = "paused"
		return nil
	}); err != nil {
		return toolError(fmt.Sprintf("pause failed: %v", err))
	}
	return toolResult("paused", id)
}

func handleCronResume(args map[string]any) string {
	id, _ := args["id"].(string)
	if id == "" {
		return toolError("id is required for resume")
	}
	if err := globalCronStore.Update(id, func(j *cron.Job) error {
		j.Enabled = true
		j.State = "scheduled"
		return nil
	}); err != nil {
		return toolError(fmt.Sprintf("resume failed: %v", err))
	}
	return toolResult("resumed", id)
}

func handleCronRun(args map[string]any) string {
	id, _ := args["id"].(string)
	if id == "" {
		return toolError("id is required for run")
	}
	if globalCronScheduler == nil {
		return toolError("cron scheduler not running")
	}
	if err := globalCronScheduler.RunNow(id); err != nil {
		return toolError(fmt.Sprintf("run failed: %v", err))
	}
	return toolResult("triggered", id)
}

func parseSkillsArg(args map[string]any) []string {
	var skills []string
	if s, ok := args["skills"].([]any); ok {
		for _, v := range s {
			if str, ok := v.(string); ok && str != "" {
				skills = append(skills, str)
			}
		}
	}
	if len(skills) == 0 {
		if s, ok := args["skill"].(string); ok && s != "" {
			skills = []string{s}
		}
	}
	return skills
}
