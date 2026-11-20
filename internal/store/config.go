package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"
)

// The configuration file holds what the user chose, and nothing else: a
// default is not written there, so a value that is there is one somebody
// set. A person can edit it, and rumbo changes it one value at a time,
// leaving the rest, comments included, as it was:
//
//	system:
//	  default_provider: anthropic
//	providers:
//	  local:
//	    command: ["{self}", provide, openai]
//	    model: qwen3-coder
//	    config:
//	      base_url: http://localhost:11434/v1
//	frontends:
//	  tui:
//	    config:
//	      theme: plain

const (
	sectionSystem = "system"

	fieldCommand = "command"
	fieldModel   = "model"
	fieldConfig  = "config"
)

// sections are where each kind of program is registered.
var sections = map[string]string{
	KindProvider: "providers",
	KindFrontend: "frontends",
}

func sectionOf(kind string) (string, error) {
	section, known := sections[kind]

	if !known {
		return "", fmt.Errorf("unknown program kind %q", kind)
	}

	return section, nil
}

// isNull says a key was written with nothing after it, which is the same as
// leaving it out.
func isNull(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.Tag == "!!null"
}

// actual is the node an alias stands for, or the node itself.
func actual(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return node.Alias
	}

	return node
}

// lookup returns the value of key in a mapping, or nil when it has none. A
// mapping that is nil, or written as nothing, has no keys.
func lookup(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}

	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return actual(mapping.Content[index+1])
		}
	}

	return nil
}

// put sets the value of key in a mapping. A value that replaces another
// keeps the comments written next to it.
func put(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			old := mapping.Content[index+1]
			value.HeadComment, value.LineComment, value.FootComment = old.HeadComment, old.LineComment, old.FootComment
			mapping.Content[index+1] = value

			return
		}
	}

	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
}

// putBefore sets the value of key in a mapping as put does, and a key that
// is new goes before the key named, when the mapping has it, so a program's
// settings read in the order a person expects.
func putBefore(mapping *yaml.Node, key string, value *yaml.Node, before string) {
	if lookup(mapping, key) != nil {
		put(mapping, key, value)
		return
	}

	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == before {
			mapping.Content = slices.Insert(mapping.Content, index, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
			return
		}
	}

	put(mapping, key, value)
}

// remove takes key out of a mapping, and reports whether it was there.
func remove(mapping *yaml.Node, key string) bool {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return false
	}

	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			mapping.Content = slices.Delete(mapping.Content, index, index+2)
			return true
		}
	}

	return false
}

// within returns the mapping under key, making it when there is none.
func within(mapping *yaml.Node, key string) *yaml.Node {
	if found := lookup(mapping, key); found != nil && found.Kind == yaml.MappingNode {
		return found
	}

	made := &yaml.Node{Kind: yaml.MappingNode}
	put(mapping, key, made)

	return made
}

// text is a value written as the user typed it, so 150 reads as a number in
// the file. One that would read as nothing is quoted, to stay a value.
func text(value string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: value}

	if slices.Contains([]string{"", "~", "null", "Null", "NULL"}, value) {
		node.Tag, node.Style = "!!str", yaml.DoubleQuotedStyle
	}

	return node
}

// document reads the configuration file as a tree whose root is a mapping,
// an empty one while there is no file.
func (f *Files) document() (*yaml.Node, error) {
	raw, err := os.ReadFile(f.config)

	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	var doc yaml.Node

	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", f.config, err)
	}

	// A file with nothing in it, or nothing but comments, which are kept.
	if doc.Kind == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode, HeadComment: strings.TrimSpace(string(raw))}
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	}

	if err := f.check(doc.Content[0]); err != nil {
		return nil, err
	}

	return &doc, nil
}

// wrong says what is wrong in the configuration file, and where.
func (f *Files) wrong(node *yaml.Node, format string, args ...any) error {
	return fmt.Errorf("%s, line %d: %s", f.config, node.Line, fmt.Sprintf(format, args...))
}

// check refuses a file rumbo would read something else from than its writer
// meant: a key it does not know, or a value of another shape.
func (f *Files) check(root *yaml.Node) error {
	if root.Kind != yaml.MappingNode {
		return f.wrong(root, "it holds system, providers, and frontends, each with what is under it")
	}

	if err := f.checkKeys(root); err != nil {
		return err
	}

	for index := 0; index+1 < len(root.Content); index += 2 {
		key, value := root.Content[index], actual(root.Content[index+1])

		if isNull(value) {
			continue
		}

		if value.Kind != yaml.MappingNode {
			return f.wrong(value, "%s holds names with their values, one to a line", key.Value)
		}

		if err := f.checkKeys(value); err != nil {
			return err
		}

		switch key.Value {
		case sectionSystem:
			for at := 0; at+1 < len(value.Content); at += 2 {
				if setting := actual(value.Content[at+1]); setting.Kind != yaml.ScalarNode {
					return f.wrong(setting, "the system setting %s takes one value", value.Content[at].Value)
				}
			}

		case sections[KindProvider], sections[KindFrontend]:
			for at := 0; at+1 < len(value.Content); at += 2 {
				if err := f.checkProgram(key.Value, value.Content[at].Value, actual(value.Content[at+1])); err != nil {
					return err
				}
			}

		default:
			return f.wrong(key, "%q is not something rumbo reads; it reads system, providers, and frontends", key.Value)
		}
	}

	return nil
}

// checkProgram checks what a section of the file holds of one program. A
// provider has a model too.
func (f *Files) checkProgram(section, name string, entry *yaml.Node) error {
	fields := []string{fieldCommand, fieldConfig}

	if section == sections[KindProvider] {
		fields = []string{fieldCommand, fieldModel, fieldConfig}
	}

	known := strings.Join(fields[:len(fields)-1], ", ") + " and " + fields[len(fields)-1]

	if isNull(entry) {
		return nil
	}

	if entry.Kind != yaml.MappingNode {
		return f.wrong(entry, "%s holds its %s, each on a line of its own", name, known)
	}

	if err := f.checkKeys(entry); err != nil {
		return err
	}

	for index := 0; index+1 < len(entry.Content); index += 2 {
		key, value := entry.Content[index], actual(entry.Content[index+1])

		switch {
		case isNull(value):

		case key.Value == fieldCommand:
			var command []string

			if err := value.Decode(&command); err != nil {
				return f.wrong(value, "the command of %s is a list of words, such as [my-program, --flag]", name)
			}

		case key.Value == fieldModel && slices.Contains(fields, fieldModel):
			if value.Kind != yaml.ScalarNode {
				return f.wrong(value, "the model of %s is one name", name)
			}

		case key.Value == fieldConfig:
			if value.Kind != yaml.MappingNode {
				return f.wrong(value, "the config of %s holds fields with their values, one to a line", name)
			}

			if _, err := configOf(value); err != nil {
				return f.wrong(value, "the config of %s: %v", name, err)
			}

		default:
			return f.wrong(key, "%q is not something rumbo reads of %s; it reads %s", key.Value, name, known)
		}
	}

	return nil
}

// checkKeys refuses a mapping that has a key twice, which YAML reads as two
// keys and a person meant as one.
func (f *Files) checkKeys(mapping *yaml.Node) error {
	seen := map[string]int{}

	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index]

		if line, twice := seen[key.Value]; twice {
			return f.wrong(key, "%s is already there, on line %d", key.Value, line)
		}

		seen[key.Value] = key.Line
	}

	return nil
}

// configOf reads a program's configuration as the JSON object the program
// is given, or as nothing when it has no fields.
func configOf(config *yaml.Node) (json.RawMessage, error) {
	if len(config.Content) == 0 {
		return nil, nil
	}

	var fields any

	if err := config.Decode(&fields); err != nil {
		return nil, err
	}

	return json.Marshal(fields)
}

// update changes the configuration file: it reads it, lets change work on
// its root, and writes it back, holding its directory meanwhile.
func (f *Files) update(change func(root *yaml.Node) error) error {
	path := f.config

	// A file that is a link, as one kept with someone's dotfiles is, stays
	// a link: what it points to is what is replaced.
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}

	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	return locked(dir, func() error {
		doc, err := f.document()

		if err != nil {
			return err
		}

		if err := change(doc.Content[0]); err != nil {
			return err
		}

		raw, err := yaml.Dump(doc, yaml.WithIndent(2))

		if err != nil {
			return err
		}

		return writeFile(path, raw)
	})
}

// root reads the configuration file and returns its root.
func (f *Files) root() (*yaml.Node, error) {
	doc, err := f.document()

	if err != nil {
		return nil, err
	}

	return doc.Content[0], nil
}

// Setting implements Store.
func (f *Files) Setting(key string) (string, bool, error) {
	settings, err := f.Settings()

	if err != nil {
		return "", false, err
	}

	value, found := settings[key]

	return value, found, nil
}

// Settings implements Store.
func (f *Files) Settings() (map[string]string, error) {
	root, err := f.root()

	if err != nil {
		return nil, err
	}

	settings := map[string]string{}
	system := lookup(root, sectionSystem)

	if system == nil || system.Kind != yaml.MappingNode {
		return settings, nil
	}

	for index := 0; index+1 < len(system.Content); index += 2 {
		if value := actual(system.Content[index+1]); !isNull(value) {
			settings[system.Content[index].Value] = value.Value
		}
	}

	return settings, nil
}

// SetSetting implements Store.
func (f *Files) SetSetting(key, value string) error {
	return f.update(func(root *yaml.Node) error {
		put(within(root, sectionSystem), key, text(value))

		return nil
	})
}

// DeleteSetting implements Store.
func (f *Files) DeleteSetting(key string) error {
	return f.update(func(root *yaml.Node) error {
		return discard(root, sectionSystem, key)
	})
}

// discard takes key out of a section of the file, and the section with it
// when nothing is left there.
func discard(root *yaml.Node, section, key string) error {
	held := lookup(root, section)

	if !remove(held, key) {
		return ErrNotFound
	}

	if len(held.Content) == 0 {
		remove(root, section)
	}

	return nil
}

// Program implements Store.
func (f *Files) Program(kind, name string) (Program, error) {
	programs, err := f.Programs(kind)

	if err != nil {
		return Program{}, err
	}

	for _, program := range programs {
		if program.Name == name {
			return program, nil
		}
	}

	return Program{}, ErrNotFound
}

// Programs implements Store.
func (f *Files) Programs(kind string) ([]Program, error) {
	section, err := sectionOf(kind)

	if err != nil {
		return nil, err
	}

	root, err := f.root()

	if err != nil {
		return nil, err
	}

	registered := lookup(root, section)

	if registered == nil || registered.Kind != yaml.MappingNode {
		return nil, nil
	}

	var programs []Program

	for index := 0; index+1 < len(registered.Content); index += 2 {
		program := Program{Name: registered.Content[index].Value}
		entry := actual(registered.Content[index+1])

		if command := lookup(entry, fieldCommand); command != nil && !isNull(command) {
			if err := command.Decode(&program.Command); err != nil {
				return nil, f.wrong(command, "the command of %s: %v", program.Name, err)
			}
		}

		if model := lookup(entry, fieldModel); model != nil && !isNull(model) {
			program.Model = model.Value
		}

		if config := lookup(entry, fieldConfig); config != nil {
			if program.Config, err = configOf(config); err != nil {
				return nil, f.wrong(config, "the config of %s: %v", program.Name, err)
			}
		}

		programs = append(programs, program)
	}

	slices.SortFunc(programs, func(a, b Program) int { return strings.Compare(a.Name, b.Name) })

	return programs, nil
}

// PutProgram implements Store.
func (f *Files) PutProgram(kind string, program Program) error {
	section, err := sectionOf(kind)

	if err != nil {
		return err
	}

	desired, err := fieldsOf(program.Config)

	if err != nil {
		return err
	}

	return f.update(func(root *yaml.Node) error {
		entry := within(within(root, section), program.Name)

		var command []string

		if written := lookup(entry, fieldCommand); written != nil {
			_ = written.Decode(&command)
		}

		switch {
		case len(program.Command) == 0:
			remove(entry, fieldCommand)

		// One written as it is asked for is left as its writer wrote it.
		case !slices.Equal(command, program.Command):
			words := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}

			for _, word := range program.Command {
				words.Content = append(words.Content, text(word))
			}

			putBefore(entry, fieldCommand, words, fieldModel)
		}

		switch written := lookup(entry, fieldModel); {
		case program.Model == "":
			remove(entry, fieldModel)

		case written == nil || written.Value != program.Model:
			putBefore(entry, fieldModel, text(program.Model), fieldConfig)
		}

		if len(desired) == 0 {
			remove(entry, fieldConfig)

			return nil
		}

		return reconcile(within(entry, fieldConfig), desired)
	})
}

// DeleteProgram implements Store.
func (f *Files) DeleteProgram(kind, name string) error {
	section, err := sectionOf(kind)

	if err != nil {
		return err
	}

	return f.update(func(root *yaml.Node) error {
		return discard(root, section, name)
	})
}

// fieldsOf reads a JSON object as the values the file is written from. A
// whole number stays one, so 32768 is not written as a fraction.
func fieldsOf(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var fields map[string]any

	if err := decoder.Decode(&fields); err != nil {
		return nil, fmt.Errorf("a configuration is a JSON object: %w", err)
	}

	return numbered(fields).(map[string]any), nil
}

// numbered gives every number in a decoded JSON value the type YAML writes
// it from: a whole number, or else a fraction.
func numbered(value any) any {
	switch value := value.(type) {
	case json.Number:
		if whole, err := value.Int64(); err == nil {
			return whole
		}

		fraction, _ := value.Float64()

		return fraction

	case map[string]any:
		for key, field := range value {
			value[key] = numbered(field)
		}

	case []any:
		for index, item := range value {
			value[index] = numbered(item)
		}
	}

	return value
}

// reconcile makes a mapping of the file hold the fields desired, touching
// only what differs: a field that already has its value stays as it was
// written, with its comments.
func reconcile(mapping *yaml.Node, desired map[string]any) error {
	for index := 0; index+1 < len(mapping.Content); {
		if _, kept := desired[mapping.Content[index].Value]; kept {
			index += 2
			continue
		}

		mapping.Content = slices.Delete(mapping.Content, index, index+2)
	}

	for _, name := range slices.Sorted(maps.Keys(desired)) {
		value := desired[name]
		written := lookup(mapping, name)

		if written != nil {
			if nested, isMapping := value.(map[string]any); isMapping && written.Kind == yaml.MappingNode {
				if err := reconcile(written, nested); err != nil {
					return err
				}

				continue
			}

			var current any

			if written.Decode(&current) == nil && same(current, value) {
				continue
			}
		}

		var node yaml.Node

		if err := node.Encode(value); err != nil {
			return err
		}

		put(mapping, name, &node)
	}

	return nil
}

// same says two values are written as the same JSON.
func same(a, b any) bool {
	first, err := json.Marshal(a)

	if err != nil {
		return false
	}

	second, err := json.Marshal(b)

	return err == nil && bytes.Equal(first, second)
}
