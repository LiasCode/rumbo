package openai

import (
	"encoding/json"
	"testing"

	"github.com/LiasCode/rumbo/protocol"
)

var withImage = protocol.Message{Role: protocol.RoleUser, Content: []protocol.Block{{
	Type: protocol.BlockToolResult, ID: "call_1", Text: "[screenshot]",
	Images: []protocol.Image{{MediaType: "image/png", Data: []byte("png bytes")}},
}}}

func TestTheResponsesAPIIsShownAToolsImages(t *testing.T) {
	sent, _ := json.Marshal(inputItems(withImage, "gpt-test"))
	want := `[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"[screenshot]"},{"type":"input_image","image_url":"data:image/png;base64,cG5nIGJ5dGVz"}]}]`

	if string(sent) != want {
		t.Fatalf("sent %s\nwant %s", sent, want)
	}
}

func TestAResultWithoutImagesIsStillText(t *testing.T) {
	plain := protocol.Message{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockToolResult, ID: "call_1", Text: ""}}}
	sent, _ := json.Marshal(inputItems(plain, "gpt-test"))

	if want := `[{"type":"function_call_output","call_id":"call_1","output":""}]`; string(sent) != want {
		t.Fatalf("sent %s\nwant %s", sent, want)
	}
}

// A tool's message takes text alone, so its images follow it in a message
// of the user's, before what the person said.
func TestChatCompletionsShowAToolsImagesAfterIt(t *testing.T) {
	message := withImage
	message.Content = append(message.Content, protocol.Block{Type: protocol.BlockText, Text: "and now?"})
	sent, _ := json.Marshal(messages(message))
	want := `[{"role":"tool","content":"[screenshot]","tool_call_id":"call_1"},` +
		`{"role":"user","content":[{"type":"text","text":"The image(s) of the result of tool call call_1:"},{"type":"image_url","image_url":{"url":"data:image/png;base64,cG5nIGJ5dGVz"}}]},` +
		`{"role":"user","content":"and now?"}]`

	if string(sent) != want {
		t.Fatalf("sent %s\nwant %s", sent, want)
	}
}

var given = protocol.Message{Role: protocol.RoleUser, Content: []protocol.Block{{
	Type: protocol.BlockText, Text: "what is this?",
	Images: []protocol.Image{{MediaType: "image/png", Data: []byte("png bytes")}},
}}}

func TestTheResponsesAPIIsShownAPersonsImages(t *testing.T) {
	sent, _ := json.Marshal(inputItems(given, "gpt-test"))
	want := `[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,cG5nIGJ5dGVz"},{"type":"input_text","text":"what is this?"}]}]`

	if string(sent) != want {
		t.Fatalf("sent %s\nwant %s", sent, want)
	}
}

func TestChatCompletionsAreShownAPersonsImages(t *testing.T) {
	sent, _ := json.Marshal(messages(given))
	want := `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,cG5nIGJ5dGVz"}},{"type":"text","text":"what is this?"}]}]`

	if string(sent) != want {
		t.Fatalf("sent %s\nwant %s", sent, want)
	}

	alone := given
	alone.Content = []protocol.Block{{Type: protocol.BlockText, Images: given.Content[0].Images}}

	if sent, _ := json.Marshal(messages(alone)); string(sent) != `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,cG5nIGJ5dGVz"}}]}]` {
		t.Fatalf("images alone sent %s", sent)
	}
}
