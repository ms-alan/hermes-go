package agent

import (
	"log/slog"

	"github.com/nousresearch/hermes-go/pkg/model"
)

// Config holds the configuration for the agent.
type Config struct {
	// Model is the model identifier (e.g. "anthropic/claude-sonnet-4-20250514").
	Model string
	// MaxIterations is the maximum number of tool-use loops (default 90).
	MaxIterations int
	// BaseURL is the API endpoint base URL.
	BaseURL string
	// APIKey is the API key for authentication.
	APIKey string
	// ExtraHeaders are extra HTTP headers sent with every request.
	ExtraHeaders map[string]string
	// TimeoutSeconds is the request timeout in seconds.
	TimeoutSeconds int
	// Logger is the structured logger (uses log/slog).
	Logger Logger

	// Tools is the list of tools available to the agent.
	Tools []*model.Tool
}

// Defaults applies sensible defaults to the config.
func (c *Config) Defaults() {
	if c.MaxIterations == 0 {
		c.MaxIterations = 90
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 120
	}
	if c.Logger == nil {
		c.Logger = &slogLogger{log: slog.Default()}
	}
}

// slogLogger is a log/slog-backed Logger implementation.
type slogLogger struct {
	log *slog.Logger
}

func (s *slogLogger) Debug(msg string, args ...any) { s.log.Debug(msg, args...) }
func (s *slogLogger) Info(msg string, args ...any)  { s.log.Info(msg, args...) }
func (s *slogLogger) Warn(msg string, args ...any) { s.log.Warn(msg, args...) }
func (s *slogLogger) Error(msg string, args ...any) { s.log.Error(msg, args...) }

// Logger is the logging interface used by the agent.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Ensure Logger is satisfied by slogLogger at compile time.
var _ Logger = &slogLogger{}
