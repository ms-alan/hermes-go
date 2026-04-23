// Package skill provides skillsets-based dynamic tool loading.
//
// SkillsHub manages groups of skills ("skillsets") that can be enabled or
// disabled at runtime via a YAML config file.
//
// YAML config format (~/.hermes/skills.yaml):
//
//	skillsets:
//	  mlops:           # skillset name → enables all skills in that category
//	    enabled: true
//	    skills:         # optional per-skill overrides
//	      fine-tuning-with-trl:
//	        enabled: false
//	  research:
//	    enabled: true
//
// Each skill's SKILL.md frontmatter declares its category:
//
//	---
//	name: fine-tuning-with-trl
//	category: mlops    # used as the skillset key
//	---
//
// A skill is loaded if (a) its category is in the config and enabled, or
// (b) the skill itself has an explicit enabled:true override.
package skill

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// SkillsetConfig represents the configuration for one named skillset.
type SkillsetConfig struct {
	Enabled *bool            `yaml:"enabled"` // nil means "default to disabled"
	Skills  map[string]SkillOverride `yaml:"skills"`
}

// SkillOverride allows per-skill overrides within a skillset.
type SkillOverride struct {
	Enabled *bool `yaml:"enabled"` // nil means "inherit from skillset"
}

// SkillsHub is the global skillsets configuration manager.
type SkillsHub struct {
	mu       sync.RWMutex
	skillsDir string
	configPath string
	config    map[string]SkillsetConfig // skillsetName → config
	logger   *slog.Logger
}

var globalHub = &SkillsHub{
	logger: slog.Default(),
}

// ============================================================================
// Config loading
// ============================================================================

// DefaultSkillsYAMLPath returns the default skills config path.
func DefaultSkillsYAMLPath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".hermes", "skills.yaml")
}

// LoadSkillsets reads the skills YAML config from path.
// If the file does not exist, all skillsets default to enabled.
// If parsing fails, an error is returned and the previous config is kept.
func LoadSkillsets(path string) error {
	globalHub.mu.Lock()
	defer globalHub.mu.Unlock()
	return globalHub.loadSkillsets(path)
}

func (h *SkillsHub) loadSkillsets(path string) error {
	h.configPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No config → all skillsets enabled by default
			h.config = nil
			return nil
		}
		return fmt.Errorf("read skills config: %w", err)
	}

	var raw struct {
		Skillsets map[string]SkillsetConfig `yaml:"skillsets"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse skills config: %w", err)
	}
	h.config = raw.Skillsets
	h.logger.Info("loaded skillsets config", "path", path, "count", len(h.config))
	return nil
}

// SetSkillsDir sets the skills directory for the global hub.
func SetSkillsDir(dir string) {
	globalHub.mu.Lock()
	defer globalHub.mu.Unlock()
	globalHub.skillsDir = dir
}

// SetLogger sets the logger for the global hub.
func SetHubLogger(logger *slog.Logger) {
	globalHub.mu.Lock()
	defer globalHub.mu.Unlock()
	globalHub.logger = logger
}

// ============================================================================
// Skillset membership
// ============================================================================

// IsSkillsetEnabled returns true if the named skillset is enabled.
// A skillset is enabled if: (a) it is in the config with enabled:true, or
// (b) it is not in the config at all (default → enabled).
// Thread-safe.
func IsSkillsetEnabled(name string) bool {
	globalHub.mu.RLock()
	defer globalHub.mu.RUnlock()
	cfg, ok := globalHub.config[name]
	if !ok {
		return true // not in config → default enabled
	}
	if cfg.Enabled == nil {
		return true // in config but no Enabled field → enabled
	}
	return *cfg.Enabled
}

// IsSkillEnabled returns whether a specific skill should be loaded.
// It checks: (a) per-skill override, then (b) skillset-level default.
func IsSkillEnabled(category, skillName string) bool {
	globalHub.mu.RLock()
	defer globalHub.mu.RUnlock()
	cfg, ok := globalHub.config[category]
	if !ok {
		return true // skillset not in config → enabled by default
	}
	// Per-skill override?
	if override, has := cfg.Skills[skillName]; has && override.Enabled != nil {
		return *override.Enabled
	}
	// Skillset-level default?
	if cfg.Enabled != nil {
		return *cfg.Enabled
	}
	return true
}

// ============================================================================
// Filtering helpers (used by agent loop)
// ============================================================================

// FilterBySkillsets filters a list of loaded skills to only those whose
// categories are enabled (or who have explicit enable overrides).
func FilterBySkillsets(skills []*Skill) []*Skill {
	globalHub.mu.RLock()
	defer globalHub.mu.RUnlock()

	// If no config loaded, return all
	if len(globalHub.config) == 0 {
		return skills
	}

	var result []*Skill
	for _, s := range skills {
		cat := categoryFromSkill(s)
		if !globalHub.isSkillEnabledRLocked(cat, s.Name) {
			globalHub.logger.Debug("skill excluded by skillsets config",
				"skill", s.Name, "category", cat)
			continue
		}
		result = append(result, s)
	}
	return result
}

func (h *SkillsHub) isSkillEnabledRLocked(category, skillName string) bool {
	cfg, ok := h.config[category]
	if !ok {
		return true
	}
	if override, has := cfg.Skills[skillName]; has && override.Enabled != nil {
		return *override.Enabled
	}
	if cfg.Enabled != nil {
		return *cfg.Enabled
	}
	return true
}

// categoryFromSkill returns the skill's category (first path component of name).
func categoryFromSkill(s *Skill) string {
	parts := strings.SplitN(s.Name, "/", 2)
	return parts[0]
}

// ============================================================================
// Skeleton loader integration
// ============================================================================

// LoaderWithSkillsets wraps a Loader and filters loaded skills by the
// active skillsets config, returning only those that should be active.
func LoaderWithSkillsets(skillsDir string, logger *slog.Logger) (*Loader, *SkillsHub) {
	loader := NewLoader(skillsDir, logger)
	return loader, globalHub
}

// LoadSkillsFromDisk loads all skills from dir, filtering by active skillsets.
// This is the main entry point for the agent startup.
func LoadSkillsFromDisk(skillsDir string) ([]*Skill, error) {
	skills, err := loadAllSkillsFromDisk(skillsDir)
	if err != nil {
		return nil, err
	}
	filtered := FilterBySkillsets(skills)
	globalHub.logger.Info("loaded skills filtered by skillsets",
		"total", len(skills), "active", len(filtered))
	return filtered, nil
}

// loadAllSkillsFromDisk walks skillsDir and loads every skill.
func loadAllSkillsFromDisk(skillsDir string) ([]*Skill, error) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read skills dir: %w", err)
	}

	var skills []*Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(skillsDir, entry.Name())
		manifestPath := filepath.Join(skillPath, "SKILL.md")
		if _, err := os.Stat(manifestPath); err != nil {
			continue
		}
		content, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}

		skill := &Skill{Name: entry.Name()}
		if err := parseSkillManifest(content, skill); err != nil {
			globalHub.logger.Warn("failed to parse skill manifest",
				"path", skillPath, "error", err)
			continue
		}
		skills = append(skills, skill)
	}
	return skills, nil
}

// parseSkillManifest reads YAML frontmatter from SKILL.md content and
// populates the skill's Name, Description, and Commands fields.
func parseSkillManifest(content []byte, skill *Skill) error {
	const fence = "---"
	idx := strings.Index(string(content), fence)
	if idx < 0 {
		return nil
	}
	rest := string(content)[idx+len(fence):]
	endIdx := strings.Index(rest, fence)
	if endIdx < 0 {
		return nil
	}
	frontmatter := strings.TrimSpace(rest[:endIdx])

	var meta struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Commands    []string `yaml:"commands"`
		Category    string   `yaml:"category"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &meta); err != nil {
		return fmt.Errorf("parse frontmatter: %w", err)
	}

	if meta.Name != "" {
		skill.Name = meta.Name
	}
	if meta.Description != "" {
		skill.Description = meta.Description
	}
	if len(meta.Commands) > 0 {
		skill.Commands = meta.Commands
	}
	// Inject category as prefix so FilterBySkillsets can filter by it
	if meta.Category != "" && !strings.Contains(skill.Name, "/") {
		skill.Name = meta.Category + "/" + skill.Name
	}
	return nil
}

// ============================================================================
// Config write helpers (for `hermes skills config` equivalent)
// ============================================================================

// EnsureSkillsConfig creates a default skills.yaml if it doesn't exist.
func EnsureSkillsConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}
	// Create with default: all skillsets enabled
	defaultConfig := `# hermes-go skills configuration
# Skills are grouped by "category" as declared in each SKILL.md frontmatter.
# Set a skillset's enabled: false to disable all skills in that category.
#
# Example:
# skillsets:
#   mlops:
#     enabled: true
#     skills:
#       fine-tuning-with-trl:
#         enabled: false   # override: disable a specific skill
#   research:
#     enabled: true
#
skillsets:
  # Uncomment and edit to customize:
  # mlops:
  #   enabled: true
  # research:
  #   enabled: true
`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(defaultConfig), 0o644); err != nil {
		return fmt.Errorf("write skills config: %w", err)
	}
	return nil
}

// ListSkillsets returns the current config as a map suitable for display.
func ListSkillsets() map[string]bool {
	globalHub.mu.RLock()
	defer globalHub.mu.RUnlock()
	result := make(map[string]bool)
	for name, cfg := range globalHub.config {
		enabled := true
		if cfg.Enabled != nil {
			enabled = *cfg.Enabled
		}
		result[name] = enabled
	}
	return result
}
