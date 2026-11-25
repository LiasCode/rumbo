package tui

import (
	"fmt"
	"strings"

	"github.com/LiasCode/rumbo/internal/frontends/render"
)

// A panel shows something beside the conversation, as in rumbo-gui: the
// session's details, or what changed in the project's folder.
const (
	panelNone = iota
	panelDetails
	panelChanges
)

// reading is what was asked for a panel: what came, or why it did not.
type reading struct {
	text    string
	err     error
	waiting bool
}

// panels are the panel shown, how far it is scrolled, and what it shows.
type panels struct {
	shown int
	top   int
	// status and skills are what the host says of the session shown.
	status reading
	skills reading
	// changed is what Git says changed, and asked numbers the readings, so
	// one that comes after a later one is left aside.
	changed      *changes
	changedError error
	changing     bool
	asked        int
}

// details are what the details panel shows of the session: who it is, the
// folder it works in, the model it uses, and what its runs spent, which
// the app knows, over what the host says of it.
type details struct {
	session string
	title   string
	dir     string
	model   string
	meter   string
	spent   string
}

// detailRows draws the session's details in rows of width columns.
func (p *panels) detailRows(t theme, shown details, width int) []string {
	var rows []string

	section := func(title string, body ...string) {
		if len(rows) > 0 {
			rows = append(rows, "")
		}

		rows = append(rows, styleDim+strings.ToUpper(title)+styleReset)
		rows = append(rows, body...)
	}

	if shown.session == "" {
		section("session", styleDim+"New; its first task starts it."+styleReset)
	} else {
		section("session", append([]string{styleBold + shown.session + styleReset}, wrapAll(styleDim+shown.title+styleReset, width)...)...)
	}

	section("folder", wrapAll(shown.dir, width)...)
	section("model", cut(shown.model, width), styleDim+"/model changes it"+styleReset)

	if shown.meter != "" || shown.spent != "" {
		section("context and tokens", cut(shown.meter, width), cut(shown.spent, width))
	}

	said := func(title string, read reading) {
		switch {
		case read.waiting && read.text == "":
			section(title, styleDim+"asking rumbo…"+styleReset)
		case read.err != nil:
			section(title, wrapAll(t.failure+read.err.Error()+styleReset, width)...)
		case read.text != "":
			section(title, outlined(read.text, width)...)
		}
	}

	said("skills", p.skills)
	said("everything rumbo reports", p.status)

	return rows
}

// changeRows draws what changed in rows of width columns: the branch and
// how much changed, then each file with its hunks, what was added in the
// color of success and what was taken away in that of failure.
func (p *panels) changeRows(t theme, width int) []string {
	switch {
	case p.changedError != nil:
		return wrapAll(t.failure+p.changedError.Error()+styleReset, width)
	case p.changed == nil:
		return []string{styleDim + "asking Git…" + styleReset}
	}

	files := filesOf(p.changed.patch)
	added, removed := 0, 0

	for _, file := range files {
		added += file.added
		removed += file.removed
	}

	var summary []string

	if p.changed.branch != "" {
		summary = append(summary, "on "+p.changed.branch)
	}

	switch len(files) {
	case 0:
		summary = append(summary, "nothing changed since the last commit")
	case 1:
		summary = append(summary, "1 file")
	default:
		summary = append(summary, fmt.Sprintf("%d files", len(files)))
	}

	line := strings.Join(summary, " · ")

	if len(files) > 0 {
		line += " · " + t.success + fmt.Sprintf("+%d", added) + styleColorOff + " " + t.failure + fmt.Sprintf("−%d", removed) + styleColorOff
	}

	rows := []string{cut(line, width)}

	if p.changed.moreNew > 0 {
		rows = append(rows, styleDim+fmt.Sprintf("and %d more new files, not shown", p.changed.moreNew)+styleReset)
	}

	for _, file := range files {
		counts := t.success + fmt.Sprintf("+%d", file.added) + styleColorOff + " " + t.failure + fmt.Sprintf("−%d", file.removed) + styleColorOff
		rows = append(rows, "", spread(styleBold+cut(file.path, max(width-12, 8))+styleWeightOff, counts, width))

		for _, text := range file.lines {
			style := ""

			switch {
			case strings.HasPrefix(text, "@@"), strings.HasPrefix(text, "Binary files "), strings.HasPrefix(text, `\`):
				style = styleDim
			case strings.HasPrefix(text, "+"):
				style = t.success
			case strings.HasPrefix(text, "-"):
				style = t.failure
			}

			rows = append(rows, cut(style+untabbed(text)+styleReset, width))
		}
	}

	return rows
}

// panelRows draws the panel shown, height rows high and width columns
// wide: its title over what it shows, scrolled to top, which is kept
// within what there is.
func (p *panels) panelRows(t theme, shown details, width, height int, focused bool) []string {
	title, body, toggle := "Session", []string(nil), "Ctrl-Alt-B"
	inner := max(width-2, 1)

	switch p.shown {
	case panelDetails:
		body = p.detailRows(t, shown, inner)
	case panelChanges:
		title, toggle = "Changes", "Ctrl-G"
		body = p.changeRows(t, inner)
	}

	keys := toggle + " puts it away"

	if focused {
		keys = "↑ ↓ scroll · Esc puts it away"
	}

	if p.changing && p.shown == panelChanges {
		keys = "reading again…"
	}

	rows := []string{" " + spread(styleBold+title+styleReset, styleDim+keys+styleReset, inner), ""}
	room := max(height-len(rows), 1)
	p.top = max(min(p.top, len(body)-room), 0)

	for _, row := range body[p.top:min(p.top+room, len(body))] {
		rows = append(rows, " "+row)
	}

	for len(rows) < height {
		rows = append(rows, "")
	}

	return rows[:height]
}

// spentOf says what a session spent, as the composer and the details say
// it.
func spentOf(usage render.Usage) string {
	total := usage.Total()

	if render.Input(total)+total.Output == 0 {
		return ""
	}

	return fmt.Sprintf("%s in, %s cached, %s out", tokens(render.Input(total)), tokens(total.CacheRead), tokens(total.Output))
}
