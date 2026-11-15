package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// echo is a provider that streams the model name and then answers with it.
// A model named "hang" blocks until cancelled.
type echo struct{}

func (echo) Initialize(context.Context, protocol.InitializeParams) (protocol.InitializeResult, error) {
	return protocol.InitializeResult{Protocol: protocol.Version, Name: "echo"}, nil
}

func (echo) Complete(ctx context.Context, params protocol.CompleteParams, emit func(string)) (protocol.CompleteResult, error) {
	if params.Model == "hang" {
		<-ctx.Done()
		return protocol.CompleteResult{}, ctx.Err()
	}

	if params.Model == "fail" {
		return protocol.CompleteResult{}, errors.New("no credit")
	}

	emit(params.Model)

	return protocol.CompleteResult{
		Message: protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{{Type: protocol.BlockText, Text: params.Model}}},
		Stop:    protocol.StopEnd,
	}, nil
}

// InvokeDialog has one function, login, which talks with the person the way
// a sign-in does: it says where to sign in, asks for the code it gets back,
// and answers with what it was told.
func (echo) InvokeDialog(ctx context.Context, name string, _ json.RawMessage, dialog protocol.Dialog) (any, error) {
	if name != "login" {
		return nil, errors.New("no function " + name)
	}

	dialog.Say("open https://example.test/device and sign in")

	code, err := dialog.Ask(ctx, "the code the page shows", true)

	if err != nil {
		return nil, fmt.Errorf("signing in was given up: %w", err)
	}

	return map[string]string{"signed_in_with": code}, nil
}

// The test binary doubles as the provider process.
func TestMain(m *testing.M) {
	if os.Getenv("RUMBO_TEST_PROVIDER") == "1" {
		if err := protocol.Serve(context.Background(), os.Stdin, os.Stdout, echo{}); err != nil {
			os.Exit(1)
		}

		os.Exit(0)
	}

	os.Exit(m.Run())
}

func TestStdioRoundTrip(t *testing.T) {
	t.Setenv("RUMBO_TEST_PROVIDER", "1")

	process, err := Start(context.Background(), []string{os.Args[0]}, nil, io.Discard)

	if err != nil {
		t.Fatal(err)
	}

	defer process.Close()

	var streamed strings.Builder
	result, err := process.Complete(context.Background(), protocol.CompleteParams{Model: "hello"}, func(text string) { streamed.WriteString(text) })

	if err != nil {
		t.Fatal(err)
	}

	if streamed.String() != "hello" || result.Message.Content[0].Text != "hello" {
		t.Fatalf("streamed %q, result %+v", streamed.String(), result)
	}

	if _, err := process.Complete(context.Background(), protocol.CompleteParams{Model: "fail"}, nil); err == nil || err.Error() != "no credit" {
		t.Fatalf("provider error not propagated: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()

	if _, err := process.Complete(ctx, protocol.CompleteParams{Model: "hang"}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: err = %v", err)
	}

	if time.Since(started) > 2*time.Second {
		t.Fatal("cancel was not forwarded to the provider")
	}

	// The connection is still usable after a cancelled request.
	if _, err := process.Complete(context.Background(), protocol.CompleteParams{Model: "again"}, nil); err != nil {
		t.Fatal(err)
	}
}

// person is a Dialog that keeps what it is told, and answers questions with
// answer, or waits until the call ends when answer is empty.
type person struct {
	mu     sync.Mutex
	said   []string
	asked  []string
	answer string
}

func (p *person) Say(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.said = append(p.said, text)
}

func (p *person) Ask(ctx context.Context, question string, secret bool) (string, error) {
	p.mu.Lock()
	p.asked = append(p.asked, fmt.Sprintf("%s (secret %t)", question, secret))
	p.mu.Unlock()

	if p.answer == "" {
		<-ctx.Done()
		return "", ctx.Err()
	}

	return p.answer, nil
}

// A function talks with the person while it works: what it says and asks
// reaches the dialog, and the answer reaches the function.
func TestAFunctionTalksWithThePerson(t *testing.T) {
	t.Setenv("RUMBO_TEST_PROVIDER", "1")

	process, err := Start(context.Background(), []string{os.Args[0]}, nil, io.Discard)

	if err != nil {
		t.Fatal(err)
	}

	defer process.Close()

	someone := &person{answer: "4711"}
	result, err := process.Invoke(context.Background(), "login", nil, someone)

	if err != nil || string(result) != `{"signed_in_with":"4711"}` {
		t.Fatalf("result = %s, err = %v", result, err)
	}

	if len(someone.said) != 1 || !strings.Contains(someone.said[0], "sign in") || len(someone.asked) != 1 || someone.asked[0] != "the code the page shows (secret true)" {
		t.Fatalf("said %q, asked %q", someone.said, someone.asked)
	}

	// Without a dialog the function's question is declined.
	if _, err := process.Invoke(context.Background(), "login", nil, nil); err == nil || !strings.Contains(err.Error(), "no one to ask") {
		t.Fatalf("a question with nobody to answer it: err = %v", err)
	}

	// A function waiting for an answer is cancelled like a completion.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()

	if _, err := process.Invoke(ctx, "login", nil, &person{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelling a function that waits: err = %v", err)
	}

	if time.Since(started) > 2*time.Second {
		t.Fatal("the cancel did not reach the function")
	}

	if _, err := process.Invoke(context.Background(), "login", nil, someone); err != nil {
		t.Fatalf("after a cancelled function: %v", err)
	}
}
