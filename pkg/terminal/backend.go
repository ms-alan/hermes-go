// Package terminal provides a multi-backend terminal execution engine.
//
// Supported backends:
//   - local:     exec.Command on the local machine (default)
//   - docker:<id>: docker exec into a running container
//   - ssh:<id>:  SSH to a remote host and execute
//   - singularity:<id>: singularity/apptainer exec into a SIF container
//   - modal:<id>: Modal cloud sandbox execution
//   - daytona:<id>: Daytona cloud sandbox execution
//
// All backends share the same interface: they execute a command and return
// stdout/stderr. The Manager routes terminal_tool calls to the appropriate
// backend based on the `backend` parameter.
package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Backend is the interface for terminal backends.
type Backend interface {
	// Run executes a command and returns stdout, stderr, and an exit code.
	// The ctx deadline is respected; if cancelled, the command is terminated.
	Run(ctx context.Context, command string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)

	// Name returns the backend name (e.g., "local", "docker", "ssh").
	Name() string
}

// ---------------------------------------------------------------------------
// Local backend — exec.Command on the local machine
// ---------------------------------------------------------------------------

type LocalBackend struct{}

func (b *LocalBackend) Name() string { return "local" }

func (b *LocalBackend) Run(ctx context.Context, command string, timeout time.Duration) (string, string, int, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if timeout > 600*time.Second {
		timeout = 600 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if strings.Contains(command, "&&") || strings.Contains(command, "||") || strings.Contains(command, ";") {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	} else {
		parts := strings.Fields(command)
		if len(parts) == 0 {
			return "", "", -1, fmt.Errorf("empty command")
		}
		cmd = exec.CommandContext(ctx, parts[0], parts[1:]...)
	}

	cmd.Env = append(cmd.Env, os.Environ()...)
	out, err := cmd.Output()
	errOut := ""
	if exitErr, ok := err.(*exec.ExitError); ok {
		errOut = string(exitErr.Stderr)
		if exitErr.ExitCode() >= 0 {
			return string(out), errOut, exitErr.ExitCode(), nil
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), errOut, -1, fmt.Errorf("command timed out after %v", timeout)
	}
	if err != nil {
		return string(out), errOut, -1, err
	}
	return string(out), errOut, 0, nil
}

// RunPty executes a command in a pseudo-terminal (PTY) for interactive CLI tools.
func (b *LocalBackend) RunPty(ctx context.Context, command string, timeout time.Duration) (string, string, int, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if timeout > 600*time.Second {
		timeout = 600 * time.Second
	}

	env := os.Environ()
	env = append(env, "TERM=xterm-256color")

	var args []string
	if strings.Contains(command, "&&") || strings.Contains(command, "||") || strings.Contains(command, ";") {
		args = []string{"sh", "-c", command}
	} else {
		parts := strings.Fields(command)
		if len(parts) == 0 {
			return "", "", -1, fmt.Errorf("empty command")
		}
		args = parts
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = env

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to start PTY: %w", err)
	}
	defer ptmx.Close()

	if dl, ok := ctx.Deadline(); ok {
		ptmx.SetReadDeadline(dl)
	}

	var wg sync.WaitGroup
	var stdout []byte
	var mu sync.Mutex

	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				stdout = append(stdout, buf[:n]...)
				mu.Unlock()
			}
			if err != nil {
				break
			}
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		ptmx.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		wg.Wait()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
				if exitCode < 0 {
					exitCode = -1
				}
			} else {
				exitCode = -1
			}
		}
		return string(stdout), "", exitCode, nil
	case <-ctx.Done():
		cmd.Process.Kill()
		cmd.Wait()
		wg.Wait()
		return string(stdout), "", -1, fmt.Errorf("command timed out after %v", timeout)
	}
}

// ---------------------------------------------------------------------------
// Docker backend — docker exec into a container
// ---------------------------------------------------------------------------

type DockerBackend struct {
	ContainerID string // required: container ID or name
	User        string // optional: run as user (e.g., "root")
	Workdir     string // optional: working directory inside container
}

func (b *DockerBackend) Name() string { return "docker" }

func (b *DockerBackend) Run(ctx context.Context, command string, timeout time.Duration) (string, string, int, error) {
	if b.ContainerID == "" {
		return "", "", -1, fmt.Errorf("docker backend requires container_id")
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	args := []string{"exec", "-i"}
	if b.User != "" {
		args = append(args, "-u", b.User)
	}
	if b.Workdir != "" {
		args = append(args, "-w", b.Workdir)
	}
	args = append(args, b.ContainerID, "sh", "-c", command)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(cmd.Env, "DOCKER_HOST="+getEnvOrDefault("DOCKER_HOST", ""))
	out, err := cmd.CombinedOutput()

	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode := exitErr.ExitCode()
		if exitCode >= 0 {
			return string(out), "", exitCode, nil
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), "", -1, fmt.Errorf("docker exec timed out after %v", timeout)
	}
	if err != nil {
		if strings.Contains(string(out), "command not found") || strings.Contains(string(out), "'docker' not found") {
			return "", "", -1, fmt.Errorf("docker not found in PATH")
		}
		return string(out), "", -1, err
	}
	return string(out), "", 0, nil
}

// ---------------------------------------------------------------------------
// SSH backend — SSH to a remote host
// ---------------------------------------------------------------------------

type SSHBackend struct {
	Host     string // required: user@hostname or hostname
	Port     int    // optional: SSH port (default 22)
	KeyFile  string // optional: path to private key
	Password string // optional: password (use KeyFile preferably)
	User     string // optional: override user part of Host
	Workdir  string // optional: working directory
}

func (b *SSHBackend) Name() string { return "ssh" }

func (b *SSHBackend) Run(ctx context.Context, command string, timeout time.Duration) (string, string, int, error) {
	if b.Host == "" {
		return "", "", -1, fmt.Errorf("ssh backend requires host")
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	sshArgs := []string{}
	if b.KeyFile != "" {
		sshArgs = append(sshArgs, "-i", b.KeyFile)
	}
	if b.Port > 0 && b.Port != 22 {
		sshArgs = append(sshArgs, "-p", fmt.Sprintf("%d", b.Port))
	}
	sshArgs = append(sshArgs, "-o", "StrictHostKeyChecking=no")
	sshArgs = append(sshArgs, "-o", "BatchMode=yes")

	host := b.Host
	if b.User != "" && !strings.Contains(host, "@") {
		host = b.User + "@" + host
	}

	remoteCmd := command
	if b.Workdir != "" {
		remoteCmd = "cd " + b.Workdir + " && " + command
	}

	fullArgs := append(sshArgs, host, remoteCmd)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh", fullArgs...)
	out, err := cmd.CombinedOutput()

	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode := exitErr.ExitCode()
		return string(out), "", exitCode, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), "", -1, fmt.Errorf("ssh command timed out after %v", timeout)
	}
	if err != nil {
		return string(out), "", -1, err
	}
	return string(out), "", 0, nil
}

// ---------------------------------------------------------------------------
// Singularity backend — singularity/apptainer exec into a SIF container
// ---------------------------------------------------------------------------

type SingularityBackend struct {
	Image    string // required: SIF image path or docker/OCI image reference
	User     string // optional: run as user (e.g., "root")
	Workdir  string // optional: working directory inside container
	Overlay  string // optional: path to overlay image for persistence
	Scratch  string // optional: scratch directory path
	Bind     string // optional: host:container bind mounts (comma-separated)
}

func (b *SingularityBackend) Name() string { return "singularity" }

// findSingularityExecutable locates the apptainer or singularity binary.
func findSingularityExecutable() (string, error) {
	exe, err := exec.LookPath("apptainer")
	if err == nil {
		return exe, nil
	}
	exe, err = exec.LookPath("singularity")
	if err == nil {
		return exe, nil
	}
	return "", fmt.Errorf("neither 'apptainer' nor 'singularity' found in PATH")
}

func (b *SingularityBackend) Run(ctx context.Context, command string, timeout time.Duration) (string, string, int, error) {
	if b.Image == "" {
		return "", "", -1, fmt.Errorf("singularity backend requires image")
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	exe, err := findSingularityExecutable()
	if err != nil {
		return "", "", -1, err
	}

	args := []string{"exec"}
	if b.User != "" {
		args = append(args, "--user", b.User)
	}
	if b.Workdir != "" {
		args = append(args, "--pwd", b.Workdir)
	}
	if b.Overlay != "" {
		args = append(args, "--overlay", b.Overlay)
	}
	if b.Scratch != "" {
		args = append(args, "--scratch", b.Scratch)
	}
	if b.Bind != "" {
		for _, bind := range strings.Split(b.Bind, ",") {
			if bind = strings.TrimSpace(bind); bind != "" {
				args = append(args, "--bind", bind)
			}
		}
	}
	// Security-hardened flags matching hermes-agent
	args = append(args, "--containall", "--no-home")
	args = append(args, b.Image, "sh", "-c", command)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()

	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode := exitErr.ExitCode()
		return string(out), "", exitCode, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), "", -1, fmt.Errorf("singularity exec timed out after %v", timeout)
	}
	if err != nil {
		return string(out), "", -1, err
	}
	return string(out), "", 0, nil
}

// ---------------------------------------------------------------------------
// Modal backend — Modal cloud sandbox execution via REST API
// ---------------------------------------------------------------------------

// ModalBackend executes commands in Modal cloud sandboxes.
// Requires MODAL_API_TOKEN and MODAL_WORKSPACE env vars (or MODAL_TOKEN_ID + MODAL_TOKEN_SECRET).
type ModalBackend struct {
	Workspace   string // Modal workspace name (or from MODAL_WORKSPACE env)
	Image       string // Modal image tag or S3 path
	CPU         int    // number of CPUs (default 1)
	MemoryMB    int    // memory in MB (default 512)
	TimeoutSec  int    // sandbox timeout (default 300)
	Cloud       string // "modal" or "aws" or "gcp" (default "modal")
}

func (b *ModalBackend) Name() string { return "modal" }

func (b *ModalBackend) Run(ctx context.Context, command string, timeout time.Duration) (string, string, int, error) {
	apiToken := os.Getenv("MODAL_API_TOKEN")
	if apiToken == "" {
		tokenID := os.Getenv("MODAL_TOKEN_ID")
		tokenSecret := os.Getenv("MODAL_TOKEN_SECRET")
		if tokenID != "" && tokenSecret != "" {
			apiToken = tokenID + ":" + tokenSecret
		}
	}
	if apiToken == "" {
		return "", "", -1, fmt.Errorf("Modal backend requires MODAL_API_TOKEN or MODAL_TOKEN_ID+MODAL_TOKEN_SECRET environment variables")
	}

	workspace := b.Workspace
	if workspace == "" {
		workspace = os.Getenv("MODAL_WORKSPACE")
	}
	if workspace == "" {
		return "", "", -1, fmt.Errorf("Modal backend requires workspace name (set MODAL_WORKSPACE or pass Workspace field)")
	}

	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if timeout > 600*time.Second {
		timeout = 600 * time.Second
	}

	timeoutSec := b.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 300
	}
	if int(timeout.Seconds()) < timeoutSec {
		timeoutSec = int(timeout.Seconds())
	}

	cpu := b.CPU
	if cpu <= 0 {
		cpu = 1
	}
	mem := b.MemoryMB
	if mem <= 0 {
		mem = 512
	}

	// Create a Modal sandbox via REST API
	cloud := b.Cloud
	if cloud == "" {
		cloud = "modal"
	}

	createPayload := map[string]any{
		"image":     b.Image,
		"timeout":  timeoutSec,
		"resources": map[string]any{
			"cpu": cpu,
			"memory_mb": mem,
		},
		"cloud": cloud,
	}

	createBody, err := json.Marshal(createPayload)
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to marshal create request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	createReq, err := http.NewRequestWithContext(reqCtx, "POST",
		"https://api.modal.com/api/v2/sandboxes", bytes.NewReader(createBody))
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to create request: %w", err)
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.SetBasicAuth(workspace, apiToken)

	resp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		return "", "", -1, fmt.Errorf("Modal API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", "", -1, fmt.Errorf("Modal create returned status %d: %s", resp.StatusCode, string(body))
	}

	var sandboxResp struct {
		ID   string `json:"id"`
		IP   string `json:"ip"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &sandboxResp); err != nil {
		return "", "", -1, fmt.Errorf("failed to parse Modal response: %w", err)
	}

	sandboxID := sandboxResp.ID
	if sandboxID == "" {
		return "", "", -1, fmt.Errorf("Modal sandbox created but no ID returned")
	}

	// Execute command in the sandbox
	execPayload := map[string]any{
		"command": []string{"sh", "-c", command},
	}
	execBody, err := json.Marshal(execPayload)
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to marshal exec request: %w", err)
	}

	execReqCtx, execReqCancel := context.WithTimeout(ctx, 15*time.Second)
	defer execReqCancel()

	execReq, err := http.NewRequestWithContext(execReqCtx, "POST",
		fmt.Sprintf("https://api.modal.com/api/v2/sandboxes/%s/exec", sandboxID),
		bytes.NewReader(execBody))
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to create exec request: %w", err)
	}
	execReq.Header.Set("Content-Type", "application/json")
	execReq.SetBasicAuth(workspace, apiToken)

	execResp, err := http.DefaultClient.Do(execReq)
	if err != nil {
		return "", "", -1, fmt.Errorf("Modal exec request failed: %w", err)
	}
	defer execResp.Body.Close()

	execBodyOut, err := io.ReadAll(execResp.Body)
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to read exec response: %w", err)
	}

	if execResp.StatusCode != http.StatusOK {
		return "", "", -1, fmt.Errorf("Modal exec returned status %d: %s", execResp.StatusCode, string(execBodyOut))
	}

	var execResult struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal(execBodyOut, &execResult); err != nil {
		// Try alternate field names
		var altResult struct {
			Output     string `json:"output"`
			ExitCode   int    `json:"exitCode"`
		}
		if uerr := json.Unmarshal(execBodyOut, &altResult); uerr == nil {
			return altResult.Output, "", altResult.ExitCode, nil
		}
		return "", "", -1, fmt.Errorf("failed to parse exec response: %w", err)
	}

	return execResult.Stdout, execResult.Stderr, execResult.ExitCode, nil
}

// ---------------------------------------------------------------------------
// Daytona backend — Daytona cloud sandbox execution via REST API
// ---------------------------------------------------------------------------

// DaytonaBackend executes commands in Daytona cloud sandboxes.
// Requires DAYTONA_API_KEY and DAYTONA_TARGET (workspace URL) env vars.
type DaytonaBackend struct {
	Target     string // Daytona target URL (or from DAYTONA_TARGET env)
	Image      string // container image to use
	CPU        int    // number of CPUs (default 1)
	MemoryMB   int    // memory in MB (default 2048)
	DiskMB     int    // disk in MB (default 10240)
	Persistent bool   // keep sandbox alive between calls (default true)
}

func (b *DaytonaBackend) Name() string { return "daytona" }

func (b *DaytonaBackend) Run(ctx context.Context, command string, timeout time.Duration) (string, string, int, error) {
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		return "", "", -1, fmt.Errorf("Daytona backend requires DAYTONA_API_KEY environment variable")
	}

	target := b.Target
	if target == "" {
		target = os.Getenv("DAYTONA_TARGET")
	}
	if target == "" {
		return "", "", -1, fmt.Errorf("Daytona backend requires target URL (set DAYTONA_TARGET or pass Target field)")
	}
	target = strings.TrimSuffix(target, "/")

	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if timeout > 600*time.Second {
		timeout = 600 * time.Second
	}

	cpu := b.CPU
	if cpu <= 0 {
		cpu = 1
	}
	mem := b.MemoryMB
	if mem <= 0 {
		mem = 2048
	}
	disk := b.DiskMB
	if disk <= 0 {
		disk = 10240
	}

	// Create a Daytona sandbox
	createPayload := map[string]any{
		"image": b.Image,
		"resources": map[string]any{
			"cpu":    cpu,
			"memory": mem,
			"disk":   disk,
		},
		"persistent": b.Persistent,
	}
	createBody, err := json.Marshal(createPayload)
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to marshal create request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	createReq, err := http.NewRequestWithContext(reqCtx, "POST",
		target+"/api/v1/sandboxes", bytes.NewReader(createBody))
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to create request: %w", err)
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("x-api-key", apiKey)

	resp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		return "", "", -1, fmt.Errorf("Daytona API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", "", -1, fmt.Errorf("Daytona create returned status %d: %s", resp.StatusCode, string(body))
	}

	var sandboxResp struct {
		ID   string `json:"id"`
		IP   string `json:"ip"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &sandboxResp); err != nil {
		return "", "", -1, fmt.Errorf("failed to parse Daytona response: %w", err)
	}

	sandboxID := sandboxResp.ID
	if sandboxID == "" {
		return "", "", -1, fmt.Errorf("Daytona sandbox created but no ID returned")
	}

	// Execute command in the sandbox
	execPayload := map[string]any{
		"command": command,
	}
	execBody, err := json.Marshal(execPayload)
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to marshal exec request: %w", err)
	}

	execReqCtx, execReqCancel := context.WithTimeout(ctx, timeout)
	defer execReqCancel()

	execReq, err := http.NewRequestWithContext(execReqCtx, "POST",
		fmt.Sprintf("%s/api/v1/sandboxes/%s/exec", target, sandboxID),
		bytes.NewReader(execBody))
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to create exec request: %w", err)
	}
	execReq.Header.Set("Content-Type", "application/json")
	execReq.Header.Set("x-api-key", apiKey)

	execResp, err := http.DefaultClient.Do(execReq)
	if err != nil {
		return "", "", -1, fmt.Errorf("Daytona exec request failed: %w", err)
	}
	defer execResp.Body.Close()

	execBodyOut, err := io.ReadAll(execResp.Body)
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to read exec response: %w", err)
	}

	if execResp.StatusCode != http.StatusOK {
		return "", "", -1, fmt.Errorf("Daytona exec returned status %d: %s", execResp.StatusCode, string(execBodyOut))
	}

	var execResult struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exitCode"`
	}
	if err := json.Unmarshal(execBodyOut, &execResult); err != nil {
		return "", "", -1, fmt.Errorf("failed to parse exec response: %w", err)
	}

	return execResult.Output, "", execResult.ExitCode, nil
}

// ---------------------------------------------------------------------------
// Manager — routes terminal calls to the appropriate backend
// ---------------------------------------------------------------------------

type Manager struct {
	local      *LocalBackend
	dockers    map[string]*DockerBackend
	sshs       map[string]*SSHBackend
	singularity map[string]*SingularityBackend
	modal      map[string]*ModalBackend
	daytona    map[string]*DaytonaBackend
}

func NewManager() *Manager {
	return &Manager{
		local:      &LocalBackend{},
		dockers:    make(map[string]*DockerBackend),
		sshs:       make(map[string]*SSHBackend),
		singularity: make(map[string]*SingularityBackend),
		modal:      make(map[string]*ModalBackend),
		daytona:    make(map[string]*DaytonaBackend),
	}
}

// RegisterDocker registers a named docker backend.
func (m *Manager) RegisterDocker(id string, cfg DockerBackend) {
	m.dockers[id] = &cfg
}

// RegisterSSH registers a named SSH backend.
func (m *Manager) RegisterSSH(id string, cfg SSHBackend) {
	m.sshs[id] = &cfg
}

// RegisterSingularity registers a named singularity backend.
func (m *Manager) RegisterSingularity(id string, cfg SingularityBackend) {
	m.singularity[id] = &cfg
}

// RegisterModal registers a named modal backend.
func (m *Manager) RegisterModal(id string, cfg ModalBackend) {
	m.modal[id] = &cfg
}

// RegisterDaytona registers a named daytona backend.
func (m *Manager) RegisterDaytona(id string, cfg DaytonaBackend) {
	m.daytona[id] = &cfg
}

// Run executes a command using the specified backend.
// backendID can be "local" (default), "docker:<id>", "ssh:<id>",
// "singularity:<id>", "modal:<id>", or "daytona:<id>".
// When pty=true and backend is "local", runs in PTY mode for interactive CLI tools.
func (m *Manager) Run(ctx context.Context, backendID, command string, timeout time.Duration, pty bool) (string, string, int, error) {
	var backend Backend
	var err error

	switch {
	case backendID == "" || backendID == "local":
		if pty {
			return m.local.RunPty(ctx, command, timeout)
		}
		backend = m.local

	case strings.HasPrefix(backendID, "docker:"):
		name := strings.TrimPrefix(backendID, "docker:")
		if b, ok := m.dockers[name]; ok {
			backend = b
		} else {
			err = fmt.Errorf("unknown docker backend: %s", name)
		}

	case strings.HasPrefix(backendID, "ssh:"):
		name := strings.TrimPrefix(backendID, "ssh:")
		if b, ok := m.sshs[name]; ok {
			backend = b
		} else {
			err = fmt.Errorf("unknown ssh backend: %s", name)
		}

	case strings.HasPrefix(backendID, "singularity:"):
		name := strings.TrimPrefix(backendID, "singularity:")
		if b, ok := m.singularity[name]; ok {
			backend = b
		} else {
			err = fmt.Errorf("unknown singularity backend: %s", name)
		}

	case strings.HasPrefix(backendID, "modal:"):
		name := strings.TrimPrefix(backendID, "modal:")
		if b, ok := m.modal[name]; ok {
			backend = b
		} else {
			err = fmt.Errorf("unknown modal backend: %s", name)
		}

	case strings.HasPrefix(backendID, "daytona:"):
		name := strings.TrimPrefix(backendID, "daytona:")
		if b, ok := m.daytona[name]; ok {
			backend = b
		} else {
			err = fmt.Errorf("unknown daytona backend: %s", name)
		}

	default:
		// Try to parse as host — use SSH
		backend = &SSHBackend{Host: backendID}
	}

	if err != nil {
		return "", "", -1, err
	}
	if backend == nil {
		backend = m.local
	}

	return backend.Run(ctx, command, timeout)
}

// getEnvOrDefault returns the environment variable or a default value.
func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
