package provider

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"rumbo/protocol"
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
