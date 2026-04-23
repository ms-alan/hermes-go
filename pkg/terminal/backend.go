// Package terminal provides a multi-backend terminal execution engine.
//
// Supported backends:
//   - local:  exec.Command on the local machine (default)
//   - docker: docker exec into a running container
//   - ssh:    SSH to a remote host and execute
//
// All backends share the same interface: they execute a command and return
// stdout/stderr. The Manager routes terminal_tool calls to the appropriate
// backend based on the `backend` parameter.
package terminal

import (
	"context"
	"fmt"
	"io"
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
// Unlike Run, this captures full terminal output including ANSI escape codes.
// PTY mode is required for interactive programs like vim, nano, htop, python REPL, etc.
func (b *LocalBackend) RunPty(ctx context.Context, command string, timeout time.Duration) (string, string, int, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if timeout > 600*time.Second {
		timeout = 600 * time.Second
	}

	// Prepare environment — set TERM for proper terminal emulation
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

	// Use pty.Start which forks, sets up PTY as controlling terminal, and starts the process
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", "", -1, fmt.Errorf("failed to start PTY: %w", err)
	}
	defer ptmx.Close()

	// Create a context with deadline for reading
	if dl, ok := ctx.Deadline(); ok {
		ptmx.SetReadDeadline(dl)
	}

	// Read output from PTY master
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
				if err != io.EOF {
					// Log but don't fail on read errors
				}
				break
			}
		}
	}()

	// Wait for the process with timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		// Process finished — drain any remaining output
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
// Manager — routes terminal calls to the appropriate backend
// ---------------------------------------------------------------------------

type Manager struct {
	local   *LocalBackend
	dockers map[string]*DockerBackend // key: backend ID
	sshs    map[string]*SSHBackend    // key: backend ID
}

func NewManager() *Manager {
	return &Manager{
		local:   &LocalBackend{},
		dockers: make(map[string]*DockerBackend),
		sshs:    make(map[string]*SSHBackend),
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

// Run executes a command using the specified backend.
// backendID can be "local" (default), "docker:<id>", or "ssh:<id>".
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
