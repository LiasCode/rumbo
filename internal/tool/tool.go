// Package tool holds the tools the model can call. The set is deliberately
// small: read, write, edit, and bash work with files and run programs, and
// everything else is reachable through bash, except what a screen shows,
// which the computer tool sees when the user turns it on.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// Tool is one capability offered to the model.
type Tool interface {
	Def() protocol.ToolDef
	// Run executes the call in dir. A returned error is shown to the model as
	// a failed result, so its message should say how to recover.
	Run(ctx context.Context, dir string, args json.RawMessage) (Result, error)
}

// Result is what a call gives the model: text, and the images it shows, if
// any. The text says what each image is, for the providers that cannot
// send it and for the person following the run.
type Result struct {
	Text   string
	Images []protocol.Image
}

// sessionKey is where a context carries the session a call is made in.
type sessionKey struct{}

// InSession returns ctx naming the session the calls made with it belong
// to, for a tool that keeps something for each session.
func InSession(ctx context.Context, session string) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}

// SessionOf returns the session ctx names, or "" when it names none.
func SessionOf(ctx context.Context) string {
	session, _ := ctx.Value(sessionKey{}).(string)

	return session
}

// Person is how a tool reaches the person following the run it is called
// in, when someone is.
type Person interface {
	// Progress shows what the call to tool is doing while it works.
	Progress(tool, text string)
	// Ask asks the person something for tool, and waits for the answer. It
	// fails when they decline, when nobody is there, or when ctx ends.
	Ask(ctx context.Context, tool, question string, secret bool) (string, error)
}

// personKey is where a context carries the person of a run.
type personKey struct{}

// WithPerson returns ctx with the person the calls made with it can reach.
func WithPerson(ctx context.Context, person Person) context.Context {
	return context.WithValue(ctx, personKey{}, person)
}

// PersonOf returns the person ctx carries, or nil when it carries none.
func PersonOf(ctx context.Context) Person {
	person, _ := ctx.Value(personKey{}).(Person)

	return person
}

// text is a result of text alone.
func text(value string) Result {
	return Result{Text: value}
}

// Defaults returns the built-in tools, for callers that have nothing to
// clean up afterwards: what they save stays in the system's temporary
// directory, and the processes they start are not tracked.
func Defaults() []Tool {
	return (&Set{seen: newSeen()}).Tools()
}

// Limits on what one call may put in the context. A tool result is paid for
// again on every later request, so results are kept small and say how to get
// the rest.
const (
	// readLines is the window read returns when no limit is given.
	readLines = 500
	// maxReadLines and maxReadBytes bound a window whatever limit is asked.
	maxReadLines = 2_000
	maxReadBytes = 50_000
	// outputBytes is how much of a command's output is returned inline.
	outputBytes = 12_000
)

func decode(args json.RawMessage, into any) error {
	if err := json.Unmarshal(args, into); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}

	return nil
}

func resolve(dir, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}

	return filepath.Join(dir, path), nil
}

// clip returns output whole when it is small. Otherwise it returns the
// beginning and the end, which is where a command says what it is doing and
// how it ended, and saves the full output to a file the model can search.
func clip(output string, saved *saved) string {
	if len(output) <= outputBytes {
		return output
	}

	head := output[:outputBytes/4]
	tail := output[len(output)-outputBytes*3/4:]

	// Cut at line boundaries when there are any.
	if index := strings.LastIndexByte(head, '\n'); index >= 0 {
		head = head[:index]
	}

	if index := strings.IndexByte(tail, '\n'); index >= 0 {
		tail = tail[index+1:]
	}

	head = strings.ToValidUTF8(head, "")
	tail = strings.ToValidUTF8(tail, "")

	total := strings.Count(output, "\n") + 1
	omitted := total - strings.Count(head, "\n") - strings.Count(tail, "\n") - 2
	where := "the full output could not be saved"

	if path, err := save(output, saved); err == nil {
		where = fmt.Sprintf("the full output, %d lines, is in %s; search it with grep or read it in windows", total, path)
	}

	return fmt.Sprintf("%s\n[... %d lines omitted; %s ...]\n%s", head, omitted, where, tail)
}

// save writes output to a file outside the project.
func save(output string, saved *saved) (string, error) {
	file, err := saved.create("rumbo-output-*.txt")

	if err != nil {
		return "", err
	}

	defer file.Close()

	_, err = file.WriteString(output)

	return file.Name(), err
}

func schema(properties string, required ...string) json.RawMessage {
	quoted, _ := json.Marshal(required)

	return json.RawMessage(fmt.Sprintf(
		`{"type":"object","properties":%s,"required":%s,"additionalProperties":false}`,
		properties, quoted,
	))
}
