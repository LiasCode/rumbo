//go:build unix

package frontend

import (
	"os"

	"github.com/LiasCode/rumbo/protocol"
)

// Attached opens the front end's ends of the protocol, which the host
// gives it as file descriptors 3 and 4. Started by hand they are not open,
// and the front end reports that the host did not answer.
func Attached() (reader, writer *os.File, err error) {
	reader = os.NewFile(protocol.FrontendReadFD, "host-to-frontend")
	writer = os.NewFile(protocol.FrontendWriteFD, "frontend-to-host")

	return reader, writer, nil
}
