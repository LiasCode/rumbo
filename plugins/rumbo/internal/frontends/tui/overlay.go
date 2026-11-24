package tui

import (
	"strings"
)

// What floats over the screen, such as the command palette or a form, is a
// box drawn over it, which shows dimmed under it.

// box draws body in a frame width columns wide, with title in its top
// border, in the style border.
func box(title string, body []string, width int, border string) []string {
	inner := max(width-4, 1)
	top := "─ " + title + " "

	if title == "" {
		top = ""
	}

	top = cut(top, inner+2)

	rows := []string{border + "╭" + styleBold + top + styleWeightOff + strings.Repeat("─", max(inner+2-visibleWidth(top), 0)) + "╮" + styleReset}

	for _, row := range body {
		rows = append(rows, border+"│"+styleReset+" "+fit(row, inner)+" "+border+"│"+styleReset)
	}

	return append(rows, border+"╰"+strings.Repeat("─", inner+2)+"╯"+styleReset)
}

// lay puts rows over screen, from column x and row y, and returns the
// screen. What is under them is kept on either side.
func lay(screen, rows []string, x, y int) []string {
	for index, row := range rows {
		at := y + index

		if at < 0 || at >= len(screen) {
			continue
		}

		under := screen[at]
		total := visibleWidth(under)
		wide := visibleWidth(row)

		screen[at] = columns(under, 0, x) + styleReset + row + styleReset + columns(under, x+wide, total)
	}

	return screen
}

// columns is the part of row between the columns from and to, with the
// styles in force where it starts. A wide rune cut in two shows as spaces.
func columns(row string, from, to int) string {
	if to <= from {
		return ""
	}

	var out strings.Builder

	cells, _ := cellsOf(row)
	styles, column, started := "", 0, false

	for _, each := range cells {
		styles = carried(styles, each.lead)
		wide := runeWidth(each.r)
		inside := column+wide > from && column < to

		// What is in force where the part starts goes first, once.
		if inside && !started {
			out.WriteString(styles)
			started = true
		} else if inside {
			out.WriteString(each.lead)
		}

		switch {
		case !inside:
		case column < from || column+wide > to:
			out.WriteString(strings.Repeat(" ", min(column+wide, to)-max(column, from)))
		default:
			out.WriteRune(each.r)
		}

		column += wide
	}

	return out.String() + strings.Repeat(" ", max(to-max(column, from), 0)) + styleReset
}

// dimmed is a row as it shows under what floats over it: its text, without
// its styles, faint.
func dimmed(row string) string {
	return styleDim + stripped(row) + styleReset
}
