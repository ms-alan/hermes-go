package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/nousresearch/hermes-go/pkg/agent"
	"github.com/nousresearch/hermes-go/pkg/config"
	"github.com/nousresearch/hermes-go/pkg/model"
	"github.com/nousresearch/hermes-go/pkg/session"
)

func main() {
	// Parse command-line flags.
	modelFlag := flag.String("model", "", "Model identifier (e.g. anthropic/claude-sonnet-4-20250514)")
	apiKeyFlag := flag.String("api-key", "", "API key for the provider")
	baseURLFlag := flag.String("base-url", "", "API endpoint base URL")
	sessionIDFlag := flag.String("session-id", "", "Session ID to resume")
	flag.Parse()

	// Load configuration from ~/.hermes/config.yaml or default locations.
	// If no config file is found, use zero values (CLI flags will override).
	cfg, err := config.Load()
	if err != nil {
		// Config file missing — not a fatal error; CLI flags + defaults will apply.
		fmt.Fprintf(os.Stderr, "Note: no config file found (%v); using defaults.\n", err)
		cfg = &config.Config{}
	}

	// Determine effective values: CLI flags override config defaults.
	modelName := *modelFlag
	if modelName == "" {
		modelName = cfg.Model.ModelName
		if modelName == "" {
			modelName = "gpt-4o"
		}
	}
	baseURL := *baseURLFlag
	if baseURL == "" {
		baseURL = cfg.Model.APIBase
		if baseURL == "" {
			baseURL = os.Getenv("MINIMAX_CN_BASE_URL")
		}
		if baseURL == "" {
			baseURL = os.Getenv("OPENAI_BASE_URL")
		}
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
	}
	apiKey := *apiKeyFlag
	if apiKey == "" {
		apiKey = cfg.Model.APIKey
		if apiKey == "" {
			apiKey = os.Getenv("MINIMAX_CN_API_KEY")
		}
		if apiKey == "" {
			apiKey = os.Getenv("OPENAI_API_KEY")
		}
	}
	sessionID := *sessionIDFlag

	// Create logger.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Build agent config.
	agentConfig := agent.Config{
		Model:         modelName,
		APIKey:        apiKey,
		BaseURL:       baseURL,
		MaxIterations: 90,
		Logger:        &slogLogger{log: logger},
	}

	// Open session store.
	store, err := session.NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening session store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Create LLM client. The placeholder uses the baseURL to select a backend.
	// For a real implementation, this would be an actual LLM client (e.g., OpenAI, Anthropic).
	modelClient, err := newLLMClient(baseURL, apiKey, modelName, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating LLM client: %v\n", err)
		os.Exit(1)
	}
	defer modelClient.Close()

	logger.Info("hermes started",
		"model", modelName,
		"base_url", baseURL,
	)

	// Set up graceful shutdown: cancel the context on SIGINT or SIGTERM.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	// Run the REPL.
	repl := newREPL(agentConfig, store, logger, modelClient, modelName)
	if sessionID != "" {
		if err := repl.sessionAgent.Switch(sessionID); err != nil {
			logger.Warn("failed to resume session", "session_id", sessionID, "error", err)
		} else {
			logger.Info("resumed session", "session_id", sessionID)
		}
	}
	if err := repl.Run(ctx); err != nil {
		logger.Error("REPL error", "error", err)
		os.Exit(1)
	}
	logger.Info("hermes stopped gracefully")
}

// slogLogger wraps *slog.Logger to satisfy the agent.Logger interface.
type slogLogger struct {
	log *slog.Logger
}

func (s *slogLogger) Debug(msg string, args ...any) { s.log.Debug(msg, args...) }
func (s *slogLogger) Info(msg string, args ...any)  { s.log.Info(msg, args...) }
func (s *slogLogger) Warn(msg string, args ...any)  { s.log.Warn(msg, args...) }
func (s *slogLogger) Error(msg string, args ...any) { s.log.Error(msg, args...) }

// newLLMClient creates an LLM client based on the base URL.
func newLLMClient(baseURL, apiKey, modelName string, logger *slog.Logger) (model.LLMClient, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("base-url is required")
	}
	opts := []model.Option{
		model.WithBaseURL(baseURL),
		model.WithAPIKey(apiKey),
		model.WithModel(modelName),
	}
	return model.NewOpenAIClient(opts...)
}
