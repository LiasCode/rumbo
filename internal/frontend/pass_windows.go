//go:build windows

package frontend

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/LiasCode/rumbo/protocol"
)

// passProtocol gives cmd the front end's ends of the protocol: what it
// reads, then what it writes. Windows passes a program no file descriptor
// past stderr, so they are handles it inherits, named in its environment.
// Only the handles listed are inherited, so no other program gets them.
func passProtocol(cmd *exec.Cmd, reads, writes *os.File) error {
	handles := []syscall.Handle{syscall.Handle(reads.Fd()), syscall.Handle(writes.Fd())}

	for _, handle := range handles {
		if err := windows.SetHandleInformation(windows.Handle(handle), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return fmt.Errorf("passing the protocol to the front end: %w", err)
		}
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: handles}
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d,%d", protocol.FrontendHandlesEnv, handles[0], handles[1]))

	return nil
}
