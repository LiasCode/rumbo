package computer

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os/exec"
	"strconv"
	"strings"
)

// x11Buttons are the X numbers of the pointer's buttons.
var x11Buttons = map[Button]string{Left: "1", Middle: "2", Right: "3"}

// x11Modifiers are the names xdotool gives the modifiers.
var x11Modifiers = map[Modifier]string{Shift: "shift", Control: "ctrl", Alt: "alt", Super: "super"}

// x11 is an X11 session, driven with xdotool. Screenshots come from maim,
// or ImageMagick's import, whichever is installed.
type x11 struct {
	// capture is the command that prints a PNG of the whole screen.
	capture []string
}

func openX11(_ context.Context) (Screen, error) {
	desktop := desktopName() + " (X11)"

	if _, err := exec.LookPath("xdotool"); err != nil {
		return nil, missing("xdotool", "moves the pointer and types", desktop)
	}

	for _, capture := range [][]string{
		{"maim", "--format", "png"},
		{"import", "-silent", "-window", "root", "png:-"},
		{"magick", "import", "-silent", "-window", "root", "png:-"},
	} {
		if _, err := exec.LookPath(capture[0]); err == nil {
			return &x11{capture: capture}, nil
		}
	}

	return nil, missing("maim (or ImageMagick)", "takes screenshots", desktop)
}

func (x *x11) Desktop() string {
	return desktopName() + " (X11)"
}

func (x *x11) Close() error {
	return nil
}

func (x *x11) Output(context.Context) (string, error) {
	return "screen", nil
}

func (x *x11) Capture(ctx context.Context) (Shot, error) {
	data, err := output(ctx, x.capture[0], x.capture[1:]...)

	if err != nil {
		return Shot{}, err
	}

	img, err := png.Decode(bytes.NewReader(data))

	if err != nil {
		return Shot{}, fmt.Errorf("reading %s's screenshot: %w", x.capture[0], err)
	}

	// X11 places windows in the screen's own pixels, all monitors together.
	bounds := img.Bounds()

	return Shot{Image: img, Area: Rect{Width: bounds.Dx(), Height: bounds.Dy()}, Output: "screen"}, nil
}

func (x *x11) Move(ctx context.Context, p Point) error {
	_, err := output(ctx, "xdotool", "mousemove", strconv.Itoa(p.X), strconv.Itoa(p.Y))

	return err
}

func (x *x11) Button(ctx context.Context, button Button, down bool) error {
	action := "mouseup"

	if down {
		action = "mousedown"
	}

	_, err := output(ctx, "xdotool", action, x11Buttons[button])

	return err
}

func (x *x11) Scroll(ctx context.Context, dx, dy int) error {
	// X turns a wheel with buttons: 4 up, 5 down, 6 left, 7 right.
	for _, turn := range []struct {
		steps             int
		backward, forward string
	}{{dy, "4", "5"}, {dx, "6", "7"}} {
		if turn.steps == 0 {
			continue
		}

		button, steps := turn.forward, turn.steps

		if steps < 0 {
			button, steps = turn.backward, -steps
		}

		if _, err := output(ctx, "xdotool", "click", "--repeat", strconv.Itoa(steps), "--delay", "30", button); err != nil {
			return err
		}
	}

	return nil
}

func (x *x11) Press(ctx context.Context, keys Keys, down bool) error {
	var names []string

	for _, modifier := range modifierOrder {
		if keys.Modifiers&modifier != 0 {
			names = append(names, x11Modifiers[modifier])
		}
	}

	if keys.Key != "" {
		names = append(names, keysym(keys.Key))
	}

	action := "keyup"

	if down {
		action = "keydown"
	}

	_, err := output(ctx, "xdotool", action, strings.Join(names, "+"))

	return err
}

func (x *x11) Type(ctx context.Context, text string) error {
	_, err := output(ctx, "xdotool", "type", "--delay", "8", "--", text)

	return err
}

func (x *x11) Pointer(ctx context.Context) (Point, error) {
	data, err := output(ctx, "xdotool", "getmouselocation", "--shell")

	if err != nil {
		return Point{}, err
	}

	var at Point

	for line := range strings.Lines(string(data)) {
		name, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		number, _ := strconv.Atoi(value)

		switch name {
		case "X":
			at.X = number
		case "Y":
			at.Y = number
		}
	}

	return at, nil
}
