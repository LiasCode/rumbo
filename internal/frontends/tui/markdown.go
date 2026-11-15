package tui

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Styles for what the model writes, and the ones that end one effect and
// leave the others in force.
const (
	styleItalic    = escape + "[3m"
	styleUnderline = escape + "[4m"
	styleGreen     = escape + "[32m"
	styleCode      = styleYellow

	// styleWeightOff ends bold and dim alike.
	styleWeightOff    = escape + "[22m"
	styleItalicOff    = escape + "[23m"
	styleUnderlineOff = escape + "[24m"
	styleColorOff     = escape + "[39m"
)

// markdown shows what the model writes as the Markdown it is: headings,
// lists, quotes, rules, code blocks, and tables, and in a line code, bold,
// italics, and links. It works a line at a time, as the text arrives, and
// what it shows can still be copied: code keeps its text as it was.
type markdown struct {
	// fence is the mark that opened the code block the lines are in, if
	// they are in one.
	fence string
	// table holds the rows of a table until it ends, so its columns line up.
	table [][]string
}

var (
	fenceLine   = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	headingLine = regexp.MustCompile(`^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	ruleLine    = regexp.MustCompile(`^ {0,3}([-*_])(\s*[-*_]){2,}\s*$`)
	bulletLine  = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	quoteLine   = regexp.MustCompile(`^ {0,3}>\s?(.*)$`)
	tableRule   = regexp.MustCompile(`^\s*:?-+:?\s*$`)
)

// line shows one whole line. It returns nothing while a table is still
// coming, and the table, lined up, with the line that ends it.
func (m *markdown) line(text string, width int) []string {
	var shown []string

	if m.fence == "" && isTableRow(text) {
		m.table = append(m.table, cellsOfRow(text))
		return nil
	}

	if len(m.table) > 0 {
		shown = renderTable(m.table)
		m.table = nil
	}

	return append(shown, m.render(text, width, true))
}

// peek shows the line still being written, without taking it.
func (m *markdown) peek(text string, width int) []string {
	if m.fence == "" && isTableRow(text) {
		return renderTable(append(m.table, cellsOfRow(text)))
	}

	shown := renderTable(m.table)

	return append(shown, m.render(text, width, false))
}

// held is what is kept back to be shown later: a table not ended yet.
func (m *markdown) held() []string {
	return renderTable(m.table)
}

// end shows what was kept back, as a text ends, and starts again outside
// any block.
func (m *markdown) end() []string {
	shown := renderTable(m.table)
	m.table, m.fence = nil, ""

	return shown
}

// render shows one line outside a table. Only a whole line opens or closes
// a code block.
func (m *markdown) render(text string, width int, whole bool) string {
	if found := fenceLine.FindStringSubmatch(text); found != nil {
		mark := found[1]

		switch {
		case m.fence == "" && whole:
			m.fence = mark[:3]
		case m.fence != "" && strings.HasPrefix(mark, m.fence) && strings.TrimSpace(text) == mark && whole:
			m.fence = ""
		}

		return styleDim + text + styleReset
	}

	if m.fence != "" {
		return styleCode + text + styleReset
	}

	if found := headingLine.FindStringSubmatch(text); found != nil {
		style := styleBold

		if len(found[1]) == 1 {
			style += styleUnderline
		}

		return style + inline(found[2]) + styleReset
	}

	if ruleLine.MatchString(text) {
		return styleDim + strings.Repeat("─", max(min(width-1, 60), 3)) + styleReset
	}

	if found := bulletLine.FindStringSubmatch(text); found != nil {
		return found[1] + "• " + inline(found[2])
	}

	if found := quoteLine.FindStringSubmatch(text); found != nil {
		return styleDim + "│ " + styleReset + styleItalic + inline(found[1]) + styleReset
	}

	return inline(text)
}

// isTableRow reports whether a line is a row of a table: it starts with a
// bar, and has another one after.
func isTableRow(text string) bool {
	trimmed := strings.TrimSpace(text)

	return strings.HasPrefix(trimmed, "|") && strings.Count(trimmed, "|") >= 2
}

// cellsOfRow splits a table's row into its cells, without the bars at its
// edges.
func cellsOfRow(text string) []string {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(text), "|"), "|")
	cells := strings.Split(trimmed, "|")

	for index, cell := range cells {
		cells[index] = strings.TrimSpace(cell)
	}

	return cells
}

// renderTable lines up the columns of a table, with its header in bold and
// the row under the header drawn as a rule.
func renderTable(rows [][]string) []string {
	if len(rows) == 0 {
		return nil
	}

	styled := make([][]string, len(rows))
	widths := []int{}

	for index, row := range rows {
		for column, text := range row {
			cell := inline(text)

			if isRule(row) {
				cell = ""
			}

			styled[index] = append(styled[index], cell)

			if column >= len(widths) {
				widths = append(widths, 0)
			}

			widths[column] = max(widths[column], visibleWidth(cell))
		}
	}

	shown := make([]string, len(rows))

	for index, row := range rows {
		cells := make([]string, len(widths))

		for column, width := range widths {
			switch {
			case isRule(row):
				cells[column] = strings.Repeat("─", width)
			case column < len(styled[index]):
				cells[column] = pad(styled[index][column], width)
			default:
				cells[column] = strings.Repeat(" ", width)
			}

			if index == 0 && len(rows) > 1 && isRule(rows[1]) {
				cells[column] = styleBold + cells[column] + styleWeightOff
			}
		}

		separator := styleDim + " │ " + styleReset

		if isRule(row) {
			separator = styleDim + "─┼─"
			shown[index] = styleDim + strings.Join(cells, separator) + styleReset

			continue
		}

		shown[index] = strings.Join(cells, separator)
	}

	return shown
}

// isRule reports whether a table's row is the one under its header.
func isRule(row []string) bool {
	for _, cell := range row {
		if !tableRule.MatchString(cell) {
			return false
		}
	}

	return len(row) > 0
}

// escapable are the marks a backslash makes plain.
const escapable = "\\`*_{}[]()#+-.!|>~"

// inline styles the spans of one line: code, bold, italics, and links. A
// mark that nothing closes stays as it is.
func inline(text string) string {
	var out strings.Builder

	for index := 0; index < len(text); {
		rest := text[index:]

		switch {
		case rest[0] == '\\' && len(rest) > 1 && strings.IndexByte(escapable, rest[1]) >= 0:
			out.WriteByte(rest[1])
			index += 2

			continue

		case rest[0] == '`':
			ticks := len(rest) - len(strings.TrimLeft(rest, "`"))

			if end := strings.Index(rest[ticks:], rest[:ticks]); end >= 0 {
				out.WriteString(styleCode + rest[ticks:ticks+end] + styleColorOff)
				index += 2*ticks + end

				continue
			}

			out.WriteString(rest[:ticks])
			index += ticks

			continue

		case strings.HasPrefix(rest, "**"):
			if end := closing(rest[2:], "**"); end >= 0 {
				out.WriteString(styleBold + inline(rest[2:2+end]) + styleWeightOff)
				index += 4 + end

				continue
			}

		case rest[0] == '*' || rest[0] == '_':
			end := closing(rest[1:], rest[:1])

			// An underscore inside a word, as in snake_case, is part of it.
			if end >= 0 && (rest[0] == '*' || (!wordBefore(text, index) && !wordAfter(text, index+2+end))) {
				out.WriteString(styleItalic + inline(rest[1:1+end]) + styleItalicOff)
				index += 2 + end

				continue
			}

		case rest[0] == '[':
			if label, target, size, ok := link(rest); ok {
				out.WriteString(styleUnderline + inline(label) + styleUnderlineOff)

				if target != label {
					out.WriteString(styleDim + " (" + target + ")" + styleWeightOff)
				}

				index += size

				continue
			}
		}

		_, size := utf8.DecodeRuneInString(rest)
		out.WriteString(rest[:size])
		index += size
	}

	return out.String()
}

// closing finds where mark closes a span that starts at the beginning of
// text: a span holds something, and neither starts nor ends with a space.
func closing(text, mark string) int {
	if text == "" || text[0] == ' ' {
		return -1
	}

	for from := 1; from < len(text); {
		found := strings.Index(text[from:], mark)

		if found < 0 {
			return -1
		}

		end := from + found

		if text[end-1] != ' ' {
			return end
		}

		from = end + len(mark)
	}

	return -1
}

// link reads [label](target) at the start of text, and how long it is.
func link(text string) (label, target string, size int, ok bool) {
	close := strings.Index(text, "](")

	if close < 0 || strings.ContainsAny(text[1:close], "[]") {
		return "", "", 0, false
	}

	end := strings.IndexByte(text[close+2:], ')')

	if end < 0 {
		return "", "", 0, false
	}

	return text[1:close], text[close+2 : close+2+end], close + 3 + end, true
}

func wordBefore(text string, index int) bool {
	r, _ := utf8.DecodeLastRuneInString(text[:index])

	return index > 0 && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

func wordAfter(text string, index int) bool {
	r, _ := utf8.DecodeRuneInString(text[min(index, len(text)):])

	return index < len(text) && (unicode.IsLetter(r) || unicode.IsDigit(r))
}
