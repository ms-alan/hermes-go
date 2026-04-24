package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/nousresearch/hermes-go/pkg/agent"
	"github.com/nousresearch/hermes-go/pkg/cron"
	hermescontext "github.com/nousresearch/hermes-go/pkg/context"
	hermesmemory "github.com/nousresearch/hermes-go/pkg/memory"
	"github.com/nousresearch/hermes-go/pkg/model"
	"github.com/nousresearch/hermes-go/pkg/prompt"
	"github.com/nousresearch/hermes-go/pkg/session"
	"github.com/nousresearch/hermes-go/pkg/skill"
	"github.com/nousresearch/hermes-go/pkg/tools"
)

// repl represents the interactive Read-Eval-Print-Loop.
type repl struct {
	sessionAgent   *agent.SessionAgent
	store          *session.Store
	ctxMgr         *hermescontext.Manager
	memMgr         *hermesmemory.MemoryManager
	ctxLoader      *hermescontext.Loader
	logger         *slog.Logger
	modelClient    model.LLMClient
	defaultModel   string
	systemPrompt   string                 // frozen snapshot assembled at startup
	cronScheduler  *cron.Scheduler        // nil if cron unavailable
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

// initCronScheduler creates and starts the cron scheduler for the CLI.
// It uses the SessionAgent for job execution and logs to the provided logger.
func initCronScheduler(sessionAgent *agent.SessionAgent, logger *slog.Logger) *cron.Scheduler {
	cronStore, err := cron.NewStore("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to create cron store: %v\n", err)
		return nil
	}
	tools.SetCronStore(cronStore)

	runner := &cron.AicallRunner{
		SessionAgent: sessionAgent,
		SkillLoader:  skill.GetLoader(),
		Logger:       logger,
	}

	scheduler := cron.NewScheduler(cronStore, runner, nil, logger)
	scheduler.Start()
	tools.SetCronScheduler(scheduler)

	logger.Info("cron scheduler started")
	return scheduler
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
	memStore := hermesmemory.NewMemoryStore()
	if err := memStore.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load memory store: %v\n", err)
	}
	hermesmemory.SetMemoryStore(memStore)

	// Create memory manager with built-in provider
	memMgr := hermesmemory.NewMemoryManager()
	memMgr.SetLogger(logger)
	memMgr.WithBuiltinProvider(memStore)
	if err := memMgr.InitializeAll("", "", map[string]any{"platform": "cli"}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize memory manager: %v\n", err)
	}

	// Create context file loader (hermesHome defaults to ~/.hermes, cwd for project context)
	cwd, _ := os.Getwd()
	ctxLoader := hermescontext.NewLoader("", cwd)

	// Initialize skills system: load skillsets config + skills from disk
	hermesHome := os.Getenv("HERMES_HOME")
	if hermesHome == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			hermesHome = filepath.Join(home, ".hermes")
		}
	}
	skillsDir := filepath.Join(hermesHome, "skills")
	if err := skill.LoadSkillsets(skill.DefaultSkillsYAMLPath()); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load skillsets config: %v\n", err)
	}
	if skills, err := skill.LoadSkillsFromDisk(skillsDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load skills from disk: %v\n", err)
	} else if len(skills) > 0 {
		fmt.Fprintf(os.Stderr, "Loaded %d skill(s) from %s\n", len(skills), skillsDir)
	}
	// Wire skill loader for cron jobs
	skillLoader := skill.GetLoader()
	if skillLoader == nil {
		skillLoader = skill.NewLoader(skillsDir, logger)
		skill.SetLoader(skillLoader)
	}

	// Build system prompt from SOUL.md, memory, project context, and platform identity
	systemPrompt := prompt.NewBuilder(ctxLoader, memMgr).WithPlatform("cli").Build()

	// Create AIAgent
	aiAgent := agent.NewAIAgent(modelClient, agentCfg)

	// Register built-in tools from the tools package
	// (populates both the handler registry and the LLM-facing tool schemas)
	registerBuiltinTools(aiAgent, nil)

	// Sync tools into agent.Config.Tools so AIAgent sends them in LLM requests
	aiAgent.SyncToolsToConfig()

	// Create SessionAgent
	sessionAgent := agent.NewSessionAgent(aiAgent, store, ctxMgr, memMgr, logger)

	// Initialize cron scheduler (store + runner + nil deliverer for CLI)
	cronScheduler := initCronScheduler(sessionAgent, logger)

	r := &repl{
		sessionAgent:   sessionAgent,
		store:         store,
		ctxMgr:        ctxMgr,
		memMgr:        memMgr,
		ctxLoader:     ctxLoader,
		logger:        logger,
		modelClient:   modelClient,
		defaultModel:  defaultModel,
		systemPrompt:  systemPrompt,
		cronScheduler: cronScheduler,
	}
	return r
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

	// Graceful shutdown: stop cron scheduler when REPL exits
	if r.cronScheduler != nil {
		defer r.cronScheduler.Stop()
	}

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
	case "/cron":
		if err := r.handleCronCommand(ctx, args); err != nil {
			return err
		}
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

func (r *repl) printHelp() {
	fmt.Println("Available commands:")
	fmt.Println("  /help     - Show this help message")
	fmt.Println("  /tools    - List available tools")
	fmt.Println("  /sessions - List active sessions")
	fmt.Println("  /skills   - Show skillsets and loaded skills")
	fmt.Println("  /cron     - Manage cron jobs (/cron help for subcommands)")
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

// handleCronCommand processes /cron <subcommand> from the CLI REPL.
func (r *repl) handleCronCommand(ctx context.Context, args string) error {
	store := tools.CronStore()
	if store == nil {
		return fmt.Errorf("cron store not configured")
	}

	parts := strings.Fields(args)
	if len(parts) == 0 {
		r.printCronList(store)
		return nil
	}

	sub := parts[0]
	rest := strings.Join(parts[1:], " ")

	switch sub {
	case "list", "ls":
		r.printCronList(store)
	case "get":
		if rest == "" {
			return fmt.Errorf("usage: /cron get <job-id>")
		}
		r.printCronGet(store, rest)
	case "pause":
		if rest == "" {
			return fmt.Errorf("usage: /cron pause <job-id>")
		}
		return r.cronPause(store, rest)
	case "resume":
		if rest == "" {
			return fmt.Errorf("usage: /cron resume <job-id>")
		}
		return r.cronResume(store, rest)
	case "remove", "rm", "delete":
		if rest == "" {
			return fmt.Errorf("usage: /cron remove <job-id>")
		}
		return r.cronRemove(store, rest)
	case "run", "trigger":
		if rest == "" {
			return fmt.Errorf("usage: /cron run <job-id>")
		}
		return r.cronRun(rest)
	case "help":
		r.printCronHelp()
	default:
		return fmt.Errorf("unknown cron subcommand %q — try /cron help", sub)
	}
	return nil
}

func (r *repl) printCronList(store *cron.Store) {
	jobs := store.List()
	if len(jobs) == 0 {
		fmt.Println("No cron jobs.")
		return
	}
	fmt.Printf("%d cron job(s):\n", len(jobs))
	for _, j := range jobs {
		status := "🟡"
		if j.State == "running" {
			status = "🔵"
		} else if j.State == "done" || !j.Enabled {
			status = "⚪️"
		} else if j.LastStatus == "error" {
			status = "🔴"
		} else if j.Enabled {
			status = "🟢"
		}
		repeatStr := "∞"
		if j.Repeat.Times != nil {
			repeatStr = fmt.Sprintf("%d/%d", j.Repeat.Completed, *j.Repeat.Times)
		}
		fmt.Printf("  %s [%s] %s  next=%s  repeat=%s  state=%s\n",
			status, j.ID, j.Name, j.NextRunAt, repeatStr, j.State)
	}
}

func (r *repl) printCronGet(store *cron.Store, id string) {
	job := store.Get(id)
	if job == nil {
		fmt.Printf("Job not found: %s\n", id)
		return
	}
	fmt.Printf("Job: %s (%s)\n", job.Name, job.ID)
	fmt.Printf("  Schedule : %s\n", job.Schedule.Display)
	fmt.Printf("  Enabled  : %v\n", job.Enabled)
	fmt.Printf("  State    : %s\n", job.State)
	fmt.Printf("  Repeat   : ")
	if job.Repeat.Times != nil {
		fmt.Printf("%d/%d\n", job.Repeat.Completed, *job.Repeat.Times)
	} else {
		fmt.Println("∞")
	}
	fmt.Printf("  Next run : %s\n", job.NextRunAt)
	fmt.Printf("  Last run : %s\n", job.LastRunAt)
	fmt.Printf("  Last status: %s\n", job.LastStatus)
	if job.LastError != "" {
		fmt.Printf("  Last error: %s\n", job.LastError)
	}
	if job.Deliver != "" {
		fmt.Printf("  Deliver  : %s\n", job.Deliver)
	}
}

func (r *repl) cronPause(store *cron.Store, id string) error {
	if err := store.Update(id, func(j *cron.Job) error {
		j.Enabled = false
		j.State = "paused"
		return nil
	}); err != nil {
		return fmt.Errorf("pause failed: %w", err)
	}
	fmt.Printf("Paused: %s\n", id)
	return nil
}

func (r *repl) cronResume(store *cron.Store, id string) error {
	if err := store.Update(id, func(j *cron.Job) error {
		j.Enabled = true
		j.State = "scheduled"
		return nil
	}); err != nil {
		return fmt.Errorf("resume failed: %w", err)
	}
	fmt.Printf("Resumed: %s\n", id)
	return nil
}

func (r *repl) cronRemove(store *cron.Store, id string) error {
	if err := store.Remove(id); err != nil {
		return fmt.Errorf("remove failed: %w", err)
	}
	fmt.Printf("Removed: %s\n", id)
	return nil
}

func (r *repl) cronRun(id string) error {
	sched := tools.CronScheduler()
	if sched == nil {
		return fmt.Errorf("cron scheduler not running")
	}
	if err := sched.RunNow(id); err != nil {
		return fmt.Errorf("run failed: %w", err)
	}
	fmt.Printf("Triggered: %s\n", id)
	return nil
}

func (r *repl) printCronHelp() {
	fmt.Println("Cron jobs — available subcommands:")
	fmt.Println("  /cron list              — list all jobs")
	fmt.Println("  /cron get <id>          — show job details")
	fmt.Println("  /cron pause <id>        — pause a job")
	fmt.Println("  /cron resume <id>       — resume a paused job")
	fmt.Println("  /cron remove <id>       — delete a job")
	fmt.Println("  /cron run <id>          — trigger a job immediately")
	fmt.Println("")
	fmt.Println("To create cron jobs, ask the AI to use the cronjob tool.")
}
