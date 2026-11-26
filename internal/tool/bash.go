package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/LiasCode/rumbo/internal/procgroup"
	"github.com/LiasCode/rumbo/protocol"
)

const (
	defaultTimeout = 2 * time.Minute
	maxTimeout     = 30 * time.Minute
)

// shellProgram is the shell the system has, which differs by operating
// system (shell_unix.go, shell_windows.go).
type shellProgram struct {
	// name is the tool's name, and the shell's.
	name string

	// argv runs a command given after it.
	argv []string

	// prefix goes before every command.
	prefix string

	// stopped says how to stop a background process, given its group.
	stopped string

	// crlf says the shell ends its lines with CR LF, which are given as LF.
	crlf bool
}

// lines gives output with its lines ended as the model reads them.
func (s shellProgram) lines(output string) string {
	if s.crlf {
		return strings.ReplaceAll(output, "\r\n", "\n")
	}

	return output
}

// command runs line with the shell.
func (s shellProgram) command(ctx context.Context, line string) *exec.Cmd {
	args := append(s.argv[1:len(s.argv):len(s.argv)], s.prefix+line)

	return exec.CommandContext(ctx, s.argv[0], args...)
}

// Bash runs a command in the working directory with the system's shell:
// bash on Unix, and PowerShell on Windows, where the tool is named after it.
type Bash struct {
	jobs  *jobs
	saved *saved
}

// Def implements Tool.
func (Bash) Def() protocol.ToolDef {
	return protocol.ToolDef{
		Name:        shell.name,
		Description: "Run a " + shell.name + " command in the working directory and return its combined output and exit status. Long output is shortened to its beginning and end, and saved whole to a file you can search. Default timeout is 120 seconds. For a server or anything else that keeps running, set background.",
		Schema: schema(`{
			"command": {"type": "string", "description": "Command to run"},
			"timeout": {"type": "integer", "description": "Timeout in seconds"},
			"background": {"type": "boolean", "description": "Start the command and return at once; its output goes to a file"}
		}`, "command"),
	}
}

// Run implements Tool.
func (b Bash) Run(ctx context.Context, dir string, args json.RawMessage) (Result, error) {
	output, err := b.run(ctx, dir, args)

	return text(output), err
}

func (b Bash) run(ctx context.Context, dir string, args json.RawMessage) (string, error) {
	var input struct {
		Command    string `json:"command"`
		Timeout    int    `json:"timeout"`
		Background bool   `json:"background"`
	}

	if err := decode(args, &input); err != nil {
		return "", err
	}

	if strings.TrimSpace(input.Command) == "" {
		return "", fmt.Errorf("command is required")
	}

	if input.Background {
		return b.start(dir, input.Command)
	}

	timeout := defaultTimeout

	if input.Timeout > 0 {
		timeout = min(time.Duration(input.Timeout)*time.Second, maxTimeout)
	}

	output, err := Shell(ctx, dir, input.Command, timeout)
	output = clip(strings.TrimRight(shell.lines(output), "\n"), b.saved)

	if err != nil {
		return "", fmt.Errorf("%s\n[%w]", output, err)
	}

	if output == "" {
		return "[no output]", nil
	}

	return output, nil
}

// settle is how long a background command is watched before it is left
// alone. One that fails at once, such as a mistyped name, is reported like
// any command instead of as started.
const settle = time.Second

// start runs command without waiting for it to end. It outlives the tool
// call and the run, and is stopped when the host exits.
func (b Bash) start(dir, command string) (string, error) {
	log, err := b.saved.create("rumbo-background-*.log")

	if err != nil {
		return "", err
	}

	defer log.Close()

	cmd := shell.command(context.Background(), command)
	cmd.Dir = dir
	cmd.Stdout = log
	cmd.Stderr = log
	procgroup.Own(cmd)

	if err := cmd.Start(); err != nil {
		return "", err
	}

	group := cmd.Process.Pid
	ended := make(chan error, 1)

	go func() {
		ended <- cmd.Wait()
	}()

	select {
	case err := <-ended:
		output, _ := os.ReadFile(log.Name())
		_ = os.Remove(log.Name())
		text := clip(strings.TrimRight(shell.lines(string(output)), "\n"), b.saved)

		if err != nil {
			return "", fmt.Errorf("%s\n[ended at once: %w]", text, err)
		}

		return text + "\n[ended at once, successfully]", nil

	case <-time.After(settle):
	}

	b.jobs.add(group)

	return fmt.Sprintf(
		"started in the background as process group %d. Its output is in %s. Stop it with: "+shell.stopped,
		group, log.Name(), group,
	), nil
}

// Shell runs command with the system's shell in dir and returns its combined output. The
// command gets its own process group so a timeout or cancel also stops the
// processes it started.
func Shell(ctx context.Context, dir, command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var output bytes.Buffer

	cmd := shell.command(ctx, command)
	cmd.Dir = dir
	cmd.Stdout = &output
	cmd.Stderr = &output
	procgroup.Own(cmd)
	cmd.WaitDelay = 2 * time.Second

	cmd.Cancel = func() error {
		return procgroup.Kill(cmd.Process.Pid)
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
