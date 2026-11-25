package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/protocol"
)

// A form asks for the list of a field with what was given for the fields
// before it, again only when that changes, and leaves aside an answer to an
// earlier request.
func TestAFormAsksForItsListsWithWhatCameBefore(t *testing.T) {
	host := []protocol.Function{
		{Name: "providers", Lists: true},
		{Name: "provider-models", Lists: true, Params: []protocol.Option{{Name: "provider", Type: protocol.OptionString}}},
	}
	use := protocol.Function{Name: "use", Params: []protocol.Option{
		{Name: "provider", Type: protocol.OptionString, Values: "providers"},
		{Name: "model", Type: protocol.OptionString, Values: "provider-models"},
		{Name: "effort", Type: protocol.OptionString, Enum: []string{"low", "high"}, Optional: true},
	}}

	f := pickerForm(use, false, protocol.DescribeResult{}, host)
	asked := f.wanted()

	if len(asked) != 2 || asked[0].function != "providers" || asked[0].params != nil || asked[1].function != "provider-models" || asked[1].params != nil {
		t.Fatalf("asked %+v", asked)
	}

	if len(f.fields[2].choices) != 3 || f.fields[2].choices[0].label != "leave it out" {
		t.Fatalf("the fixed values: %+v", f.fields[2].choices)
	}

	f.take(0, asked[0].generation, []protocol.Value{{Value: "toy"}, {Value: "openai"}}, "", nil)

	if !f.fields[0].move(1) || f.fields[0].value.String() != "toy" {
		t.Fatalf("the provider is %q", f.fields[0].value.String())
	}

	again := f.wanted()

	if len(again) != 1 || string(again[0].params) != `{"provider":"toy"}` {
		t.Fatalf("asked again %+v", again)
	}

	if more := f.wanted(); len(more) != 0 {
		t.Fatalf("asked once more %+v", more)
	}

	// The answer to the first request for the models comes last, and is
	// left aside.
	f.take(1, again[0].generation, []protocol.Value{{Value: "toy-1"}}, "", nil)
	f.take(1, asked[1].generation, []protocol.Value{{Value: "stale"}}, "", nil)

	if len(f.fields[1].choices) != 1 || f.fields[1].choices[0].value != "toy-1" {
		t.Fatalf("the models: %+v", f.fields[1].choices)
	}

	// Typing narrows the list, and what is typed is the value.
	f.fields[1].value.set("toy-9")
	f.fields[1].edited()

	if len(f.fields[1].shown()) != 0 {
		t.Fatalf("narrowed to %+v", f.fields[1].shown())
	}

	changes, err := f.changes()

	if err != nil || len(changes) != 1 || string(changes[0].use) != `{"model":"toy-9","provider":"toy"}` || changes[0].line() != "/model toy/toy-9" {
		t.Fatalf("changes %+v, %v", changes, err)
	}

	f.fields[0].value.clear()

	if _, err := f.changes(); err == nil || err.Error() != "provider is needed" {
		t.Fatalf("without a provider: %v", err)
	}

	f.take(1, again[0].generation+1, nil, "", errors.New("toy did not start"))
	f.fields[1].generation++
	f.take(1, f.fields[1].generation, nil, "", errors.New("toy did not start"))

	if f.fields[1].note != "not listed: toy did not start" {
		t.Fatalf("note %q", f.fields[1].note)
	}
}

// The picker asks for use when the provider or the model changed, and for
// each option changed one at a time, but not for the options when the
// provider changed, which takes its own.
func TestThePickerAsksForWhatChanged(t *testing.T) {
	use := protocol.Function{Name: "use", Params: []protocol.Option{
		{Name: "provider", Type: protocol.OptionString, Values: "providers"},
		{Name: "model", Type: protocol.OptionString, Values: "provider-models"},
	}}
	described := protocol.DescribeResult{
		Provider: "toy", Model: "toy-1",
		Options: json.RawMessage(`{"effort":"high"}`),
		Offers: protocol.Description{Options: []protocol.Option{
			{Name: "effort", Type: protocol.OptionString, Enum: []string{"low", "high"}, Default: json.RawMessage(`"low"`)},
			{Name: "depth", Type: protocol.OptionNumber, Default: json.RawMessage(`2`)},
		}},
	}

	f := pickerForm(use, true, described, nil)
	values := f.values()

	if strings.Join(values, ",") != "toy,toy-1,high,2" || f.fields[2].heading != "Options of toy" {
		t.Fatalf("opened with %v", values)
	}

	if changes, err := f.changes(); err != nil || len(changes) != 0 {
		t.Fatalf("unchanged: %+v, %v", changes, err)
	}

	f.fields[1].value.set("toy-2")
	f.fields[2].value.set("")
	f.fields[3].value.set("3")

	changes, err := f.changes()

	if err != nil || len(changes) != 3 || changes[0].line() != "/model toy/toy-2" || changes[1].line() != "/model effort=" || changes[2].line() != "/model depth=3" {
		t.Fatalf("changes %+v, %v", changes, err)
	}

	f.fields[3].value.set("deep")

	if _, err := f.changes(); err == nil || err.Error() != "depth is a number" {
		t.Fatalf("a depth that is no number: %v", err)
	}

	f.fields[0].value.set("openai")

	if changes, err := f.changes(); err != nil || len(changes) != 1 || changes[0].line() != "/model openai/toy-2" {
		t.Fatalf("a new provider: %+v, %v", changes, err)
	}
}

// A box laid over the screen keeps what is on either side of it, with its
// styles, and every row keeps its width.
func TestABoxIsLaidOverTheScreen(t *testing.T) {
	screen := []string{
		styleBold + "abcdefghij" + styleReset,
		"0123" + defaultTheme.failure + "456789" + styleReset,
		"short",
	}

	laid := lay(screen, box("t", []string{"x"}, 6, ""), 2, 0)

	for index, want := range []string{"ab╭─ t ╮ij", "01│ x  │89", "sh╰────╯"} {
		if stripped(laid[index]) != want {
			t.Fatalf("row %d is %q, want %q", index, stripped(laid[index]), want)
		}
	}

	if !strings.HasPrefix(laid[0], styleBold+"ab") || !strings.Contains(laid[1], defaultTheme.failure+"89") {
		t.Fatalf("the styles under the box are lost: %q", laid)
	}

	if got := stripped(columns("日本語", 1, 5)); got != " 本 " {
		t.Fatalf("a wide rune cut in two: %q", got)
	}
}
