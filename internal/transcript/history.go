package transcript

import (
	"encoding/json"
	"fmt"

	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// Compacted is what a session's history says where its conversation was
// replaced by a summary.
const Compacted = "the conversation was summarized to free the model's context"

// History tells what a session holds in the events a front end follows runs
// by, oldest first, so a front end can show a session it opens as if it had
// followed every run: what the person asked, the model's text, the tools it
// called and what they returned, the context sent, the tokens spent, the
// retries, the requests that missed the provider's cache, and how each run
// ended. What only the model sees, such as the system prompt and the
// harness's instructions, is left out, as it is from a run's events.
func History(entries []store.Entry) ([]protocol.Event, error) {
	var told history

	for _, entry := range entries {
		if err := told.add(entry); err != nil {
			return nil, fmt.Errorf("entry %d (%s): %w", entry.Seq, entry.Kind, err)
		}
	}

	told.unanswered()

	return told.events, nil
}

type history struct {
	events []protocol.Event
	// calls are the tool calls of the last response without a result yet,
	// in order. Each is told when its result is, as a run tells it.
	calls []protocol.Block
	// cache is what the last request left in the provider's cache, to tell
	// the responses that missed it as a run does.
	cache Cache
}

func (h *history) add(entry store.Entry) error {
	if entry.Kind != KindToolResult {
		h.unanswered()
	}

	switch entry.Kind {
	case KindSystem:
		// An instruction to the model, which runs do not show either. It
		// changes the start of every request, which the cache held.
		h.cache = Cache{}

	case KindHarness:
		// Instructions to the model, which runs do not show either.

	case KindContext:
		var context Context

		if err := json.Unmarshal(entry.Payload, &context); err != nil {
			return err
		}

		names := make([]string, len(context.Sections))

		for index, section := range context.Sections {
			names[index] = section.Name
		}

		h.tell(protocol.Event{Type: protocol.EventContext, Sections: names})

	case KindUser, KindSteer:
		var text Text

		if err := json.Unmarshal(entry.Payload, &text); err != nil {
			return err
		}

		kind := protocol.EventObjective

		if entry.Kind == KindSteer {
			kind = protocol.EventSteer
		}

		h.tell(protocol.Event{Type: kind, Text: text.Text, Images: text.Images})

	case KindAssistant:
		var assistant Assistant

		if err := json.Unmarshal(entry.Payload, &assistant); err != nil {
			return err
		}

		for _, block := range assistant.Message.Content {
			switch block.Type {
			case protocol.BlockText:
				h.tell(protocol.Event{Type: protocol.EventText, Text: block.Text})
			case protocol.BlockToolCall:
				h.calls = append(h.calls, block)
			}
		}

		usage := assistant.Usage

		h.tell(protocol.Event{
			Type:    protocol.EventTurn,
			Usage:   &usage,
			Context: usage.Input + usage.CacheRead + usage.CacheWrite + usage.Output,
			Window:  assistant.Window,
		})
		h.missed(assistant.Provider, assistant.Model, usage)
		h.cache = cached(assistant.Provider, assistant.Model, usage)

	case KindToolResult:
		var result ToolResult

		if err := json.Unmarshal(entry.Payload, &result); err != nil {
			return err
		}

		for index, call := range h.calls {
			if call.ID == result.ID {
				h.calls = append(h.calls[:index], h.calls[index+1:]...)
				h.tell(protocol.Event{Type: protocol.EventToolStarted, Name: call.Name, Args: call.Args})

				break
			}
		}

		h.tell(protocol.Event{Type: protocol.EventToolFinished, Name: result.Name, Result: result.Text, Failed: result.IsError})

	case KindNote:
		var note Note

		if err := json.Unmarshal(entry.Payload, &note); err != nil {
			return err
		}

		switch note.Type {
		case NoteDone, NoteFailed, NoteCancelled:
			h.tell(protocol.Event{Type: protocol.EventEnded, Status: note.Type, Text: note.Text})
		case NoteRetry:
			h.tell(protocol.Event{Type: protocol.EventLog, Text: note.Text, Source: protocol.TargetHost})
		}

	case KindCompaction:
		var compaction Compaction

		if err := json.Unmarshal(entry.Payload, &compaction); err != nil {
			return err
		}

		usage := compaction.Usage

		h.tell(protocol.Event{Type: protocol.EventLog, Text: Compacted, Source: protocol.TargetHost})
		h.tell(protocol.Event{Type: protocol.EventTurn, Usage: &usage})
		h.missed(compaction.Provider, compaction.Model, usage)

		// The next request starts over from the summary.
		h.cache = Cache{}

	default:
		return fmt.Errorf("unknown kind")
	}

	return nil
}

// unanswered tells the calls that never got a result, as started: their run
// ended, or the process died, before they finished.
func (h *history) unanswered() {
	for _, call := range h.calls {
		h.tell(protocol.Event{Type: protocol.EventToolStarted, Name: call.Name, Args: call.Args})
	}

	h.calls = nil
}

// missed tells a response that did not read all the cache held, as the run
// that received it did.
func (h *history) missed(provider, model string, usage protocol.Usage) {
	if miss := h.cache.Miss(provider, model, usage); miss != "" {
		h.tell(protocol.Event{Type: protocol.EventLog, Text: miss, Source: protocol.TargetHost})
	}
}

func (h *history) tell(event protocol.Event) {
	h.events = append(h.events, event)
}
