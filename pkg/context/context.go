// Package context provides context file loading for agent personality
// and project instructions (SOUL.md, AGENTS.md, .hermes.md, etc.).
package context

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// BlockedPaths are relative path patterns blocked from @file: references.
var BlockedPaths = []string{
	".ssh/id_rsa", ".ssh/id_ed25519", ".ssh/authorized_keys", ".ssh/config",
	".bashrc", ".zshrc", ".profile", ".bash_profile", ".zprofile",
	".netrc", ".pgpass", ".npmrc", ".pypirc",
}

// Loader loads context files for the agent.
type Loader struct {
	hermesHome string
	cwd        string
}

// NewLoader creates a context loader with the given Hermes home and working directory.
func NewLoader(hermesHome, cwd string) *Loader {
	if hermesHome == "" {
		hermesHome = filepath.Join(os.Getenv("HOME"), ".hermes")
	}
	return &Loader{hermesHome: hermesHome, cwd: cwd}
}

// LoadSOUL loads SOUL.md from HERMES_HOME — the agent's primary identity (slot #1).
func (l *Loader) LoadSOUL() (string, error) {
	path := filepath.Join(l.hermesHome, "SOUL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("load SOUL.md: %w", err)
	}
	return l.scanAndTrim(string(data))
}

// LoadProjectContext loads the highest-priority project context file.
// Priority: .hermes.md → HERMES.md → AGENTS.md → CLAUDE.md → .cursorrules
func (l *Loader) LoadProjectContext() (string, error) {
	dir := l.cwd
	for {
		candidates := []string{
			filepath.Join(dir, ".hermes.md"),
			filepath.Join(dir, "HERMES.md"),
			filepath.Join(dir, "AGENTS.md"),
			filepath.Join(dir, "CLAUDE.md"),
			filepath.Join(dir, ".cursorrules"),
		}
		for _, p := range candidates {
			data, err := os.ReadFile(p)
			if err == nil {
				return l.scanAndTrim(string(data))
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", nil
}

// scanAndTrim performs basic security scan and truncation.
func (l *Loader) scanAndTrim(content string) (string, error) {
	const maxContextLen = 50 * 1024
	if len(content) > maxContextLen {
		return content[:maxContextLen] + "\n\n[... truncated ...]", nil
	}
	return content, nil
}

// IsBlockedPath returns true if path is in the blocked list.
func IsBlockedPath(path string) bool {
	home := os.Getenv("HOME")
	abs, err := filepath.Abs(filepath.Join(home, path))
	if err == nil {
		for _, bp := range BlockedPaths {
			if abs == filepath.Join(home, bp) {
				return true
			}
		}
		if abs == filepath.Join(home, ".hermes", ".env") {
			return true
		}
	}
	return false
}

// RefType represents a context reference type.
type RefType string

const (
	RefFile   RefType = "file"
	RefFolder RefType = "folder"
	RefDiff   RefType = "diff"
	RefStaged RefType = "staged"
	RefGit    RefType = "git"
	RefURL    RefType = "url"
)

// ParseRef parses a reference string like "@file:path/to/file.py:10-25"
// and returns (type, path, lineRange).
func ParseRef(ref string) (RefType, string, string) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "@file:") {
		val := strings.TrimPrefix(ref, "@file:")
		if idx := strings.LastIndex(val, ":"); idx > 0 {
			lineRange := val[idx+1:]
			filePath := val[:idx]
			// Check if lineRange is numeric or range
			if matched, _ := regexp.MatchString(`^\d+(-\d+)?$`, lineRange); matched {
				return RefFile, filePath, lineRange
			}
		}
		return RefFile, val, ""
	}
	if strings.HasPrefix(ref, "@folder:") {
		return RefFolder, strings.TrimPrefix(ref, "@folder:"), ""
	}
	if ref == "@diff" {
		return RefDiff, "", ""
	}
	if ref == "@staged" {
		return RefStaged, "", ""
	}
	if strings.HasPrefix(ref, "@git:") {
		return RefGit, strings.TrimPrefix(ref, "@git:"), ""
	}
	if strings.HasPrefix(ref, "@url:") {
		return RefURL, strings.TrimPrefix(ref, "@url:"), ""
	}
	return "", "", ""
}

// ExpandRefs expands @references in a user message.
func (l *Loader) ExpandRefs(msg string) (string, error) {
	var result strings.Builder
	remain := msg

	for {
		idx := strings.Index(remain, "@")
		if idx < 0 {
			result.WriteString(remain)
			break
		}
		result.WriteString(remain[:idx])
		remain = remain[idx+1:]

		// Find end of reference
		endIdx := 0
		for endIdx < len(remain) {
			r := rune(remain[endIdx])
			if r == ' ' || r == '\n' || r == ',' || r == '.' || r == ';' || r == '!' || r == '?' {
				break
			}
			endIdx++
		}
		refStr := remain[:endIdx]
		remain = remain[endIdx:]

		expanded, err := l.expandSingleRef(refStr)
		if err != nil {
			result.WriteString(" @" + refStr)
			continue
		}
		result.WriteString(expanded)
	}

	return result.String(), nil
}

func (l *Loader) expandSingleRef(ref string) (string, error) {
	refType, path, lineRange := ParseRef("@" + ref)

	switch refType {
	case RefFile:
		if IsBlockedPath(path) {
			return "", fmt.Errorf("blocked path: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("@file %s: %w", path, err)
		}
		content := string(data)
		if lineRange != "" {
			content = applyLineRange(content, lineRange)
		}
		return fmt.Sprintf("\n[File: %s]\n%s\n[/File]\n", path, content), nil

	case RefFolder:
		if IsBlockedPath(path) {
			return "", fmt.Errorf("blocked path: %s", path)
		}
		var buf bytes.Buffer
		count := 0
		filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if err != nil || count > 200 {
				return nil
			}
			rel, _ := filepath.Rel(path, p)
			if rel == "." {
				return nil
			}
			if info.IsDir() {
				buf.WriteString(fmt.Sprintf("  [DIR]  %s/\n", rel))
			} else {
				buf.WriteString(fmt.Sprintf("  %5d  %s\n", info.Size(), rel))
			}
			count++
			return nil
		})
		return fmt.Sprintf("\n[Folder: %s]\n%s[/Folder]\n", path, buf.String()), nil

	case RefDiff:
		var out bytes.Buffer
		cmd := exec.Command("git", "diff", "--no-color")
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		_ = cmd.Run()
		return fmt.Sprintf("\n[Git Diff]\n%s[/Git Diff]\n", out.String()), nil

	case RefStaged:
		var out bytes.Buffer
		cmd := exec.Command("git", "diff", "--cached", "--no-color")
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		_ = cmd.Run()
		return fmt.Sprintf("\n[Git Staged]\n%s[/Git Staged]\n", out.String()), nil

	case RefGit:
		n := 5
		fmt.Sscanf(path, "%d", &n)
		if n < 1 {
			n = 1
		}
		if n > 10 {
			n = 10
		}
		// Show commits with patches (--patch) for review context
		var out bytes.Buffer
		cmd := exec.Command("git", "log", fmt.Sprintf("-%d", n), "--no-color", "--patch")
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		_ = cmd.Run()
		return fmt.Sprintf("\n[Git Log %d commits with patches]\n%s[/Git Log]\n", n, out.String()), nil

	case RefURL:
		data, err := fetchURL(path)
		if err != nil {
			return "", fmt.Errorf("@url %s: %w", path, err)
		}
		return fmt.Sprintf("\n[URL: %s]\n%s\n[/URL]\n", path, data), nil
	}

	return "", fmt.Errorf("unknown reference: @%s", ref)
}

func applyLineRange(content, lineRange string) string {
	lines := strings.Split(content, "\n")
	if strings.Contains(lineRange, "-") {
		var start, end int
		fmt.Sscanf(lineRange, "%d-%d", &start, &end)
		if start < 1 {
			start = 1
		}
		if end > len(lines) {
			end = len(lines)
		}
		if start > end {
			return ""
		}
		return strings.Join(lines[start-1:end], "\n")
	}
	var line int
	fmt.Sscanf(lineRange, "%d", &line)
	if line < 1 || line > len(lines) {
		return ""
	}
	return lines[line-1]
}

func fetchURL(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	text := stripHTMLTags(string(body))
	const max = 10 * 1024
	if len(text) > max {
		text = text[:max] + "\n[... truncated ...]"
	}
	return text, nil
}

func stripHTMLTags(html string) string {
	var result strings.Builder
	inTag := false
	for _, r := range html {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}
	return result.String()
}
