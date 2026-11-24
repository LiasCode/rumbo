package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// exportedLines bounds the lines of what a tool returned that an export
// keeps.
const exportedLines = 40

// writeNew writes text to a file that does not exist yet, and leaves one
// that does as it is.
func writeNew(path, text string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)

	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s exists, and is left as it is; /export <path> writes elsewhere", path)
	}

	if err != nil {
		return err
	}

	_, err = file.WriteString(text)

	if closed := file.Close(); err == nil {
		err = closed
	}

	return err
}

// markdownOf tells a session's history as a Markdown document: what the
// person asked, what the model wrote as it wrote it, each tool call with
// the change it made or what it returned, and how each run ended.
func markdownOf(told protocol.HistoryResult, now time.Time) string {
	var out strings.Builder

	fmt.Fprintf(&out, "# Session %s\n\nExported from rumbo on %s.\n", told.Session, now.Format("2006-01-02 15:04"))

	var run tally
	var calls []protocol.Event
	var text strings.Builder

	// What the model wrote in pieces goes in as one, apart from what comes
	// before and after it by one blank line.
	ending := func() {
		if written := strings.Trim(text.String(), "\n"); written != "" {
			out.WriteString("\n" + written + "\n")
		}

		text.Reset()
	}

	for _, event := range told.Events {
		if event.Type != protocol.EventText {
			ending()
		}

		switch event.Type {
		case protocol.EventObjective:
			run = tally{}
			fmt.Fprintf(&out, "\n---\n\n**You**\n\n%s\n", event.Text)
			given(&out, event.Images)

		case protocol.EventSteer:
			fmt.Fprintf(&out, "\n**You, while it worked**\n\n%s\n", event.Text)
			given(&out, event.Images)

		case protocol.EventText:
			text.WriteString(event.Text)

		case protocol.EventToolStarted:
			calls = append(calls, event)

		case protocol.EventToolFinished:
			var started protocol.Event

			if len(calls) > 0 {
				started, calls = calls[0], calls[1:]
			}

			out.WriteString(exportedCall(started.Args, event))

		case protocol.EventTurn:
			run.add(event.Usage)

		case protocol.EventLog:
			fmt.Fprintf(&out, "\n> %s\n", event.Text)

		case protocol.EventEnded:
			ended := markDone + " done"

			switch {
			case event.Status == protocol.RunCancelled:
				ended = markFailed + " cancelled"
			case event.Status != protocol.RunDone:
				ended = markFailed + " " + strings.TrimSpace(event.Status+": "+event.Text)
			}

			if took := run.took(now); took != "" {
				ended += " · " + took
			}

			fmt.Fprintf(&out, "\n*%s*\n", ended)
		}
	}

	ending()

	// Calls left without a result never finished.
	for _, call := range calls {
		fmt.Fprintf(&out, "\n**%s %s**%s (it never finished)\n", markUnknown, call.Name, subject(call.Name, call.Args))
	}

	return out.String()
}

// exportedCall is a tool call in an export: a line saying what it acted on
// and how it went, and under it the change an edit made, or what the call
// returned.
func exportedCall(args json.RawMessage, finished protocol.Event) string {
	mark := markDone

	if finished.Failed {
		mark = markFailed
	}

	var out strings.Builder

	fmt.Fprintf(&out, "\n**%s %s**%s\n", mark, finished.Name, subject(finished.Name, args))

	switch {
	case finished.Name == "edit" && !finished.Failed:
		var edit struct {
			Old string `json:"old"`
			New string `json:"new"`
		}

		if json.Unmarshal(args, &edit) == nil {
			var lines []string

			for _, step := range diff(strings.Split(edit.Old, "\n"), strings.Split(edit.New, "\n")) {
				lines = append(lines, string(step.op)+" "+step.text)
			}

			out.WriteString(fenced("diff", strings.Join(lines, "\n")))
		}

	case finished.Name == "read" && !finished.Failed:

	case strings.TrimSpace(finished.Result) != "" && finished.Result != "[no output]":
		lines := strings.Split(strings.TrimRight(finished.Result, "\n"), "\n")

		if left := len(lines) - exportedLines; left > 0 {
			lines = append(lines[:exportedLines], fmt.Sprintf("… %d more lines", left))
		}

		out.WriteString(fenced("text", strings.Join(lines, "\n")))
	}

	return out.String()
}

// subject is what a call acts on, as inline code after a space, or nothing
// when its arguments name nothing.
func subject(name string, args json.RawMessage) string {
	described := strings.TrimSpace(strings.TrimPrefix(render.Tool(name, args), name))

	if described == "" {
		return ""
	}

	return " " + inlineCode(described)
}

// fenced is text in a code block, behind a fence longer than any run of
// backticks it holds.
func fenced(language, text string) string {
	fence := "```"

	for strings.Contains(text, fence) {
		fence += "`"
	}

	return "\n" + fence + language + "\n" + text + "\n" + fence + "\n"
}

// inlineCode is text as inline code, behind more backticks than it holds.
func inlineCode(text string) string {
	ticks := "`"

	for strings.Contains(text, ticks) {
		ticks += "`"
	}

	if len(ticks) > 1 {
		return ticks + " " + text + " " + ticks
	}

	return ticks + text + ticks
}

// given tells the images a person gave, which a document names, not shows.
func given(out *strings.Builder, images []protocol.Image) {
	if len(images) > 0 {
		fmt.Fprintf(out, "\n*%s*\n", imageLabels(images))
	}
}
