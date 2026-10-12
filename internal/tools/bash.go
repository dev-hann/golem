package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// defaultBashTimeoutSecs bounds a single bash invocation unless the model
// requests a shorter one. See docs/CONFIG.md ([tools].bash_timeout_secs).
const defaultBashTimeoutSecs = 120

// BashTool runs a shell command line via bash -c in the workspace root,
// capturing stdout and stderr. The agent loop gates it behind the approval
// policy; the tool itself executes whatever it is given.
type BashTool struct {
	// Root is the working directory for the command.
	Root string
}

// bashArgs are the model-facing arguments of BashTool.
type bashArgs struct {
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_secs"`
}

// Name implements Tool.
func (t BashTool) Name() string { return "bash" }

// Description implements Tool.
func (BashTool) Description() string {
	return "Run a shell command with bash -c and return combined stdout/stderr."
}

// InputSchema implements Tool.
func (BashTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command line to execute.",
			},
			"timeout_secs": map[string]any{
				"type":        "integer",
				"description": "Optional wall-clock timeout in seconds.",
			},
		},
		"required": []string{"command"},
	}
}

// Execute implements Tool. The command runs in its own process group so that
// cancellation kills the whole tree, not just the shell.
func (t BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a bashArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bash: parsing args: %w", err)
	}

	if a.Command == "" {
		return "", fmt.Errorf("bash: missing command")
	}

	timeout := a.TimeoutSec
	if timeout <= 0 || timeout > defaultBashTimeoutSecs {
		timeout = defaultBashTimeoutSecs
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Model-supplied command execution is the tool's entire purpose.
	cmd := exec.CommandContext(runCtx, "bash", "-c", a.Command) //nolint:gosec // by design
	cmd.Dir = t.Root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var buf bytes.Buffer

	cmd.Stdout = &buf

	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("bash %q: %w", a.Command, err)
	}
	// CommandContext kills only the shell; kill the group to reap children.
	go func() {
		<-runCtx.Done()

		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()

	err := cmd.Wait()

	out := buf.String()
	if runCtx.Err() != nil {
		return out, fmt.Errorf("bash %q: %w", a.Command, runCtx.Err())
	}

	if err != nil {
		return out, fmt.Errorf("bash %q: %w", a.Command, err)
	}

	if out == "" {
		out = "(no output)"
	}

	return out, nil
}
