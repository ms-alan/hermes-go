package skill

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Skill represents a loaded skill with its metadata and handler.
type Skill struct {
	Name        string        // skill name: "ai-daily-news"
	Description string        // short description shown in /skills list
	Commands    []string      // slash command aliases: ["news", "ainews"]
	Handler     SkillHandler  // the skill's main function
	Persistent  bool         // if true, skill state persists across calls
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

// Register registers a skill. Panics if a skill with the same name is already registered.
func Register(name, description string, commands []string, handler SkillHandler) {
	skill := &Skill{
		Name:        name,
		Description: description,
		Commands:    commands,
		Handler:     handler,
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
