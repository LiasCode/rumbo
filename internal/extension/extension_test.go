package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/internal/ambient"
	"github.com/LiasCode/rumbo/internal/tool"
	"github.com/LiasCode/rumbo/protocol"
)

// fake is an extension that declares the tools its configuration names, or
// echo and the rest when it names none. What each does is in Call.
type fake struct {
	// heard are the events it was told, each as its type and what it says.
	heard *[]string
	// hearing guards heard, which a call reads while events arrive.
	hearing *sync.Mutex
	// changes is what it tells the host when what it declares changed.
	changes chan struct{}
}

// Changes implements protocol.Changer.
func (f fake) Changes() <-chan struct{} {
	return f.changes
}

// Event keeps what it was told.
func (f fake) Event(event protocol.Event) {
	f.hearing.Lock()
	defer f.hearing.Unlock()

	*f.heard = append(*f.heard, event.Type+":"+event.Session+":"+event.Text+event.Name+event.Status)
}

func (fake) Initialize(_ context.Context, params protocol.InitializeParams) (protocol.ExtensionInitializeResult, error) {
	var config struct {
		Tools     []string `json:"tools"`
		Schema    string   `json:"schema"`
		Sections  []string `json:"sections"`
		Events    []string `json:"events"`
		Functions []string `json:"functions"`
	}

	_ = json.Unmarshal(params.Config, &config)

	if len(config.Tools) == 0 {
		config.Tools = []string{"process", "echo", "fail", "hang", "die", "picture"}
	}

	if config.Schema == "" {
		config.Schema = `{"type":"object"}`
	}

	result := protocol.ExtensionInitializeResult{Protocol: protocol.ExtensionVersion, Name: "fake", Sections: config.Sections, Events: config.Events}

	for _, name := range config.Tools {
		result.Tools = append(result.Tools, protocol.ToolDef{Name: name, Description: "does " + name, Schema: json.RawMessage(config.Schema)})
	}

	for _, name := range config.Functions {
		result.Functions = append(result.Functions, protocol.Function{Name: name})
	}

	return result, nil
}

func (f fake) Call(ctx context.Context, params protocol.CallParams, host protocol.ExtensionHost) (protocol.CallResult, error) {
	switch params.Name {
	case "echo":
		return protocol.CallResult{Text: fmt.Sprintf("%s in %s", params.Args, params.Dir)}, nil

	// bash is rumbo's own, with a word before what it says; given a tool
	// to call instead, it calls that one.
	case "bash":
		var args struct {
			Tool string `json:"tool"`
		}

		if _ = json.Unmarshal(params.Args, &args); args.Tool == "" {
			args.Tool = "bash"
		}

		result, err := host.Call(ctx, args.Tool, params.Args)
		result.Text = "wrapped: " + result.Text

		return result, err

	case "session":
		return protocol.CallResult{Text: params.Session}, nil

	// change says that what the extension declared is no longer so.
	case "change":
		f.changes <- struct{}{}

		return protocol.CallResult{Text: "changed"}, nil

	// confirm says what it is about to do, and does it if the person agrees.
	case "confirm":
		host.Say("about to do it")

		answer, err := host.Ask(ctx, "go on?", false)

		if err != nil {
			return protocol.CallResult{Text: "not done: " + err.Error(), IsError: true}, nil
		}

		return protocol.CallResult{Text: "done, since the answer was " + answer}, nil

	case "heard":
		f.hearing.Lock()
		defer f.hearing.Unlock()

		return protocol.CallResult{Text: strings.Join(*f.heard, " ")}, nil

	case "process":
		return protocol.CallResult{Text: fmt.Sprint(os.Getpid())}, nil

	case "fail":
		return protocol.CallResult{Text: "no such thing; try another", IsError: true}, nil

	case "hang":
		<-ctx.Done()
		return protocol.CallResult{}, ctx.Err()

	case "die":
		os.Exit(3)

	case "picture":
		var encoded bytes.Buffer
		_ = png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 4, 2)))

		return protocol.CallResult{Images: []protocol.Image{{MediaType: "image/png", Data: encoded.Bytes()}, {MediaType: "image/png", Data: []byte("not one")}}}, nil
	}

	return protocol.CallResult{}, errors.New("no tool " + params.Name)
}

// InvokeDialog has one function, greet, which talks with the person: it
// says something, asks a secret, and answers with what it was told.
func (fake) InvokeDialog(ctx context.Context, name string, params json.RawMessage, dialog protocol.Dialog) (any, error) {
	if name != "greet" {
		return nil, errors.New("no function " + name)
	}

	dialog.Say("hello")

	word, err := dialog.Ask(ctx, "the word", true)

	if err != nil {
		return nil, fmt.Errorf("given up: %w", err)
	}

	return map[string]string{"word": word, "params": string(params)}, nil
}

// asked counts the times the section once was asked for, in this process.
var asked atomic.Int32

// Section says where it was asked from. The section long is longer than a
// model is told, and once is said the first time only.
func (fake) Section(_ context.Context, params protocol.SectionParams) (protocol.SectionResult, error) {
	switch params.Name {
	case "long":
		return protocol.SectionResult{Text: strings.Repeat("a line of the section\n", 1000)}, nil

	case "once":
		if asked.Add(1) > 1 {
			return protocol.SectionResult{}, errors.New("not again")
		}
	}

	return protocol.SectionResult{Text: "  " + params.Name + " in " + params.Dir + "\n"}, nil
}

// The test binary doubles as the extension process.
func TestMain(m *testing.M) {
	if os.Getenv("RUMBO_TEST_EXTENSION") == "1" {
		if err := protocol.ServeExtension(context.Background(), os.Stdin, os.Stdout, fake{heard: &[]string{}, hearing: &sync.Mutex{}, changes: make(chan struct{}, 1)}); err != nil {
			os.Exit(1)
		}

		os.Exit(0)
	}

	os.Exit(m.Run())
}

// rumbo is what the tests take rumbo's own tools and sections to be.
var rumbo = Reserved{Tools: []string{"bash", "read"}, Sections: []string{"environment"}}

func testSet(t *testing.T) *Set {
	t.Helper()
	t.Setenv("RUMBO_TEST_EXTENSION", "1")

	set := NewSet(func(string) io.Writer { return io.Discard }, func(text string) string { return "clipped:" + text })
	t.Cleanup(set.Close)

	return set
}

func program(name, config string) Program {
	return Program{Name: name, Command: []string{os.Args[0]}, Config: json.RawMessage(config)}
}

// run calls the tool called name of those given.
func run(t *testing.T, set *Set, programs []Program, name string) (string, error) {
	t.Helper()

	offers, err := set.Start(context.Background(), programs, Reserved{})

	if err != nil {
		t.Fatal(err)
	}

	for _, each := range offers.Tools {
		if each.Def().Name == name {
			result, err := each.Run(context.Background(), "/work", json.RawMessage(`{"a":1}`))

			return result.Text, err
		}
	}

	t.Fatalf("no tool %s among %d", name, len(offers.Tools))

	return "", nil
}

func TestAProcessAnswersCallsMadeAtOnce(t *testing.T) {
	t.Setenv("RUMBO_TEST_EXTENSION", "1")

	process, err := Start(context.Background(), "fake", []string{os.Args[0]}, nil, io.Discard)

	if err != nil {
		t.Fatal(err)
	}

	defer process.Close()

	// A call that hangs does not keep the others waiting.
	hanging, stop := context.WithCancel(context.Background())
	hung := make(chan error, 1)

	go func() {
		_, err := process.Call(hanging, protocol.CallParams{Name: "hang"}, Host{})
		hung <- err
	}()

	var wait sync.WaitGroup

	for index := range 8 {
		wait.Go(func() {
			args := json.RawMessage(fmt.Sprint(index))
			result, err := process.Call(context.Background(), protocol.CallParams{Name: "echo", Args: args, Dir: "/d"}, Host{})

			if want := fmt.Sprintf("%d in /d", index); err != nil || result.Text != want {
				t.Errorf("call %d = %q, %v", index, result.Text, err)
			}
		})
	}

	wait.Wait()
	stop()

	select {
	case err := <-hung:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the cancelled call ended with %v", err)
		}

	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled call did not end")
	}

	if _, err := process.Call(context.Background(), protocol.CallParams{Name: "nothing"}, Host{}); err == nil || err.Error() != "no tool nothing" {
		t.Fatalf("the extension's error was not passed on: %v", err)
	}
}

func TestToolsAreDeclaredInAnOrderThatDoesNotChange(t *testing.T) {
	set := testSet(t)
	programs := []Program{program("zeta", `{"tools":["b","a"]}`), program("alpha", `{"tools":["z","c"]}`)}

	var names []string

	offers, err := set.Start(context.Background(), programs, rumbo)

	if err != nil {
		t.Fatal(err)
	}

	for _, each := range offers.Tools {
		names = append(names, each.Def().Name)
	}

	if got := strings.Join(names, " "); got != "c z a b" {
		t.Fatalf("declared %q", got)
	}
}

func TestAToolTheModelCannotBeGivenIsRefused(t *testing.T) {
	cases := map[string]struct {
		programs []Program
		want     string
	}{
		"rumbo's name":    {[]Program{program("mine", `{"tools":["bash"]}`)}, `extension mine declares the tool "bash", which rumbo already has; to have it take rumbo's place, add it with -replaces bash, or set replaces=bash for it`},
		"no such tool":    {[]Program{{Name: "mine", Command: []string{os.Args[0]}, Replaces: []string{"shell"}}}, `extension mine replaces "shell", which is no tool of rumbo's; those are bash, read`},
		"not declared":    {[]Program{{Name: "mine", Command: []string{os.Args[0]}, Config: json.RawMessage(`{"tools":["find"]}`), Replaces: []string{"bash"}}}, `extension mine replaces "bash", and declares no tool of that name`},
		"rumbo's section": {[]Program{program("mine", `{"tools":["find"],"sections":["environment"]}`)}, `extension mine declares the section "environment", which rumbo already has`},
		"a bad section":   {[]Program{program("mine", `{"tools":["find"],"sections":["My Notes"]}`)}, `the section name "My Notes" is not one`},
		"a bad function":  {[]Program{program("mine", `{"tools":["find"],"functions":["Log In"]}`)}, `the function name "Log In" is not one`},
		"another's name":  {[]Program{program("one", `{"tools":["find"]}`), program("two", `{"tools":["find"]}`)}, `extension two declares the tool "find", which the extension one already has`},
		"a bad name":      {[]Program{program("mine", `{"tools":["my tool"]}`)}, `the tool name "my tool" is not one a model takes`},
		"a bad schema":    {[]Program{program("mine", `{"tools":["find"],"schema":"[]"}`)}, `the schema of the tool "find" is not a JSON Schema object`},
		"no start":        {[]Program{{Name: "gone", Command: []string{"/nonexistent/extension"}}}, `extension gone: start extension`},
	}

	for name, each := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := testSet(t).Start(context.Background(), each.programs, rumbo)

			if err == nil || !strings.Contains(err.Error(), each.want) {
				t.Fatalf("err = %v, want %q in it", err, each.want)
			}
		})
	}
}

func TestAResultIsBoundedAndAFailureIsTheModels(t *testing.T) {
	set := testSet(t)
	programs := []Program{program("fake", "")}

	if text, err := run(t, set, programs, "echo"); err != nil || text != `clipped:{"a":1} in /work` {
		t.Fatalf("echo = %q, %v", text, err)
	}

	if _, err := run(t, set, programs, "fail"); err == nil || err.Error() != "clipped:no such thing; try another" {
		t.Fatalf("fail = %v", err)
	}

	offers, _ := set.Start(context.Background(), programs, Reserved{})

	for _, each := range offers.Tools {
		if each.Def().Name != "picture" {
			continue
		}

		result, err := each.Run(context.Background(), "/work", json.RawMessage(`{}`))

		if err != nil || len(result.Images) != 1 || !strings.Contains(result.Text, "[image 1: png, 4×2]") || !strings.Contains(result.Text, "[image 2 could not be shown") {
			t.Fatalf("picture = %q, %d images, %v", result.Text, len(result.Images), err)
		}
	}
}

// One process serves every call, so what an extension keeps lasts; one
// that died, or whose configuration changed, is started again; and one
// taken out is stopped.
func TestAnExtensionIsKeptAndReplacedWhenItMustBe(t *testing.T) {
	set := testSet(t)
	programs := []Program{program("fake", "")}

	first, _ := run(t, set, programs, "process")

	if again, _ := run(t, set, programs, "process"); again != first {
		t.Fatalf("a second run started another process: %s, then %s", first, again)
	}

	if _, err := run(t, set, programs, "die"); err == nil || !strings.Contains(err.Error(), "the extension process exited") {
		t.Fatalf("die = %v", err)
	}

	second, err := run(t, set, programs, "process")

	if err != nil || second == first {
		t.Fatalf("the process that died was not replaced: %s, %v", second, err)
	}

	changed := []Program{program("fake", `{"tools":["process"]}`)}

	if third, _ := run(t, set, changed, "process"); third == second {
		t.Fatal("a changed configuration did not start the extension again")
	}

	// A run that declared the tools before the extension was taken out.
	offers, _ := set.Start(context.Background(), changed, Reserved{})

	if _, err := set.Start(context.Background(), nil, Reserved{}); err != nil {
		t.Fatal(err)
	}

	if _, err := offers.Tools[0].Run(context.Background(), "/work", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "no longer registered") {
		t.Fatalf("a call to an extension taken out = %v", err)
	}
}

// An extension the user has replace a tool of rumbo's declares it in its
// place, and the run is told which of rumbo's it does not have.
func TestAnExtensionReplacesAToolOfRumbos(t *testing.T) {
	set := testSet(t)
	mine := Program{Name: "mine", Command: []string{os.Args[0]}, Config: json.RawMessage(`{"tools":["bash","echo"]}`), Replaces: []string{"bash"}}

	offers, err := set.Start(context.Background(), []Program{mine}, rumbo)

	if err != nil || len(offers.Tools) != 2 || strings.Join(offers.Replaced, " ") != "bash" {
		t.Fatalf("offers = %+v, err = %v", offers, err)
	}

	// Two cannot replace the same one.
	other := mine
	other.Name = "other"

	if _, err := set.Start(context.Background(), []Program{mine, other}, rumbo); err == nil || !strings.Contains(err.Error(), `extension other declares the tool "bash", which the extension mine already has`) {
		t.Fatalf("err = %v", err)
	}
}

// A section says what the extension says for the directory, cut when it is
// long, and what it said last when the extension cannot say it again.
func TestSectionsAreToldBoundedAndKept(t *testing.T) {
	var said strings.Builder

	t.Setenv("RUMBO_TEST_EXTENSION", "1")

	set := NewSet(func(string) io.Writer { return &said }, func(text string) string { return text })
	t.Cleanup(set.Close)

	offers, err := set.Start(context.Background(), []Program{program("fake", `{"tools":["echo"],"sections":["once","long","here"]}`)}, rumbo)

	if err != nil {
		t.Fatal(err)
	}

	told := map[string]string{}

	for _, collector := range offers.Collectors {
		told[collector.Name()] = collector.Collect(context.Background(), "/work")
	}

	if len(told) != 3 || told["here"] != "here in /work" || offers.Collectors[0].Name() != "here" {
		t.Fatalf("told = %q, first %s", told, offers.Collectors[0].Name())
	}

	if long := told["long"]; len(long) > maxSectionBytes+100 || !strings.HasSuffix(long, "of 21999 bytes]") {
		t.Fatalf("a long section was told as %d bytes, ending %q", len(long), long[len(long)-60:])
	}

	// The second time the extension fails: the person is told, and what
	// the model was told stays, so nothing changes.
	known := map[string]string{"once": told["once"], "long": told["long"], "here": told["here"]}

	if changes := ambient.Changes(context.Background(), "/work", offers.Collectors, known); len(changes) != 0 || !strings.Contains(said.String(), "did not say its section once") {
		t.Fatalf("changes = %+v; said %q", changes, said.String())
	}
}

// own is a tool of rumbo's own for the tests: it says its name, what it was
// given, and where, and fails when asked to.
type own string

func (o own) Def() protocol.ToolDef {
	return protocol.ToolDef{Name: string(o)}
}

func (o own) Run(_ context.Context, dir string, args json.RawMessage) (tool.Result, error) {
	if strings.Contains(string(args), "fail") {
		return tool.Result{}, errors.New("it failed; try something else")
	}

	return tool.Result{Text: fmt.Sprintf("rumbo's %s got %s in %s", string(o), args, dir)}, nil
}

// An extension that replaces a tool reaches rumbo's own through the host,
// so it can do something before and after it instead of doing it all again.
func TestAnExtensionCallsRumbosOwnTools(t *testing.T) {
	set := testSet(t)
	mine := Program{Name: "mine", Command: []string{os.Args[0]}, Config: json.RawMessage(`{"tools":["bash","session"]}`), Replaces: []string{"bash"}}
	rumbo := Reserved{Tools: []string{"bash", "read"}, Own: []tool.Tool{own("bash"), own("read")}}

	offers, err := set.Start(context.Background(), []Program{mine}, rumbo)

	if err != nil {
		t.Fatal(err)
	}

	bash, session := offers.Tools[0], offers.Tools[1]
	ctx := tool.InSession(context.Background(), "s1")

	if result, err := bash.Run(ctx, "/work", json.RawMessage(`{"command":"ls"}`)); err != nil || result.Text != `clipped:wrapped: rumbo's bash got {"command":"ls"} in /work` {
		t.Fatalf("bash = %q, %v", result.Text, err)
	}

	// Another of rumbo's tools.
	if result, err := bash.Run(ctx, "/work", json.RawMessage(`{"tool":"read"}`)); err != nil || !strings.Contains(result.Text, "wrapped: rumbo's read got") {
		t.Fatalf("read through bash = %q, %v", result.Text, err)
	}

	// A failure of rumbo's tool is a result the extension reads, which this
	// one passes on as a failure of its own.
	if _, err := bash.Run(ctx, "/work", json.RawMessage(`{"command":"fail"}`)); err == nil || err.Error() != "clipped:wrapped: it failed; try something else" {
		t.Fatalf("a failure of rumbo's = %v", err)
	}

	if _, err := bash.Run(ctx, "/work", json.RawMessage(`{"tool":"write"}`)); err == nil || !strings.Contains(err.Error(), `rumbo has no tool "write" in this run; it has bash, read`) {
		t.Fatalf("a tool rumbo has not = %v", err)
	}

	// A call says the session it is made in.
	if result, err := session.Run(ctx, "/work", json.RawMessage(`{}`)); err != nil || result.Text != "clipped:s1" {
		t.Fatalf("session = %q, %v", result.Text, err)
	}
}

// An extension is told the events of the types it asked for, in order, and
// no others.
func TestAnExtensionIsToldTheEventsItAskedFor(t *testing.T) {
	set := testSet(t)
	programs := []Program{program("fake", `{"tools":["heard"],"events":["objective","tool_finished","ended"]}`), program("deaf", `{"tools":["echo"]}`)}

	offers, err := set.Start(context.Background(), programs, Reserved{})

	if err != nil {
		t.Fatal(err)
	}

	set.Tell(protocol.Event{Type: protocol.EventObjective, Session: "s1", Text: "do it"})
	set.Tell(protocol.Event{Type: protocol.EventText, Session: "s1", Text: "not asked for"})
	set.Tell(protocol.Event{Type: protocol.EventToolFinished, Session: "s1", Name: "bash"})
	set.Tell(protocol.Event{Type: protocol.EventEnded, Session: "s1", Status: protocol.RunDone})

	want := "clipped:objective:s1:do it tool_finished:s1:bash ended:s1:done"

	for range 200 {
		result, err := offers.Tools[1].Run(context.Background(), "/work", json.RawMessage(`{}`))

		if err != nil {
			t.Fatal(err)
		}

		if result.Text == want {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("the extension did not hear what it asked for, as it happened")
}

// person is who follows a run, for the tests: it keeps what it was shown,
// and answers what it is asked, or declines.
type person struct {
	mu     sync.Mutex
	shown  []string
	answer string
}

func (p *person) show(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.shown = append(p.shown, text)
}

// seen is all it was shown, in order.
func (p *person) seen() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return strings.Join(p.shown, "; ")
}

func (p *person) Progress(tool, text string) {
	p.show(tool + ": " + text)
}

func (p *person) Ask(_ context.Context, tool, question string, _ bool) (string, error) {
	p.show(tool + " asks: " + question)

	if p.answer == "" {
		return "", errors.New("the person declined")
	}

	return p.answer, nil
}

func (p *person) Say(text string) {
	p.show("said: " + text)
}

// While it answers a call, an extension tells the person who follows the
// run what it is doing, and asks them; with no one there, asking fails and
// the tool decides what that means.
func TestACallReachesThePersonWhoFollowsTheRun(t *testing.T) {
	set := testSet(t)

	offers, err := set.Start(context.Background(), []Program{program("fake", `{"tools":["confirm"]}`)}, Reserved{})

	if err != nil {
		t.Fatal(err)
	}

	confirm := offers.Tools[0]
	there := &person{answer: "yes"}

	result, err := confirm.Run(tool.WithPerson(context.Background(), there), "/work", json.RawMessage(`{}`))

	if err != nil || result.Text != "clipped:done, since the answer was yes" || there.seen() != "confirm: about to do it; confirm asks: go on?" {
		t.Fatalf("result = %q, %v; shown %q", result.Text, err, there.seen())
	}

	if _, err := confirm.Run(tool.WithPerson(context.Background(), &person{}), "/work", json.RawMessage(`{}`)); err == nil || err.Error() != "clipped:not done: the person declined" {
		t.Fatalf("declined = %v", err)
	}

	if _, err := confirm.Run(context.Background(), "/work", json.RawMessage(`{}`)); err == nil || err.Error() != "clipped:not done: there is no one to ask" {
		t.Fatalf("with no one there = %v", err)
	}
}

// An extension's functions are the person's to call: they are listed by
// extension, and one talks through the dialog it is called with.
func TestAnExtensionsFunctionsTalkWithThePerson(t *testing.T) {
	set := testSet(t)
	programs := []Program{program("zeta", `{"tools":["echo"]}`), program("fake", `{"tools":["find"],"functions":["greet"]}`)}

	if _, err := set.Start(context.Background(), programs, Reserved{}); err != nil {
		t.Fatal(err)
	}

	offered := set.Functions()

	if len(offered) != 1 || offered[0].Name != "fake" || offered[0].Functions[0].Name != "greet" {
		t.Fatalf("offered %+v", offered)
	}

	there := &person{answer: "sesame"}
	asking := dialogOf{there}

	raw, err := set.Invoke(context.Background(), "fake", "greet", json.RawMessage(`{"a":1}`), asking)

	if err != nil || string(raw) != `{"params":"{\"a\":1}","word":"sesame"}` || there.seen() != "said: hello; fake asks: the word" {
		t.Fatalf("greet = %s, %v; shown %q", raw, err, there.seen())
	}

	// With no dialog it can say and ask nothing.
	if _, err := set.Invoke(context.Background(), "fake", "greet", nil, nil); err == nil || !strings.Contains(err.Error(), "there is no one to ask") {
		t.Fatalf("with no dialog = %v", err)
	}

	if _, err := set.Invoke(context.Background(), "gone", "greet", nil, asking); err == nil {
		t.Fatal("a function of an extension there is none of was called")
	}
}

// dialogOf is a person as the Dialog a function is called with.
type dialogOf struct {
	*person
}

func (d dialogOf) Ask(ctx context.Context, question string, secret bool) (string, error) {
	return d.person.Ask(ctx, "fake", question, secret)
}

// An extension that says what it declared has changed is started again
// for the next run, which hears what it declares now.
func TestAnExtensionThatChangedIsStartedAgain(t *testing.T) {
	set := testSet(t)
	programs := []Program{program("fake", `{"tools":["process","change"]}`)}

	first, _ := run(t, set, programs, "process")

	if text, err := run(t, set, programs, "change"); err != nil || text != "clipped:changed" {
		t.Fatalf("change = %q, %v", text, err)
	}

	// The notice comes its own way, a moment after the call's answer.
	for range 200 {
		if again, _ := run(t, set, programs, "process"); again != first {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("the extension said it changed and was not started again")
}
