// Package mcp provides types for the Model Context Protocol (MCP).
package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultConfigPaths returns the standard locations to search for config.
func DefaultConfigPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".hermes", "config.yaml"),
		filepath.Join(home, ".config", "hermes", "config.yaml"),
	}
}

// LoadMCPConfig loads MCP server configurations from a YAML config file.
// It looks for a top-level key "mcp_servers" containing a map of server name -> config.
// Supports both the list format and the map-of-maps format used by Claude Desktop.
func LoadMCPConfig(configPath string) (*MCPConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	// Try parsing as MCPConfig directly first
	var direct MCPConfig
	if err := yaml.Unmarshal(data, &direct); err == nil && len(direct.Servers) > 0 {
		return &direct, nil
	}

	// Try parsing as a generic map and extracting mcp_servers
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config YAML: %w", err)
	}

	mcpServersRaw, ok := raw["mcp_servers"]
	if !ok {
		return nil, fmt.Errorf("no mcp_servers key found in config")
	}

	servers, err := parseMCPServers(mcpServersRaw)
	if err != nil {
		return nil, fmt.Errorf("parsing mcp_servers: %w", err)
	}

	return &MCPConfig{Servers: servers}, nil
}

// LoadMCPServers is a convenience wrapper that returns just the server list.
func LoadMCPServers(configPaths ...string) ([]MCPServerConfig, error) {
	if len(configPaths) == 0 {
		configPaths = DefaultConfigPaths()
	}
	for _, p := range configPaths {
		cfg, err := LoadMCPConfig(p)
		if err == nil {
			return cfg.Servers, nil
		}
	}
	return nil, fmt.Errorf("no config file found in: %v", configPaths)
}

// parseMCPServers handles the various YAML formats used for MCP server configs.
func parseMCPServers(raw interface{}) ([]MCPServerConfig, error) {
	switch v := raw.(type) {
	case []interface{}:
		// List format: [{name: "foo", transport: "stdio", command: "..."}, ...]
		var servers []MCPServerConfig
		for i, item := range v {
			node, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("server[%d]: expected map, got %T", i, item)
			}
			srv, err := mapToServer(node)
			if err != nil {
				return nil, fmt.Errorf("server[%d]: %w", i, err)
			}
			servers = append(servers, srv)
		}
		return servers, nil

	case map[string]interface{}:
		// Map format: {serverName: {transport: "stdio", ...}}
		var servers []MCPServerConfig
		for name, item := range v {
			node, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("server %q: expected map, got %T", name, item)
			}
			srv, err := mapToServer(node)
			if err != nil {
				return nil, fmt.Errorf("server %q: %w", name, err)
			}
			srv.Name = name
			servers = append(servers, srv)
		}
		return servers, nil

	default:
		return nil, fmt.Errorf("mcp_servers: expected list or map, got %T", raw)
	}
}

// mapToServer converts a map to a MCPServerConfig.
func mapToServer(m map[string]interface{}) (MCPServerConfig, error) {
	srv := MCPServerConfig{}

	if v, ok := m["transport"].(string); ok {
		srv.Transport = v
	} else if _, ok := m["url"].(string); ok {
		srv.Transport = "http"
	} else {
		srv.Transport = "stdio"
	}

	if v, ok := m["command"].(string); ok {
		srv.Command = v
	}

	if v, ok := m["url"].(string); ok {
		srv.URL = v
	}

	if v, ok := m["args"].([]interface{}); ok {
		for _, a := range v {
			if s, ok := a.(string); ok {
				srv.Args = append(srv.Args, s)
			}
		}
	}

	if v, ok := m["env"].(map[string]interface{}); ok {
		srv.Env = make(map[string]string)
		for k, val := range v {
			if s, ok := val.(string); ok {
				srv.Env[k] = s
			}
		}
	}

	if v, ok := m["headers"].(map[string]interface{}); ok {
		srv.Headers = make(map[string]string)
		for k, val := range v {
			if s, ok := val.(string); ok {
				srv.Headers[k] = s
			}
		}
	}

	if v, ok := m["timeout"].(int); ok {
		srv.Timeout = v
	}

	if v, ok := m["connectTimeout"].(int); ok {
		srv.ConnectTimeout = v
	}

	if v, ok := m["disabled"].(bool); ok {
		srv.Disabled = v
	}

	// Detect disabled from name convention (ending with _disabled)
	if strings.HasSuffix(srv.Name, "_disabled") {
		srv.Disabled = true
	}

	return srv, nil
}
