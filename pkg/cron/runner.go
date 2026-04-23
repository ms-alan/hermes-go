package cron

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nousresearch/hermes-go/pkg/agent"
	"github.com/nousresearch/hermes-go/pkg/skill"
)

// AicallRunner runs a job's prompt using the AIAgent.
type AicallRunner struct {
	SessionAgent *agent.SessionAgent
	SkillLoader  *skill.Loader
	Logger       *slog.Logger
}

// Run executes the job's prompt and returns the response text.
func (r *AicallRunner) Run(ctx context.Context, job *Job) (string, error) {
	if r.SessionAgent == nil {
		return "", fmt.Errorf("no session agent available")
	}
	resp, err := r.SessionAgent.Chat(ctx, job.Prompt)
	if err != nil {
		return "", fmt.Errorf("session agent error: %w", err)
	}
	return resp, nil
}
