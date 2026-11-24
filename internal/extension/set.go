package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/LiasCode/rumbo/internal/ambient"
	"github.com/LiasCode/rumbo/internal/picture"
	"github.com/LiasCode/rumbo/internal/tool"
	"github.com/LiasCode/rumbo/protocol"
)

// Program is an extension as it is started: the name the user registered
// it under, its command, the configuration the user chose for it, and the
// tools of rumbo's own that the user has it take the place of.
type Program struct {
	Name     string
	Command  []string
	Config   json.RawMessage
	Replaces []string
}

// Reserved are the names rumbo's own tools and sections have, and Own the
// tools of rumbo's a run has, which an extension's tool can call while it
// works: the ones it replaces among them.
type Reserved struct {
	Tools    []string
	Sections []string
	Own      []tool.Tool
}

// Offers are what the extensions give a run.
type Offers struct {
	// Tools are their tools, in the order they are declared to the model.
	Tools []tool.Tool
	// Collectors are their sections of ambient context.
	Collectors []ambient.Collector
	// Replaced are the tools of rumbo's own that one of theirs takes the
	// place of, which the run therefore does not have.
	Replaced []string
}

// toolName is what every model API takes as the name of a tool, and
// sectionName and functionName what a section and a function can be called.
var (
	toolName     = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	sectionName  = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	functionName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

// How long an extension gets to say a section, which every run waits for
// before it starts, and how much of it the model is told.
const (
	sectionTimeout  = 10 * time.Second
	maxSectionBytes = 8_000
)

// Set is the extensions of a host: one process each, started when a run
// first needs it and stopped with the host.
type Set struct {
	// stderr returns where the diagnostics of the extension called name go.
	stderr func(name string) io.Writer
	// clip bounds the text a call puts in the context.
	clip func(text string) string

	mu      sync.Mutex
	running map[string]*running

	// listening guards listeners, the processes told of events. They are
	// kept apart from running so that telling never waits for an extension
	// that is starting.
	listening sync.Mutex
	listeners map[string]*Process
}

// running is an extension's process, and what it was started from.
type running struct {
	program Program
	process *Process
}

// NewSet returns a set with nothing started. The diagnostics of each
// extension go where stderr says, and the text of each result goes through
// clip.
func NewSet(stderr func(name string) io.Writer, clip func(text string) string) *Set {
	return &Set{stderr: stderr, clip: clip, running: map[string]*running{}, listeners: map[string]*Process{}}
}

// Start returns what programs give a run, in an order that does not change
// while the extensions declare the same: by extension, then by name. It
// starts the extensions that are not running as they are given, and stops
// the ones no longer among them. An extension that does not start, or
// declares what the model cannot be given, fails all of it, saying which: a
// run with some tools missing would be one the person did not ask for.
func (s *Set) Start(ctx context.Context, programs []Program, rumbo Reserved) (Offers, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	programs = slices.Clone(programs)
	slices.SortFunc(programs, func(a, b Program) int { return strings.Compare(a.Name, b.Name) })

	for name := range s.running {
		if !slices.ContainsFunc(programs, func(program Program) bool { return program.Name == name }) {
			s.stop(name)
		}
	}

	// owners say whose each tool name and each section name is.
	tools, sections := map[string]string{}, map[string]string{}

	for _, name := range rumbo.Tools {
		tools[name] = "rumbo"
	}

	for _, name := range rumbo.Sections {
		sections[name] = "rumbo"
	}

	var offers Offers

	for _, program := range programs {
		process, err := s.started(ctx, program)

		if err != nil {
			return Offers{}, fmt.Errorf("extension %s: %w", program.Name, err)
		}

		declared := slices.Clone(process.Tools())
		slices.SortFunc(declared, func(a, b protocol.ToolDef) int { return strings.Compare(a.Name, b.Name) })

		for _, name := range program.Replaces {
			switch {
			case !slices.Contains(rumbo.Tools, name):
				return Offers{}, fmt.Errorf("extension %s replaces %q, which is no tool of rumbo's; those are %s", program.Name, name, strings.Join(rumbo.Tools, ", "))

			case !slices.ContainsFunc(declared, func(def protocol.ToolDef) bool { return def.Name == name }):
				return Offers{}, fmt.Errorf("extension %s replaces %q, and declares no tool of that name to take its place", program.Name, name)
			}
		}

		for _, def := range declared {
			if err := checkDef(def); err != nil {
				return Offers{}, fmt.Errorf("extension %s: %w", program.Name, err)
			}

			owner, taken := tools[def.Name]

			switch {
			case taken && owner == "rumbo" && slices.Contains(program.Replaces, def.Name):
				offers.Replaced = append(offers.Replaced, def.Name)

			case taken && owner == "rumbo":
				return Offers{}, fmt.Errorf("extension %[1]s declares the tool %[2]q, which rumbo already has; to have it take rumbo's place, add it with -replaces %[2]s, or set replaces=%[2]s for it", program.Name, def.Name)

			case taken:
				return Offers{}, fmt.Errorf("extension %s declares the tool %q, which %s already has; a tool has one name for the model", program.Name, def.Name, owner)
			}

			tools[def.Name] = "the extension " + program.Name
			offers.Tools = append(offers.Tools, extensionTool{set: s, extension: program.Name, def: def, own: rumbo.Own})
		}

		// A front end offers a function as a command, named after it.
		for _, function := range process.Functions() {
			if !functionName.MatchString(function.Name) {
				return Offers{}, fmt.Errorf("extension %s: the function name %q is not one: lowercase letters, digits, _ and -, starting with a letter", program.Name, function.Name)
			}
		}

		for _, name := range slices.Sorted(slices.Values(process.Sections())) {
			if !sectionName.MatchString(name) {
				return Offers{}, fmt.Errorf("extension %s: the section name %q is not one: lowercase letters, digits, _ and -, at most 64", program.Name, name)
			}

			if owner, taken := sections[name]; taken {
				return Offers{}, fmt.Errorf("extension %s declares the section %q, which %s already has", program.Name, name, owner)
			}

			sections[name] = "the extension " + program.Name
			offers.Collectors = append(offers.Collectors, section{set: s, extension: program.Name, name: name})
		}
	}

	return offers, nil
}

// checkDef refuses a declaration a model API would refuse, which would fail
// every request of the run.
func checkDef(def protocol.ToolDef) error {
	if !toolName.MatchString(def.Name) {
		return fmt.Errorf("the tool name %q is not one a model takes: letters, digits, _ and -, at most 64", def.Name)
	}

	var schema map[string]any

	if err := json.Unmarshal(def.Schema, &schema); err != nil || schema == nil {
		return fmt.Errorf(`the schema of the tool %q is not a JSON Schema object, such as {"type":"object","properties":{}}`, def.Name)
	}

	return nil
}

// started returns the process of program, starting it when it is not
// running, has exited, was started from another command or configuration,
// or said that what it declares has changed. The caller holds the lock.
func (s *Set) started(ctx context.Context, program Program) (*Process, error) {
	if known, found := s.running[program.Name]; found {
		same := slices.Equal(known.program.Command, program.Command) && string(known.program.Config) == string(program.Config)

		if same && !known.process.Exited() && !known.process.Stale() {
			return known.process, nil
		}

		s.stop(program.Name)
	}

	process, err := Start(ctx, program.Name, program.Command, program.Config, s.stderr(program.Name))

	if err != nil {
		return nil, err
	}

	s.running[program.Name] = &running{program: program, process: process}

	s.listening.Lock()
	s.listeners[program.Name] = process
	s.listening.Unlock()

	return process, nil
}

// stop ends the process of the extension called name. The caller holds the
// lock.
func (s *Set) stop(name string) {
	s.listening.Lock()
	delete(s.listeners, name)
	s.listening.Unlock()

	_ = s.running[name].process.Close()
	delete(s.running, name)
}

// process returns the process a call to the extension called name goes to,
// started again when it died.
func (s *Set) process(ctx context.Context, name string) (*Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	known, found := s.running[name]

	if !found {
		return nil, fmt.Errorf("the extension %s is no longer registered, so its tools are gone", name)
	}

	return s.started(ctx, known.program)
}

// Close stops every extension.
func (s *Set) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for name := range s.running {
		s.stop(name)
	}
}

// extensionTool is a tool of an extension as the agent sees it. It names
// its extension instead of holding the process, so a call reaches the
// process there is when it is made.
type extensionTool struct {
	set       *Set
	extension string
	def       protocol.ToolDef
	// own are rumbo's own tools, which the extension can call meanwhile.
	own []tool.Tool
}

// Def implements tool.Tool.
func (t extensionTool) Def() protocol.ToolDef {
	return t.def
}

// Run implements tool.Tool. The text is bounded as a command's output is,
// and each image prepared as any image the model is shown.
func (t extensionTool) Run(ctx context.Context, dir string, args json.RawMessage) (tool.Result, error) {
	process, err := t.set.process(ctx, t.extension)

	if err != nil {
		return tool.Result{}, err
	}

	params := protocol.CallParams{Name: t.def.Name, Args: args, Dir: dir, Session: tool.SessionOf(ctx)}

	host := Host{Tool: func(ctx context.Context, name string, args json.RawMessage) (protocol.CallResult, error) {
		return t.rumbo(ctx, dir, name, args)
	}}

	// What the call says and asks reaches whoever follows the run.
	if person := tool.PersonOf(ctx); person != nil {
		host.Say = func(text string) { person.Progress(t.def.Name, text) }
		host.Ask = func(ctx context.Context, question string, secret bool) (string, error) {
			return person.Ask(ctx, t.def.Name, question, secret)
		}
	}

	answer, err := process.Call(ctx, params, host)

	if err != nil {
		return tool.Result{}, fmt.Errorf("the extension %s: %w", t.extension, err)
	}

	result := tool.Result{Text: t.set.clip(answer.Text)}

	if answer.IsError {
		return tool.Result{}, errors.New(result.Text)
	}

	// The text says what each image is, for a provider that cannot send it.
	for index, image := range answer.Images {
		kind := picture.Kind(image.Data)

		if kind == "" {
			result.Text += fmt.Sprintf("\n[image %d could not be shown: it is not a PNG, JPEG, GIF, or WebP image]", index+1)
			continue
		}

		prepared, about, err := picture.Prepare(image.Data, kind)

		if err != nil {
			result.Text += fmt.Sprintf("\n[image %d could not be shown: %v]", index+1, err)
			continue
		}

		result.Text += fmt.Sprintf("\n[image %d: %s]", index+1, about)
		result.Images = append(result.Images, prepared)
	}

	result.Text = strings.TrimPrefix(result.Text, "\n")

	return result, nil
}

// rumbo runs one of rumbo's own tools for the extension, where the call it
// is answering was made. A failure of the tool is a result, as it is for
// the model; a tool there is none of is the extension's mistake.
func (t extensionTool) rumbo(ctx context.Context, dir, name string, args json.RawMessage) (protocol.CallResult, error) {
	names := make([]string, len(t.own))

	for index, own := range t.own {
		if names[index] = own.Def().Name; names[index] != name {
			continue
		}

		if !json.Valid(args) {
			return protocol.CallResult{Text: "invalid arguments: not valid JSON", IsError: true}, nil
		}

		result, err := own.Run(ctx, dir, args)

		if err != nil {
			return protocol.CallResult{Text: err.Error(), IsError: true}, nil
		}

		return protocol.CallResult{Text: result.Text, Images: result.Images}, nil
	}

	return protocol.CallResult{}, fmt.Errorf("rumbo has no tool %q in this run; it has %s", name, strings.Join(names, ", "))
}

// Functions returns the functions of the extensions that are running and
// have any, by extension.
func (s *Set) Functions() []protocol.ExtensionOffer {
	s.mu.Lock()
	defer s.mu.Unlock()

	var offers []protocol.ExtensionOffer

	for _, name := range slices.Sorted(maps.Keys(s.running)) {
		if functions := s.running[name].process.Functions(); len(functions) > 0 {
			offers = append(offers, protocol.ExtensionOffer{Name: name, Functions: functions})
		}
	}

	return offers
}

// Invoke calls a function of the extension called name, which talks with
// the person through dialog while it works. A nil dialog lets it say and
// ask nothing.
func (s *Set) Invoke(ctx context.Context, name, function string, params json.RawMessage, dialog protocol.Dialog) (json.RawMessage, error) {
	process, err := s.process(ctx, name)

	if err != nil {
		return nil, err
	}

	var host Host

	if dialog != nil {
		host.Say, host.Ask = dialog.Say, dialog.Ask
	}

	return process.Invoke(ctx, function, params, host)
}

// Tell tells the extensions that are running, and asked for events of its
// type, what happened in a run. It does not wait for them.
func (s *Set) Tell(event protocol.Event) {
	s.listening.Lock()
	defer s.listening.Unlock()

	for _, process := range s.listeners {
		process.Tell(event)
	}
}

// Pids are the system's ids of the extensions' processes that run now, by
// extension.
func (s *Set) Pids() map[string]int {
	s.listening.Lock()
	defer s.listening.Unlock()

	pids := map[string]int{}

	for name, process := range s.listeners {
		if !process.Exited() {
			pids[name] = process.Pid()
		}
	}

	return pids
}

// Declared are the tools and the sections the running extension called
// name declared, which is nothing for one that is not running.
func (s *Set) Declared(name string) ([]protocol.ToolDef, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if known, found := s.running[name]; found {
		return known.process.Tools(), known.process.Sections()
	}

	return nil, nil
}

// section is a section of an extension as the agent sees it.
type section struct {
	set       *Set
	extension string
	name      string
}

// Name implements ambient.Collector.
func (c section) Name() string {
	return c.name
}

// Collect implements ambient.Collector.
func (c section) Collect(ctx context.Context, dir string) string {
	text, _ := c.Try(ctx, dir)

	return text
}

// Try implements ambient.Fallible. A long text is cut, since it is sent
// whole. When the extension cannot say it, the person is told why and the
// model goes on being told what it was told last, so a passing failure
// neither holds the run nor takes the section away.
func (c section) Try(ctx context.Context, dir string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, sectionTimeout)
	defer cancel()

	process, err := c.set.process(ctx, c.extension)

	var text string

	if err == nil {
		text, err = process.Section(ctx, protocol.SectionParams{Name: c.name, Dir: dir, Session: tool.SessionOf(ctx)})
	}

	if err != nil {
		fmt.Fprintf(c.set.stderr(c.extension), "did not say its section %s, so the model is told what it was told last: %v\n", c.name, err)

		return "", false
	}

	if text = strings.ToValidUTF8(strings.TrimSpace(text), ""); len(text) > maxSectionBytes {
		kept := text[:maxSectionBytes]

		if index := strings.LastIndexByte(kept, '\n'); index > 0 {
			kept = kept[:index]
		}

		text = fmt.Sprintf("%s\n[cut: this is the first %d of %d bytes]", strings.ToValidUTF8(kept, ""), len(kept), len(text))
	}

	return text, true
}
