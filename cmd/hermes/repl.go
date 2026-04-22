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
	"github.com/nousresearch/hermes-go/pkg/model"
	"github.com/nousresearch/hermes-go/pkg/session"
	"github.com/nousresearch/hermes-go/pkg/tools"
	ctxmgr "github.com/nousresearch/hermes-go/pkg/context"
)

// repl represents the interactive Read-Eval-Print-Loop.
type repl struct {
	sessionAgent *agent.SessionAgent
	store        *session.Store
	ctxMgr       *ctxmgr.Manager
	logger       *slog.Logger
	modelClient  model.LLMClient
}

// newREPL creates a new REPL instance.
func newREPL(agentCfg agent.Config, store *session.Store, logger *slog.Logger, modelClient model.LLMClient) *repl {
	if logger == nil {
		logger = slog.Default()
	}
	// Create context manager
	ctxCfg := ctxmgr.DefaultManagerConfig(200000)
	ctxMgr := ctxmgr.NewManager(ctxCfg, logger, modelClient)

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
		logger:       logger,
		modelClient:  modelClient,
	}
}

// Run starts the REPL and runs it until the user types /exit or an error occurs.
func (r *repl) Run(ctx context.Context) error {
	fmt.Println("Hermes REPL v0.1.0")
	fmt.Println("Type /help for available commands. Use Ctrl+C or /exit to quit.")
	fmt.Println()

	// Auto-create a new session
	_, err := r.sessionAgent.New("cli", "claude-sonnet-4", "")
	if err != nil {
		r.logger.Warn("failed to create initial session", "error", err)
	}
	r.logger.Info("session started", "session_id", r.sessionAgent.SessionID())

	scanner := bufio.NewScanner(os.Stdin)
	for {
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

		// Regular user message — send to SessionAgent
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

	switch name {
	case "/help":
		r.printHelp()
	case "/tools":
		r.printTools()
	case "/sessions":
		r.printSessions()
	case "/new":
		sessID, err := r.sessionAgent.New("cli", "claude-sonnet-4", "")
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

func (r *repl) printHelp() {
	fmt.Println("Available commands:")
	fmt.Println("  /help     - Show this help message")
	fmt.Println("  /tools    - List available tools")
	fmt.Println("  /sessions - List active sessions")
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
