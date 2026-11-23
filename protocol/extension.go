package protocol

import (
	"context"
	"encoding/json"
	"io"
)

// The Agent Extension Protocol connects the host to an extension: a program
// that gives the model tools of its own, tells it things without being
// asked, may be told what the runs do, and offers the person functions. It
// uses the same framing as the provider protocol, and its initialize,
// invoke, delta, ask, cancel, and shutdown. See docs/extension-protocol.md.

// ExtensionVersion is the version of the extension protocol this package
// implements.
const ExtensionVersion = 1

// Extension method names: one asks an extension to run one of its tools,
// the other for the text of one of its sections. An extension sends call
// too, while it answers one, for a tool of rumbo's own. What the runs do is
// told with event, as it is to a front end.
const (
	MethodCall    = "call"
	MethodSection = "section"
	// MethodChanged is a notification of the extension's: what it declared
	// when it started is no longer so, and the host starts it again.
	MethodChanged = "changed"
)

// ExtensionInitializeResult identifies the extension and declares its
// tools, which the host declares to the model as they are, the names of its
// sections of ambient context, the types of the events it is told, and the
// functions a person can ask of it, which a front end offers as commands.
type ExtensionInitializeResult struct {
	Protocol  int        `json:"protocol"`
	Name      string     `json:"name"`
	Tools     []ToolDef  `json:"tools,omitempty"`
	Sections  []string   `json:"sections,omitempty"`
	Events    []string   `json:"events,omitempty"`
	Functions []Function `json:"functions,omitempty"`
}

// SectionParams asks for the text of a section, for a run that starts in
// Dir, in Session.
type SectionParams struct {
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Session string `json:"session,omitempty"`
}

// SectionResult is what the model is told. An empty Text means the section
// does not apply, and takes away what was told before.
type SectionResult struct {
	Text string `json:"text"`
}

// CallParams runs a tool of the extension. Args are the arguments the model
// gave, an object the tool's schema describes, Dir is the directory the
// session works in, and Session the session, for an extension that keeps
// something for each.
type CallParams struct {
	Name    string          `json:"name"`
	Args    json.RawMessage `json:"args"`
	Dir     string          `json:"dir"`
	Session string          `json:"session,omitempty"`
}

// HostCallParams is a call an extension makes to a tool of rumbo's own,
// while it answers a call of its own. ID is the id of that call, which
// says where the tool runs and for which session.
type HostCallParams struct {
	ID   int64           `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ExtensionHost is what an extension can ask of the host while it answers
// a call. Through its Dialog it reaches the person following the run: Say
// shows what the call is doing, and Ask asks them something, such as
// whether to go on, which fails when nobody is there to answer.
type ExtensionHost interface {
	Dialog
	// Call runs one of rumbo's own tools, such as bash, as the model would
	// have: in the session's directory, and for its session. An extension
	// that replaces a tool reaches rumbo's that way.
	Call(ctx context.Context, name string, args json.RawMessage) (CallResult, error)
}

// CallResult is what a call gives the model: text, and the images it shows,
// if any. IsError says the call failed, and Text then says how to recover.
type CallResult struct {
	Text    string  `json:"text"`
	Images  []Image `json:"images,omitempty"`
	IsError bool    `json:"is_error,omitempty"`
}

// ExtensionHandler is what an extension implements. ServeExtension turns it
// into a protocol server, so an extension written in Go needs nothing else.
// Call may run for several calls at once.
type ExtensionHandler interface {
	Initialize(ctx context.Context, params InitializeParams) (ExtensionInitializeResult, error)
	Call(ctx context.Context, params CallParams, host ExtensionHost) (CallResult, error)
}

// Changer is what an extension implements whose tools, sections, or
// functions can change while it runs, as when a sign-in gives it tools.
// Each value Changes gives tells the host, which starts the extension
// again before the next run, to hear what it declares now.
type Changer interface {
	Changes() <-chan struct{}
}

// Listener is what an extension that asked for events also implements.
// Event is called for one event at a time, in the order they happened.
type Listener interface {
	Event(event Event)
}

// hostOf is the ExtensionHost of one call: what it says and asks goes to
// the host as a function's does, naming the call.
type hostOf struct {
	dialog
}

func (h hostOf) Call(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	var result CallResult

	err := h.server.ask(ctx, MethodCall, HostCallParams{ID: h.id, Name: name, Args: args}, &result)

	return result, err
}

// Sectioner is what an extension that declares sections also implements.
type Sectioner interface {
	Section(ctx context.Context, params SectionParams) (SectionResult, error)
}

// ServeExtension speaks the extension protocol on r and w until the host
// sends shutdown or closes the stream. The calls still running then are
// cancelled and waited for.
func ServeExtension(ctx context.Context, r io.Reader, w io.Writer, handler ExtensionHandler) error {
	server := newServer(w)
	server.functions = handler

	if changer, able := handler.(Changer); able {
		go func() {
			for range changer.Changes() {
				server.send(Frame{Method: MethodChanged})
			}
		}()
	}

	return server.serve(ctx, r, func(ctx context.Context, frame Frame) bool {
		switch frame.Method {
		case MethodInitialize:
			var params InitializeParams

			if server.decode(frame, &params) {
				result, err := handler.Initialize(ctx, params)
				server.reply(frame.ID, result, err)
			}

		case MethodCall:
			var params CallParams

			if server.decode(frame, &params) && frame.ID != nil {
				id := *frame.ID

				server.background(ctx, id, func(ctx context.Context) (any, error) {
					return handler.Call(ctx, params, hostOf{dialog{server: server, id: id}})
				})
			}

		case MethodInvoke:
			var params InvokeParams

			if server.decode(frame, &params) && frame.ID != nil {
				server.invoke(ctx, *frame.ID, params)
			}

		case MethodSection:
			var params SectionParams

			if server.decode(frame, &params) && frame.ID != nil {
				server.background(ctx, *frame.ID, func(ctx context.Context) (any, error) {
					if sectioner, able := handler.(Sectioner); able {
						return sectioner.Section(ctx, params)
					}

					return nil, &Error{Code: CodeMethodNotFound, Message: "this extension has no sections"}
				})
			}

		case MethodEvent:
			var event Event

			if listener, able := handler.(Listener); able && json.Unmarshal(frame.Params, &event) == nil {
				listener.Event(event)
			}

		default:
			return server.common(frame)
		}

		return false
	})
}
