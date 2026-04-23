package config

import (
	"os"
	"path/filepath"
	"testing"
)

// ---- NewLoader ----

func TestNewLoader(t *testing.T) {
	t.Run("default prefix", func(t *testing.T) {
		loader := NewLoader()
		if loader.envPrefix != "HERMES" {
			t.Errorf("envPrefix = %q, want %q", loader.envPrefix, "HERMES")
		}
	})

	t.Run("with env prefix option", func(t *testing.T) {
		loader := NewLoader(WithEnvPrefix("MYAPP"))
		if loader.envPrefix != "MYAPP" {
			t.Errorf("envPrefix = %q, want %q", loader.envPrefix, "MYAPP")
		}
	})

	t.Run("with config files option", func(t *testing.T) {
		loader := NewLoader(WithConfigFiles("a.json", "b.yaml"))
		if len(loader.configFiles) != 2 {
			t.Errorf("len(configFiles) = %d, want 2", len(loader.configFiles))
		}
	})

	t.Run("with secrets paths option", func(t *testing.T) {
		loader := NewLoader(WithSecretsPaths("/tmp/secrets"))
		if len(loader.secretsPaths) != 1 {
			t.Errorf("len(secretsPaths) = %d, want 1", len(loader.secretsPaths))
		}
	})
}

// ---- Load / YAML parsing ----

func TestLoad_YAML(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	yamlContent := `model:
  provider: openai
  default: gpt-4o
  temperature: 0.7
  max_tokens: 4096
agent:
  name: hermes
  max_retries: 3
context:
  max_tokens: 128000
  window_size: 10000
  strategy: sliding
  summary_enabled: true
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewLoader(WithConfigFiles(configPath))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", cfg.Model.Provider, "openai")
	}
	if cfg.Model.ModelName != "gpt-4o" {
		t.Errorf("ModelName = %q, want %q", cfg.Model.ModelName, "gpt-4o")
	}
	if cfg.Model.Temperature != 0.7 {
		t.Errorf("Temperature = %f, want 0.7", cfg.Model.Temperature)
	}
	if cfg.Model.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", cfg.Model.MaxTokens)
	}
	if cfg.Agent.Name != "hermes" {
		t.Errorf("Agent.Name = %q, want %q", cfg.Agent.Name, "hermes")
	}
	if cfg.Agent.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.Agent.MaxRetries)
	}
	if cfg.Context.MaxTokens != 128000 {
		t.Errorf("Context.MaxTokens = %d, want 128000", cfg.Context.MaxTokens)
	}
	if cfg.Context.Strategy != "sliding" {
		t.Errorf("Context.Strategy = %q, want %q", cfg.Context.Strategy, "sliding")
	}
	if !cfg.Context.SummaryEnabled {
		t.Error("Context.SummaryEnabled = false, want true")
	}
}

// ---- Load / JSON parsing ----

func TestLoad_JSON(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.json")
	jsonContent := `{
  "model": {
    "provider": "anthropic",
    "default": "claude-3-5-sonnet",
    "temperature": 1.0,
    "max_tokens": 8192,
    "top_p": 0.9,
    "frequency_penalty": 0.5,
    "presence_penalty": 0.3
  },
  "agent": {
    "name": "test-agent",
    "version": "1.0.0",
    "think_mode": true,
    "memory_enabled": false
  },
  "session": {
    "storage_path": "/data/sessions",
    "auto_save": true,
    "auto_save_delay": 5000,
    "max_history": 1000,
    "session_ttl": 86400
  }
}
`
	if err := os.WriteFile(configPath, []byte(jsonContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewLoader(WithConfigFiles(configPath))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", cfg.Model.Provider, "anthropic")
	}
	if cfg.Model.ModelName != "claude-3-5-sonnet" {
		t.Errorf("ModelName = %q, want %q", cfg.Model.ModelName, "claude-3-5-sonnet")
	}
	if cfg.Model.Temperature != 1.0 {
		t.Errorf("Temperature = %f, want 1.0", cfg.Model.Temperature)
	}
	if cfg.Model.TopP != 0.9 {
		t.Errorf("TopP = %f, want 0.9", cfg.Model.TopP)
	}
	if cfg.Model.FrequencyPenalty != 0.5 {
		t.Errorf("FrequencyPenalty = %f, want 0.5", cfg.Model.FrequencyPenalty)
	}
	if cfg.Agent.Name != "test-agent" {
		t.Errorf("Agent.Name = %q, want %q", cfg.Agent.Name, "test-agent")
	}
	if !cfg.Agent.ThinkMode {
		t.Error("Agent.ThinkMode = false, want true")
	}
	if cfg.Session.StoragePath != "/data/sessions" {
		t.Errorf("Session.StoragePath = %q, want %q", cfg.Session.StoragePath, "/data/sessions")
	}
	if !cfg.Session.AutoSave {
		t.Error("Session.AutoSave = false, want true")
	}
	if cfg.Session.AutoSaveDelay != 5000 {
		t.Errorf("AutoSaveDelay = %d, want 5000", cfg.Session.AutoSaveDelay)
	}
}

// ---- Missing config file ----

func TestLoad_MissingFile(t *testing.T) {
	loader := NewLoader(WithConfigFiles("/nonexistent/path/config.yaml"))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load should not error on missing file: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg should not be nil")
	}
}

// ---- Merge configs ----

func TestMergeConfigs(t *testing.T) {
	tmp := t.TempDir()

	config1 := filepath.Join(tmp, "base.yaml")
	if err := os.WriteFile(config1, []byte(`model:
  provider: openai
  default: gpt-4
  temperature: 0.5
agent:
  name: base-agent
  max_retries: 2
`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	config2 := filepath.Join(tmp, "override.yaml")
	if err := os.WriteFile(config2, []byte(`model:
  temperature: 0.9
  max_tokens: 2048
agent:
  max_retries: 5
`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewLoader(WithConfigFiles(config1, config2))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Base values preserved
	if cfg.Model.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", cfg.Model.Provider, "openai")
	}
	if cfg.Model.ModelName != "gpt-4" {
		t.Errorf("ModelName = %q, want %q", cfg.Model.ModelName, "gpt-4")
	}

	// Overridden values
	if cfg.Model.Temperature != 0.9 {
		t.Errorf("Temperature = %f, want 0.9", cfg.Model.Temperature)
	}
	if cfg.Model.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048", cfg.Model.MaxTokens)
	}

	// Agent: name preserved, max_retries overridden
	if cfg.Agent.Name != "base-agent" {
		t.Errorf("Agent.Name = %q, want %q", cfg.Agent.Name, "base-agent")
	}
	if cfg.Agent.MaxRetries != 5 {
		t.Errorf("Agent.MaxRetries = %d, want 5", cfg.Agent.MaxRetries)
	}
}

// ---- Environment variable overrides ----

func TestApplyEnvOverrides(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "env_test.yaml")
	if err := os.WriteFile(configPath, []byte(`model:
  provider: openai
  default: gpt-4
  temperature: 0.5
agent:
  name: test
  max_retries: 1
  think_mode: false
`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Set env vars
	os.Setenv("HERMES_MODEL_PROVIDER", "anthropic")
	os.Setenv("HERMES_MODEL_DEFAULT", "claude-3")
	os.Setenv("HERMES_MODEL_TEMPERATURE", "0.8")
	os.Setenv("HERMES_AGENT_MAX_RETRIES", "10")
	os.Setenv("HERMES_AGENT_THINK_MODE", "true")
	os.Setenv("HERMES_CONTEXT_MAX_TOKENS", "200000")
	defer func() {
		os.Unsetenv("HERMES_MODEL_PROVIDER")
		os.Unsetenv("HERMES_MODEL_DEFAULT")
		os.Unsetenv("HERMES_MODEL_TEMPERATURE")
		os.Unsetenv("HERMES_AGENT_MAX_RETRIES")
		os.Unsetenv("HERMES_AGENT_THINK_MODE")
		os.Unsetenv("HERMES_CONTEXT_MAX_TOKENS")
	}()

	loader := NewLoader(WithConfigFiles(configPath), WithEnvPrefix("HERMES"))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Model.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", cfg.Model.Provider, "anthropic")
	}
	if cfg.Model.ModelName != "claude-3" {
		t.Errorf("ModelName = %q, want %q", cfg.Model.ModelName, "claude-3")
	}
	if cfg.Model.Temperature != 0.8 {
		t.Errorf("Temperature = %f, want 0.8", cfg.Model.Temperature)
	}
	if cfg.Agent.MaxRetries != 10 {
		t.Errorf("Agent.MaxRetries = %d, want 10", cfg.Agent.MaxRetries)
	}
	if !cfg.Agent.ThinkMode {
		t.Error("Agent.ThinkMode = false, want true")
	}
	if cfg.Context.MaxTokens != 200000 {
		t.Errorf("Context.MaxTokens = %d, want 200000", cfg.Context.MaxTokens)
	}
}

// ---- Custom env prefix ----

func TestEnvPrefix(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "custom_prefix.yaml")
	if err := os.WriteFile(configPath, []byte(`model:
  provider: openai
`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	os.Setenv("MYAPP_MODEL_PROVIDER", "ollama")
	defer os.Unsetenv("MYAPP_MODEL_PROVIDER")

	loader := NewLoader(WithConfigFiles(configPath), WithEnvPrefix("MYAPP"))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.Provider != "ollama" {
		t.Errorf("Provider = %q, want %q", cfg.Model.Provider, "ollama")
	}
}

// ---- Secrets resolution ----

func TestResolveSecrets(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "secrets_test.yaml")
	if err := os.WriteFile(configPath, []byte(`model:
  api_key: "${SECRET:api_key}"
`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Create .secrets file
	secretsPath := filepath.Join(tmp, ".secrets")
	if err := os.WriteFile(secretsPath, []byte(`api_key=sk-test-12345
`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewLoader(WithConfigFiles(configPath), WithSecretsPaths(secretsPath))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.APIKey != "sk-test-12345" {
		t.Errorf("APIKey = %q, want %q", cfg.Model.APIKey, "sk-test-12345")
	}
}

func TestResolveSecrets_EnvFallback(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "secrets_env.yaml")
	if err := os.WriteFile(configPath, []byte(`model:
  api_key: "${SECRET:TEST_API_KEY}"
`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	os.Setenv("TEST_API_KEY", "sk-from-env")
	defer os.Unsetenv("TEST_API_KEY")

	loader := NewLoader(WithConfigFiles(configPath))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.APIKey != "sk-from-env" {
		t.Errorf("APIKey = %q, want %q", cfg.Model.APIKey, "sk-from-env")
	}
}

func TestResolveSecrets_SecretEnvSyntax(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "secret_env.yaml")
	if err := os.WriteFile(configPath, []byte(`model:
  api_key: "${SECRET:env:MY_SECRET_KEY}"
`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	os.Setenv("MY_SECRET_KEY", "sk-secret-xyz")
	defer os.Unsetenv("MY_SECRET_KEY")

	loader := NewLoader(WithConfigFiles(configPath))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.APIKey != "sk-secret-xyz" {
		t.Errorf("APIKey = %q, want %q", cfg.Model.APIKey, "sk-secret-xyz")
	}
}

// ---- MCPServers config ----

func TestMCPServersConfig(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "mcp.yaml")
	yamlContent := `mcp_servers:
  filesystem:
    enabled: true
    command: npx
    args:
      - "-y"
      - "@modelcontextprotocol/server-filesystem"
    env:
      HOME: /home/user
    timeout: 30
  slack:
    enabled: false
    url: "https://my-slack-server.com/mcp"
    transport: streamable-http
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewLoader(WithConfigFiles(configPath))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.MCPServers) != 2 {
		t.Errorf("len(MCPServers) = %d, want 2", len(cfg.MCPServers))
	}
	fs := cfg.MCPServers["filesystem"]
	if !fs.Enabled {
		t.Error("filesystem.Enabled = false, want true")
	}
	if fs.Command != "npx" {
		t.Errorf("filesystem.Command = %q, want %q", fs.Command, "npx")
	}
	if len(fs.Args) != 2 {
		t.Errorf("len(filesystem.Args) = %d, want 2", len(fs.Args))
	}
	if fs.Timeout != 30 {
		t.Errorf("filesystem.Timeout = %d, want 30", fs.Timeout)
	}
}

// ---- Skills config ----

func TestSkillsConfig(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "skills.yaml")
	yamlContent := `skills:
  - name: code-review
    enabled: true
    type: agent
    path: ./skills/code-review
    config:
      max_files: 50
  - name: document
    enabled: false
    type: skill
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewLoader(WithConfigFiles(configPath))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Skills) != 2 {
		t.Errorf("len(Skills) = %d, want 2", len(cfg.Skills))
	}
	if cfg.Skills[0].Name != "code-review" {
		t.Errorf("Skills[0].Name = %q, want %q", cfg.Skills[0].Name, "code-review")
	}
	if !cfg.Skills[0].Enabled {
		t.Error("Skills[0].Enabled = false, want true")
	}
	if cfg.Skills[1].Enabled {
		t.Error("Skills[1].Enabled = true, want false")
	}
}

// ---- Logging config ----

func TestLoggingConfig(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "logging.yaml")
	yamlContent := `logging:
  level: debug
  format: json
  output: file
  file_path: /var/log/hermes.log
  max_size: 100
  max_backups: 5
  max_age: 30
  compress: true
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewLoader(WithConfigFiles(configPath))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "debug")
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("Logging.Format = %q, want %q", cfg.Logging.Format, "json")
	}
	if cfg.Logging.MaxSize != 100 {
		t.Errorf("Logging.MaxSize = %d, want 100", cfg.Logging.MaxSize)
	}
	if !cfg.Logging.Compress {
		t.Error("Logging.Compress = false, want true")
	}
}

// ---- Platforms config ----

func TestPlatformsConfig(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "platforms.yaml")
	yamlContent := `platforms:
  qq:
    enabled: true
    bot_token: "bot-token-here"
    app_id: "123456"
    app_secret: "secret123"
    guild_id: "guild-1"
    channel_id: "channel-1"
  slack:
    enabled: false
    bot_token: "xoxb-..."
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewLoader(WithConfigFiles(configPath))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Platforms.QQ.Enabled {
		t.Error("QQ.Enabled = false, want true")
	}
	if cfg.Platforms.QQ.BotToken != "bot-token-here" {
		t.Errorf("QQ.BotToken = %q, want %q", cfg.Platforms.QQ.BotToken, "bot-token-here")
	}
	if cfg.Platforms.QQ.AppID != "123456" {
		t.Errorf("QQ.AppID = %q, want %q", cfg.Platforms.QQ.AppID, "123456")
	}
	if cfg.Platforms.Slack.Enabled {
		t.Error("Slack.Enabled = true, want false")
	}
}

// ---- Config.Save / LoadWithArgs ----

func TestSaveAndLoad(t *testing.T) {
	tmp := t.TempDir()
	savePath := filepath.Join(tmp, "saved.json")

	cfg := &Config{
		Model: ModelConfig{
			Provider:    "openai",
			ModelName:   "gpt-4o",
			Temperature: 0.8,
			MaxTokens:   8192,
		},
		Agent: AgentConfig{
			Name:        "test-save",
			MaxRetries:  5,
			ThinkMode:   true,
		},
	}

	err := cfg.Save(savePath)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load it back
	loadedCfg, err := Load(savePath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loadedCfg.Model.Provider != "openai" {
		t.Errorf("Provider after reload = %q, want %q", loadedCfg.Model.Provider, "openai")
	}
	if loadedCfg.Model.ModelName != "gpt-4o" {
		t.Errorf("ModelName after reload = %q, want %q", loadedCfg.Model.ModelName, "gpt-4o")
	}
	if !loadedCfg.Agent.ThinkMode {
		t.Error("Agent.ThinkMode after reload = false, want true")
	}
}

func TestLoadWithArgs(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "loadwithargs.yaml")
	if err := os.WriteFile(configPath, []byte(`model:
  provider: test
  max_tokens: 5000
`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	os.Setenv("TEST_MODEL_PROVIDER", "from-env")
	defer os.Unsetenv("TEST_MODEL_PROVIDER")

	cfg, err := LoadWithArgs(configPath, "TEST", "/tmp/nonexistent-secrets")
	if err != nil {
		t.Fatalf("LoadWithArgs: %v", err)
	}
	if cfg.Model.Provider != "from-env" {
		t.Errorf("Provider = %q, want %q (env override)", cfg.Model.Provider, "from-env")
	}
	if cfg.Model.MaxTokens != 5000 {
		t.Errorf("MaxTokens = %d, want 5000", cfg.Model.MaxTokens)
	}
}

// ---- Config getter methods ----

func TestConfigGetters(t *testing.T) {
	cfg := &Config{
		Model:   ModelConfig{Provider: "openai", ModelName: "gpt-4"},
		Agent:   AgentConfig{Name: "agent1"},
		Session: SessionConfig{MaxHistory: 100},
		Context: ContextConfig{MaxTokens: 128000},
	}

	if cfg.GetModel() == nil {
		t.Fatal("GetModel returned nil")
	}
	if cfg.GetModel().Provider != "openai" {
		t.Errorf("GetModel().Provider = %q, want %q", cfg.GetModel().Provider, "openai")
	}
	if cfg.GetAgent().Name != "agent1" {
		t.Errorf("GetAgent().Name = %q, want %q", cfg.GetAgent().Name, "agent1")
	}
	if cfg.GetSession().MaxHistory != 100 {
		t.Errorf("GetSession().MaxHistory = %d, want 100", cfg.GetSession().MaxHistory)
	}
	if cfg.GetContext().MaxTokens != 128000 {
		t.Errorf("GetContext().MaxTokens = %d, want 128000", cfg.GetContext().MaxTokens)
	}
	if cfg.GetMCPServers() == nil {
		t.Error("GetMCPServers returned nil")
	}
	if cfg.GetSkills() == nil {
		t.Error("GetSkills returned nil")
	}
	if cfg.GetLogging() == nil {
		t.Error("GetLogging returned nil")
	}
	if cfg.GetPlatforms() == nil {
		t.Error("GetPlatforms returned nil")
	}
}

// ---- parseEnvFile ----

func TestParseEnvFile(t *testing.T) {
	content := `# comment
API_KEY=sk-12345
DATABASE_URL="postgres://localhost/db"
EMPTY_VAR=
QUOTED="value with spaces"
`
	secrets := make(map[string]string)
	parseEnvFile(content, secrets)

	if secrets["API_KEY"] != "sk-12345" {
		t.Errorf("API_KEY = %q, want %q", secrets["API_KEY"], "sk-12345")
	}
	if secrets["DATABASE_URL"] != "postgres://localhost/db" {
		t.Errorf("DATABASE_URL = %q, want %q", secrets["DATABASE_URL"], "postgres://localhost/db")
	}
	if secrets["EMPTY_VAR"] != "" {
		t.Errorf("EMPTY_VAR = %q, want %q", secrets["EMPTY_VAR"], "")
	}
	if secrets["QUOTED"] != "value with spaces" {
		t.Errorf("QUOTED = %q, want %q", secrets["QUOTED"], "value with spaces")
	}
}

// ---- resolveSecretString ----

func TestResolveSecretString(t *testing.T) {
	secrets := map[string]string{
		"my-key": "secret-value",
	}

	cases := []struct {
		input    string
		envKey   string
		envVal   string
		expected string
	}{
		{"${SECRET:my-key}", "", "", "secret-value"},
		{"${SECRET:env:TEST_VAR}", "TEST_VAR", "env-secret", "env-secret"},
		{"no secret", "", "", "no secret"},
		{"${SECRET:unknown-key:fallback}", "", "", "fallback"},
	}

	for _, c := range cases {
		if c.envKey != "" {
			os.Setenv(c.envKey, c.envVal)
			defer os.Unsetenv(c.envKey)
		}
		got := resolveSecretString(c.input, secrets)
		if got != c.expected {
			t.Errorf("resolveSecretString(%q) = %q, want %q", c.input, got, c.expected)
		}
	}
}

// ---- Extra fields (map) ----

func TestExtraMapFields(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "extra.yaml")
	yamlContent := `model:
  provider: openai
  extra:
    organization: my-org
    base_url: https://api.openai.com/v1
agent:
  extra:
    debug: true
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loader := NewLoader(WithConfigFiles(configPath))
	cfg, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.Extra == nil {
		t.Fatal("Model.Extra is nil")
	}
	if cfg.Model.Extra["organization"] != "my-org" {
		t.Errorf("Model.Extra[organization] = %v, want %q", cfg.Model.Extra["organization"], "my-org")
	}
	if cfg.Agent.Extra == nil {
		t.Fatal("Agent.Extra is nil")
	}
}
