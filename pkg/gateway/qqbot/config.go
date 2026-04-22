package qqbot

import (
	"os"
	"strconv"
)

// Config holds QQ bot configuration.
type Config struct {
	AppID           string
	AppSecret       string
	DMEnabled       bool
	GroupEnabled    bool
	AllowFrom       []string
	GroupAllowFrom  []string
	MarkdownEnabled bool
}

// DefaultConfig returns a Config from environment variables.
func DefaultConfig() *Config {
	appID := os.Getenv("QQ_APP_ID")
	appSecret := os.Getenv("QQ_CLIENT_SECRET")
	if appID == "" || appSecret == "" {
		return nil
	}
	return &Config{
		AppID:           appID,
		AppSecret:       appSecret,
		DMEnabled:       getEnvBool("QQ_DM_ENABLED", true),
		GroupEnabled:    getEnvBool("QQ_GROUP_ENABLED", true),
		MarkdownEnabled: getEnvBool("QQ_MARKDOWN_ENABLED", true),
	}
}

func getEnvBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return defaultVal
}
