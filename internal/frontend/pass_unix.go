//go:build unix

package frontend

import (
	"os"
	"os/exec"
)

// passProtocol gives cmd the front end's ends of the protocol: what it
// reads, then what it writes, as file descriptors 3 and 4.
func passProtocol(cmd *exec.Cmd, reads, writes *os.File) error {
	// ExtraFiles start at descriptor 3, in order.
	cmd.ExtraFiles = []*os.File{reads, writes}

	return nil
}
