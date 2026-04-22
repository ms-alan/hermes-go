package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/nousresearch/hermes-go/pkg/config"
	"github.com/nousresearch/hermes-go/pkg/agent"
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

	// Load configuration from ~/.hermes/config.yaml.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Determine effective values: CLI flags override config defaults.
	model := *modelFlag
	if model == "" {
		model = cfg.Model.ModelName
		if model == "" {
			model = "gpt-4o"
		}
	}
	baseURL := *baseURLFlag
	if baseURL == "" {
		baseURL = cfg.Model.APIBase
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
	}
	apiKey := *apiKeyFlag
	if apiKey == "" {
		apiKey = cfg.Model.APIKey
	}
	sessionID := *sessionIDFlag

	// Create logger.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Build agent config.
	agentConfig := agent.Config{
		Model:         model,
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
	modelClient, err := newLLMClient(baseURL, apiKey, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating LLM client: %v\n", err)
		os.Exit(1)
	}
	defer modelClient.Close()

	logger.Info("hermes started",
		"model", model,
		"base_url", baseURL,
	)

	// Run the REPL.
	repl := newREPL(agentConfig, store, logger, modelClient)
	if sessionID != "" {
		if err := repl.sessionAgent.Switch(sessionID); err != nil {
			logger.Warn("failed to resume session", "session_id", sessionID, "error", err)
		} else {
			logger.Info("resumed session", "session_id", sessionID)
		}
	}
	if err := repl.Run(context.Background()); err != nil {
		logger.Error("REPL error", "error", err)
		os.Exit(1)
	}
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
func newLLMClient(baseURL, apiKey string, logger *slog.Logger) (model.LLMClient, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("base-url is required")
	}
	opts := []model.Option{
		model.WithBaseURL(baseURL),
		model.WithAPIKey(apiKey),
	}
	return model.NewOpenAIClient(opts...)
}
