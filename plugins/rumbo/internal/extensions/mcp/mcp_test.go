package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// serverTools are what the tests' server lists, two to a page.
var serverTools = []map[string]any{
	{"name": "echo", "description": "Says it back", "inputSchema": map[string]any{"type": "object"}},
	{"name": "untold", "title": "Has a title only"},
	{"name": "mixed", "description": "Every kind of content"},
	{"name": "hang", "description": "Never answers"},
	{"name": "leave", "description": "Exits"},
}

// respond is the tests' MCP server: what it answers to one request, and
// nothing for a notification.
func respond(asked message) *message {
	if asked.ID == nil {
		return nil
	}

	answer := &message{JSONRPC: "2.0", ID: asked.ID}
	result := func(value any) *message {
		answer.Result, _ = json.Marshal(value)

		return answer
	}

	switch asked.Method {
	case "initialize":
		return result(map[string]any{"protocolVersion": "2025-03-26", "serverInfo": map[string]string{"name": "fake"}})

	case "tools/list":
		var params struct {
			Cursor string `json:"cursor"`
		}

		_ = json.Unmarshal(asked.Params, &params)

		start := 0
		fmt.Sscan(params.Cursor, &start)

		page := map[string]any{"tools": serverTools[start:min(start+2, len(serverTools))]}

		if start+2 < len(serverTools) {
			page["nextCursor"] = fmt.Sprint(start + 2)
		}

		return result(page)

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}

		_ = json.Unmarshal(asked.Params, &params)

		switch params.Name {
		case "echo":
			return result(map[string]any{"content": []map[string]any{{"type": "text", "text": string(params.Arguments)}}})

		case "untold":
			return result(map[string]any{"structuredContent": map[string]int{"n": 1}, "isError": true})

		case "mixed":
			return result(map[string]any{"content": []map[string]any{
				{"type": "text", "text": "one"},
				{"type": "image", "data": "aGk=", "mimeType": "image/png"},
				{"type": "resource", "resource": map[string]string{"uri": "file:///a", "text": "two"}},
				{"type": "resource", "resource": map[string]string{"uri": "file:///b", "blob": "AA=="}},
				{"type": "resource_link", "uri": "file:///c"},
				{"type": "audio", "data": "AA==", "mimeType": "audio/wav"},
			}})

		case "leave":
			os.Exit(0)
		}
	}

	answer.Error = &rpcError{Code: codeMethodNotFound, Message: "no " + asked.Method}

	return answer
}

// The test binary doubles as an MCP server on stdin and stdout. It pings
// its client before answering initialize, as a server may, and a call to
// hang is never answered; what it is told meanwhile goes to stderr.
func TestMain(m *testing.M) {
	if os.Getenv("RUMBO_TEST_MCP") != "1" {
		os.Exit(m.Run())
	}

	reader := bufio.NewReader(os.Stdin)
	out := json.NewEncoder(os.Stdout)

	for {
		line, err := reader.ReadBytes('\n')

		var asked message

		if json.Unmarshal(line, &asked) == nil {
			switch {
			case asked.Method == "initialize":
				_ = out.Encode(message{JSONRPC: "2.0", ID: json.RawMessage(`"p1"`), Method: "ping"})
				_ = out.Encode(respond(asked))

			case asked.Method == "tools/call" && strings.Contains(string(asked.Params), `"hang"`):

			case asked.Method == "" || asked.ID == nil:
				fmt.Fprintf(os.Stderr, "told %s%s%s\n", asked.Method, asked.ID, asked.Result)

			default:
				_ = out.Encode(respond(asked))
			}
		}

		if err != nil {
			os.Exit(0)
		}
	}
}

// told collects what the stdio server wrote to stderr.
type told struct {
	mu   sync.Mutex
	text strings.Builder
}

func (t *told) Write(data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.text.Write(data)
}

func (t *told) has(text string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return strings.Contains(t.text.String(), text)
}

// eventually waits for something another process does.
func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()

	for range 200 {
		if done() {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal(what)
}

func overStdio(t *testing.T, config string) (*Adapter, protocol.ExtensionInitializeResult, *told) {
	t.Helper()
	t.Setenv("RUMBO_TEST_MCP", "1")

	heard := &told{}
	adapter := New([]string{os.Args[0]}, "test", heard)
	t.Cleanup(adapter.Close)

	result, err := adapter.Initialize(context.Background(), protocol.InitializeParams{Config: json.RawMessage(config)})

	if err != nil {
		t.Fatal(err)
	}

	return adapter, result, heard
}

func call(t *testing.T, adapter *Adapter, name, args string) protocol.CallResult {
	t.Helper()

	result, err := adapter.Call(context.Background(), protocol.CallParams{Name: name, Args: json.RawMessage(args)}, nil)

	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	return result
}

func TestAServersToolsAreTheModels(t *testing.T) {
	adapter, declared, heard := overStdio(t, "")

	if declared.Name != "fake" || len(declared.Tools) != len(serverTools) {
		t.Fatalf("declared %+v", declared)
	}

	if untold := declared.Tools[1]; untold.Description != "Has a title only" || string(untold.Schema) != `{"type":"object"}` {
		t.Fatalf("a tool with a title and no schema was declared as %+v", untold)
	}

	// The server's ping was answered, and it was told the session is open.
	eventually(t, "the server heard no answer to its ping", func() bool { return heard.has(`told "p1"{}`) })
	eventually(t, "the server was not told it is initialized", func() bool { return heard.has("told notifications/initialized") })

	if said := call(t, adapter, "echo", `{"a":1}`); said.Text != `{"a":1}` || said.IsError {
		t.Fatalf("echo = %+v", said)
	}

	if said := call(t, adapter, "untold", ""); said.Text != `{"n":1}` || !said.IsError {
		t.Fatalf("a failed result given as data = %+v", said)
	}

	mixed := call(t, adapter, "mixed", "{}")
	want := "one\ntwo\n[resource file:///b, which is not text]\n[resource file:///c]\n[audio content, which cannot be shown]"

	if mixed.Text != want || len(mixed.Images) != 1 || string(mixed.Images[0].Data) != "hi" || mixed.Images[0].MediaType != "image/png" {
		t.Fatalf("mixed = %q, images %+v", mixed.Text, mixed.Images)
	}

	if _, err := adapter.Call(context.Background(), protocol.CallParams{Name: "other"}, nil); err == nil {
		t.Fatal("a tool that was not declared was called")
	}
}

func TestACancelledCallTellsTheServerAndALostServerIsSaid(t *testing.T) {
	adapter, _, heard := overStdio(t, "")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := adapter.Call(ctx, protocol.CallParams{Name: "hang"}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hang = %v", err)
	}

	eventually(t, "the server was not told of the cancel", func() bool { return heard.has("told notifications/cancelled") })

	select {
	case <-adapter.Lost():
		t.Fatal("the server is said to be lost while it runs")
	default:
	}

	if _, err := adapter.Call(context.Background(), protocol.CallParams{Name: "leave"}, nil); err == nil || err.Error() != "the MCP server exited" {
		t.Fatalf("leave = %v", err)
	}

	select {
	case <-adapter.Lost():
	case <-time.After(2 * time.Second):
		t.Fatal("the server exited and was not said to be lost")
	}
}

func TestTheConfigurationChoosesAndNamesTheTools(t *testing.T) {
	adapter, declared, _ := overStdio(t, `{"prefix":"fake_","tools":"mixed, echo"}`)

	if len(declared.Tools) != 2 || declared.Tools[0].Name != "fake_echo" || declared.Tools[1].Name != "fake_mixed" {
		t.Fatalf("declared %+v", declared.Tools)
	}

	if said := call(t, adapter, "fake_echo", `{}`); said.Text != "{}" {
		t.Fatalf("fake_echo = %+v", said)
	}

	// What lists the tools lists every one the server has, by the name the
	// tools field takes, those chosen marked, for a front end to choose
	// among.
	listed, err := adapter.InvokeDialog(context.Background(), "tools", nil, nil)

	if err != nil {
		t.Fatal(err)
	}

	var names []string

	for _, value := range listed.(protocol.ValuesResult).Values {
		names = append(names, strings.Join(strings.Fields(value.Value+" "+value.Label+" "+value.Description), " "))
	}

	if want := []string{"echo chosen Says it back", "untold Has a title only", "mixed chosen Every kind of content", "hang Never answers", "leave Exits"}; !slices.Equal(names, want) {
		t.Fatalf("listed %q", names)
	}

	t.Setenv("RUMBO_TEST_TOKEN", "")

	refused := map[string]struct {
		command []string
		config  string
		want    string
	}{
		"a misspelled field":   {[]string{os.Args[0]}, `{"prefx":"a"}`, `unknown field "prefx"; its fields are url, token_env, client_id, prefix, and tools`},
		"a tool it has not":    {[]string{os.Args[0]}, `{"tools":"echo,sum"}`, `tools names "sum", which the server does not have; it has echo, hang, leave, mixed, untold`},
		"both ways":            {[]string{os.Args[0]}, `{"url":"http://x"}`, "the server is given twice"},
		"neither way":          {nil, ``, "no server"},
		"a token for a child":  {[]string{os.Args[0]}, `{"token_env":"X"}`, "token_env and client_id are for a server reached at a url"},
		"a token that is not":  {nil, `{"url":"http://x","token_env":"RUMBO_TEST_TOKEN"}`, "token_env names RUMBO_TEST_TOKEN, which is not set"},
		"a command that fails": {[]string{"/nonexistent/server"}, ``, "start the MCP server"},
	}

	for name, each := range refused {
		t.Run(name, func(t *testing.T) {
			adapter := New(each.command, "test", io.Discard)
			defer adapter.Close()

			_, err := adapter.Initialize(context.Background(), protocol.InitializeParams{Config: json.RawMessage(each.config)})

			if err == nil || !strings.Contains(err.Error(), each.want) {
				t.Fatalf("err = %v, want %q in it", err, each.want)
			}
		})
	}
}

// Over HTTP a server answers with JSON or with a stream of events, keeps a
// session, and takes a token; the adapter gives back what it was given.
func TestAServerIsReachedOverHTTP(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		t.Run(fmt.Sprintf("streamed=%v", streamed), func(t *testing.T) {
			var mu sync.Mutex
			var ended bool
			var versions []string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer s3cret" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				if r.Method == http.MethodDelete {
					mu.Lock()
					ended = r.Header.Get("Mcp-Session-Id") == "session-1"
					mu.Unlock()

					return
				}

				var asked message

				_ = json.NewDecoder(r.Body).Decode(&asked)

				if asked.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "session-1" {
					http.Error(w, "no session", http.StatusBadRequest)
					return
				}

				mu.Lock()
				versions = append(versions, r.Header.Get("MCP-Protocol-Version"))
				mu.Unlock()

				answer := respond(asked)

				if answer == nil {
					w.WriteHeader(http.StatusAccepted)
					return
				}

				w.Header().Set("Mcp-Session-Id", "session-1")

				raw, _ := json.Marshal(answer)

				if !streamed {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(raw)

					return
				}

				// Something else comes before the answer, as progress does.
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
				fmt.Fprintf(w, "event: message\r\ndata: %s\r\n\r\n", raw)
			}))

			defer server.Close()

			t.Setenv("RUMBO_TEST_TOKEN", "s3cret")

			adapter := New(nil, "test", io.Discard)
			config := fmt.Sprintf(`{"url":%q,"token_env":"RUMBO_TEST_TOKEN","tools":"echo"}`, server.URL)

			declared, err := adapter.Initialize(context.Background(), protocol.InitializeParams{Config: json.RawMessage(config)})

			if err != nil || len(declared.Tools) != 1 {
				t.Fatalf("declared %+v, err = %v", declared, err)
			}

			if said := call(t, adapter, "echo", `{"over":"http"}`); said.Text != `{"over":"http"}` {
				t.Fatalf("echo = %+v", said)
			}

			adapter.Close()

			mu.Lock()
			defer mu.Unlock()

			if !ended {
				t.Fatal("the session was not ended")
			}

			// The version the server answered goes with every request after it.
			if versions[0] != "" || versions[len(versions)-1] != "2025-03-26" {
				t.Fatalf("versions sent: %q", versions)
			}
		})
	}

	// A server that refuses for lack of a token, and says nothing of where
	// to sign in, is offered a sign-in that says what it lacks.
	t.Run("without its token", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
		defer server.Close()

		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		adapter := New(nil, "test", io.Discard)
		declared, err := adapter.Initialize(context.Background(), protocol.InitializeParams{Config: json.RawMessage(fmt.Sprintf(`{"url":%q}`, server.URL))})

		if err != nil || len(declared.Tools) != 0 || len(declared.Functions) != 1 {
			t.Fatalf("declared %+v, err = %v", declared, err)
		}

		_, err = adapter.InvokeDialog(context.Background(), "login", nil, nil)

		if err == nil || !strings.Contains(err.Error(), "does not say where to sign in") || !strings.Contains(err.Error(), "token_env names the environment variable") {
			t.Fatalf("err = %v", err)
		}
	})
}
