// Package rumbo is the plugin that comes with rumbo: the providers
// anthropic, openai, and chatgpt, the terminal front end tui, and the
// extension for the servers of the Model Context Protocol. plugin.yaml says
// what it brings, and the host reads it as it reads the manifest of any
// plugin a person adds. Its code is built into the same executable, which
// serves each of its programs through a command of its own: rumbo provide,
// attach, and extend.
//
// This file is all the host reaches of it. What is under internal is the
// plugin's, and speaks to the host only through the protocols.
package rumbo

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/LiasCode/rumbo/plugins/rumbo/internal/extensions/mcp"
	"github.com/LiasCode/rumbo/plugins/rumbo/internal/frontends/tui"
	"github.com/LiasCode/rumbo/plugins/rumbo/internal/providers/anthropic"
	"github.com/LiasCode/rumbo/plugins/rumbo/internal/providers/chatgpt"
	"github.com/LiasCode/rumbo/plugins/rumbo/internal/providers/openai"
	"github.com/LiasCode/rumbo/protocol"
)

// Manifest is the plugin's plugin.yaml, as it is in this directory.
//
//go:embed plugin.yaml
var Manifest []byte

// Provide serves the provider called name on stdin and stdout.
func Provide(name string) error {
	var handler protocol.Handler

	switch name {
	case "anthropic":
		handler = anthropic.New()
	case "openai":
		handler = openai.New()
	case "chatgpt":
		handler = chatgpt.New()
	default:
		return fmt.Errorf("rumbo's plugin has no provider called %q; it has anthropic, openai, and chatgpt", name)
	}

	return protocol.Serve(context.Background(), os.Stdin, os.Stdout, handler)
}

// Attach runs the front end called name on its connection to the host, and
// returns the front end's exit code.
func Attach(name string, reader io.Reader, writer io.Writer) (int, error) {
	if name != "tui" {
		return 0, fmt.Errorf("rumbo's plugin has no front end called %q; it has tui", name)
	}

	return tui.Run(reader, writer), nil
}

// Extend serves the extension called name on stdin and stdout. mcp is the
// only one: it gives the model the tools of the MCP server that command
// starts, or of the one at the url of its configuration. version is the
// rumbo it says it is to the server.
func Extend(name string, command []string, version string) error {
	if name != "mcp" {
		return fmt.Errorf("rumbo's plugin has no extension called %q; it has mcp", name)
	}

	adapter := mcp.New(command, version, os.Stderr)
	defer adapter.Close()

	served := make(chan error, 1)

	go func() {
		served <- protocol.ServeExtension(context.Background(), os.Stdin, os.Stdout, adapter)
	}()

	select {
	case err := <-served:
		return err

	// Without its server it can answer nothing; the host starts it again,
	// and the server with it, for the next call.
	case <-adapter.Lost():
		return errors.New("the MCP server exited")
	}
}
