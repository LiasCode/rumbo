//go:build unix

package procgroup

import (
	"os/exec"
	"syscall"
)

// Own makes cmd start in a process group of its own, so the terminal's
// Ctrl-C does not reach it and Kill stops what it started too. It is called
// before cmd starts.
func Own(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Kill stops at once the group that the process pid leads.
func Kill(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// Interrupt sends the interrupt to the group that the process pid leads, as
// Ctrl-C would.
func Interrupt(pid int) error {
	return syscall.Kill(-pid, syscall.SIGINT)
}
