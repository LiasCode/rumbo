package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// The test binary doubles as rumbo's built-in providers, which "{self}"
// names in a registered command, so tests can start a real one; and as
// twoOptions, a provider of the tests' own, and oneTool, an extension of
// theirs.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "provide" && os.Args[2] == "two-options" {
		if err := protocol.Serve(context.Background(), os.Stdin, os.Stdout, twoOptions{}); err != nil {
			os.Exit(1)
		}

		os.Exit(0)
	}

	if len(os.Args) > 2 && os.Args[1] == "extend" {
		if err := protocol.ServeExtension(context.Background(), os.Stdin, os.Stdout, oneTool(os.Args[2])); err != nil {
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

// oneTool is an extension with one tool, of the name it is, which answers
// with the directory it was called in. It refuses a configuration whose
// broken field is true, and has a section, where, when told is.
type oneTool string

func (name oneTool) Initialize(_ context.Context, params protocol.InitializeParams) (protocol.ExtensionInitializeResult, error) {
	var config struct {
		Broken bool `json:"broken"`
		Told   bool `json:"told"`
	}

	if _ = json.Unmarshal(params.Config, &config); config.Broken {
		return protocol.ExtensionInitializeResult{}, errors.New("broken is not something to turn on")
	}

	tools := []protocol.ToolDef{{Name: string(name), Description: "Says where it was called.\nMore about it.", Schema: json.RawMessage(`{"type":"object"}`)}}

	result := protocol.ExtensionInitializeResult{Protocol: protocol.ExtensionVersion, Name: "one-tool", Tools: tools}

	if config.Told {
		result.Sections = []string{"where"}
	}

	return result, nil
}

func (oneTool) Section(_ context.Context, params protocol.SectionParams) (protocol.SectionResult, error) {
	return protocol.SectionResult{Text: "you are in " + params.Dir}, nil
}

func (oneTool) Call(_ context.Context, params protocol.CallParams, _ protocol.ExtensionHost) (protocol.CallResult, error) {
	return protocol.CallResult{Text: "called in " + params.Dir}, nil
}
