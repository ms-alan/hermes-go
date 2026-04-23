// Package tools provides the code_execution tool for hermes-go.
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Allowed tools inside the sandbox (intersection with session enabled tools).
var sandboxAllowedTools = []string{
	"web_search",
	"web_extract",
	"read_file",
	"write_file",
	"search_files",
	"patch",
	"terminal",
}

// Config holds code execution settings.
type CodeExecutionConfig struct {
	Timeout      int // seconds, default 300
	MaxToolCalls int // default 50
	EnvPassthrough []string // env vars to pass through
}

// DefaultConfig returns the default configuration.
func DefaultCodeExecutionConfig() CodeExecutionConfig {
	return CodeExecutionConfig{
		Timeout:      300,
		MaxToolCalls: 50,
		EnvPassthrough: []string{"PATH", "HOME"},
	}
}

// ExecuteCode runs a Python script in a sandboxed subprocess.
// It communicates with the subprocess via a Unix Domain Socket (local) or
// file-based RPC (remote backend like docker/ssh).
func ExecuteCode(ctx context.Context, code string, config CodeExecutionConfig, toolHandler func(toolName string, args map[string]any) string) (string, error) {
	if config.Timeout == 0 {
		config.Timeout = 300
	}
	if config.MaxToolCalls == 0 {
		config.MaxToolCalls = 50
	}

	// Generate hermes_tools.py stub
	stub := generateToolsStub(sandboxAllowedTools)

	// Create a temp dir for this execution
	tmpDir, err := os.MkdirTemp("", "hermes-code-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	stubPath := filepath.Join(tmpDir, "hermes_tools.py")
	if err := os.WriteFile(stubPath, []byte(stub), 0600); err != nil {
		return "", fmt.Errorf("write stub: %w", err)
	}

	scriptPath := filepath.Join(tmpDir, "script.py")
	if err := os.WriteFile(scriptPath, []byte(code), 0600); err != nil {
		return "", fmt.Errorf("write script: %w", err)
	}

	// Set up RPC socket
	rpcSocket := filepath.Join(tmpDir, "rpc.sock")
	listener, err := net.Listen("unix", rpcSocket)
	if err != nil {
		return "", fmt.Errorf("listen on UDS: %w", err)
	}
	defer listener.Close()

	// Build stripped environment
	env := buildSandboxEnv(config.EnvPassthrough)
	env["HERMES_RPC_SOCKET"] = rpcSocket
	env["PYTHONPATH"] = tmpDir

	// Spawn Python subprocess
	var pythonBin string
	for _, candidate := range []string{"python3", "python", "python3.11", "python3.12"} {
		if path, err := lookPath(candidate); err == nil {
			pythonBin = path
			break
		}
	}
	if pythonBin == "" {
		return "", fmt.Errorf("python3 not found in PATH")
	}

	cmd := exec.Command(pythonBin, scriptPath)
	cmd.Env = envToSlice(env)
	cmd.Dir = tmpDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start python: %w", err)
	}

	// RPC server: handle tool calls from the script
	toolCalls := 0
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleRPC(conn, toolHandler, &toolCalls, config.MaxToolCalls)
		}
	}()

	// Wait for subprocess with timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		cmd.Process.Kill() //nolint:errcheck
		return "", ctx.Err()
	case err := <-done:
		// Close listener so RPC goroutine exits
		listener.Close()
		wg.Wait()

		if err != nil {
			// Read stderr for error details
			var stderrBytes []byte
			if s, readErr := io.ReadAll(stderr); readErr == nil {
				stderrBytes = s
			}
			return "", fmt.Errorf("python exited: %v, stderr: %s", err, string(stderrBytes))
		}
	}

	// Collect stdout
	stdoutBytes, _ := io.ReadAll(stdout)
	if len(stdoutBytes) > 50*1024 {
		stdoutBytes = stdoutBytes[:50*1024]
	}
	return string(stdoutBytes), nil
}

func handleRPC(conn net.Conn, toolHandler func(string, map[string]any) string, toolCalls *int, maxCalls int) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}

		var req struct {
			Tool string         `json:"tool"`
			Args map[string]any `json:"args"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}

		*toolCalls++
		if *toolCalls > maxCalls {
			json.NewEncoder(conn).Encode("Tool call limit exceeded\n") //nolint:errcheck
			return
		}

		result := toolHandler(req.Tool, req.Args)
		// Send response with newline delimiter
		if _, err := conn.Write([]byte(result + "\n")); err != nil {
			return
		}
	}
}

// lookPath searches for a binary in PATH.
func lookPath(name string) (string, error) {
	if runtime.GOOS == "windows" {
		return exec.LookPath(name)
	}
	path := os.Getenv("PATH")
	if path == "" {
		return "", fmt.Errorf("PATH not set")
	}
	for _, dir := range filepath.SplitList(path) {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("not found")
}

// buildSandboxEnv creates a stripped environment for the subprocess.
func buildSandboxEnv(passthrough []string) map[string]string {
	var allowed = make(map[string]string)
	for _, k := range passthrough {
		if v := os.Getenv(k); v != "" {
			allowed[k] = v
		}
	}
	return allowed
}

// envToSlice converts a map to a []string suitable for os/exec Cmd.Env.
func envToSlice(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

// generateToolsStub creates the hermes_tools.py stub that the sandboxed
// Python script imports to call Hermes tools over the RPC socket.
func generateToolsStub(tools []string) string {
	var b strings.Builder
	b.WriteString(`"""Auto-generated Hermes tools RPC stubs."""
import json, os, socket, time

_sock = None

def _connect():
    global _sock
    if _sock is None:
        _sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        _sock.connect(os.environ["HERMES_RPC_SOCKET"])
        _sock.settimeout(300)
    return _sock

def _call(tool_name, args):
    """Send a tool call to the parent process and return the parsed result."""
    conn = _connect()
    request = json.dumps({"tool": tool_name, "args": args}) + "\n"
    conn.sendall(request.encode())
    buf = b""
    while True:
        chunk = conn.recv(65536)
        if not chunk:
            raise RuntimeError("Agent process disconnected")
        buf += chunk
        if buf.endswith(b"\n"):
            break
    raw = buf.decode().strip()
    result = json.loads(raw)
    if isinstance(result, str):
        try:
            return json.loads(result)
        except (json.JSONDecodeError, TypeError):
            return result
    return result

`)
	for _, tool := range tools {
		switch tool {
		case "web_search":
			b.WriteString(`
def web_search(query: str, limit: int = 5) -> dict:
    """Search the web. Returns dict with data.web list of {url, title, description}."""
    return _call("web_search", {"query": query, "limit": limit})
`)
		case "web_extract":
			b.WriteString(`
def web_extract(urls: list) -> dict:
    """Extract content from URLs. Returns dict with results list of {url, title, content, error}."""
    return _call("web_extract", {"urls": urls})
`)
		case "read_file":
			b.WriteString(`
def read_file(path: str, offset: int = 1, limit: int = 500) -> dict:
    """Read a text file. Returns dict with content, total_lines."""
    return _call("read_file", {"path": path, "offset": offset, "limit": limit})
`)
		case "write_file":
			b.WriteString(`
def write_file(path: str, content: str) -> dict:
    """Write content to a file. Returns dict with success status."""
    return _call("write_file", {"path": path, "content": content})
`)
		case "search_files":
			b.WriteString(`
def search_files(pattern: str, target: str = "content", path: str = ".") -> dict:
    """Search files by name or content. Returns dict with matches."""
    return _call("search_files", {"pattern": pattern, "target": target, "path": path})
`)
		case "patch":
			b.WriteString(`
def patch(path: str, old_string: str, new_string: str) -> dict:
    """Patch a file by replacing old_string with new_string."""
    return _call("patch", {"path": path, "old_string": old_string, "new_string": new_string})
`)
		case "terminal":
			b.WriteString(`
def terminal(command: str, timeout: int = 60) -> dict:
    """Execute a shell command. Returns dict with output, exit_code."""
    return _call("terminal", {"command": command, "timeout": timeout})
`)
		}
	}

	b.WriteString("\n")
	return b.String()
}
