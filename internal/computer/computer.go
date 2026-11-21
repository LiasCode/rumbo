// Package computer is the screen of the machine rumbo runs on, and the
// pointer and keyboard that act on it, for the computer tool. Open finds
// out which desktop it is and acts through what that desktop offers: on a
// wlroots compositor such as Hyprland or Sway, its virtual pointer and
// keyboard and grim; on X11, xdotool; on macOS, screencapture and Quartz
// events through osascript.
package computer

import (
	"context"
	"errors"
	"image"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Point is a place on the screen, in the coordinates the desktop places
// windows and the pointer in, across all of its monitors.
type Point struct {
	X, Y int
}

// Rect is an area of the screen, in the same coordinates as Point.
type Rect struct {
	X, Y, Width, Height int
}

// Contains reports whether p is in the area.
func (r Rect) Contains(p Point) bool {
	return p.X >= r.X && p.Y >= r.Y && p.X < r.X+r.Width && p.Y < r.Y+r.Height
}

// Shot is a picture of one monitor. Image has the monitor's own pixels,
// which on a scaled monitor are more than Area holds.
type Shot struct {
	Image image.Image
	// Area is where the monitor is.
	Area Rect
	// Output is the monitor's name.
	Output string
}

// Button is a pointer button.
type Button int

// The pointer's buttons.
const (
	Left Button = iota
	Middle
	Right
)

// Screen is the desktop rumbo runs in. Its methods act on it at once; none
// of them waits for what a program does in answer.
type Screen interface {
	// Desktop names the desktop, such as "Hyprland (Wayland)".
	Desktop() string
	// Capture takes a picture of the monitor in use.
	Capture(ctx context.Context) (Shot, error)
	// Output names the monitor in use, which Capture would take.
	Output(ctx context.Context) (string, error)
	// Move puts the pointer at p.
	Move(ctx context.Context, p Point) error
	// Button presses or releases a button where the pointer is.
	Button(ctx context.Context, button Button, down bool) error
	// Scroll turns the wheel by notches: a positive dy is down, a positive
	// dx right.
	Scroll(ctx context.Context, dx, dy int) error
	// Press presses the keys of a combination, the modifiers first, or
	// releases them, in the other order.
	Press(ctx context.Context, keys Keys, down bool) error
	// Type types text as it is written.
	Type(ctx context.Context, text string) error
	// Pointer returns where the pointer is. ErrUnknown says the desktop
	// does not tell, and the pointer has not been moved yet.
	Pointer(ctx context.Context) (Point, error)
	Close() error
}

// ErrUnknown is what Pointer returns when where the pointer is cannot be
// known.
var ErrUnknown = errors.New("this desktop does not say where the pointer is, and it has not been moved yet")

// Open returns the screen of the desktop rumbo runs in, acting through the
// tools that desktop has. The error says why there is none, and what would
// give one.
func Open(ctx context.Context) (Screen, error) {
	switch runtime.GOOS {
	case "darwin":
		return openDarwin(ctx)
	case "windows":
		return nil, errors.New("the computer tool does not support Windows")
	}

	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return openWayland(ctx)
	}

	if os.Getenv("DISPLAY") != "" {
		return openX11(ctx)
	}

	return nil, errors.New("rumbo is not running in a desktop session: neither WAYLAND_DISPLAY nor DISPLAY is set, so there is no screen to act on. Start rumbo from a terminal of the desktop")
}

// desktopName is the desktop the session says it is, for messages.
func desktopName() string {
	if name := os.Getenv("XDG_CURRENT_DESKTOP"); name != "" {
		return strings.ReplaceAll(name, ":", ", ")
	}

	return "this desktop"
}

// output runs a program and returns what it printed, with what it printed
// on its standard error in the error when it fails.
func output(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)

	var stderr strings.Builder
	command.Stderr = &stderr

	out, err := command.Output()

	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return out, errors.New(name + ": " + message)
		}

		return out, errors.New(name + ": " + err.Error())
	}

	return out, nil
}

// missing returns an error naming the program a desktop needs and does not
// have.
func missing(program, purpose, desktop string) error {
	return errors.New(program + " is not installed, and on " + desktop + " it is what " + purpose + ": install it and try again")
}
