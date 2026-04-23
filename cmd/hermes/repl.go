package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/nousresearch/hermes-go/pkg/agent"
	hermescontext "github.com/nousresearch/hermes-go/pkg/context"
	hermesmemory "github.com/nousresearch/hermes-go/pkg/memory"
	"github.com/nousresearch/hermes-go/pkg/model"
	"github.com/nousresearch/hermes-go/pkg/session"
	"github.com/nousresearch/hermes-go/pkg/skill"
	"github.com/nousresearch/hermes-go/pkg/tools"
)

// repl represents the interactive Read-Eval-Print-Loop.
type repl struct {
	sessionAgent *agent.SessionAgent
	store        *session.Store
	ctxMgr       *hermescontext.Manager
	memStore     *hermesmemory.MemoryStore
	ctxLoader    *hermescontext.Loader
	logger       *slog.Logger
	modelClient  model.LLMClient
	defaultModel string
	systemPrompt string // frozen snapshot assembled at startup
}

// agentInterfaceWrapper adapts *SessionAgent to skill.AgentInterface.
type agentInterfaceWrapper struct {
	sa           *agent.SessionAgent
	systemPrompt string
}

func (w *agentInterfaceWrapper) Chat(ctx context.Context, message string) (string, error) {
	return w.sa.Chat(ctx, message)
}

func (w *agentInterfaceWrapper) SystemPrompt() string {
	return w.systemPrompt
}

// runSkill invokes a skill handler and prints its output.
func (r *repl) runSkill(ctx context.Context, sk *skill.Skill, args string) error {
	agent := &agentInterfaceWrapper{sa: r.sessionAgent, systemPrompt: r.systemPrompt}
	r.logger.Info("invoking skill", "skill", sk.Name, "args", args)
	result, err := sk.Handler(ctx, args, agent)
	if err != nil {
		return fmt.Errorf("skill %s failed: %w", sk.Name, err)
	}
	fmt.Printf("%s\n", result)
	return nil
}

// newREPL creates a new REPL instance.
func newREPL(agentCfg agent.Config, store *session.Store, logger *slog.Logger, modelClient model.LLMClient, defaultModel string) *repl {
	if logger == nil {
		logger = slog.Default()
	}
	// Create context manager (for message compression)
	ctxCfg := hermescontext.DefaultManagerConfig(200000)
	ctxMgr := hermescontext.NewManager(ctxCfg, logger, modelClient)

	// Create memory store
	memStore, err := hermesmemory.NewMemoryStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to create memory store: %v\n", err)
	} else {
		tools.SetMemoryStore(memStore)
	}

	// Create context file loader
	ctxLoader := hermescontext.NewLoader("", "")

	// Build system prompt from SOUL.md and memory
	systemPrompt := buildSystemPrompt(ctxLoader, memStore)

	// Create AIAgent
	aiAgent := agent.NewAIAgent(modelClient, agentCfg)

	// Register built-in tools from the tools package
	// (populates both the handler registry and the LLM-facing tool schemas)
	registerBuiltinTools(aiAgent)

	// Sync tools into agent.Config.Tools so AIAgent sends them in LLM requests
	aiAgent.SyncToolsToConfig()

	// Create SessionAgent
	sessionAgent := agent.NewSessionAgent(aiAgent, store, ctxMgr, logger)

	return &repl{
		sessionAgent: sessionAgent,
		store:        store,
		ctxMgr:       ctxMgr,
		memStore:     memStore,
		ctxLoader:    ctxLoader,
		logger:       logger,
		modelClient:  modelClient,
		defaultModel: defaultModel,
		systemPrompt: systemPrompt,
	}
}

// Run starts the REPL and runs it until the user types /exit,
// the context is cancelled (e.g. Ctrl+C), or an error occurs.
func (r *repl) Run(ctx context.Context) error {
	fmt.Println("Hermes REPL v0.1.0")
	fmt.Println("Type /help for available commands. Use Ctrl+C or /exit to quit.")
	fmt.Println()

	// Auto-create a new session
	_, err := r.sessionAgent.New("cli", r.defaultModel, r.systemPrompt)
	if err != nil {
		r.logger.Warn("failed to create initial session", "error", err)
	}
	r.logger.Info("session started", "session_id", r.sessionAgent.SessionID())

	// Goroutine: when ctx is cancelled, close stdin to unblock the scanner.
	// This lets us respond to Ctrl+C gracefully instead of getting killed.
	go func() {
		<-ctx.Done()
		// Context cancelled — close stdin to break scanner.Scan().
		// The next Scan() call will return false with io.EOF.
		_ = os.Stdin.Close()
	}()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("shutdown requested, exiting REPL")
			return nil
		default:
		}

		fmt.Print("> ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				if err == io.EOF {
					return nil
				}
				return fmt.Errorf("scanner error: %w", err)
			}
			return nil
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Check for slash commands.
		if strings.HasPrefix(line, "/") {
			if err := r.handleCommand(ctx, line); err != nil {
				r.logger.Error("command error", "error", err)
			}
			continue
		}

		// Regular user message — expand @ references, then send to SessionAgent
		line, _ = r.ctxLoader.ExpandRefs(line)
		resp, err := r.sessionAgent.Chat(ctx, line)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
		} else {
			fmt.Printf("%s\n", resp)
		}
	}
}

// handleCommand processes a slash command.
func (r *repl) handleCommand(ctx context.Context, cmd string) error {
	parts := strings.Fields(cmd)
	name := parts[0]
	args := ""
	if len(parts) > 1 {
		args = strings.Join(parts[1:], " ")
	}

	// Dynamic skill command dispatch — check registered skill aliases first
	if strings.HasPrefix(name, "/") {
		cmdName := strings.TrimPrefix(name, "/")
		if sk := skill.GetByCommand(cmdName); sk != nil {
			return r.runSkill(ctx, sk, args)
		}
		if sk := skill.Get(cmdName); sk != nil {
			return r.runSkill(ctx, sk, args)
		}
	}

	switch name {
	case "/help":
		r.printHelp()
	case "/tools":
		r.printTools()
	case "/sessions":
		r.printSessions()
	case "/skills":
		r.printSkills()
	case "/new":
		sessID, err := r.sessionAgent.New("cli", r.defaultModel, r.systemPrompt)
		if err != nil {
			return fmt.Errorf("failed to create session: %w", err)
		}
		r.logger.Info("new session started", "session_id", sessID)
		fmt.Printf("Switched to new session: %s\n", sessID)
	case "/switch":
		if args == "" {
			return fmt.Errorf("usage: /switch <session-id>")
		}
		if err := r.sessionAgent.Switch(args); err != nil {
			return fmt.Errorf("failed to switch session: %w", err)
		}
		fmt.Printf("Switched to session: %s\n", r.sessionAgent.SessionID())
	case "/search":
		if args == "" {
			return fmt.Errorf("usage: /search <query>")
		}
		results, err := r.sessionAgent.Search(args, 20, 0)
		if err != nil {
			return fmt.Errorf("search failed: %w", err)
		}
		if len(results) == 0 {
			fmt.Println("No results found.")
		} else {
			fmt.Printf("Found %d result(s):\n", len(results))
			for _, res := range results {
				fmt.Printf("  [%s] %s: %s\n", res.SessionID, res.Role, res.Snippet)
			}
		}
	case "/exit", "/quit":
		fmt.Println("Goodbye!")
		os.Exit(0)
	case "/clear":
		// Simple screen clear by printing newlines.
		fmt.Print("\033[2J\033[H")
	default:
		return fmt.Errorf("unknown command: %s", name)
	}
	return nil
}

// buildSystemPrompt assembles the initial system prompt from SOUL.md, memory, and project context.
func buildSystemPrompt(ctxLoader *hermescontext.Loader, memStore *hermesmemory.MemoryStore) string {
	var parts []string

	// Slot #1: SOUL.md — agent identity
	if soul, err := ctxLoader.LoadSOUL(); err == nil && soul != "" {
		parts = append(parts, soul)
	}

	// Memory snapshot
	if memStore != nil {
		parts = append(parts, memStore.FrozenSnapshot())
	}

	// Project context
	if proj, err := ctxLoader.LoadProjectContext(); err == nil && proj != "" {
		parts = append(parts, proj)
	}

	// Fallback identity if nothing loaded
	if len(parts) == 0 {
		parts = append(parts, "You are Hermes, a helpful AI assistant.")
	}

	result := strings.Join(parts, "\n\n")
	return result
}

func (r *repl) printHelp() {
	fmt.Println("Available commands:")
	fmt.Println("  /help     - Show this help message")
	fmt.Println("  /tools    - List available tools")
	fmt.Println("  /sessions - List active sessions")
	fmt.Println("  /skills   - Show skillsets and loaded skills")
	fmt.Println("  /new      - Start a new session")
	fmt.Println("  /switch   - Switch to a session (/switch <session-id>)")
	fmt.Println("  /search   - Search session messages (/search <query>)")
	fmt.Println("  /exit     - Exit the REPL")
	fmt.Println("  /quit     - Alias for /exit")
	fmt.Println("  /clear    - Clear the screen")
}

func (r *repl) printTools() {
	fmt.Println("Available tools:")
	toolNames := tools.List()
	if len(toolNames) == 0 {
		fmt.Println("  (no tools registered)")
		return
	}
	for _, name := range toolNames {
		fmt.Printf("  - %s\n", name)
	}
}

func (r *repl) printSkills() {
	skillsets := skill.ListSkillsets()
	allSkills := skill.List()

	fmt.Println("Skills Hub")
	fmt.Println("===========")

	if len(skillsets) == 0 {
		fmt.Println("  No skillsets configured — all skills loaded by default.")
		fmt.Println("  Create ~/.hermes/skills.yaml to enable skillsets filtering.")
	} else {
		fmt.Println("Skillsets:")
		for name, enabled := range skillsets {
			status := "✅ enabled"
			if !enabled {
				status = "❌ disabled"
			}
			fmt.Printf("  %s — %s\n", name, status)
		}
	}

	fmt.Println()
	fmt.Println("Loaded skills:")
	if len(allSkills) == 0 {
		fmt.Println("  (no skills registered)")
	} else {
		// Group by category (prefix before /)
		byCategory := make(map[string][]string)
		var categories []string
		for _, s := range allSkills {
			parts := strings.SplitN(s.Name, "/", 2)
			cat := "general"
			name := s.Name
			if len(parts) == 2 {
				cat, name = parts[0], parts[1]
			}
			if _, ok := byCategory[cat]; !ok {
				categories = append(categories, cat)
			}
			byCategory[cat] = append(byCategory[cat], name)
		}
		for _, cat := range categories {
			fmt.Printf("  [%s]\n", cat)
			for _, name := range byCategory[cat] {
				enabled := "✅"
				if !skill.IsSkillsetEnabled(cat) {
					enabled = "❌"
				}
				fmt.Printf("    %s %s\n", enabled, name)
			}
		}
	}
}

func (r *repl) printSessions() {
	sessions, err := r.sessionAgent.Sessions(20, 0)
	if err != nil {
		fmt.Printf("Error loading sessions: %v\n", err)
		return
	}
	if len(sessions) == 0 {
		fmt.Println("No active sessions.")
	} else {
		fmt.Println("Active sessions:")
		for _, s := range sessions {
			title := "(untitled)"
			if s.Title != nil && *s.Title != "" {
				title = *s.Title
			}
			fmt.Printf("  %s  %s  %s\n", s.ID, title, s.StartedTime().Format("2006-01-02 15:04"))
		}
	}
}
