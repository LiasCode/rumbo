package tui

import (
	"strings"
)

// What a function of the host says in words is laid out by its shape rather
// than its meaning, as rumbo-gui's outline does: a line that ends in a colon
// is a heading, a line that names something before a colon is a label and
// its value, and what is set in under a line belongs to it. The words are
// kept as they are; only where each goes is read, so a host that words
// things differently still shows, as lines of words.

// shortLabel is the longest a heading or a label is: a longer line is a
// sentence.
const shortLabel = 48

// The kinds of part of what was said.
const (
	partHeading = iota
	partField
	partItem
	partParagraph
)

// part is one part of what was said: a heading, a label and its value, an
// item set in under nothing with its title, text, and the details set in
// under it, or words that are none of those.
type part struct {
	kind    int
	title   string
	text    string
	details []string
}

// outline reads what was said into its parts.
func outline(said string) []part {
	var parts []part

	itemDepth := -1

	for _, line := range strings.Split(said, "\n") {
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			itemDepth = -1
			continue
		}

		depth := len(line) - len(strings.TrimLeft(line, " \t"))

		// What is set in deeper than an item belongs to it.
		if last := len(parts) - 1; itemDepth >= 0 && depth > itemDepth && parts[last].kind == partItem {
			parts[last].details = append(parts[last].details, trimmed)
			continue
		}

		itemDepth = -1

		if depth > 0 {
			title, text, ok := labeled(trimmed)

			if !ok {
				title, text = trimmed, ""
			}

			parts = append(parts, part{kind: partItem, title: title, text: text})
			itemDepth = depth

			continue
		}

		heading, isHeading := strings.CutSuffix(trimmed, ":")

		if isHeading && len(heading) <= shortLabel {
			parts = append(parts, part{kind: partHeading, title: heading})
			continue
		}

		if label, value, ok := labeled(trimmed); ok {
			parts = append(parts, part{kind: partField, title: label, text: value})
			continue
		}

		parts = append(parts, part{kind: partParagraph, text: trimmed})
	}

	return parts
}

// labeled splits a line that names something: a short label, which is no
// sentence, before a colon and a space, and something after them.
func labeled(line string) (string, string, bool) {
	label, value, found := strings.Cut(line, ": ")
	label, value = strings.TrimSpace(label), strings.TrimSpace(value)

	if !found || label == "" || value == "" || len(label) > shortLabel || strings.Contains(label, ". ") {
		return "", "", false
	}

	return label, value, true
}

// outlined draws what was said in rows of width columns: headings in bold,
// labels dimmed in a column of their own, items with their details set in
// under them, and words wrapped.
func outlined(said string, width int) []string {
	parts := outline(said)
	labels := 0

	for _, each := range parts {
		if each.kind == partField {
			labels = max(labels, min(visibleWidth(each.title), width/3))
		}
	}

	var rows []string

	for index, each := range parts {
		switch each.kind {
		case partHeading:
			if index > 0 {
				rows = append(rows, "")
			}

			rows = append(rows, cut(styleBold+each.title+styleReset, width))

		case partField:
			value := wrap(each.text, max(width-labels-2, 8))

			for line, text := range value {
				label := strings.Repeat(" ", labels)

				if line == 0 {
					label = styleDim + pad(cut(each.title, labels), labels) + styleReset
				}

				rows = append(rows, label+"  "+text)
			}

		case partItem:
			title := styleBold + each.title + styleWeightOff

			if each.text != "" {
				title += "  " + each.text
			}

			rows = append(rows, wrapAll("• "+title+styleReset, width)...)

			for _, detail := range each.details {
				for _, text := range wrap(detail, max(width-2, 8)) {
					rows = append(rows, "  "+styleDim+text+styleReset)
				}
			}

		default:
			rows = append(rows, wrapAll(each.text, width)...)
		}
	}

	return rows
}
