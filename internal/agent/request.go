package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/transcript"
	"github.com/LiasCode/rumbo/protocol"
)

// request is what the provider is sent: the conversation of state, or the
// messages made from it, with the system prompt and tools in force.
func request(state transcript.State, messages []protocol.Message, model string, maxTokens int, options json.RawMessage) protocol.CompleteParams {
	return protocol.CompleteParams{
		Model:     model,
		System:    state.System.Text,
		Messages:  messages,
		Tools:     state.System.Tools,
		MaxTokens: maxTokens,
		Options:   options,
	}
}

// summaryRequest is the conversation with the instruction that asks for its
// summary. The instruction joins the last user message when there is one,
// since roles must alternate.
func summaryRequest(conversation []protocol.Message, instruction string) []protocol.Message {
	messages := slices.Clone(conversation)
	block := protocol.Block{Type: protocol.BlockText, Text: instruction}
	last := len(messages) - 1

	if last >= 0 && messages[last].Role == protocol.RoleUser {
		messages[last].Content = append(slices.Clone(messages[last].Content), block)
	} else {
		messages = append(messages, protocol.Message{Role: protocol.RoleUser, Content: []protocol.Block{block}})
	}

	return messages
}

// withoutLastStep returns the conversation before the model's last message
// and what answered it, or false when that would leave nothing.
func withoutLastStep(messages []protocol.Message) ([]protocol.Message, bool) {
	last := len(messages) - 1

	for last >= 0 && messages[last].Role != protocol.RoleAssistant {
		last--
	}

	if last < 1 {
		return nil, false
	}

	return messages[:last], true
}

// Request rebuilds what the provider was sent for a turn of a session, from
// the entries before its response, with the code the loop sends requests
// with: the proof that a stored session reproduces every request. Turns are
// the responses, counted from 1, as transcript.Summarize counts them. It
// also returns what the response recorded of the request, which is empty
// for one recorded before rumbo kept it.
func Request(entries []store.Entry, turn int) (protocol.CompleteParams, transcript.Sent, error) {
	if turns := transcript.Summarize(entries).Turns; turn < 1 || turn > turns {
		return protocol.CompleteParams{}, transcript.Sent{}, fmt.Errorf("there is no turn %d: the session has %d", turn, turns)
	}

	count := 0

	for index, entry := range entries {
		if !transcript.IsResponse(entry.Kind) {
			continue
		}

		if count++; count < turn {
			continue
		}

		state, err := transcript.Build(entries[:index])

		if err != nil {
			return protocol.CompleteParams{}, transcript.Sent{}, err
		}

		if entry.Kind == transcript.KindAssistant {
			var answer transcript.Assistant

			if err := json.Unmarshal(entry.Payload, &answer); err != nil {
				return protocol.CompleteParams{}, transcript.Sent{}, fmt.Errorf("entry %d: %w", entry.Seq, err)
			}

			return request(state, state.Messages, answer.Model, answer.MaxTokens, answer.Options), answer.Sent, nil
		}

		var compaction transcript.Compaction

		if err := json.Unmarshal(entry.Payload, &compaction); err != nil {
			return protocol.CompleteParams{}, transcript.Sent{}, fmt.Errorf("entry %d: %w", entry.Seq, err)
		}

		// A summary made because a request did not fit was asked for
		// without the step that tipped it over, and its text says so.
		conversation := state.Messages

		if strings.HasSuffix(compaction.Text, shedNote) {
			conversation, _ = withoutLastStep(conversation)
		}

		messages := summaryRequest(conversation, compaction.Prompt)

		return request(state, messages, compaction.Model, compaction.MaxTokens, compaction.Options), compaction.Sent, nil
	}

	return protocol.CompleteParams{}, transcript.Sent{}, fmt.Errorf("there is no turn %d", turn)
}
