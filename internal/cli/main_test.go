package cli

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// The test binary doubles as rumbo's built-in providers, which "{self}"
// names in a registered command, so tests can start a real one; and as
// twoOptions, a provider of the tests' own.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "provide" && os.Args[2] == "two-options" {
		if err := protocol.Serve(context.Background(), os.Stdin, os.Stdout, twoOptions{}); err != nil {
			os.Exit(1)
		}

		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "provide" {
		os.Exit(Main(os.Args[1:]))
	}

	os.Exit(m.Run())
}

// twoOptions describes two options, so two changes can be told apart, and
// answers nothing else. It is slow to start, as a provider may be, which is
// what leaves two calls that each start one overlapping.
type twoOptions struct{}

func (twoOptions) Initialize(context.Context, protocol.InitializeParams) (protocol.InitializeResult, error) {
	time.Sleep(200 * time.Millisecond)

	return protocol.InitializeResult{
		Protocol: protocol.Version,
		Name:     "two-options",
		Description: protocol.Description{Options: []protocol.Option{
			{Name: "effort", Type: protocol.OptionString},
			{Name: "note", Type: protocol.OptionString},
		}},
	}, nil
}

func (twoOptions) Complete(context.Context, protocol.CompleteParams, func(string)) (protocol.CompleteResult, error) {
	return protocol.CompleteResult{}, errors.New("two-options answers no requests")
}
