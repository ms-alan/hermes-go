package cron

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

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
// It prepends skill content and script output to the prompt if configured.
func (r *AicallRunner) Run(ctx context.Context, job *Job) (string, error) {
	if r.SessionAgent == nil {
		return "", fmt.Errorf("no session agent available")
	}

	prompt := job.Prompt

	// Prepend script output if configured
	if job.Script != "" {
		if output, err := r.runScript(ctx, job.Script); err != nil {
			r.Logger.Warn("script failed, continuing without it",
				"script", job.Script, "error", err)
		} else if output != "" {
			prompt = fmt.Sprintf("[Script %s output]:\n%s\n\n[Original prompt]:\n%s",
				job.Script, output, prompt)
		}
	}

	// Prepend skill content if configured
	if len(job.Skills) > 0 {
		skillContent := r.loadSkillsForPrompt(job.Skills, job.Name)
		if skillContent != "" {
			prompt = skillContent + "\n\n" + prompt
		}
	}

	resp, err := r.SessionAgent.Chat(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("session agent error: %w", err)
	}
	return resp, nil
}

// loadSkillsForPrompt loads skill content and returns it as a formatted string
// to prepend to the job prompt. Mirrors hermes-agent's build_skill_prompt().
func (r *AicallRunner) loadSkillsForPrompt(skillNames []string, jobName string) string {
	var parts []string
	var missing []string

	for _, name := range skillNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		sk := skill.Get(name)
		if sk == nil {
			missing = append(missing, name)
			continue
		}

		// Get the skill description + commands as the main instruction block
		var block strings.Builder
		block.WriteString(fmt.Sprintf("[SYSTEM: The user has invoked the %q skill.]", name))

		if sk.Description != "" {
			block.WriteString("\n\n")
			block.WriteString(sk.Description)
		}
		if len(sk.Commands) > 0 {
			block.WriteString("\n\nCommands: ")
			block.WriteString(strings.Join(sk.Commands, ", "))
		}

		// Load tier-3 linked files (references/, templates/, scripts/)
		if r.SkillLoader != nil {
			if _, err := skill.EnsureLinkedFiles(name); err != nil {
				r.Logger.Warn("failed to load linked files for skill",
					"skill", name, "error", err)
			}
		}
		sk = skill.Get(name) // refresh after EnsureLinkedFiles
		if sk != nil && len(sk.LinkedFiles) > 0 {
			block.WriteString("\n\n[Linked resources]:")
			for relPath := range sk.LinkedFiles {
				block.WriteString(fmt.Sprintf("\n- %s (use skill_view(%q, %q) to load content)",
					relPath, name, relPath))
			}
		}

		parts = append(parts, block.String())
	}

	var builder strings.Builder

	// Add found skills
	if len(parts) > 0 {
		builder.WriteString("You have been loaded with the following skill(s). Follow their instructions:\n\n")
		builder.WriteString(strings.Join(parts, "\n\n---\n\n"))
	}

	// Report missing skills
	if len(missing) > 0 {
		if len(parts) > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(fmt.Sprintf(
			"[SYSTEM NOTE: The following skill(s) were listed for this job %q but could not be found: %v. Proceed without them.]",
			jobName, missing))
	}

	return builder.String()
}

// runScript executes a script and returns its stdout.
// The script is run with the hermes environment variables set.
func (r *AicallRunner) runScript(ctx context.Context, scriptPath string) (string, error) {
	scriptPath = os.ExpandEnv(scriptPath)
	if _, err := os.Stat(scriptPath); err != nil {
		return "", fmt.Errorf("script not found: %w", err)
	}

	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"HERMES_CRON=1",
	)

	output, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("script exited: %s", string(ee.Stderr))
		}
		return "", fmt.Errorf("run script: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}
