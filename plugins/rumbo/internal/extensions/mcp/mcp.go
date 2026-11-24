// Package mcp is rumbo's plugin's extension for the Model Context Protocol:
// it gives the model the tools of one MCP server, which it starts as a
// child process or reaches over HTTP. The host does not know MCP; this
// adapter speaks it on one side and the Agent Extension Protocol on the
// other, like an extension a user writes.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// protocolVersion is the MCP revision asked for. A server answers with the
// one it speaks, and its tools are listed and called the same way in all of
// them.
const protocolVersion = "2025-06-18"

// config is what the user chose for one server.
type config struct {
	// URL is where a server is reached over HTTP, for one that is not
	// started with a command.
	URL string `json:"url"`
	// TokenEnv names the environment variable holding the token such a
	// server takes, so that the token is in no file of rumbo's.
	TokenEnv string `json:"token_env"`
	// Prefix goes before the name of each tool, to tell them from the tools
	// of another server.
	Prefix string `json:"prefix"`
	// Tools are the names of the server's tools the model is given,
	// separated by commas; all of them when it is empty. Every tool is
	// declared with every request, and a server may have dozens.
	Tools string `json:"tools"`
	// ClientID is the client the user registered by hand with the
	// authorization server of a server at a URL, for one that registers no
	// clients by itself.
	ClientID string `json:"client_id"`
}

// Adapter is the extension. It implements protocol.ExtensionHandler.
type Adapter struct {
	command []string
	version string
	stderr  io.Writer

	conn conn
	// names are the server's names of the tools declared, by the names they
	// were declared with.
	names map[string]string
	gone  chan struct{}

	// name is what the user registered the extension as, which its commands
	// are named after. signing signs in to a server at a URL that takes no
	// token from the environment, and hint is what the server said of where
	// to, when it refused for lack of a sign-in.
	name    string
	signing *signIn
	hint    string
	// signed is whether someone was signed in when the adapter started.
	signed bool
	// changes tells the host that what was declared is no longer so.
	changes chan struct{}
}

// New returns the adapter for the server command starts, or, without a
// command, for the one its configuration gives the address of. version is
// rumbo's, which the server is told, and the server's stderr goes to stderr.
func New(command []string, version string, stderr io.Writer) *Adapter {
	return &Adapter{command: command, version: version, stderr: stderr, gone: make(chan struct{}), changes: make(chan struct{}, 1)}
}

// Changes implements protocol.Changer: signing in gives the adapter tools
// to declare, and signing out takes them away.
func (a *Adapter) Changes() <-chan struct{} {
	return a.changes
}

// changed tells the host to start the adapter again.
func (a *Adapter) changed() {
	select {
	case a.changes <- struct{}{}:
	default:
	}
}

// Lost is closed once a server that was started has exited, after which no
// call can be answered.
func (a *Adapter) Lost() <-chan struct{} {
	return a.gone
}

// Close ends the connection, and the server when the adapter started it.
func (a *Adapter) Close() {
	if a.conn != nil {
		a.conn.close()
	}
}

// Initialize implements protocol.ExtensionHandler: it connects to the
// server and declares its tools.
func (a *Adapter) Initialize(ctx context.Context, params protocol.InitializeParams) (protocol.ExtensionInitializeResult, error) {
	chosen, err := a.read(params.Config)

	if err != nil {
		return protocol.ExtensionInitializeResult{}, err
	}

	connected, err := a.connect(chosen)

	if err != nil {
		return protocol.ExtensionInitializeResult{}, err
	}

	a.conn, a.name = connected, params.Name

	// A sign-in that ended while nobody looked is said, and then the server
	// is asked as by someone who never signed in.
	if a.signing != nil {
		a.signed = a.signing.signedIn()

		if _, err := a.signing.token(ctx); err != nil && !a.signing.signedIn() {
			fmt.Fprintf(a.stderr, "the sign-in to %s is over: %v\n", chosen.URL, err)
			a.signed = false
		}
	}

	name, err := a.handshake(ctx)

	// A server that wants a sign-in gives no tools until there is one: what
	// the adapter declares meanwhile is the command that signs in.
	refusal, refused := errors.AsType[*unauthorized](err)

	if refused && a.signing != nil {
		a.hint = refusal.hint
		fmt.Fprintf(a.stderr, "not signed in to %s, so it gives no tools yet; %s signs in\n", chosen.URL, a.commandFor("login"))

		return protocol.ExtensionInitializeResult{Protocol: protocol.ExtensionVersion, Name: params.Name, Functions: []protocol.Function{
			{Name: "login", Description: "Sign in to " + chosen.URL + " in your browser"},
		}}, nil
	}

	if refused {
		return protocol.ExtensionInitializeResult{}, fmt.Errorf("the MCP server did not initialize: it did not take the token %s holds", chosen.TokenEnv)
	}

	if err != nil {
		return protocol.ExtensionInitializeResult{}, fmt.Errorf("the MCP server did not initialize: %w", err)
	}

	offered, err := a.list(ctx)

	if err != nil {
		return protocol.ExtensionInitializeResult{}, fmt.Errorf("the MCP server did not list its tools: %w", err)
	}

	tools, err := a.declare(offered, chosen)

	if err != nil {
		return protocol.ExtensionInitializeResult{}, err
	}

	if lost := a.conn.lost(); lost != nil {
		go func() {
			<-lost
			close(a.gone)
		}()
	}

	result := protocol.ExtensionInitializeResult{Protocol: protocol.ExtensionVersion, Name: name, Tools: tools}

	if a.signed {
		result.Functions = []protocol.Function{{Name: "logout", Description: "Forget the sign-in to " + chosen.URL}}
	}

	return result, nil
}

// commandFor is how a function of the adapter is called in a front end:
// named after what the user registered the extension as.
func (a *Adapter) commandFor(function string) string {
	if a.name == "" {
		return "its " + function + " command"
	}

	return "/" + a.name + "-" + function
}

// InvokeDialog implements protocol.DialogInvoker: login signs the person
// in to the server, in their browser, and logout forgets the sign-in. Both
// change what the adapter declares, so the host starts it again.
func (a *Adapter) InvokeDialog(ctx context.Context, name string, _ json.RawMessage, dialog protocol.Dialog) (any, error) {
	if a.signing == nil {
		return nil, errors.New("this server takes no sign-in")
	}

	switch name {
	case "login":
		if err := a.signing.login(ctx, a.hint, dialog); err != nil {
			return nil, err
		}

		a.changed()

		return "signed in to " + a.signing.server + "; its tools are the model's from the next run", nil

	case "logout":
		was, err := a.signing.logout()

		if err != nil {
			return nil, err
		}

		a.changed()

		if !was {
			return "nobody was signed in to " + a.signing.server, nil
		}

		return "signed out of " + a.signing.server + "; its tools are gone from the next run", nil
	}

	return nil, fmt.Errorf("there is no function %q", name)
}

// read reads the configuration, refusing a field there is none of, such as
// a misspelled one, and a server given both ways or neither.
func (a *Adapter) read(raw json.RawMessage) (config, error) {
	var chosen config

	if len(raw) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&chosen); err != nil {
			return chosen, fmt.Errorf("the configuration: %w; its fields are url, token_env, client_id, prefix, and tools", err)
		}
	}

	switch {
	case len(a.command) > 0 && chosen.URL != "":
		return chosen, errors.New("the server is given twice: as a command, and as url; take one out")

	case len(a.command) == 0 && chosen.URL == "":
		return chosen, errors.New(`no server: give its command after "extend mcp", or add the extension with -config '{"url":"https://..."}'`)

	case len(a.command) > 0 && (chosen.TokenEnv != "" || chosen.ClientID != ""):
		return chosen, errors.New("token_env and client_id are for a server reached at a url; one started with a command reads its environment itself")

	case chosen.TokenEnv != "" && chosen.ClientID != "":
		return chosen, errors.New("token_env gives the server a token, and client_id signs in for one; take one out")
	}

	return chosen, nil
}

func (a *Adapter) connect(chosen config) (conn, error) {
	// A server that did not start is no connection, not an empty one.
	if len(a.command) > 0 {
		started, err := startStdio(a.command, a.stderr)

		if err != nil {
			return nil, err
		}

		return started, nil
	}

	// A token in the environment is the sign-in; without one, the person
	// signs in when the server asks for it.
	if chosen.TokenEnv != "" {
		token, err := tokenFrom(chosen.TokenEnv)

		if err != nil {
			return nil, err
		}

		return newHTTP(chosen.URL, fixed(token)), nil
	}

	a.signing = newSignIn(chosen.URL, chosen.ClientID)

	return newHTTP(chosen.URL, a.signing), nil
}

// handshake opens the MCP session and returns the server's name.
func (a *Adapter) handshake(ctx context.Context) (string, error) {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "rumbo", "version": a.version},
	}

	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}

	if err := a.conn.request(ctx, "initialize", params, &result); err != nil {
		return "", err
	}

	a.conn.negotiated(result.ProtocolVersion)

	return result.ServerInfo.Name, a.conn.notify("notifications/initialized", struct{}{})
}

// offeredTool is a tool as an MCP server lists it.
type offeredTool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// list asks the server for all its tools, page by page.
func (a *Adapter) list(ctx context.Context) ([]offeredTool, error) {
	var offered []offeredTool
	cursor := ""

	for {
		params := map[string]string{}

		if cursor != "" {
			params["cursor"] = cursor
		}

		var page struct {
			Tools      []offeredTool `json:"tools"`
			NextCursor string        `json:"nextCursor"`
		}

		if err := a.conn.request(ctx, "tools/list", params, &page); err != nil {
			return nil, err
		}

		offered = append(offered, page.Tools...)

		if cursor = page.NextCursor; cursor == "" {
			return offered, nil
		}
	}
}

// declare turns the server's tools into the ones the model is given: those
// the configuration chose, each under its prefix.
func (a *Adapter) declare(offered []offeredTool, chosen config) ([]protocol.ToolDef, error) {
	var wanted []string

	for name := range strings.SplitSeq(chosen.Tools, ",") {
		if name = strings.TrimSpace(name); name != "" {
			wanted = append(wanted, name)
		}
	}

	known := make([]string, len(offered))

	for index, each := range offered {
		known[index] = each.Name
	}

	for _, name := range wanted {
		if !slices.Contains(known, name) {
			slices.Sort(known)

			return nil, fmt.Errorf("tools names %q, which the server does not have; it has %s", name, strings.Join(known, ", "))
		}
	}

	a.names = map[string]string{}

	var tools []protocol.ToolDef

	for _, each := range offered {
		if len(wanted) > 0 && !slices.Contains(wanted, each.Name) {
			continue
		}

		def := protocol.ToolDef{Name: chosen.Prefix + each.Name, Description: each.Description, Schema: each.InputSchema}

		if def.Description == "" {
			def.Description = each.Title
		}

		if len(def.Schema) == 0 {
			def.Schema = json.RawMessage(`{"type":"object"}`)
		}

		a.names[def.Name] = each.Name
		tools = append(tools, def)
	}

	return tools, nil
}

// content is one piece of a tool's result as MCP has them.
type content struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     []byte `json:"data"`
	MimeType string `json:"mimeType"`
	URI      string `json:"uri"`
	Resource struct {
		URI  string `json:"uri"`
		Text string `json:"text"`
	} `json:"resource"`
}

// Call implements protocol.ExtensionHandler: it calls the server's tool and
// gives its result as text and images. What a model cannot be shown, such
// as audio, is said in a line.
func (a *Adapter) Call(ctx context.Context, params protocol.CallParams, _ protocol.ExtensionHost) (protocol.CallResult, error) {
	name, declared := a.names[params.Name]

	if !declared {
		return protocol.CallResult{}, fmt.Errorf("there is no tool %q", params.Name)
	}

	arguments := params.Args

	if len(arguments) == 0 {
		arguments = json.RawMessage("{}")
	}

	var answer struct {
		Content           []content       `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}

	err := a.conn.request(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &answer)

	// A sign-in that ended and could not be renewed leaves the adapter with
	// tools it cannot call, so the host starts it again, to offer the
	// sign-in instead.
	if _, refused := errors.AsType[*unauthorized](err); err != nil && a.signed && (refused || !a.signing.signedIn()) {
		a.changed()

		return protocol.CallResult{}, fmt.Errorf("the sign-in to %s is over, so its tools do not work; the user signs in again with %s", a.signing.server, a.commandFor("login"))
	}

	if err != nil {
		return protocol.CallResult{}, err
	}

	result := protocol.CallResult{IsError: answer.IsError}

	var lines []string

	for _, each := range answer.Content {
		switch {
		case each.Type == "text":
			lines = append(lines, each.Text)

		case each.Type == "image":
			result.Images = append(result.Images, protocol.Image{MediaType: each.MimeType, Data: each.Data})

		case each.Type == "resource" && each.Resource.Text != "":
			lines = append(lines, each.Resource.Text)

		case each.Type == "resource":
			lines = append(lines, fmt.Sprintf("[resource %s, which is not text]", each.Resource.URI))

		case each.Type == "resource_link":
			lines = append(lines, fmt.Sprintf("[resource %s]", each.URI))

		default:
			lines = append(lines, fmt.Sprintf("[%s content, which cannot be shown]", each.Type))
		}
	}

	// A result given only as data is read as the JSON it is.
	if len(lines) == 0 && len(answer.StructuredContent) > 0 {
		lines = append(lines, string(answer.StructuredContent))
	}

	result.Text = strings.Join(lines, "\n")

	return result, nil
}
