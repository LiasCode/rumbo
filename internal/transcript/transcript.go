// Package transcript defines what a session's entries mean and rebuilds the
// model request from them. The request is a pure function of the entries, so
// a stored session reproduces exactly what the model was sent at every turn.
package transcript

import (
	"encoding/json"
	"fmt"
	"slices"
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
	KindNote       = "note"
	KindCompaction = "compaction"
)

// Note types. Every run ends with exactly one of done, failed, or cancelled;
// a run with none of them was killed.
const (
	NoteDone      = "done"
	NoteFailed    = "failed"
	NoteCancelled = "cancelled"
	NoteRetry     = "retry"
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
	// Options are the provider options the request was sent with.
	Options json.RawMessage `json:"options,omitempty"`
}

// ToolResult is the outcome of one tool call.
type ToolResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Text    string `json:"text"`
	IsError bool   `json:"is_error,omitempty"`
}

// Note records something that happened in a run and is not part of the
// conversation: how the run ended, or a retried request.
// Notes are never sent to the model.
type Note struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Compaction replaces everything the model saw before it with Text, which
// holds a summary of that conversation. Prompt is the instruction that
// produced the summary, kept so the request can be reproduced.
type Compaction struct {
	Prompt   string          `json:"prompt"`
	Text     string          `json:"text"`
	Usage    protocol.Usage  `json:"usage"`
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	Options  json.RawMessage `json:"options,omitempty"`
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
	// Objective is the one the last run worked on.
	Objective string
	// Unfinished is true when the last run did not end with a done note, so
	// it can be resumed.
	Unfinished bool
	// Tokens is the size of the context as of the last model response, as the
	// provider counted it. It is zero right after a compaction.
	Tokens int64
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
	// Section names in the order they first appeared, so rendering them all
	// again is deterministic.
	order []string
}

func (b *builder) add(entry store.Entry) error {
	switch entry.Kind {
	case KindSystem:
		// Opaque blocks are only valid after the exact prefix that produced
		// them. A new system prompt changes that prefix, so they go.
		b.dropOpaque()

		return json.Unmarshal(entry.Payload, &b.state.System)

	case KindCompaction:
		var compaction Compaction

		if err := json.Unmarshal(entry.Payload, &compaction); err != nil {
			return err
		}

		// The conversation starts over: the ambient context the model still
		// needs, then the summary. Nothing earlier is replayed.
		b.state.Messages = nil
		b.pending = nil
		b.state.Tokens = 0

		if current := b.sections(); len(current) > 0 {
			b.user(protocol.Block{Type: protocol.BlockText, Text: RenderContext(Context{Sections: current})})
		}

		b.user(protocol.Block{Type: protocol.BlockText, Text: compaction.Text})

	case KindContext:
		var context Context

		if err := json.Unmarshal(entry.Payload, &context); err != nil {
			return err
		}

		b.closePending()
		b.user(protocol.Block{Type: protocol.BlockText, Text: RenderContext(context)})

		for _, section := range context.Sections {
			b.set(section)
		}

	case KindNote:
		var note Note

		if err := json.Unmarshal(entry.Payload, &note); err != nil {
			return err
		}

		if note.Type == NoteDone {
			b.state.Unfinished = false
		}

	case KindUser, KindHarness:
		var text Text

		if err := json.Unmarshal(entry.Payload, &text); err != nil {
			return err
		}

		if entry.Kind == KindUser {
			b.state.Objective = text.Text
			b.state.Unfinished = true
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

		usage := assistant.Usage
		b.state.Tokens = usage.Input + usage.CacheRead + usage.CacheWrite + usage.Output

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

// set records the current text of a section, or removes it.
func (b *builder) set(section Section) {
	_, known := b.state.Sections[section.Name]

	if section.Text == "" {
		delete(b.state.Sections, section.Name)

		b.order = slices.DeleteFunc(b.order, func(name string) bool { return name == section.Name })

		return
	}

	if !known {
		b.order = append(b.order, section.Name)
	}

	b.state.Sections[section.Name] = section.Text
}

// sections returns every current section, in a stable order.
func (b *builder) sections() []Section {
	current := make([]Section, len(b.order))

	for index, name := range b.order {
		current[index] = Section{Name: name, Text: b.state.Sections[name]}
	}

	return current
}

func (b *builder) dropOpaque() {
	for index, message := range b.state.Messages {
		b.state.Messages[index].Content = slices.DeleteFunc(message.Content, func(block protocol.Block) bool {
			return block.Type == protocol.BlockOpaque
		})
	}
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
