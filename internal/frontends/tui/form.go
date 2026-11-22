package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// A form asks for the parameters of a function, or for what the picker
// changes, built from their descriptions alone, as rumbo-gui's does. A
// parameter whose values a function lists offers them under its field,
// asked for with what was given for the parameters before it, and asked
// again when that changes: the models listed after a provider are that
// provider's. Such a list need not hold every value a parameter takes, so
// the value is always what is typed in the field, and the list only helps
// to fill it: typing narrows it, and Up and Down put one of its values in
// the field.

// field is one parameter of a form.
type field struct {
	param protocol.Option
	// target says whose functions list its values: the host's, or the
	// provider's.
	target string
	// heading starts a part of the form with this field, and empty is what
	// the list's empty value says, when it has one.
	heading string
	empty   string
	value   inputLine
	// lister is the function that lists its values, if one does; choices
	// are what it listed, or the values the parameter takes when they are
	// fixed, and note what came with them, or why there are none.
	lister  string
	choices []choice
	note    string
	// asked is what the list was last asked with, and generation the number
	// of that request, so an answer to an earlier one is left aside;
	// loading is true until it comes.
	asked      *string
	generation int
	loading    bool
	// typed is what the person typed last, which narrows the list, and
	// selected is the choice of it the arrows are on.
	typed    string
	narrowed bool
	selected int
}

func newField(param protocol.Option, target, initial, empty string) *field {
	f := &field{param: param, target: target, empty: empty, selected: -1}
	f.value.set(initial)

	switch {
	case param.Values != "" && param.Type != protocol.OptionBoolean:
		f.lister = param.Values
	case param.Type == protocol.OptionBoolean:
		f.listed([]protocol.Value{{Value: "true"}, {Value: "false"}}, "")
	case len(param.Enum) > 0:
		values := make([]protocol.Value, len(param.Enum))

		for index, value := range param.Enum {
			values[index] = protocol.Value{Value: value}
		}

		f.listed(values, "")
	}

	return f
}

// listed takes the values the field offers, after its empty one.
func (f *field) listed(values []protocol.Value, said string) {
	f.choices, f.note, f.loading = nil, said, false

	if f.empty != "" {
		f.choices = append(f.choices, choice{value: "", label: f.empty})
	}

	for _, value := range values {
		f.choices = append(f.choices, choice{value: value.Value, label: value.Label, description: value.Description})
	}

	f.selected = -1
}

// shown are the choices the field offers now: those that match what was
// typed, by value or by what describes them, or all of them.
func (f *field) shown() []choice {
	if !f.narrowed {
		return f.choices
	}

	query := strings.ToLower(strings.TrimSpace(f.typed))

	var matching []choice

	for _, offered := range f.choices {
		if strings.HasPrefix(strings.ToLower(offered.value), query) || query != "" && strings.Contains(strings.ToLower(offered.label+" "+offered.description), query) {
			matching = append(matching, offered)
		}
	}

	return matching
}

// edited takes what the person typed, which narrows the list.
func (f *field) edited() {
	f.typed, f.narrowed, f.selected = f.value.String(), true, -1
}

// move puts the choice step away from the one selected in the field. The
// list stays narrowed to what was typed.
func (f *field) move(step int) bool {
	shown := f.shown()

	if len(shown) == 0 {
		return false
	}

	if f.selected < 0 {
		for index, offered := range shown {
			if offered.value == strings.TrimSpace(f.value.String()) {
				f.selected = index
			}
		}
	}

	switch {
	case f.selected < 0 && step > 0:
		f.selected = 0
	case f.selected < 0:
		f.selected = len(shown) - 1
	default:
		f.selected = (f.selected + step + len(shown)) % len(shown)
	}

	f.value.set(shown[f.selected].value)

	return true
}

// wanted is a list a form needs, which the app asks for.
type wanted struct {
	field      int
	generation int
	function   string
	target     string
	params     json.RawMessage
}

// form is the fields of a function's parameters, or of the picker, and
// which has the keyboard.
type form struct {
	title       string
	description string
	fields      []*field
	focus       int
	// problem says why it was not run.
	problem string
	// host and provider are the functions that can list a field's values.
	host     []protocol.Function
	provider []protocol.Function
	// function is the function a form of its parameters calls, and source
	// whose it is; picking holds what the picker opened with instead.
	function protocol.Function
	source   string
	picking  *picking
}

// functionForm is a form for the parameters of a function.
func functionForm(function protocol.Function, source string, host, provider []protocol.Function) *form {
	shown := &form{title: function.Name, description: function.Description, host: host, provider: provider, function: function, source: source}

	target := protocol.TargetHost

	if source == fromProvider {
		target = protocol.TargetProvider
	}

	for _, param := range function.Params {
		empty := ""

		if param.Optional {
			empty = "leave it out"
		}

		shown.fields = append(shown.fields, newField(param, target, "", empty))
	}

	return shown
}

// values are what each field holds, in order.
func (f *form) values() []string {
	values := make([]string, len(f.fields))

	for index, each := range f.fields {
		values[index] = strings.TrimSpace(each.value.String())
	}

	return values
}

// wanted returns the lists to ask for now: those never asked for, and those
// whose function would be given something else than it was.
func (f *form) wanted() []wanted {
	values := f.values()

	var asked []wanted

	for index, each := range f.fields {
		if each.lister == "" {
			continue
		}

		functions := f.host

		if each.target == protocol.TargetProvider {
			functions = f.provider
		}

		given := map[string]string{}

		for before := range index {
			given[f.fields[before].param.Name] = values[before]
		}

		params := passedTo(functions, each.lister, given)
		key := string(params)

		if each.asked != nil && *each.asked == key {
			continue
		}

		each.asked = &key
		each.generation++
		each.loading = true
		asked = append(asked, wanted{field: index, generation: each.generation, function: each.lister, target: each.target, params: params})
	}

	return asked
}

// take gives a field the list asked for it, unless a later one was asked.
func (f *form) take(index, generation int, values []protocol.Value, said string, err error) {
	if index >= len(f.fields) || f.fields[index].generation != generation {
		return
	}

	each := f.fields[index]

	if err != nil {
		each.listed(nil, "not listed: "+err.Error())
		return
	}

	each.listed(values, said)
}

// arguments are the parameters of the function's call, from what each
// field holds. A field left empty is left out when its parameter is
// optional.
func (f *form) arguments() (json.RawMessage, error) {
	params := map[string]json.RawMessage{}

	for index, text := range f.values() {
		param := f.fields[index].param

		if text == "" {
			if param.Optional {
				continue
			}

			return nil, fmt.Errorf("%s is needed", param.Name)
		}

		encoded, err := encode(param, text)

		if err != nil {
			return nil, err
		}

		params[param.Name] = encoded
	}

	if len(params) == 0 {
		return nil, nil
	}

	return json.Marshal(params)
}

// line is the command a call of the form's function is, as it would be
// typed.
func (f *form) line() string {
	words := []string{"/" + f.function.Name}

	for _, value := range f.values() {
		if value != "" {
			words = append(words, value)
		}
	}

	return strings.Join(words, " ")
}

// rows draws the form in rows of width columns, at most height of them,
// with the field that has the keyboard in view, and returns where the
// cursor is among them.
func (f *form) rows(t theme, width, height int) ([]string, int, int) {
	var rows []string

	if f.description != "" {
		rows = append(rows, wrapAll(styleDim+f.description+styleReset, width)...)
	}

	cursorRow, cursorColumn, from, to := 0, 0, 0, 0

	for index, each := range f.fields {
		if each.heading != "" {
			rows = append(rows, "", styleBold+each.heading+styleReset)
		}

		rows = append(rows, "")

		if index == f.focus {
			from = len(rows)
		}

		name := styleBold + each.param.Name + styleWeightOff

		if each.param.Optional {
			name += styleDim + "  optional" + styleWeightOff
		}

		if each.param.Description != "" {
			name += styleDim + "  " + each.param.Description + styleWeightOff
		}

		rows = append(rows, cut(name, width))

		typed, row, column := each.value.view(width-2, 1, false)
		text := typed[0]

		if each.value.empty() {
			text = styleDim + placeholderOf(each) + styleReset
		}

		mark := "  "

		if index == f.focus {
			mark = t.progress + "› " + styleColorOff
			cursorRow, cursorColumn = len(rows)+row, 2+column
		}

		rows = append(rows, mark+text)

		if index == f.focus {
			rows = append(rows, each.list(t, width, 6)...)
		}

		if each.note != "" {
			rows = append(rows, cut("  "+styleDim+each.note+styleReset, width))
		}

		if index == f.focus {
			to = len(rows)
		}
	}

	if f.problem != "" {
		rows = append(rows, "", cut(t.failure+f.problem+styleReset, width))
	}

	// The field with the keyboard stays in view, its name with it.
	top := 0

	if to > height {
		top = min(max(from, to-height), max(len(rows)-height, 0))
	}

	rows = rows[top:min(top+height, len(rows))]

	return rows, cursorRow - top, cursorColumn
}

// placeholderOf says what an empty field takes.
func placeholderOf(each *field) string {
	switch {
	case each.empty != "":
		return each.empty
	case len(each.param.Default) > 0:
		return "by default " + readable(each.param.Default)
	}

	return "type a value"
}

// list draws the values a field offers, at most height of them, the one
// selected in reverse.
func (f *field) list(t theme, width, height int) []string {
	shown := f.shown()

	switch {
	case f.loading:
		return []string{"  " + styleDim + "asking for the values…" + styleReset}
	case len(shown) == 0:
		return nil
	}

	selected := f.selected

	if selected < 0 {
		for index, offered := range shown {
			if offered.value == strings.TrimSpace(f.value.String()) {
				selected = index
			}
		}
	}

	top := max(min(selected-height/2, len(shown)-height), 0)

	var rows []string

	for index := top; index < min(top+height, len(shown)); index++ {
		offered := shown[index]
		value := offered.value

		if value == "" {
			value = offered.label
		}

		row := "    " + value

		if offered.value != "" && offered.label != "" {
			row += "  " + offered.label
		}

		if offered.description != "" {
			row += "  " + styleDim + offered.description + styleReset
		}

		if index == selected {
			row = styleReverse + "  ▸ " + pad(string([]rune(stripped(cut(row, width)))[4:]), width-4) + styleReset
		}

		rows = append(rows, cut(row, width))
	}

	if len(shown) > height {
		rows = append(rows, "    "+styleDim+fmt.Sprintf("%d values; type to narrow them", len(shown))+styleReset)
	}

	return rows
}

// readable is a JSON value as a person reads it: text as it is, anything
// else as its JSON.
func readable(value json.RawMessage) string {
	var text string

	if json.Unmarshal(value, &text) == nil {
		return text
	}

	return string(value)
}
