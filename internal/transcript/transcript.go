// Package transcript defines what a session's entries mean and rebuilds the
// model request from them. The request is a pure function of the entries, so
// a stored session reproduces exactly what the model was sent at every turn.
package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"rumbo/internal/store"
	"rumbo/protocol"
)

// Entry kinds.
const (
	KindSystem     = "system"
	KindContext    = "context"
	KindUser       = "user"
	KindAssistant  = "assistant"
	KindToolResult = "tool_result"
	KindHarness    = "harness"
)

// System is the system prompt and tool declarations in force from this entry
// on.
type System struct {
	Text  string             `json:"text"`
	Tools []protocol.ToolDef `json:"tools"`
}

// Section is one named piece of ambient context. An empty Text removes the
// section.
type Section struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Context carries the ambient sections that were added or changed.
type Context struct {
	Sections []Section `json:"sections"`
}

// Text is the payload of user and harness entries.
type Text struct {
	Text string `json:"text"`
}

// Assistant is a model response and where it came from.
type Assistant struct {
	Message  protocol.Message `json:"message"`
	Stop     string           `json:"stop"`
	Usage    protocol.Usage   `json:"usage"`
	Provider string           `json:"provider"`
	Model    string           `json:"model"`
}

// ToolResult is the outcome of one tool call.
type ToolResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Text    string `json:"text"`
	IsError bool   `json:"is_error,omitempty"`
}

// Interrupted is the result given to a tool call that never got one, for
// example because the run was cancelled. Providers reject a history where a
// call has no result.
const Interrupted = "[interrupted before this tool call finished]"

// State is what the entries of a session add up to.
type State struct {
	System   System
	Messages []protocol.Message
	Sections map[string]string
}

// Build replays entries into the current system prompt, tools, messages, and
// ambient sections.
func Build(entries []store.Entry) (State, error) {
	builder := builder{state: State{Sections: map[string]string{}}}

	for _, entry := range entries {
		if err := builder.add(entry); err != nil {
			return State{}, fmt.Errorf("entry %d (%s): %w", entry.Seq, entry.Kind, err)
		}
	}

	builder.closePending()

	return builder.state, nil
}

type builder struct {
	state State
	// Tool calls of the last assistant message that have no result yet.
	pending []protocol.Block
}

func (b *builder) add(entry store.Entry) error {
	switch entry.Kind {
	case KindSystem:
		return json.Unmarshal(entry.Payload, &b.state.System)

	case KindContext:
		var context Context

		if err := json.Unmarshal(entry.Payload, &context); err != nil {
			return err
		}

		b.closePending()
		b.user(protocol.Block{Type: protocol.BlockText, Text: RenderContext(context)})

		for _, section := range context.Sections {
			if section.Text == "" {
				delete(b.state.Sections, section.Name)
			} else {
				b.state.Sections[section.Name] = section.Text
			}
		}

	case KindUser, KindHarness:
		var text Text

		if err := json.Unmarshal(entry.Payload, &text); err != nil {
			return err
		}

		b.closePending()
		b.user(protocol.Block{Type: protocol.BlockText, Text: text.Text})

	case KindAssistant:
		var assistant Assistant

		if err := json.Unmarshal(entry.Payload, &assistant); err != nil {
			return err
		}

		b.closePending()
		b.state.Messages = append(b.state.Messages, assistant.Message)

		for _, block := range assistant.Message.Content {
			if block.Type == protocol.BlockToolCall {
				b.pending = append(b.pending, block)
			}
		}

	case KindToolResult:
		var result ToolResult

		if err := json.Unmarshal(entry.Payload, &result); err != nil {
			return err
		}

		for index, call := range b.pending {
			if call.ID == result.ID {
				b.pending = append(b.pending[:index], b.pending[index+1:]...)
				break
			}
		}

		b.user(protocol.Block{Type: protocol.BlockToolResult, ID: result.ID, Text: result.Text, IsError: result.IsError})

	default:
		return fmt.Errorf("unknown kind")
	}

	return nil
}

// user appends a block to the current user message, starting one if the last
// message is not from the user. Roles must alternate.
func (b *builder) user(block protocol.Block) {
	last := len(b.state.Messages) - 1

	if last >= 0 && b.state.Messages[last].Role == protocol.RoleUser {
		b.state.Messages[last].Content = append(b.state.Messages[last].Content, block)
		return
	}

	b.state.Messages = append(b.state.Messages, protocol.Message{Role: protocol.RoleUser, Content: []protocol.Block{block}})
}

func (b *builder) closePending() {
	for _, call := range b.pending {
		b.user(protocol.Block{Type: protocol.BlockToolResult, ID: call.ID, Text: Interrupted, IsError: true})
	}

	b.pending = nil
}

// RenderContext is the text the model sees for a context entry.
func RenderContext(context Context) string {
	var out strings.Builder

	for index, section := range context.Sections {
		if index > 0 {
			out.WriteString("\n")
		}

		if section.Text == "" {
			fmt.Fprintf(&out, "<context name=%q removed=\"true\"/>\n", section.Name)
			continue
		}

		fmt.Fprintf(&out, "<context name=%q>\n%s\n</context>\n", section.Name, strings.TrimRight(section.Text, "\n"))
	}

	return strings.TrimRight(out.String(), "\n")
}
