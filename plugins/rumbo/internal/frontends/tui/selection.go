package tui

import (
	"strings"
)

// Text is chosen with the pointer as a terminal chooses it, but within the
// part of the screen the drag started in, the conversation or the panel, so
// what is copied is that part's text and nothing beside it: from where the
// button went down to where it is, every row between taken whole.

// point is a cell of the screen.
type point struct {
	x, y int
}

// area is a part of the screen: from its first column and row to before
// its last ones.
type area struct {
	left, top, right, bottom int
}

// holds reports whether a cell is in the area.
func (r area) holds(at point) bool {
	return at.x >= r.left && at.x < r.right && at.y >= r.top && at.y < r.bottom
}

// clamp is the cell of the area nearest to at.
func (r area) clamp(at point) point {
	return point{x: max(min(at.x, r.right-1), r.left), y: max(min(at.y, r.bottom-1), r.top)}
}

// selection is text chosen with the pointer, from one cell to another of an
// area.
type selection struct {
	from, to point
	within   area
}

// spans are the columns chosen on each row, from the first row chosen.
func (s *selection) spans() (int, [][2]int) {
	start, end := s.from, s.to

	if end.y < start.y || end.y == start.y && end.x < start.x {
		start, end = end, start
	}

	var spans [][2]int

	for row := start.y; row <= end.y; row++ {
		left, right := s.within.left, s.within.right

		if row == start.y {
			left = start.x
		}

		if row == end.y {
			right = end.x + 1
		}

		spans = append(spans, [2]int{left, right})
	}

	return start.y, spans
}

// text is what is chosen on the screen's rows: each row's part without its
// styles and the spaces it ends with, one under the other.
func (s *selection) text(rows []string) string {
	first, spans := s.spans()

	var lines []string

	for index, span := range spans {
		if at := first + index; at < len(rows) {
			lines = append(lines, strings.TrimRight(stripped(columns(rows[at], span[0], span[1])), " "))
		}
	}

	return strings.Join(lines, "\n")
}

// highlighted is the screen's rows with what is chosen in reverse.
func (s *selection) highlighted(rows []string) []string {
	first, spans := s.spans()

	for index, span := range spans {
		at := first + index

		if at >= len(rows) {
			break
		}

		row := rows[at]
		chosen := styleReverse + stripped(columns(row, span[0], span[1])) + styleReset
		rows[at] = columns(row, 0, span[0]) + chosen + columns(row, span[1], visibleWidth(row))
	}

	return rows
}
