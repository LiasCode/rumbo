package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"rumbo/protocol"
)

const (
	defaultTimeout = 2 * time.Minute
	maxTimeout     = 30 * time.Minute
)

// Bash runs a shell command in the working directory.
type Bash struct{}

// Def implements Tool.
func (Bash) Def() protocol.ToolDef {
	return protocol.ToolDef{
		Name:        "bash",
		Description: "Run a bash command in the working directory and return its combined output and exit status. Long output is cut to its end. Default timeout is 120 seconds.",
		Schema: schema(`{
			"command": {"type": "string", "description": "Command to run"},
			"timeout": {"type": "integer", "description": "Timeout in seconds"}
		}`, "command"),
	}
}

// Mutates implements Tool.
func (Bash) Mutates() bool {
	return true
}

// Run implements Tool.
func (Bash) Run(ctx context.Context, dir string, args json.RawMessage) (string, error) {
	var input struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}

	if err := decode(args, &input); err != nil {
		return "", err
	}

	if strings.TrimSpace(input.Command) == "" {
		return "", fmt.Errorf("command is required")
	}

	timeout := defaultTimeout

	if input.Timeout > 0 {
		timeout = min(time.Duration(input.Timeout)*time.Second, maxTimeout)
	}

	output, err := Shell(ctx, dir, input.Command, timeout)
	output = tail(strings.TrimRight(output, "\n"))

	if err != nil {
		return "", fmt.Errorf("%s\n[%w]", output, err)
	}

	if output == "" {
		return "[no output]", nil
	}

	return output, nil
}

// Shell runs command with bash in dir and returns its combined output. The
// command gets its own process group so a timeout or cancel also stops the
// processes it started.
func Shell(ctx context.Context, dir, command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var output bytes.Buffer

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = dir
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second

	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	err := cmd.Run()

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return output.String(), fmt.Errorf("timed out after %s", timeout)
	}

	if ctx.Err() != nil {
		return output.String(), ctx.Err()
	}

	var exit *exec.ExitError

	if errors.As(err, &exit) {
		return output.String(), fmt.Errorf("exit status %d", exit.ExitCode())
	}

	return output.String(), err
}
