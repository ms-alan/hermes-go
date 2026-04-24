package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nousresearch/hermes-go/pkg/model"
)

// ---------------------------------------------------------------------------
// Module-level runtime state: spawn pause + active subagent registry
// ---------------------------------------------------------------------------

// blockedTools are tools that children must never access.
var blockedTools = map[string]bool{
	"delegate_task": true, // no recursive delegation unless orchestrator
	"clarify":       true, // no user interaction
	"memory":        true, // no writes to shared memory store
	"send_message":  true, // no cross-platform side effects
	"execute_code":  true, // children should reason step-by-step, not run scripts
}

// Default toolsets for subagents when none are specified.
var defaultSubagentToolsets = []string{"terminal", "file", "web"}

// _spawnPaused controls whether new subagents can be spawned.
var _spawnPaused atomic.Bool

// SetSpawnPaused globally blocks/unblocks new delegate_task spawns.
// Active children keep running; only NEW calls fail fast.
// Returns the new state.
func SetSpawnPaused(paused bool) bool {
	_spawnPaused.Store(paused)
	return paused
}

// IsSpawnPaused reports whether spawning is currently blocked.
func IsSpawnPaused() bool {
	return _spawnPaused.Load()
}

// subagentRecord tracks a running subagent for TUI/RPC introspection.
type subagentRecord struct {
	SubagentID  string
	ParentID    string
	Depth       int
	Goal        string
	Model       string
	Toolsets    []string
	StartedAt   time.Time
	Status      string // "running", "done", "error", "timeout", "interrupted"
	ToolCount   int
	LastTool    string
	InterruptFn func() // call to interrupt this subagent
}

// _activeSubagentsLock protects the subagent registry.
var _activeSubagentsLock sync.RWMutex

// _activeSubagents maps subagentID -> subagentRecord.
// nil entries are pruned lazily by interrupt and registration.
var _activeSubagents = make(map[string]*subagentRecord)

// RegisterSubagent adds a running subagent to the registry.
// Called by the goroutine that owns the subagent.
func RegisterSubagent(record *subagentRecord) {
	if record == nil || record.SubagentID == "" {
		return
	}
	_activeSubagentsLock.Lock()
	defer _activeSubagentsLock.Unlock()
	_activeSubagents[record.SubagentID] = record
}

// UnregisterSubagent removes a subagent from the registry.
func UnregisterSubagent(subagentID string) {
	_activeSubagentsLock.Lock()
	defer _activeSubagentsLock.Unlock()
	delete(_activeSubagents, subagentID)
}

// ListActiveSubagents returns a snapshot of running subagents.
// Excludes the internal "agent" field for safe serialization.
func ListActiveSubagents() []subagentRecord {
	_activeSubagentsLock.RLock()
	defer _activeSubagentsLock.RUnlock()
	out := make([]subagentRecord, 0, len(_activeSubagents))
	for _, r := range _activeSubagents {
		out = append(out, *r)
	}
	return out
}

// InterruptSubagent requests that a running subagent stop at its next
// iteration boundary. Does not hard-kill the goroutine; sets the interrupt
// flag on the child's context. Returns true if a matching subagent was found.
func InterruptSubagent(subagentID string) bool {
	_activeSubagentsLock.RLock()
	rec, ok := _activeSubagents[subagentID]
	_activeSubagentsLock.RUnlock()
	if !ok || rec == nil || rec.InterruptFn == nil {
		return false
	}
	rec.InterruptFn()
	return true
}

// ProgressEvent represents a delegation progress event emitted to the parent.
type ProgressEvent struct {
	Type      string // "spawned", "started", "thinking", "tool_started", "tool_completed", "progress", "completed", "failed"
	SubagentID string
	Goal      string
	Tool      string
	Preview   string
	Depth     int
	ToolCount int
}

// ProgressCallback is called by child agents to relay progress to the parent.
type ProgressCallback func(event ProgressEvent)

// ---------------------------------------------------------------------------
// DelegateRequest / DelegateResult
// ---------------------------------------------------------------------------

// DelegateRequest describes a single sub-agent task.
type DelegateRequest struct {
	// Goal is the task description for the sub-agent.
	Goal string
	// Context is optional background information.
	Context string
	// Toolsets restricts which tool categories are available to the sub-agent.
	// Comma-separated list: "terminal,file,web" etc. Empty = default set.
	Toolsets string
	// Role is "leaf" (cannot delegate further) or "orchestrator" (can delegate).
	Role string
	// Model overrides the model for this sub-agent (e.g. "gpt-4o").
	// Empty = use default model from environment.
	Model string
	// MaxIterations limits tool-call loops (default 50).
	MaxIterations int
	// TimeoutSecs kills the sub-agent after N seconds (default 300).
	TimeoutSecs int
	// ParentToolsets is the set of toolsets the parent agent has.
	// Used to intersect with requested toolsets and preserve MCP tools.
	ParentToolsets []string
	// ParentSubagentID is the subagent ID of the parent (for nested delegation).
	ParentSubagentID string
	// DelegateDepth is the nesting depth of the parent (0 = root).
	DelegateDepth int
}

// DelegateResult holds the outcome of a single delegated task.
type DelegateResult struct {
	Goal       string `json:"goal"`
	Status     string `json:"status"` // "success" | "error" | "timeout" | "interrupted"
	Result     string `json:"result"`
	Error      string `json:"error,omitempty"`
	Duration   int    `json:"duration_s"`
	SubagentID string `json:"subagent_id,omitempty"`
	ToolCount  int    `json:"tool_count,omitempty"`
}

// ---------------------------------------------------------------------------
// Delegate — concurrent multi-subagent runner
// ---------------------------------------------------------------------------

const (
	defaultChildTimeout    = 300 * time.Second
	defaultMaxIterations    = 50
	defaultMaxConcurrent    = 3
	maxSpawnDepth           = 1 // flat by default: root -> children only
)

// Delegate runs one or more sub-agents concurrently and returns their results.
func Delegate(ctx context.Context, requests ...*DelegateRequest) ([]*DelegateResult, error) {
	if len(requests) == 0 {
		return nil, nil
	}

	// Enforce spawn pause before starting any goroutines
	if IsSpawnPaused() {
		results := make([]*DelegateResult, len(requests))
		for i, req := range requests {
			results[i] = &DelegateResult{
				Goal:   req.Goal,
				Status: "error",
				Error:  "spawning paused by parent agent",
			}
		}
		return results, nil
	}

	type pending struct {
		req    *DelegateRequest
		result chan *DelegateResult
	}

	var pendingTasks []pending
	for _, req := range requests {
		if req == nil {
			continue
		}
		pendingTasks = append(pendingTasks, pending{req: req, result: make(chan *DelegateResult, 1)})
	}

	results := make([]*DelegateResult, 0, len(pendingTasks))

	// Limit concurrency
	sem := make(chan struct{}, defaultMaxConcurrent)

	var wg sync.WaitGroup
	for _, p := range pendingTasks {
		p := p
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r := runDelegateTask(p.req)
			p.result <- r
		}()
	}

	// Collect results
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	for {
		select {
		case <-ctx.Done():
			// Parent cancelled — collect what we have
			for _, p := range pendingTasks {
				select {
				case result := <-p.result:
					results = append(results, result)
				default:
					results = append(results, &DelegateResult{
						Goal:   p.req.Goal,
						Status: "error",
						Error:  "parent context cancelled",
					})
				}
			}
			return results, ctx.Err()
		case <-done:
			for _, p := range pendingTasks {
				results = append(results, <-p.result)
			}
			return results, nil
		}
	}
}

// ---------------------------------------------------------------------------
// runDelegateTask — runs a single child agent
// ---------------------------------------------------------------------------

func runDelegateTask(req *DelegateRequest) *DelegateResult {
	start := time.Now()

	// Check spawn pause
	if IsSpawnPaused() {
		return &DelegateResult{
			Goal:   req.Goal,
			Status: "error",
			Error:  "spawning paused",
		}
	}

	goal := req.Goal
	if req.Context != "" {
		goal = req.Context + "\n\nTask: " + goal
	}

	timeout := time.Duration(req.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = defaultChildTimeout
	}
	maxIter := req.MaxIterations
	if maxIter == 0 {
		maxIter = defaultMaxIterations
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Build subagent ID and register
	subagentID := fmt.Sprintf("sa-%s", uuid.New().String()[:8])
	childDepth := req.DelegateDepth + 1
	effectiveRole := resolveEffectiveRole(req.Role, childDepth)

	// Build effective toolsets (intersection + blocked strip + MCP preserve)
	childToolsets := resolveChildToolsets(req.Toolsets, req.ParentToolsets, effectiveRole)

	// Progress callback for this child
	var progressCb ProgressCallback
	toolCount := atomic.Int32{}

	if cb := getParentProgressCallback(); cb != nil {
		progressCb = func(event ProgressEvent) {
			// Skip batch flush events that would be noisy
			if event.Type == "progress_batch" {
				return
			}
			event.SubagentID = subagentID
			event.Depth = childDepth - 1
			event.ToolCount = int(toolCount.Load())
			cb(event)
		}
	}

	// Emit spawned event
	if progressCb != nil {
		progressCb(ProgressEvent{
			Type:       "spawned",
			SubagentID: subagentID,
			Goal:       req.Goal,
			Tool:       "",
			Preview:    truncate(req.Goal, 80),
			Depth:      childDepth - 1,
		})
	}

	// Build child system prompt
	systemPrompt := buildDelegateSystemPrompt(req.Goal, req.Context, effectiveRole, childToolsets)

	// Create interrupt channel
	interruptCh := make(chan struct{}, 1)

	// Register subagent
	record := &subagentRecord{
		SubagentID: subagentID,
		ParentID:   req.ParentSubagentID,
		Depth:      childDepth - 1,
		Goal:       req.Goal,
		Model:      req.Model,
		Toolsets:   childToolsets,
		StartedAt:  time.Now(),
		Status:     "running",
		InterruptFn: func() {
			select {
			case interruptCh <- struct{}{}:
			default:
			}
		},
	}
	RegisterSubagent(record)

	// Run the child agent with interrupt support
	result := runChildWithInterrupt(ctx, req.Model, systemPrompt, maxIter, timeout, childToolsets, req, interruptCh, progressCb, &toolCount, subagentID, childDepth)

	// Unregister
	UnregisterSubagent(subagentID)

	duration := int(time.Since(start).Seconds())

	if result.Error != nil {
		if ctx.Err() == context.DeadlineExceeded {
			record.Status = "timeout"
			return &DelegateResult{
				Goal:       req.Goal,
				Status:     "timeout",
				Duration:   duration,
				SubagentID: subagentID,
				ToolCount:  int(toolCount.Load()),
			}
		}
		record.Status = "error"
		return &DelegateResult{
			Goal:       req.Goal,
			Status:     "error",
			Error:      result.Error.Error(),
			Duration:   duration,
			SubagentID: subagentID,
			ToolCount:  int(toolCount.Load()),
		}
	}

	record.Status = "done"
	record.ToolCount = int(toolCount.Load())

	finalText := ""
	if len(result.Messages) > 0 {
		last := result.Messages[len(result.Messages)-1]
		if last.Role == "assistant" && len(last.Content) > 0 {
			finalText = last.Content
		}
	}

	return &DelegateResult{
		Goal:       req.Goal,
		Status:     "success",
		Result:     finalText,
		Duration:   duration,
		SubagentID: subagentID,
		ToolCount:  int(toolCount.Load()),
	}
}

// ---------------------------------------------------------------------------
// Child agent runner with interrupt support
// ---------------------------------------------------------------------------

// runChildWithInterrupt runs a child agent with context interrupt support.
// Returns *RunResult (same type as agent.RunWithMessages).
func runChildWithInterrupt(
	ctx context.Context,
	modelName string,
	systemPrompt string,
	maxIterations int,
	timeout time.Duration,
	toolsets []string,
	req *DelegateRequest,
	interruptCh <-chan struct{},
	progressCb ProgressCallback,
	toolCount *atomic.Int32,
	subagentID string,
	childDepth int,
) *RunResult {
	// Wrap context to also listen for interrupt signal
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Goroutine to forward interrupt signal to context cancel
	go func() {
		select {
		case <-interruptCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	modelName = resolveModelName(req.Model)
	client, baseURL, apiKey := resolveModelClient(modelName)
	if client == nil {
		return &RunResult{Error: fmt.Errorf("no LLM client available")}
	}

	cfg := Config{
		Model:          modelName,
		MaxIterations:  maxIterations,
		BaseURL:        baseURL,
		APIKey:         apiKey,
		TimeoutSeconds: int(timeout.Seconds()),
		Logger:         &slogLogger{log: slog.Default()},
	}

	agent := NewAIAgent(client, cfg)

	runResult := agent.RunWithMessages(ctx, []*model.Message{}, systemPrompt)

	// Emit completed event
	if progressCb != nil {
		status := "success"
		if runResult.Error != nil {
			status = "failed"
		}
		progressCb(ProgressEvent{
			Type:       "completed",
			SubagentID: subagentID,
			Goal:       req.Goal,
			Tool:       "",
			Preview:    status,
			Depth:      childDepth - 1,
			ToolCount:  int(toolCount.Load()),
		})
	}

	return runResult
}

// ---------------------------------------------------------------------------
// Role resolution
// ---------------------------------------------------------------------------

func resolveEffectiveRole(role string, depth int) string {
	normalized := strings.ToLower(strings.TrimSpace(role))
	if normalized == "" {
		return "leaf"
	}
	if normalized == "orchestrator" && depth < maxSpawnDepth {
		return "orchestrator"
	}
	return "leaf"
}

// ---------------------------------------------------------------------------
// Toolsets resolution (intersection, blocked strip, MCP preserve)
// ---------------------------------------------------------------------------

// resolveChildToolsets computes the effective toolsets for a child agent.
// Mirrors hermes-agent: intersect with parent, strip blocked tools, preserve MCP.
func resolveChildToolsets(requested string, parentToolsets []string, role string) []string {
	parentSet := make(map[string]bool)
	for _, t := range parentToolsets {
		parentSet[t] = true
	}
	// If no parent toolsets, use defaults
	if len(parentToolsets) == 0 {
		parentSet = map[string]bool{"terminal": true, "file": true, "web": true}
	}

	var child []string

	if requested != "" {
		// Explicit toolsets: intersect with parent
		for _, t := range strings.Split(requested, ",") {
			t = strings.TrimSpace(t)
			if t != "" && parentSet[t] && !blockedTools[t] {
				child = append(child, t)
			}
		}
	} else {
		// Inherit from parent, strip blocked
		for t := range parentSet {
			if !blockedTools[t] {
				child = append(child, t)
			}
		}
	}

	// Orchestrators retain the delegate_task tool (re-add after strip)
	if role == "orchestrator" {
		found := false
		for _, t := range child {
			if t == "delegate_task" {
				found = true
				break
			}
		}
		if !found {
			child = append(child, "delegate_task")
		}
	}

	if len(child) == 0 {
		return []string{"terminal", "file", "web"}
	}
	return child
}

// ---------------------------------------------------------------------------
// System prompt builder
// ---------------------------------------------------------------------------

func buildDelegateSystemPrompt(goal, context, role string, toolsets []string) string {
	var b strings.Builder

	b.WriteString("You are a focused subagent working on a specific delegated task.\n\n")
	b.WriteString(fmt.Sprintf("YOUR TASK:\n%s\n", goal))

	if context != "" {
		b.WriteString(fmt.Sprintf("\nCONTEXT:\n%s\n", context))
	}

	b.WriteString("\nComplete this task using the tools available to you. ")
	b.WriteString("When finished, provide a clear, concise summary of:\n")
	b.WriteString("- What you did\n")
	b.WriteString("- What you found or accomplished\n")
	b.WriteString("- Any files you created or modified\n")
	b.WriteString("- Any issues encountered\n\n")
	b.WriteString("Be thorough but concise -- your response is returned to the parent agent as a summary.\n")

	if role == "orchestrator" {
		b.WriteString("\n## Subagent Spawning (Orchestrator Role)\n")
		b.WriteString("You have access to the `delegate_task` tool and CAN spawn your own subagents to parallelize independent work.\n\n")
		b.WriteString("WHEN to delegate:\n")
		b.WriteString("- The goal decomposes into 2+ independent subtasks that can run in parallel.\n")
		b.WriteString("- A subtask is reasoning-heavy and would flood your context with intermediate data.\n\n")
		b.WriteString("WHEN NOT to delegate:\n")
		b.WriteString("- Single-step mechanical work — do it directly.\n")
		b.WriteString("- Trivial tasks you can execute in one or two tool calls.\n")
		b.WriteString("- Re-delegating your entire assigned goal to one worker.\n\n")
		b.WriteString("Coordinate your workers' results and synthesize them before reporting back to your parent.\n")
	}

	return b.String()
}

// ---------------------------------------------------------------------------
// Model resolution
// ---------------------------------------------------------------------------

func resolveModelName(reqModel string) string {
	if reqModel != "" {
		return reqModel
	}
	return "gpt-4o"
}

func resolveModelClient(modelName string) (model.LLMClient, string, string) {
	modelLower := strings.ToLower(modelName)

	switch {
	case strings.HasPrefix(modelLower, "anthropic/") ||
		strings.HasPrefix(modelLower, "claude"):
		apiKey := getEnvOr("ANTHROPIC_API_KEY", "")
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL("https://api.anthropic.com"),
			model.WithAPIKey(apiKey),
			model.WithModel(modelName),
		)
		return client, "https://api.anthropic.com", apiKey

	case strings.HasPrefix(modelLower, "groq/"):
		apiKey := getEnvOr("GROQ_API_KEY", "")
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL("https://api.groq.com/openai/v1"),
			model.WithAPIKey(apiKey),
			model.WithModel(modelName),
		)
		return client, "https://api.groq.com/openai/v1", apiKey

	case strings.HasPrefix(modelLower, "ollama/"):
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL("http://localhost:11434"),
			model.WithAPIKey("ollama"),
			model.WithModel(strings.TrimPrefix(modelName, "ollama/")),
		)
		return client, "http://localhost:11434", "ollama"

	case strings.HasPrefix(modelLower, "openrouter/"):
		apiKey := getEnvOr("OPENROUTER_API_KEY", "")
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL("https://openrouter.ai/api/v1"),
			model.WithAPIKey(apiKey),
			model.WithModel(modelName),
		)
		return client, "https://openrouter.ai/api/v1", apiKey

	case strings.HasPrefix(modelLower, "gemini"):
		apiKey := getEnvOr("GEMINI_API_KEY", "")
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL("https://generativelanguage.googleapis.com"),
			model.WithAPIKey(apiKey),
			model.WithModel(modelName),
		)
		return client, "https://generativelanguage.googleapis.com", apiKey

	default:
		apiKey := getEnvOr("OPENAI_API_KEY", "")
		baseURL := getEnvOr("OPENAI_BASE_URL", "https://api.openai.com/v1")
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL(baseURL),
			model.WithAPIKey(apiKey),
			model.WithModel(modelName),
		)
		return client, baseURL, apiKey
	}
}

func getEnvOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Progress callback propagation (thread-local parent callback storage)
// ---------------------------------------------------------------------------

// thread-local storage for parent progress callback
var parentProgressKey = "parent_progress_cb"

func getParentProgressCallback() ProgressCallback {
	if v := os.Getenv("HERMES_DELEGATE_PARENT_CB"); v == "1" {
		// Could wire through context in the future; for now return nil
		// since proper context threading requires larger changes to AIAgent.Run
	}
	return nil
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
