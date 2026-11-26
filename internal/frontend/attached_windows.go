//go:build windows

package frontend

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// Attached opens the front end's ends of the protocol, which the host
// gives it on Windows as handles its environment names. The variable is
// taken away once read, so a program the front end starts does not take
// them for its own.
func Attached() (reader, writer *os.File, err error) {
	named := os.Getenv(protocol.FrontendHandlesEnv)
	_ = os.Unsetenv(protocol.FrontendHandlesEnv)

	if named == "" {
		return nil, nil, errors.New("rumbo attach is for rumbo to start a front end; run rumbo -f <name> instead")
	}

	read, write, found := strings.Cut(named, ",")
	readHandle, readErr := strconv.ParseUint(read, 10, 64)
	writeHandle, writeErr := strconv.ParseUint(write, 10, 64)

	if !found || readErr != nil || writeErr != nil {
		return nil, nil, fmt.Errorf("%s=%q does not name two handles", protocol.FrontendHandlesEnv, named)
	}

	reader = os.NewFile(uintptr(readHandle), "host-to-frontend")
	writer = os.NewFile(uintptr(writeHandle), "frontend-to-host")

	return reader, writer, nil
}
