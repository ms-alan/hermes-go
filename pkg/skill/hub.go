// Package skill provides skill loading, management, and remote hub fetching.
package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ============================================================================
// Hub — remote skill registry via GitHub API
// ============================================================================

// TrustedRepos maps GitHub repos considered trusted (vs community).
var TrustedRepos = map[string]bool{
	"openai/skills":           true,
	"anthropics/skills":       true,
	"NousResearch/hermes-go":  true,
	" NousResearch/hermes-agent": true,
	"VoltAgent/awesome-agent-skills": true,
	"garrytan/gstack":         true,
	"MiniMax-AI/cli":          true,
}

// DefaultTaps are the default GitHub repos searched for skills.
var DefaultTaps = []TapConfig{
	{Repo: "openai/skills", Path: "skills/"},
	{Repo: "anthropics/skills", Path: "skills/"},
	{Repo: "VoltAgent/awesome-agent-skills", Path: "skills/"},
	{Repo: "garrytan/gstack", Path: ""},
	{Repo: "MiniMax-AI/cli", Path: "skill/"},
}

// TapConfig describes a skill tap (GitHub repo + path prefix).
type TapConfig struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
}

// SkillMeta is lightweight metadata for a remote skill.
type SkillMeta struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Source       string   `json:"source"`        // "github"
	Identifier   string   `json:"identifier"`    // "owner/repo/path/to/skill-dir"
	TrustLevel   string   `json:"trust_level"`   // "trusted" | "community"
	Repo         string   `json:"repo"`          // "owner/repo"
	Path         string   `json:"path"`          // "path/to/skill-dir"
	Tags         []string `json:"tags"`
	Author       string   `json:"author"`
	Version      string   `json:"version"`
	Category     string   `json:"category"`
}

// SkillBundle is the full skill content downloaded from a remote source.
type SkillBundle struct {
	Name         string            `json:"name"`
	Files        map[string][]byte `json:"files"` // filename → content
	Source       string            `json:"source"`
	Identifier   string            `json:"identifier"`
	TrustLevel   string            `json:"trust_level"`
}

// Hub fetches skills from remote registries (GitHub taps).
type Hub struct {
	Taps    []TapConfig
	Auth    *GitHubAuth
	Cache   *HubCache
	Logger  *slog.Logger
	HTTPCli *http.Client

	// Per-Hub runtime state
	treeCache map[string]treeCacheEntry // key: "repo_path"
}

type treeCacheEntry struct {
	Branch  string
	Entries []treeEntry
}

// HubCache persists the skill index cache to disk.
type HubCache struct {
	Dir string
}

// NewHub creates a Hub with the given taps and optional auth.
func NewHub(taps []TapConfig, auth *GitHubAuth, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	if auth == nil {
		auth = NewGitHubAuth()
	}
	h := &Hub{
		Taps:      taps,
		Auth:      auth,
		Cache:     &HubCache{Dir: filepath.Join(os.Getenv("HOME"), ".hermes", "skill-cache")},
		Logger:    logger,
		HTTPCli:   &http.Client{Timeout: 30 * time.Second},
		treeCache: make(map[string]treeCacheEntry),
	}
	if h.Taps == nil {
		h.Taps = DefaultTaps
	}
	return h
}

// Search looks up skills matching query across all taps.
func (h *Hub) Search(query string, limit int) ([]SkillMeta, error) {
	if limit <= 0 {
		limit = 10
	}
	queryLower := strings.ToLower(query)
	var results []SkillMeta

	for _, tap := range h.Taps {
		entries, err := h.listTreeEntries(tap.Repo, tap.Path)
		if err != nil {
			h.Logger.Debug("hub: failed to list", "repo", tap.Repo, "path", tap.Path, "error", err)
			continue
		}

		for _, entry := range entries {
			if entry.Type != "dir" {
				continue
			}
			if strings.HasPrefix(entry.Name, ".") || strings.HasPrefix(entry.Name, "_") {
				continue
			}

			// Get SKILL.md metadata for this skill directory
			skillPath := strings.TrimSuffix(tap.Path, "/")
			if skillPath != "" {
				skillPath += "/"
			}
			skillPath += entry.Name

			meta, err := h.inspect(tap.Repo, skillPath)
			if err != nil || meta == nil {
				continue
			}

			searchable := strings.ToLower(meta.Name + " " + meta.Description + " " + strings.Join(meta.Tags, " "))
			if strings.Contains(searchable, queryLower) {
				results = append(results, *meta)
			}
		}
	}

	// Deduplicate by name, preferring trusted
	seen := make(map[string]SkillMeta)
	trustRank := map[string]int{"builtin": 2, "trusted": 1, "community": 0}
	for _, r := range results {
		existing, ok := seen[r.Name]
		if !ok || trustRank[r.TrustLevel] > trustRank[existing.TrustLevel] {
			seen[r.Name] = r
		}
	}

	var deduped []SkillMeta
	for _, v := range seen {
		deduped = append(deduped, v)
	}
	if len(deduped) > limit {
		deduped = deduped[:limit]
	}
	return deduped, nil
}

// Fetch downloads a full skill bundle by identifier ("owner/repo/path/to/skill-dir").
func (h *Hub) Fetch(identifier string) (*SkillBundle, error) {
	parts := strings.SplitN(identifier, "/", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid skill identifier %q: expected owner/repo/path", identifier)
	}
	repo := parts[0] + "/" + parts[1]
	skillPath := parts[2]

	files, err := h.downloadDirectory(repo, skillPath)
	if err != nil {
		return nil, fmt.Errorf("download directory: %w", err)
	}
	if _, ok := files["SKILL.md"]; !ok {
		return nil, fmt.Errorf("skill %q has no SKILL.md", identifier)
	}

	skillName := skillPath
	if idx := strings.LastIndex(skillName, "/"); idx >= 0 {
		skillName = skillName[idx+1:]
	}

	return &SkillBundle{
		Name:       skillName,
		Files:      files,
		Source:     "github",
		Identifier: identifier,
		TrustLevel: h.trustLevel(identifier),
	}, nil
}

// inspect fetches just the SKILL.md metadata for a skill dir.
func (h *Hub) inspect(repo, skillPath string) (*SkillMeta, error) {
	skillPath = strings.TrimSuffix(skillPath, "/")
	skillMDPath := skillPath + "/SKILL.md"

	content, err := h.fetchFileContent(repo, skillMDPath)
	if err != nil || content == nil {
		return nil, err
	}

	name, desc, tags := parseSkillMDMeta(string(content))
	if name == "" {
		name = skillPath
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
	}

	skillDir := skillPath
	if idx := strings.LastIndex(skillDir, "/"); idx >= 0 {
		skillDir = skillDir[idx+1:]
	}

	return &SkillMeta{
		Name:        name,
		Description: desc,
		Source:      "github",
		Identifier:  repo + "/" + skillPath,
		TrustLevel: h.trustLevel(repo + "/" + skillPath),
		Repo:        repo,
		Path:        skillPath,
		Tags:        tags,
	}, nil
}

// Install installs a skill bundle to the local skills directory.
// Skills are first written to a quarantine subdirectory, then moved to the
// final destination to avoid partially-installed skills.
func (h *Hub) Install(bundle *SkillBundle, skillsDir string) error {
	if skillsDir == "" {
		skillsDir = filepath.Join(os.Getenv("HOME"), ".hermes", "skills")
	}

	skillDir := filepath.Join(skillsDir, bundle.Name)
	quarantineDir := filepath.Join(skillsDir, ".quarantine", bundle.Name)

	// Write to quarantine first
	if err := os.MkdirAll(quarantineDir, 0755); err != nil {
		return fmt.Errorf("create quarantine dir: %w", err)
	}

	for filename, content := range bundle.Files {
		filePath := filepath.Join(quarantineDir, filename)
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return fmt.Errorf("create file dir %s: %w", filename, err)
		}
		if err := os.WriteFile(filePath, content, 0644); err != nil {
			return fmt.Errorf("write file %s: %w", filename, err)
		}
	}

	// Atomic rename from quarantine to final location
	if err := os.RemoveAll(skillDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing dir: %w", err)
	}
	if err := os.Rename(quarantineDir, skillDir); err != nil {
		return fmt.Errorf("rename quarantine to final: %w", err)
	}

	// Clean up quarantine parent if empty
	os.Remove(filepath.Join(skillsDir, ".quarantine"))

	h.Logger.Info("hub: installed skill", "name", bundle.Name, "trust", bundle.TrustLevel)
	return nil
}

// trustLevel returns "trusted" for trusted repos, "community" otherwise.
func (h *Hub) trustLevel(identifier string) string {
	parts := strings.SplitN(identifier, "/", 3)
	if len(parts) >= 2 {
		repo := parts[0] + "/" + parts[1]
		if TrustedRepos[repo] {
			return "trusted"
		}
	}
	return "community"
}

// ============================================================================
// Internal GitHub API helpers
// ============================================================================

type treeEntry struct {
	Name string `json:"name"`
	Type string `json:"type"` // "file" | "dir"
}

func (h *Hub) listTreeEntries(repo, path string) ([]treeEntry, error) {
	cacheKey := repo + "_" + path
	if entry, ok := h.treeCache[cacheKey]; ok {
		return entry.Entries, nil
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s", repo, strings.TrimSuffix(path, "/"))
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	h.Auth.setHeaders(req)

	resp, err := h.HTTPCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode == http.StatusForbidden {
		h.Logger.Warn("hub: GitHub API 403", "repo", repo)
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API %d", resp.StatusCode)
	}

	var entries []treeEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}

	h.treeCache[cacheKey] = treeCacheEntry{Entries: entries}
	return entries, nil
}

func (h *Hub) fetchFileContent(repo, path string) ([]byte, error) {
	// Use the raw content URL for SKILL.md (no API key needed for public repos)
	// Or use the API with proper accept header
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s", repo, path)
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	h.Auth.setHeaders(req)

	resp, err := h.HTTPCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	var entry struct {
		Content string `json:"content"` // base64-encoded
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		return nil, err
	}

	if entry.Encoding == "base64" {
		return decodeBase64(entry.Content)
	}
	return []byte(entry.Content), nil
}

func (h *Hub) downloadDirectory(repo, path string) (map[string][]byte, error) {
	entries, err := h.listTreeEntries(repo, path)
	if err != nil {
		return nil, err
	}

	files := make(map[string][]byte)
	for _, entry := range entries {
		entryPath := path
		if !strings.HasSuffix(entryPath, "/") {
			entryPath += "/"
		}
		entryPath += entry.Name

		if entry.Type == "dir" {
			subFiles, err := h.downloadDirectory(repo, entryPath)
			if err != nil {
				continue
			}
			prefix := path
			if !strings.HasSuffix(prefix, "/") {
				prefix += "/"
			}
			for fname, content := range subFiles {
				relPath := strings.TrimPrefix(fname, prefix)
				files[relPath] = content
			}
		} else {
			content, err := h.fetchFileContent(repo, entryPath)
			if err != nil || content == nil {
				continue
			}
			filename := entry.Name
			if idx := strings.IndexByte(entryPath[len(path):], '/'); idx >= 0 {
				// subdirectory file
				relPath := entryPath[len(path)+1:]
				files[relPath] = content
			} else {
				files[filename] = content
			}
		}
	}
	return files, nil
}

// ============================================================================
// GitHub Authentication
// ============================================================================

// GitHubAuth resolves GitHub API tokens using multiple strategies:
//  1. GITHUB_TOKEN / GH_TOKEN env var
//  2. gh auth token (CLI)
//  3. Unauthenticated (60 req/hr, public repos only)
type GitHubAuth struct {
	cachedToken  string
	cachedMethod string
	expiry       time.Time
}

func NewGitHubAuth() *GitHubAuth {
	return &GitHubAuth{}
}

func (a *GitHubAuth) setHeaders(req *http.Request) {
	token := a.resolveToken()
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
}

func (a *GitHubAuth) isAuthenticated() bool {
	return a.resolveToken() != ""
}

func (a *GitHubAuth) authMethod() string {
	a.resolveToken()
	if a.cachedMethod == "" {
		return "anonymous"
	}
	return a.cachedMethod
}

func (a *GitHubAuth) resolveToken() string {
	// Return cached token if still valid
	if a.cachedToken != "" {
		if a.cachedMethod != "github-app" || time.Now().Before(a.expiry) {
			return a.cachedToken
		}
	}

	// 1. Environment variable
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		a.cachedToken = token
		a.cachedMethod = "pat"
		return token
	}
	if token := os.Getenv("GH_TOKEN"); token != "" {
		a.cachedToken = token
		a.cachedMethod = "pat"
		return token
	}

	// 2. gh CLI
	if token := a.tryGhCLI(); token != "" {
		a.cachedToken = token
		a.cachedMethod = "gh-cli"
		return token
	}

	a.cachedMethod = "anonymous"
	return ""
}

func (a *GitHubAuth) tryGhCLI() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return ""
	}
	token := strings.TrimSpace(buf.String())
	if token != "" {
		return token
	}
	return ""
}

// ============================================================================
// SKILL.md metadata parsing (lightweight)
// ============================================================================

var frontmatterRe = regexp.MustCompile(`(?is)^---\s*\n(.*?)\n---`)

func parseSkillMDMeta(content string) (name, description string, tags []string) {
	m := frontmatterRe.FindStringSubmatch(content)
	if len(m) < 2 {
		return "", "", nil
	}
	lines := strings.Split(m[1], "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, "\"")

		switch key {
		case "name":
			name = val
		case "description":
			description = val
		case "tags":
			// "[tag1, tag2, tag3]" or "- tag1\n- tag2"
			if strings.HasPrefix(val, "[") {
				val = strings.Trim(val, "[]")
				for _, t := range strings.Split(val, ",") {
					t = strings.TrimSpace(strings.Trim(t, "\""))
					if t != "" {
						tags = append(tags, t)
					}
				}
			}
		}
	}
	return
}

// ============================================================================
// Base64 decoding (standard library only)
// ============================================================================

func decodeBase64(s string) ([]byte, error) {
	// Remove newlines and padding
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.TrimRight(s, "=")
	return base64StdDecode(s)
}

func base64StdDecode(s string) ([]byte, error) {
	const decodeErr = "invalid base64"
	dec := make([]byte, len(s)*6/8)
	j := 0
	buf := 0
	nbits := 0
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	inv := make(map[byte]int, 64)
	for i := range alphabet {
		inv[alphabet[i]] = i
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '=' {
			break
		}
		v, ok := inv[c]
		if !ok {
			continue // skip invalid chars
		}
		buf = buf<<6 | v
		nbits += 6
		if nbits >= 8 {
			nbits -= 8
			dec[j] = byte(buf >> nbits)
			j++
		}
	}
	return dec[:j], nil
}
