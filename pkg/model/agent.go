package model

// AgentConfig holds configuration for the agent loop.
type AgentConfig struct {
	Model           string
	MaxIterations   int
	BaseURL         string
	APIKey          string
	ExtraHeaders    map[string]string
	TimeoutSeconds  int
}

// Defaults returns a default agent config.
func (c *AgentConfig) Defaults() {
	if c.MaxIterations == 0 {
		c.MaxIterations = 90
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 120
	}
}
