package skill

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// DisclosureTier represents how much of a skill is revealed in listings.
type DisclosureTier int

const (
	// Tier1Brief is the minimal listing: name + brief description only.
	Tier1Brief DisclosureTier = 1
	// Tier2Metadata includes description, commands, entry, version, author, etc.
	Tier2Metadata DisclosureTier = 2
	// Tier3Full includes all linked files (references/, templates/, scripts/).
	Tier3Full DisclosureTier = 3
)

// Skill represents a loaded skill with its metadata and handler.
type Skill struct {
	Name        string        // skill name: "ai-daily-news"
	BriefDescription string   // one-line summary for tier-1 listings
	Description string        // full description shown in /skills list
	Commands    []string      // slash command aliases: ["news", "ainews"]
	Handler     SkillHandler  // the skill's main function
	Persistent  bool         // if true, skill state persists across calls

	// Progressive disclosure fields (tier 2+):
	Tier         DisclosureTier // minimum disclosure tier for this skill
	Version      string        // e.g. "1.0.0"
	Author       string        // e.g. "Hermes Agent"
	License      string        // e.g. "MIT"
	Category     string        // e.g. "research", "productivity", "mlops"
	Platforms    []string      // e.g. ["linux", "darwin"]
	Prerequisites []string     // e.g. ["curl", "jq"]
	Tags         []string      // e.g. ["news", "rss", "ai"]
	Config       string        // example config snippet (tier 2)

	// Tier-3 linked file content (loaded on demand).
	LinkedFiles map[string]string // filename → content
}

// SkillHandler is the function signature for a skill's main entry point.
type SkillHandler func(ctx context.Context, args string, agent AgentInterface) (string, error)

// AgentInterface exposes the agent capabilities a skill can use.
type AgentInterface interface {
	Chat(ctx context.Context, message string) (string, error)
	SystemPrompt() string
}

// Registry is the global skill registry.
type Registry struct {
	mu     sync.RWMutex
	skills map[string]*Skill
	logger *slog.Logger
}

var defaultRegistry = &Registry{
	skills: make(map[string]*Skill),
	logger: slog.Default(),
}

// defaultLoader is the package-level loader instance.
var defaultLoader *Loader

// SetLoader sets the package-level loader (called during initialization).
func SetLoader(loader *Loader) {
	defaultLoader = loader
}

// Register registers a skill. Panics if a skill with the same name is already registered.
func Register(name, description string, commands []string, handler SkillHandler) {
	skill := &Skill{
		Name:             name,
		BriefDescription: description, // tier-1 brief is same as description unless overridden
		Description:     description,
		Commands:         commands,
		Handler:          handler,
		Tier:             Tier2Metadata, // default tier 2
	}
	if err := defaultRegistry.register(skill); err != nil {
		panic(fmt.Errorf("register skill %s: %w", name, err))
	}
}

func (r *Registry) register(skill *Skill) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.skills[skill.Name]; exists {
		return fmt.Errorf("skill %q already registered", skill.Name)
	}
	r.skills[skill.Name] = skill
	return nil
}

// RegisterSkill registers a pre-populated Skill. Panics if already registered.
func RegisterSkill(skill *Skill) {
	if err := defaultRegistry.register(skill); err != nil {
		panic(fmt.Errorf("register skill %s: %w", skill.Name, err))
	}
}

// Get returns a skill by name.
func Get(name string) *Skill {
	return defaultRegistry.Get(name)
}

func (r *Registry) Get(name string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.skills[name]
}

// GetByCommand returns a skill by one of its command aliases.
func GetByCommand(cmd string) *Skill {
	return defaultRegistry.GetByCommand(cmd)
}

func (r *Registry) GetByCommand(cmd string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.skills {
		for _, c := range s.Commands {
			if c == cmd {
				return s
			}
		}
	}
	return nil
}

// ListBrief returns tier-1 skill listings (name + brief description only).
func ListBrief() []BriefSkill {
	return defaultRegistry.ListBrief()
}

func (r *Registry) ListBrief() []BriefSkill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]BriefSkill, 0, len(r.skills))
	for _, s := range r.skills {
		brief := s.BriefDescription
		if brief == "" {
			brief = s.Description
		}
		result = append(result, BriefSkill{
			Name:        s.Name,
			BriefDescription: brief,
			Category:   s.Category,
		})
	}
	return result
}

// BriefSkill is the minimal skill info returned in tier-1 listings.
type BriefSkill struct {
	Name             string
	BriefDescription string
	Category         string
}

// GetTier2 returns tier-2 metadata for a skill (no linked file content).
func GetTier2(name string) *Skill {
	return defaultRegistry.GetTier2(name)
}

func (r *Registry) GetTier2(name string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.skills[name]
}

// EnsureLinkedFiles loads linked files (references/, templates/, scripts/, assets/)
// for a skill if not already loaded. Returns the skill or nil if not found.
func EnsureLinkedFiles(name string) (*Skill, error) {
	return defaultRegistry.EnsureLinkedFiles(name)
}

func (r *Registry) EnsureLinkedFiles(name string) (*Skill, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	skill, ok := r.skills[name]
	if !ok {
		return nil, fmt.Errorf("skill %q not found", name)
	}
	if skill.LinkedFiles != nil && len(skill.LinkedFiles) > 0 {
		return skill, nil // already loaded
	}
	if defaultLoader == nil {
		skill.LinkedFiles = make(map[string]string)
		return skill, nil
	}
	files, err := defaultLoader.LoadLinkedFiles(name)
	if err != nil {
		r.logger.Warn("failed to load linked files", "skill", name, "error", err)
		skill.LinkedFiles = make(map[string]string)
		return skill, nil
	}
	skill.LinkedFiles = files
	return skill, nil
}

// GetSkillLinkedFile loads a specific linked file for a skill.
func GetSkillLinkedFile(name, filePath string) (string, error) {
	skill, err := EnsureLinkedFiles(name)
	if err != nil {
		return "", err
	}
	content, ok := skill.LinkedFiles[filePath]
	if !ok {
		return "", fmt.Errorf("file %q not found in skill %q (available: %v)", filePath, name, mapKeys(skill.LinkedFiles))
	}
	return content, nil
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// List returns all registered skills.
func List() []*Skill {
	return defaultRegistry.List()
}

func (r *Registry) List() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		result = append(result, s)
	}
	return result
}

// SetLogger sets the logger for the default registry.
func SetLogger(logger *slog.Logger) {
	defaultRegistry.logger = logger
}
