package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ============================================================================
// OSV Malware Check
// ============================================================================
//
// Checks NPM/PyPI packages for known MAL-* malware advisories via the OSV API
// (https://api.osv.dev). Inspired by Block/goose's extension malware check.
//
// Fail-open: network errors, timeouts, and parse failures allow the package
// to proceed (the MCP server is still launched — the check is advisory).
//
// Integration: call CheckPackageForMalware(cmd, args) in the MCP client
// transport before spawning any npx/uvx process.

var osvClient = &http.Client{Timeout: 10 * time.Second}

// OSVMalwareAdvisory represents a malware advisory from OSV.
type OSVMalwareAdvisory struct {
	ID      string `json:"id"`
	Summary string `json:"summary,omitempty"`
}

// CheckPackageForMalware checks if an MCP server package has known malware advisories.
// Returns an error message string if malware is found, or "" if clean/unknown.
// Fails open: network errors allow the package to proceed.
func CheckPackageForMalware(command string, args []string) string {
	ecosystem := inferEcosystem(command)
	if ecosystem == "" {
		return ""
	}

	pkg, version := parsePackageFromArgs(args, ecosystem)
	if pkg == "" {
		return ""
	}

	malware, err := queryOSV(pkg, ecosystem, version)
	if err != nil {
		return ""
	}

	if len(malware) == 0 {
		return ""
	}

	var ids []string
	var summaries []string
	for _, m := range malware {
		if len(ids) >= 3 {
			break
		}
		ids = append(ids, m.ID)
		summary := m.Summary
		if summary == "" {
			summary = m.ID
		}
		if len(summary) > 100 {
			summary = summary[:100] + "…"
		}
		summaries = append(summaries, summary)
	}

	return fmt.Sprintf(
		"BLOCKED: Package '%s' (%s) has known malware advisories: %s. Details: %s",
		pkg, ecosystem, strings.Join(ids, ", "), strings.Join(summaries, "; "),
	)
}

func inferEcosystem(command string) string {
	base := strings.ToLower(filepath.Base(command))
	if base == "npx" || base == "npx.cmd" {
		return "npm"
	}
	if base == "uvx" || base == "uvx.cmd" || base == "pipx" {
		return "PyPI"
	}
	return ""
}

func parsePackageFromArgs(args []string, ecosystem string) (pkg string, version string) {
	if len(args) == 0 {
		return "", ""
	}

	var packageToken string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			packageToken = arg
			break
		}
	}
	if packageToken == "" {
		return "", ""
	}

	if ecosystem == "npm" {
		return parseNPMPackage(packageToken)
	}
	if ecosystem == "PyPI" {
		return parsePyPIPackage(packageToken)
	}
	return packageToken, ""
}

func parseNPMPackage(token string) (pkg string, version string) {
	if strings.HasPrefix(token, "@") {
		re := regexp.MustCompile(`^(@[^/]+/[^@]+)(?:@(.+))?$`)
		m := re.FindStringSubmatch(token)
		if len(m) >= 2 {
			ver := ""
			if len(m) >= 3 {
				ver = m[2]
			}
			return m[1], ver
		}
		return token, ""
	}
	if idx := strings.LastIndex(token, "@"); idx > 0 {
		return token[:idx], token[idx+1:]
	}
	return token, ""
}

func parsePyPIPackage(token string) (pkg string, version string) {
	re := regexp.MustCompile(`^([a-zA-Z0-9._-]+)(?:\[[^\]]*\])?(?:==(.+))?$`)
	m := re.FindStringSubmatch(token)
	if len(m) >= 2 {
		ver := ""
		if len(m) >= 3 {
			ver = m[2]
		}
		return m[1], ver
	}
	return token, ""
}

func queryOSV(pkg, ecosystem, version string) ([]OSVMalwareAdvisory, error) {
	payload := map[string]interface{}{
		"package": map[string]string{
			"name":      pkg,
			"ecosystem": ecosystem,
		},
	}
	if version != "" {
		payload["version"] = version
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	endpoint := "https://api.osv.dev/v1/query"
	if ep := strings.TrimSpace(os.Getenv("OSV_ENDPOINT")); ep != "" {
		endpoint = ep
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "hermes-go-osv-check/1.0")

	resp, err := osvClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OSV API returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Vulns []struct {
			ID      string `json:"id"`
			Summary string `json:"summary,omitempty"`
		} `json:"vulns"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var malware []OSVMalwareAdvisory
	for _, v := range result.Vulns {
		if strings.HasPrefix(v.ID, "MAL-") {
			malware = append(malware, OSVMalwareAdvisory{ID: v.ID, Summary: v.Summary})
		}
	}
	return malware, nil
}

// ============================================================================
// Tool registration
// ============================================================================

func osvCheckHandler(args map[string]interface{}) string {
	command, _ := args["command"].(string)
	argsIf, _ := args["args"].([]interface{})

	var argsList []string
	for _, a := range argsIf {
		if s, ok := a.(string); ok {
			argsList = append(argsList, s)
		}
	}

	errMsg := CheckPackageForMalware(command, argsList)
	if errMsg != "" {
		return toolError(errMsg)
	}
	return toolResultData(map[string]interface{}{
		"safe":   true,
		"message": "No malware advisories found (or not a checkable command)",
	})
}

var osvCheckSchema = map[string]any{
	"description": "Check an NPM/PyPI package for known MAL-* malware advisories via the OSV API. Use this before launching any MCP server that uses npx or uvx. Fails open: network errors allow the package to proceed.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The command to check (e.g. 'npx', 'uvx').",
			},
			"args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Command arguments (e.g. ['@modelcontextprotocol/server-filesystem', '/some/path']).",
			},
		},
		"required": []string{"command", "args"},
	},
}

func osvCheckAvailable() bool { return true }

func osvCheckEmoji() string   { return "🔒" }
func osvCheckDescription() string { return "Check NPM/PyPI package for malware via OSV API" }

var (
	_ = osvCheckHandler   // referenced via Register call
	_ = osvCheckSchema    // referenced via Register call
	_ = osvCheckAvailable // referenced via Register call
	_ = osvCheckEmoji     // referenced via Register call
	_ = osvCheckDescription
)
