package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the root configuration structure
type Config struct {
	Model       ModelConfig        `json:"model" yaml:"model"`
	Agent       AgentConfig        `json:"agent" yaml:"agent"`
	Session     SessionConfig      `json:"session" yaml:"session"`
	Context     ContextConfig      `json:"context" yaml:"context"`
	MCPServers  map[string]MCPConfig `json:"mcp_servers" yaml:"mcp_servers"`
	Skills      []SkillConfig      `json:"skills" yaml:"skills"`
	Logging     LoggingConfig      `json:"logging" yaml:"logging"`
	Platforms   PlatformsConfig    `json:"platforms" yaml:"platforms"`
}

// ModelConfig holds LLM model settings
type ModelConfig struct {
	Provider       string                 `json:"provider" yaml:"provider"`
	ModelName      string                 `json:"model_name" yaml:"model_name"`
	APIKey         string                 `json:"api_key" yaml:"api_key"`
	APIBase        string                 `json:"api_base" yaml:"api_base"`
	Temperature    float64                `json:"temperature" yaml:"temperature"`
	MaxTokens      int                    `json:"max_tokens" yaml:"max_tokens"`
	TopP           float64                `json:"top_p" yaml:"top_p"`
	FrequencyPenalty float64              `json:"frequency_penalty" yaml:"frequency_penalty"`
	PresencePenalty float64               `json:"presence_penalty" yaml:"presence_penalty"`
	Timeout        int                    `json:"timeout" yaml:"timeout"`
	Extra          map[string]interface{} `json:"extra" yaml:"extra"`
}

// AgentConfig holds agent behavior settings
type AgentConfig struct {
	Name           string                 `json:"name" yaml:"name"`
	Version        string                 `json:"version" yaml:"version"`
	Description    string                 `json:"description" yaml:"description"`
	MaxRetries     int                    `json:"max_retries" yaml:"max_retries"`
	RetryDelay     int                    `json:"retry_delay" yaml:"retry_delay"`
	ThinkMode      bool                   `json:"think_mode" yaml:"think_mode"`
	MemoryEnabled  bool                   `json:"memory_enabled" yaml:"memory_enabled"`
	Extra          map[string]interface{} `json:"extra" yaml:"extra"`
}

// SessionConfig holds session management settings
type SessionConfig struct {
	StoragePath   string `json:"storage_path" yaml:"storage_path"`
	AutoSave      bool   `json:"auto_save" yaml:"auto_save"`
	AutoSaveDelay int    `json:"auto_save_delay" yaml:"auto_save_delay"`
	MaxHistory    int    `json:"max_history" yaml:"max_history"`
	SessionTTL    int    `json:"session_ttl" yaml:"session_ttl"`
}

// ContextConfig holds context window settings
type ContextConfig struct {
	MaxTokens     int  `json:"max_tokens" yaml:"max_tokens"`
	WindowSize    int  `json:"window_size" yaml:"window_size"`
	Strategy      string `json:"strategy" yaml:"strategy"`
	SummaryEnabled bool `json:"summary_enabled" yaml:"summary_enabled"`
}

// MCPConfig holds MCP server configuration
type MCPConfig struct {
	Enabled       bool                   `json:"enabled" yaml:"enabled"`
	Command       string                 `json:"command" yaml:"command"`
	Args          []string               `json:"args" yaml:"args"`
	Env           map[string]string      `json:"env" yaml:"env"`
	URL           string                 `json:"url" yaml:"url"`
	Timeout       int                    `json:"timeout" yaml:"timeout"`
	Transport     string                 `json:"transport" yaml:"transport"`
}

// SkillConfig holds skill/agent configuration
type SkillConfig struct {
	Name        string                 `json:"name" yaml:"name"`
	Enabled     bool                   `json:"enabled" yaml:"enabled"`
	Type        string                 `json:"type" yaml:"type"`
	Path        string                 `json:"path" yaml:"path"`
	Config      map[string]interface{} `json:"config" yaml:"config"`
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level      string `json:"level" yaml:"level"`
	Format     string `json:"format" yaml:"format"`
	Output     string `json:"output" yaml:"output"`
	FilePath   string `json:"file_path" yaml:"file_path"`
	MaxSize    int    `json:"max_size" yaml:"max_size"`
	MaxBackups int    `json:"max_backups" yaml:"max_backups"`
	MaxAge     int    `json:"max_age" yaml:"max_age"`
	Compress   bool   `json:"compress" yaml:"compress"`
}

// PlatformsConfig holds platform-specific settings
type PlatformsConfig struct {
	QQ          PlatformQQConfig    `json:"qq" yaml:"qq"`
	DingTalk    PlatformDingConfig   `json:"dingtalk" yaml:"dingtalk"`
	WeChat      PlatformWeChatConfig `json:"wechat" yaml:"wechat"`
	Slack       PlatformSlackConfig  `json:"slack" yaml:"slack"`
	Extra       map[string]interface{} `json:"extra" yaml:"extra"`
}

// PlatformQQConfig holds QQ platform settings
type PlatformQQConfig struct {
	Enabled     bool   `json:"enabled" yaml:"enabled"`
	BotToken    string `json:"bot_token" yaml:"bot_token"`
	AppID       string `json:"app_id" yaml:"app_id"`
	AppSecret   string `json:"app_secret" yaml:"app_secret"`
	GuildID     string `json:"guild_id" yaml:"guild_id"`
	ChannelID   string `json:"channel_id" yaml:"channel_id"`
}

// PlatformDingConfig holds DingTalk platform settings
type PlatformDingConfig struct {
	Enabled     bool   `json:"enabled" yaml:"enabled"`
	ClientID    string `json:"client_id" yaml:"client_id"`
	ClientSecret string `json:"client_secret" yaml:"client_secret"`
	AgentID     string `json:"agent_id" yaml:"agent_id"`
}

// PlatformWeChatConfig holds WeChat platform settings
type PlatformWeChatConfig struct {
	Enabled        bool   `json:"enabled" yaml:"enabled"`
	AppID          string `json:"app_id" yaml:"app_id"`
	AppSecret      string `json:"app_secret" yaml:"app_secret"`
	Token          string `json:"token" yaml:"token"`
	EncodingAESKey string `json:"encoding_aes_key" yaml:"encoding_aes_key"`
}

// PlatformSlackConfig holds Slack platform settings
type PlatformSlackConfig struct {
	Enabled    bool   `json:"enabled" yaml:"enabled"`
	BotToken   string `json:"bot_token" yaml:"bot_token"`
	AppToken   string `json:"app_token" yaml:"app_token"`
	SigningSecret string `json:"signing_secret" yaml:"signing_secret"`
}

// Loader handles configuration loading with multi-file merging and env overrides
type Loader struct {
	configFiles []string
	envPrefix   string
	secretsPaths []string
}

// Option is a functional option for Loader
type Option func(*Loader)

// WithConfigFiles sets the config files to load (in order, later files override earlier)
func WithConfigFiles(files ...string) Option {
	return func(l *Loader) {
		l.configFiles = files
	}
}

// WithEnvPrefix sets the environment variable prefix (default: "HERMES")
func WithEnvPrefix(prefix string) Option {
	return func(l *Loader) {
		l.envPrefix = prefix
	}
}

// WithSecretsPaths adds paths to resolve secrets from (e.g., env files, vault)
func WithSecretsPaths(paths ...string) Option {
	return func(l *Loader) {
		l.secretsPaths = paths
	}
}

// NewLoader creates a new config loader with options
func NewLoader(opts ...Option) *Loader {
	loader := &Loader{
		envPrefix: "HERMES",
	}
	for _, opt := range opts {
		opt(loader)
	}
	return loader
}

// Load loads configuration from all sources, merging them in order
func (l *Loader) Load() (*Config, error) {
	cfg := &Config{}

	// Load from config files
	for _, file := range l.configFiles {
		if err := l.loadFile(file, cfg); err != nil {
			return nil, fmt.Errorf("failed to load config from %s: %w", file, err)
		}
	}

	// Resolve secrets
	if err := l.resolveSecrets(cfg); err != nil {
		return nil, fmt.Errorf("failed to resolve secrets: %w", err)
	}

	// Apply environment variable overrides
	if err := l.applyEnvOverrides(cfg); err != nil {
		return nil, fmt.Errorf("failed to apply env overrides: %w", err)
	}

	return cfg, nil
}

// loadFile loads a config file (JSON or YAML) and merges into cfg
func (l *Loader) loadFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Skip missing files
		}
		return err
	}

	ext := strings.ToLower(filepath.Ext(path))
	var fileCfg Config
	switch ext {
	case ".json":
		if err := json.Unmarshal(data, &fileCfg); err != nil {
			return fmt.Errorf("JSON unmarshal error: %w", err)
		}
	case ".yaml", ".yml":
		if err := yamlUnmarshal(data, &fileCfg); err != nil {
			return fmt.Errorf("YAML unmarshal error: %w", err)
		}
	default:
		// Try JSON first, then YAML
		if err := json.Unmarshal(data, &fileCfg); err != nil {
			if err := yamlUnmarshal(data, &fileCfg); err != nil {
				return fmt.Errorf("unsupported file format: %s", ext)
			}
		}
	}

	// Merge fileCfg into cfg
	mergeConfigs(cfg, &fileCfg)
	return nil
}

// mergeConfigs merges src into dst (dst wins for conflicting values)
func mergeConfigs(dst, src *Config) {
	dstVal := reflect.ValueOf(dst).Elem()
	srcVal := reflect.ValueOf(src).Elem()

	for i := 0; i < dstVal.NumField(); i++ {
		dstField := dstVal.Field(i)
		srcField := srcVal.Field(i)

		if srcField.IsZero() {
			continue
		}

		switch dstField.Kind() {
		case reflect.Struct:
			mergeStruct(dstField, srcField)
		case reflect.Map:
			if srcField.Len() > 0 {
				mergeMap(dstField, srcField)
			}
		case reflect.Slice:
			if srcField.Len() > 0 {
				dstField.Set(srcField)
			}
		default:
			if !dstField.CanSet() {
				continue
			}
			dstField.Set(srcField)
		}
	}
}

// mergeStruct merges src struct field into dst struct field
func mergeStruct(dst, src reflect.Value) {
	for i := 0; i < dst.NumField(); i++ {
		dstField := dst.Field(i)
		srcField := src.Field(i)

		if srcField.IsZero() {
			continue
		}

		switch dstField.Kind() {
		case reflect.Struct:
			mergeStruct(dstField, srcField)
		case reflect.Map:
			if srcField.Len() > 0 {
				mergeMap(dstField, srcField)
			}
		case reflect.Slice:
			if srcField.Len() > 0 {
				dstField.Set(srcField)
			}
		default:
			if dstField.CanSet() {
				dstField.Set(srcField)
			}
		}
	}
}

// mergeMap merges src map into dst map
func mergeMap(dst, src reflect.Value) {
	if dst.IsNil() {
		dst.Set(reflect.MakeMap(dst.Type()))
	}
	iter := src.MapRange()
	for iter.Next() {
		dst.SetMapIndex(iter.Key(), iter.Value())
	}
}

// secretsPattern matches ${SECRET:path} or ${SECRET:env:VAR} patterns
var secretsPattern = regexp.MustCompile(`\$\{SECRET:([^}]+)\}`)

// resolveSecrets resolves secret placeholders in config values
func (l *Loader) resolveSecrets(cfg *Config) error {
	// Load secret values from various sources
	secrets := make(map[string]string)

	// Load from .env files if they exist
	envFiles := []string{".env", ".secrets"}
	for _, envFile := range envFiles {
		if data, err := os.ReadFile(envFile); err == nil {
			parseEnvFile(string(data), secrets)
		}
	}

	// Load from custom secrets paths
	for _, path := range l.secretsPaths {
		if data, err := os.ReadFile(path); err == nil {
			parseEnvFile(string(data), secrets)
		}
	}

	// Walk the config and resolve secrets
	return resolveSecretsWalk(cfg, secrets)
}

// resolveSecretsWalk recursively walks the config to resolve secrets
func resolveSecretsWalk(v interface{}, secrets map[string]string) error {
	switch val := v.(type) {
	case *Config, *ModelConfig, *AgentConfig, *SessionConfig, *ContextConfig, *LoggingConfig, *PlatformsConfig:
		return resolveStructSecrets(reflect.ValueOf(val).Elem(), secrets)
	case map[string]interface{}:
		for k, v := range val {
			if str, ok := v.(string); ok {
				val[k] = resolveSecretString(str, secrets)
			} else if nested, ok := v.(map[string]interface{}); ok {
				if err := resolveSecretsWalk(nested, secrets); err != nil {
					return err
				}
			}
		}
	case string:
		// Already resolved at parent level
	}
	return nil
}

// resolveStructSecrets resolves secrets in a struct's string fields
func resolveStructSecrets(v reflect.Value, secrets map[string]string) error {
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if !field.IsValid() || !field.CanSet() {
			continue
		}

		switch field.Kind() {
		case reflect.String:
			if field.String() != "" {
				field.SetString(resolveSecretString(field.String(), secrets))
			}
		case reflect.Map:
			if field.Len() > 0 && field.Type().Key().Kind() == reflect.String {
				iter := field.MapRange()
				for iter.Next() {
					if strVal, ok := iter.Value().Interface().(string); ok {
						field.SetMapIndex(iter.Key(), reflect.ValueOf(resolveSecretString(strVal, secrets)))
					}
				}
			}
		case reflect.Struct:
			if err := resolveStructSecrets(field, secrets); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveSecretString resolves ${SECRET:xxx} patterns in a string
func resolveSecretString(s string, secrets map[string]string) string {
	return secretsPattern.ReplaceAllStringFunc(s, func(match string) string {
		path := match[9 : len(match)-1] // Extract content between ${SECRET: and }
		parts := strings.SplitN(path, ":", 2)

		if len(parts) == 2 && parts[0] == "env" {
			return os.Getenv(parts[1])
		}

		// Check loaded secrets map
		if val, ok := secrets[path]; ok {
			return val
		}

		// Try environment variable as fallback
		return os.Getenv(path)
	})
}

// parseEnvFile parses a .env style file into a map
func parseEnvFile(content string, dest map[string]string) {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			val = strings.Trim(val, "\"")
			dest[key] = val
		}
	}
}

// applyEnvOverrides applies environment variable overrides to config
func (l *Loader) applyEnvOverrides(cfg *Config) error {
	return applyEnvToStruct(cfg, l.envPrefix)
}

// applyEnvToStruct recursively applies env vars to config struct
func applyEnvToStruct(v interface{}, prefix string) error {
	val := reflect.ValueOf(v)
	if val.Kind() != reflect.Ptr {
		return nil
	}
	elem := val.Elem()
	return applyEnvToValue(elem, prefix)
}

// applyEnvToValue applies env vars to a reflect.Value
func applyEnvToValue(v reflect.Value, prefix string) error {
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		return applyEnvToValue(v.Elem(), prefix)
	}

	if v.Kind() != reflect.Struct {
		return nil
	}

	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldType := t.Field(i)

		// Check for json/yaml tag first
		tag := fieldType.Tag.Get("json")
		if tag == "" {
			tag = fieldType.Tag.Get("yaml")
		}
		if tag == "" || tag == "-" {
			continue
		}
		parts := strings.SplitN(tag, ",", 2)
		fieldName := parts[0]

		envName := prefix + "_" + strings.ToUpper(strings.ReplaceAll(fieldName, ".", "_"))

		switch field.Kind() {
		case reflect.String:
			if envVal := os.Getenv(envName); envVal != "" {
				field.SetString(envVal)
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if envVal := os.Getenv(envName); envVal != "" {
				if intVal, err := strconv.Atoi(envVal); err == nil {
					field.SetInt(int64(intVal))
				}
			}
		case reflect.Float64:
			if envVal := os.Getenv(envName); envVal != "" {
				if floatVal, err := strconv.ParseFloat(envVal, 64); err == nil {
					field.SetFloat(floatVal)
				}
			}
		case reflect.Bool:
			if envVal := os.Getenv(envName); envVal != "" {
				field.SetBool(envVal == "true" || envVal == "1" || envVal == "yes")
			}
		case reflect.Slice:
			if envVal := os.Getenv(envName); envVal != "" {
				if field.Type().Elem().Kind() == reflect.String {
					field.Set(reflect.ValueOf(strings.Split(envVal, ",")))
				}
			}
		case reflect.Map:
			if envVal := os.Getenv(envName); envVal != "" && field.Type().Key().Kind() == reflect.String {
				if field.IsNil() {
					field.Set(reflect.MakeMap(field.Type()))
				}
				pairs := strings.Split(envVal, ";")
				for _, pair := range pairs {
					kv := strings.SplitN(pair, "=", 2)
					if len(kv) == 2 {
						field.SetMapIndex(reflect.ValueOf(kv[0]), reflect.ValueOf(kv[1]))
					}
				}
			}
		case reflect.Struct:
			if err := applyEnvToValue(field, envName); err != nil {
				return err
			}
		}
	}
	return nil
}

// yamlUnmarshal unmarshals YAML data using gopkg.in/yaml.v3
func yamlUnmarshal(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}

// Load is a convenience function to load config from default locations
func Load(configPaths ...string) (*Config, error) {
	if len(configPaths) == 0 {
		// Try common config locations
		locations := []string{
			"config.json",
			"config.yaml",
			"config.yml",
			".hermes/config.json",
			".hermes/config.yaml",
		}
		for _, loc := range locations {
			if _, err := os.Stat(loc); err == nil {
				configPaths = append(configPaths, loc)
				break
			}
		}
		if len(configPaths) == 0 {
			return nil, fmt.Errorf("no config file found in default locations")
		}
	}

	loader := NewLoader(WithConfigFiles(configPaths...))
	return loader.Load()
}

// LoadWithArgs loads config with full customization
func LoadWithArgs(defaultPath string, envPrefix string, secretsPaths ...string) (*Config, error) {
	opts := []Option{
		WithEnvPrefix(envPrefix),
	}
	if defaultPath != "" {
		opts = append(opts, WithConfigFiles(defaultPath))
	}
	if len(secretsPaths) > 0 {
		opts = append(opts, WithSecretsPaths(secretsPaths...))
	}

	loader := NewLoader(opts...)
	return loader.Load()
}

// Save saves config to a file (JSON format)
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// GetModel returns model configuration
func (c *Config) GetModel() *ModelConfig {
	return &c.Model
}

// GetAgent returns agent configuration
func (c *Config) GetAgent() *AgentConfig {
	return &c.Agent
}

// GetSession returns session configuration
func (c *Config) GetSession() *SessionConfig {
	return &c.Session
}

// GetContext returns context configuration
func (c *Config) GetContext() *ContextConfig {
	return &c.Context
}

// GetMCPServers returns MCP servers configuration
func (c *Config) GetMCPServers() map[string]*MCPConfig {
	result := make(map[string]*MCPConfig)
	for k, v := range c.MCPServers {
		result[k] = &v
	}
	return result
}

// GetSkills returns skills configuration
func (c *Config) GetSkills() []SkillConfig {
	return c.Skills
}

// GetLogging returns logging configuration
func (c *Config) GetLogging() *LoggingConfig {
	return &c.Logging
}

// GetPlatforms returns platforms configuration
func (c *Config) GetPlatforms() *PlatformsConfig {
	return &c.Platforms
}
