package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LiasCode/rumbo/internal/procgroup"
)

// How long a server gets to exit once it is told to, and a request that
// only tells it something gets to be taken.
const gracePeriod = 3 * time.Second

// message is a JSON-RPC 2.0 message as MCP has them. An id is a number or a
// string, as its sender chose, so it is kept as written.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return e.Message
}

const codeMethodNotFound = -32601

// conn is a connection to an MCP server, over either of its transports.
type conn interface {
	// request sends a request and waits for its result. When ctx ends first
	// the server is told the request was cancelled.
	request(ctx context.Context, method string, params, result any) error
	notify(method string, params any) error
	// negotiated tells the connection the protocol version the server
	// answered initialize with.
	negotiated(version string)
	// lost is closed when the server is gone for good.
	lost() <-chan struct{}
	close()
}

// encode writes a request or a notification; a notification has no id.
func encode(id int64, method string, params any) ([]byte, error) {
	raw, err := json.Marshal(params)

	if err != nil {
		return nil, err
	}

	sent := message{JSONRPC: "2.0", Method: method, Params: raw}

	if id != 0 {
		sent.ID = json.RawMessage(strconv.FormatInt(id, 10))
	}

	return json.Marshal(sent)
}

// decode reads the answer to a request into result.
func decode(answer message, result any) error {
	if answer.Error != nil {
		return answer.Error
	}

	if result == nil {
		return nil
	}

	return json.Unmarshal(answer.Result, result)
}

// cancelled is what tells a server a request is no longer waited for.
func cancelled(id int64) (string, any) {
	return "notifications/cancelled", map[string]any{"requestId": id, "reason": "the run was cancelled"}
}

// stdioConn is a server started as a child process, spoken to over its
// stdin and stdout, one message to a line.
type stdioConn struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	gone  chan struct{}

	mu      sync.Mutex
	nextID  int64
	waiting map[int64]chan message
}

// startStdio runs command as an MCP server. Its stderr goes to stderr.
func startStdio(command []string, stderr io.Writer) (*stdioConn, error) {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stderr = stderr
	// A group of its own, so that what the server started goes with it.
	procgroup.Own(cmd)

	stdin, err := cmd.StdinPipe()

	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()

	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the MCP server %q: %w", command[0], err)
	}

	started := &stdioConn{cmd: cmd, stdin: stdin, gone: make(chan struct{}), waiting: map[int64]chan message{}}

	go started.read(stdout)

	return started, nil
}

func (c *stdioConn) negotiated(string) {}

func (c *stdioConn) lost() <-chan struct{} {
	return c.gone
}

func (c *stdioConn) request(ctx context.Context, method string, params, result any) error {
	answer := make(chan message, 1)

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.waiting[id] = answer
	err := c.write(id, method, params)
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.waiting, id)
		c.mu.Unlock()
	}()

	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		_ = c.notify(cancelled(id))

		return ctx.Err()

	case received := <-answer:
		return decode(received, result)

	case <-c.gone:
		// The answer may have arrived just before the server went.
		select {
		case received := <-answer:
			return decode(received, result)
		default:
			return errors.New("the MCP server exited")
		}
	}
}

func (c *stdioConn) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.write(0, method, params)
}

// write sends one message. The caller holds the lock, so lines do not mix.
func (c *stdioConn) write(id int64, method string, params any) error {
	line, err := encode(id, method, params)

	if err != nil {
		return err
	}

	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("the MCP server could not be reached: %w", err)
	}

	return nil
}

// read gives each response to the request that waits for it, and answers
// what the server asks: a ping, and that nothing else is offered.
func (c *stdioConn) read(stdout io.Reader) {
	defer close(c.gone)

	reader := bufio.NewReader(stdout)

	for {
		line, err := reader.ReadBytes('\n')

		var received message

		if len(bytes.TrimSpace(line)) > 0 && json.Unmarshal(line, &received) == nil {
			switch {
			case received.Method != "" && received.ID != nil:
				c.answer(received)

			case received.Method == "" && received.ID != nil:
				id, _ := strconv.ParseInt(string(received.ID), 10, 64)

				c.mu.Lock()
				answer := c.waiting[id]
				c.mu.Unlock()

				if answer != nil {
					select {
					case answer <- received:
					default:
					}
				}
			}
		}

		if err != nil {
			return
		}
	}
}

// answer replies to a request of the server's.
func (c *stdioConn) answer(asked message) {
	reply := message{JSONRPC: "2.0", ID: asked.ID}

	if asked.Method == "ping" {
		reply.Result = json.RawMessage("{}")
	} else {
		reply.Error = &rpcError{Code: codeMethodNotFound, Message: "rumbo does not offer " + asked.Method}
	}

	line, _ := json.Marshal(reply)

	c.mu.Lock()
	defer c.mu.Unlock()

	_, _ = c.stdin.Write(append(line, '\n'))
}

// close ends the server as MCP asks of this transport: its stdin closes,
// and it is killed, with what it started, if that does not end it.
func (c *stdioConn) close() {
	_ = c.stdin.Close()

	done := make(chan struct{})

	go func() {
		_ = c.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(gracePeriod):
		_ = procgroup.Kill(c.cmd.Process.Pid)
		<-done
	}
}

// bearer gives the token a server takes with each request, and none for a
// server that takes none.
type bearer interface {
	token(ctx context.Context) (string, error)
	// refused is told the server did not take the token. It gets another,
	// or says why there is none to get.
	refused(ctx context.Context) error
}

// fixed is a token that does not change: the one an environment variable
// holds, or none.
type fixed string

func (f fixed) token(context.Context) (string, error) {
	return string(f), nil
}

func (f fixed) refused(context.Context) error {
	if f == "" {
		return errors.New("it takes a token, which token_env names the environment variable of")
	}

	return errors.New("it did not take the token token_env names")
}

// httpConn is a server reached over MCP's Streamable HTTP transport: each
// message is a POST, answered with JSON or with a stream of events that
// holds the answer.
type httpConn struct {
	url    string
	auth   bearer
	client *http.Client

	mu      sync.Mutex
	nextID  int64
	session string
	version string
}

func newHTTP(url string, auth bearer) *httpConn {
	return &httpConn{url: url, auth: auth, client: &http.Client{}}
}

func (c *httpConn) negotiated(version string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.version = version
}

// lost never closes: each request reaches the server anew.
func (c *httpConn) lost() <-chan struct{} {
	return nil
}

func (c *httpConn) request(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	body, err := encode(id, method, params)

	if err != nil {
		return err
	}

	response, err := c.post(ctx, body)

	// A token the server no longer takes is renewed, once, and the request
	// sent again with the new one.
	if _, refused := errors.AsType[*unauthorized](err); refused && c.auth.refused(ctx) == nil {
		response, err = c.post(ctx, body)
	}

	if err != nil {
		if ctx.Err() != nil {
			_ = c.notify(cancelled(id))

			return ctx.Err()
		}

		return err
	}

	defer response.Body.Close()

	answer, err := c.answer(response, id)

	if err != nil {
		if ctx.Err() != nil {
			_ = c.notify(cancelled(id))

			return ctx.Err()
		}

		return err
	}

	return decode(answer, result)
}

func (c *httpConn) notify(method string, params any) error {
	body, err := encode(0, method, params)

	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), gracePeriod)
	defer cancel()

	response, err := c.post(ctx, body)

	if err != nil {
		return err
	}

	return response.Body.Close()
}

// post sends one message, with what the server asked to be given back, and
// returns its response once the server took the message.
func (c *httpConn) post(ctx context.Context, body []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))

	if err != nil {
		return nil, err
	}

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	if err := c.identify(ctx, request); err != nil {
		return nil, err
	}

	response, err := c.client.Do(request)

	if err != nil {
		return nil, fmt.Errorf("the MCP server could not be reached: %w", err)
	}

	if session := response.Header.Get("Mcp-Session-Id"); session != "" {
		c.mu.Lock()
		c.session = session
		c.mu.Unlock()
	}

	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}

	defer response.Body.Close()

	// What it says with the refusal names where to sign in, when it says.
	if response.StatusCode == http.StatusUnauthorized {
		refusal := &unauthorized{status: response.Status}

		if found := resourceMetadata.FindStringSubmatch(response.Header.Get("WWW-Authenticate")); found != nil {
			refusal.hint = found[1]
		}

		return nil, refusal
	}

	said, _ := io.ReadAll(io.LimitReader(response.Body, 500))

	return nil, fmt.Errorf("the MCP server answered %s: %s", response.Status, strings.TrimSpace(string(said)))
}

// identify adds to a request who sends it and what was agreed with the
// server.
func (c *httpConn) identify(ctx context.Context, request *http.Request) error {
	token, err := c.auth.token(ctx)

	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	if c.session != "" {
		request.Header.Set("Mcp-Session-Id", c.session)
	}

	if c.version != "" {
		request.Header.Set("MCP-Protocol-Version", c.version)
	}

	return nil
}

// answer reads the answer to the request with id from a response: its
// body, or the event of its stream that holds it. What else the stream
// carries, such as progress, is passed over.
func (c *httpConn) answer(response *http.Response, id int64) (message, error) {
	wanted := strconv.FormatInt(id, 10)

	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		var answer message

		if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
			return message{}, fmt.Errorf("the MCP server's answer is not JSON: %w", err)
		}

		return answer, nil
	}

	reader := bufio.NewReader(response.Body)

	var data strings.Builder

	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")

		if payload, isData := strings.CutPrefix(line, "data:"); isData {
			data.WriteString(strings.TrimPrefix(payload, " "))
			data.WriteByte('\n')
		}

		// A blank line ends an event, and so does the end of the stream.
		if line == "" || err != nil {
			var received message

			if json.Unmarshal([]byte(data.String()), &received) == nil && received.Method == "" && string(received.ID) == wanted {
				return received, nil
			}

			data.Reset()
		}

		if err != nil {
			return message{}, errors.New("the MCP server closed the stream before it answered")
		}
	}
}

// close ends the session, which a server that keeps none refuses, and that
// is fine.
func (c *httpConn) close() {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()

	if session == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), gracePeriod)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)

	if err != nil {
		return
	}

	if c.identify(ctx, request) != nil {
		return
	}

	if response, err := c.client.Do(request); err == nil {
		_ = response.Body.Close()
	}
}

// tokenFrom reads the token in the environment variable called name.
func tokenFrom(name string) (string, error) {
	if name == "" {
		return "", nil
	}

	token := os.Getenv(name)

	if token == "" {
		return "", fmt.Errorf("token_env names %s, which is not set in the environment", name)
	}

	return token, nil
}
