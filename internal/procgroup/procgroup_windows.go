//go:build windows

package procgroup

import (
	"os/exec"
	"strconv"
	"syscall"
)

// createNewProcessGroup is Windows' CREATE_NEW_PROCESS_GROUP.
const createNewProcessGroup = 0x00000200

// Own makes cmd start in a console process group of its own, so the
// console's Ctrl-C does not reach it. It is called before cmd starts.
func Own(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// Kill stops at once the process pid and every process it started. Windows
// has no signal for a group, so taskkill, which every Windows has, walks
// the tree.
func Kill(pid int) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// Interrupt stops the process pid and what it started. A console program
// cannot be sent another console group's Ctrl-C, so it is stopped instead.
func Interrupt(pid int) error {
	return Kill(pid)
}
