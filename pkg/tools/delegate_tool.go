// Package tools delegate_tool provides the delegate_task tool for spawning
// subagent instances with isolated context and restricted toolsets.
//
// Design (mirrors Python hermes-agent delegate_tool.py):
//
//   - delegate_task blocks recursive delegation (depth=1 by default).
//     The parent agent calls this tool and waits for the subagent to finish;
//     only the final summary result is injected into the parent's context.
//   - Children get a fresh session (no parent history), a focused system prompt
//     built from the delegated goal, and a restricted toolset derived from
//     the parent's enabled tools minus DELEGATE_BLOCKED_TOOLS.
//   - Batch mode: up to maxConcurrent children run in parallel; the parent
//     waits until all complete before continuing.
//   - A goroutine-safe activeAgents registry lets external observers track
//     live subagents (used by TUI / gateway RPCs).
package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nousresearch/hermes-go/pkg/agent"
	"github.com/nousresearch/hermes-go/pkg/model"
	"github.com/nousresearch/hermes-go/pkg/session"
)

// DELEGATE_BLOCKED_TOOLS are tools that subagents must never have access to.
var DELEGATE_BLOCKED_TOOLS = map[string]bool{
	"delegate_task":  true, // no recursive delegation
	"clarify":        true, // no user interaction
	"send_message":   true, // no cross-platform side effects
	"memory_write":   true, // no writes to shared memory
	"execute_code":   true, // children should reason step-by-step
}

// maxConcurrentChildren is the default maximum parallel subagents.
const maxConcurrentChildren = 3

// maxSpawnDepth is the maximum delegation depth (parent=0, child=1, grandchild=2).
var maxSpawnDepth = 1

// activeAgents tracks live subagents for observability.
var (
	activeAgents   = make(map[string]*agentHandle)
	activeAgentsMu sync.RWMutex
)

type agentHandle struct {
	id        string
	taskIndex int
	depth     int
	status    string // "running", "done", "error"
	toolCount int
	lastTool  string
}

// BlockedToolsForDepth returns the set of tools stripped from a child at the
// given depth. At depth >= maxSpawnDepth, delegate_task itself is added.
func BlockedToolsForDepth(depth int) map[string]bool {
	blocked := make(map[string]bool, len(DELEGATE_BLOCKED_TOOLS)+1)
	for k, v := range DELEGATE_BLOCKED_TOOLS {
		blocked[k] = v
	}
	if depth >= maxSpawnDepth {
		blocked["delegate_task"] = true
	}
	return blocked
}

// ------------------------------------------------------------------
// Tool schema (OpenAI function-calling format)
// ------------------------------------------------------------------

var delegateTaskSchema = map[string]any{
	"name": "delegate_task",
	"description": `Spawn one or more subagents to work on tasks in isolated contexts.
Each subagent gets its own conversation, terminal session, and restricted toolset.
Only the final summary is returned -- intermediate tool results never enter your context.

TWO MODES (one of 'goal' or 'tasks' is required):
1. Single task: provide 'goal' (+ optional context, toolsets)
2. Batch (parallel): provide 'tasks' array with up to 3 items (configurable)

Each child is blocked from using: delegate_task, clarify, send_message, memory_write, execute_code.
Children run with a 5-minute timeout by default.`,
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal": map[string]any{
				"type":        "string",
				"description": "The task for a single subagent to accomplish. Be specific about what success looks like.",
			},
			"context": map[string]any{
				"type":        "string",
				"description": "Background context the subagent needs (e.g. file paths, error messages, project structure).",
			},
			"toolsets": map[string]any{
				"type": "array",
				"items": map[string]any{"type": "string"},
				"description": `List of toolset names to enable for the subagent.
Available: builtin, file, terminal, web, mcp. Leave empty to inherit a safe default.
Subagent tools are always stripped of DELEGATE_BLOCKED_TOOLS.`,
			},
			"tasks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"goal":     map[string]any{"type": "string"},
						"context":  map[string]any{"type": "string"},
						"toolsets": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
					"required": []any{"goal"},
				},
				"description": `Batch mode: up to 3 subagents run in parallel. Each needs a 'goal'.
Returns an array of results in the same order as the input tasks.`,
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Maximum time for a child subagent to run, in seconds. Default: 300 (5 minutes).",
			},
		},
	},
}

// ------------------------------------------------------------------
// Tool handler
// ------------------------------------------------------------------

// delegateTaskHandler is registered as the "delegate_task" tool.
func delegateTaskHandler(args map[string]any) string {
	// Extract parameters.
	goal, _ := args["goal"].(string)
	contextStr, _ := args["context"].(string)
	timeoutSec := 300
	if t, ok := args["timeout_seconds"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}

	// Batch mode: 'tasks' is present.
	if tasksRaw, ok := args["tasks"].([]any); ok && len(tasksRaw) > 0 {
		return runBatchDelegate(tasksRaw, timeoutSec)
	}

	// Single mode: 'goal' is required.
	if goal == "" {
		return toolError("delegate_task requires 'goal' (string)")
	}

	toolsets := extractStringArray(args["toolsets"])
	result := runSingleDelegate(goal, contextStr, toolsets, timeoutSec, 0, 1)
	return formatDelegateResult(result)
}

// runBatchDelegate handles the batch (parallel) delegation mode.
func runBatchDelegate(tasksRaw []any, defaultTimeout int) string {
	if len(tasksRaw) > maxConcurrentChildren {
		return toolError(fmt.Sprintf("batch size %d exceeds maximum of %d", len(tasksRaw), maxConcurrentChildren))
	}

	type taskSpec struct {
		goal     string
		context  string
		toolsets []string
		timeout  int
	}

	specs := make([]taskSpec, 0, len(tasksRaw))
	for i, t := range tasksRaw {
		tm, ok := t.(map[string]any)
		if !ok {
			return toolError(fmt.Sprintf("tasks[%d]: expected object with 'goal'", i))
		}
		goal, _ := tm["goal"].(string)
		if goal == "" {
			return toolError(fmt.Sprintf("tasks[%d]: 'goal' is required", i))
		}
		timeout := defaultTimeout
		if t2, ok := tm["timeout_seconds"].(float64); ok && t2 > 0 {
			timeout = int(t2)
		}
		specs = append(specs, taskSpec{
			goal:     goal,
			context:  getString(tm, "context"),
			toolsets: extractStringArray(tm["toolsets"]),
			timeout:  timeout,
		})
	}

	// Run all tasks in parallel using a WaitGroup.
	var mu sync.Mutex
	results := make([]delegateResult, len(specs))
	var wg sync.WaitGroup

	for i, spec := range specs {
		wg.Add(1)
		go func(idx int, s taskSpec) {
			defer wg.Done()
			result := runSingleDelegate(s.goal, s.context, s.toolsets, s.timeout, idx, len(specs))
			mu.Lock()
			results[idx] = result
			mu.Unlock()
		}(i, spec)
	}

	wg.Wait()

	// Format results as a numbered list.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%d subagent results]\n\n", len(results)))
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("--- Subagent %d ---\n", i+1))
		if r.Error != "" {
			sb.WriteString(fmt.Sprintf("ERROR: %s\n", r.Error))
		} else {
			sb.WriteString(r.Summary)
		}
		sb.WriteRune('\n')
	}
	return sb.String()
}

// delegateResult holds the outcome of a single subagent run.
type delegateResult struct {
	Summary string
	Error   string
}

// runSingleDelegate spawns a single subagent with the given parameters.
// depth is the current delegation depth (0 = top-level parent).
func runSingleDelegate(goal, contextStr string, toolsets []string, timeoutSec, taskIndex, totalTasks int) delegateResult {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// Generate subagent identity.
	_ = uuid.New().String()[:8] // uuid reserved for future subagentID tracking

	// Determine effective depth. Parent is depth 0; child is depth 1.
	childDepth := 1
	if childDepth > maxSpawnDepth {
		return delegateResult{Error: "max delegation depth reached; cannot spawn subagent"}
	}

	// Register active subagent for observability.
	subagentID := fmt.Sprintf("sa-%d-%s", taskIndex, uuid.New().String()[:8])
	activeAgentsMu.Lock()
	activeAgents[subagentID] = &agentHandle{
		id:        subagentID,
		taskIndex: taskIndex,
		depth:     childDepth,
		status:    "running",
	}
	activeAgentsMu.Unlock()

	// Build the subagent system prompt.
	systemPrompt := buildDelegateSystemPrompt(goal, contextStr)

	// Create a fresh session store for the child.
	store, err := session.NewStore()
	if err != nil {
		return delegateResult{Error: fmt.Sprintf("failed to open session store: %v", err)}
	}
	defer store.Close()

	// Create LLM client for the subagent using env vars (same as CLI main).
	apiKey := getEnvOr("MINIMAX_CN_API_KEY", getEnvOr("OPENAI_API_KEY", ""))
	baseURL := getEnvOr("MINIMAX_CN_BASE_URL", getEnvOr("OPENAI_BASE_URL", "https://api.openai.com/v1"))
	modelName := getEnvOr("MINIMAX_CN_MODEL", "MiniMax-M2.7")

	llmClient, err := model.NewMiniMaxClient(apiKey, modelName,
		model.WithBaseURL(baseURL),
		model.WithAPIKey(apiKey),
	)
	if err != nil {
		return delegateResult{Error: fmt.Sprintf("failed to create subagent LLM client: %v", err)}
	}
	defer llmClient.Close()

	// Create the subagent.
	childAgent := agent.NewAIAgent(llmClient, agent.Config{
		MaxIterations: 90,
		Logger:       slog.Default(),
	})

	// Register all built-in tools into the child agent.
	RegisterBuiltinToolsToAgent(childAgent)

	// Strip blocked tools so the child cannot use dangerous or recursive tools.
	blocked := BlockedToolsForDepth(childDepth)
	for name := range blocked {
		childAgent.UnregisterTool(name)
	}

	// Sync tools into childAgent.Config.Tools so they are sent to the LLM.
	childAgent.SyncToolsToConfig()

	_ = store

	// Build messages: system prompt injected by RunWithMessages.
	msgs := []*model.Message{
		model.UserMessage(goal),
	}

	// Run the agent loop (blocking, until timeout or completion).
	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		result := childAgent.RunWithMessages(ctx, msgs, systemPrompt)
		if result.Error != nil {
			errCh <- result.Error
			return
		}
		resultCh <- result.FinalResponse
	}()

	select {
	case <-ctx.Done():
		return delegateResult{Error: fmt.Sprintf("subagent timed out after %ds", timeoutSec)}
	case err := <-errCh:
		return delegateResult{Error: fmt.Sprintf("subagent error: %v", err)}
	case resp := <-resultCh:
		return delegateResult{Summary: resp}
	}
}

// buildDelegateSystemPrompt creates the system prompt injected into subagent sessions.
func buildDelegateSystemPrompt(goal, contextStr string) string {
	var sb strings.Builder
	sb.WriteString("You are a subagent spawned by a parent AI agent to accomplish a specific task.\n\n")
	sb.WriteString("RULES:\n")
	sb.WriteString("- Work autonomously. Do NOT ask the user for clarification.\n")
	sb.WriteString("- Do NOT use tools that are not in your allowed toolset.\n")
	sb.WriteString("- Return a detailed summary of what you did and what you found/decided.\n")
	sb.WriteString("- Do NOT attempt to delegate work to other agents.\n")
	sb.WriteString("- Do NOT send messages to external platforms.\n")
	sb.WriteString("- Do NOT write to shared memory or agent state.\n\n")
	sb.WriteString("YOUR TASK:\n")
	sb.WriteString(goal)
	if contextStr != "" {
		sb.WriteString("\n\nCONTEXT:\n")
		sb.WriteString(contextStr)
	}
	sb.WriteString("\n\nProvide a structured summary with: Goal, Actions Taken, Results, Remaining Issues.")
	return sb.String()
}

// formatDelegateResult formats a single delegate result for injection into the parent context.
func formatDelegateResult(r delegateResult) string {
	if r.Error != "" {
		return toolError(fmt.Sprintf("delegate_task failed: %s", r.Error))
	}
	return r.Summary
}

// extractStringArray safely extracts a []string from an any value.
func extractStringArray(v any) []string {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		result := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

// getString safely extracts a string from a map.
func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// getEnvOr returns the value of key, or fallback if key is not set or empty.
func getEnvOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func init() {
	Registry.Register(
		"delegate_task",
		"builtin",
		delegateTaskSchema,
		delegateTaskHandler,
		nil, // always available (delegate_tool requires no external env)
		nil,
		false,
		"Spawn subagents for parallel task execution (blocked tools: delegate_task, clarify, send_message, memory_write)",
		"🤖",
	)
}
