package tool

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/computer"
)

// fakeScreen is a monitor of 1920×1200 that records what is done to it.
type fakeScreen struct {
	area    computer.Rect
	output  string
	pointer computer.Point
	events  []string
	fail    error
	closed  bool
}

func newFakeScreen() *fakeScreen {
	return &fakeScreen{area: computer.Rect{X: 4000, Width: 1920, Height: 1200}, output: "HDMI-A-1", pointer: computer.Point{X: 4100, Y: 100}}
}

func (f *fakeScreen) Desktop() string { return "Test (Wayland)" }

func (f *fakeScreen) Output(context.Context) (string, error) { return f.output, nil }

func (f *fakeScreen) Capture(context.Context) (computer.Shot, error) {
	if f.fail != nil {
		return computer.Shot{}, f.fail
	}

	return computer.Shot{Image: image.NewRGBA(image.Rect(0, 0, 1920, 1200)), Area: f.area, Output: f.output}, nil
}

func (f *fakeScreen) Move(_ context.Context, p computer.Point) error {
	f.pointer = p
	f.events = append(f.events, fmt.Sprintf("move %d,%d", p.X, p.Y))

	return nil
}

func (f *fakeScreen) Button(_ context.Context, button computer.Button, down bool) error {
	f.events = append(f.events, fmt.Sprintf("button %d %v", button, down))

	return nil
}

func (f *fakeScreen) Scroll(_ context.Context, dx, dy int) error {
	f.events = append(f.events, fmt.Sprintf("scroll %d,%d", dx, dy))

	return nil
}

func (f *fakeScreen) Press(_ context.Context, keys computer.Keys, down bool) error {
	f.events = append(f.events, fmt.Sprintf("press %d+%s %v", keys.Modifiers, keys.Key, down))

	return nil
}

func (f *fakeScreen) Type(_ context.Context, text string) error {
	f.events = append(f.events, "type "+text)

	return nil
}

func (f *fakeScreen) Pointer(context.Context) (computer.Point, error) {
	return f.pointer, nil
}

func (f *fakeScreen) Close() error {
	f.closed = true

	return nil
}

// computerTool returns the tool on a fake screen, and how many times the
// screen was opened.
func computerTool(screen *fakeScreen) (Computer, *int) {
	opened := 0
	display := &display{open: func(context.Context) (computer.Screen, error) {
		opened++

		return screen, nil
	}}

	return Computer{display: display}, &opened
}

func act(t *testing.T, tool Computer, args map[string]any) (Result, error) {
	t.Helper()

	return runTool(t, tool, t.TempDir(), args)
}

func TestAScreenshotIsShownSmaller(t *testing.T) {
	tool, _ := computerTool(newFakeScreen())
	result, err := act(t, tool, map[string]any{"action": "screenshot"})

	if err != nil {
		t.Fatal(err)
	}

	if width, height := size(t, result); width != 1280 || height != 800 {
		t.Fatalf("shown at %d×%d", width, height)
	}

	if !strings.Contains(result.Text, "HDMI-A-1") || !strings.Contains(result.Text, "1280×800") {
		t.Fatalf("text = %q", result.Text)
	}
}

// The model points at the screenshot it was shown, which the tool maps to
// the monitor: here two thirds of its size, and beside another monitor.
func TestAClickLandsWhereTheScreenshotShowed(t *testing.T) {
	screen := newFakeScreen()
	tool, _ := computerTool(screen)

	if _, err := act(t, tool, map[string]any{"action": "screenshot"}); err != nil {
		t.Fatal(err)
	}

	result, err := act(t, tool, map[string]any{"action": "double_click", "coordinate": []int{640, 400}})

	if err != nil {
		t.Fatal(err)
	}

	want := []string{"move 4960,600", "button 0 true", "button 0 false", "button 0 true", "button 0 false"}

	if strings.Join(screen.events, "; ") != strings.Join(want, "; ") {
		t.Fatalf("events = %v", screen.events)
	}

	if width, _ := size(t, result); width != 1280 || !strings.HasPrefix(result.Text, "double click at [640, 400]") {
		t.Fatalf("answer = %q", result.Text)
	}

	cursor, err := act(t, tool, map[string]any{"action": "cursor_position"})

	if err != nil || !strings.Contains(cursor.Text, "[640, 400]") {
		t.Fatalf("cursor = %q, %v", cursor.Text, err)
	}
}

func TestNothingIsDoneBeforeAScreenshot(t *testing.T) {
	screen := newFakeScreen()
	tool, _ := computerTool(screen)

	if _, err := act(t, tool, map[string]any{"action": "type", "text": "hello"}); err == nil || !strings.Contains(err.Error(), "screenshot first") {
		t.Fatalf("err = %v", err)
	}

	if len(screen.events) != 0 || screen.closed {
		t.Fatalf("events %v, closed %v", screen.events, screen.closed)
	}
}

// A person who moves the pointer, or turns to another monitor, has taken
// the computer back: the tool does nothing until the model looks again.
func TestTheToolStopsWhenSomeoneElseUsesTheComputer(t *testing.T) {
	screen := newFakeScreen()
	tool, _ := computerTool(screen)

	act(t, tool, map[string]any{"action": "screenshot"})
	screen.pointer.X += 50

	if _, err := act(t, tool, map[string]any{"action": "key", "text": "Return"}); err == nil || !strings.Contains(err.Error(), "someone else") {
		t.Fatalf("err = %v", err)
	}

	if _, err := act(t, tool, map[string]any{"action": "key", "text": "Return"}); err == nil || !strings.Contains(err.Error(), "screenshot first") {
		t.Fatalf("after a refusal, err = %v", err)
	}

	act(t, tool, map[string]any{"action": "screenshot"})
	screen.output = "eDP-1"

	if _, err := act(t, tool, map[string]any{"action": "left_click", "coordinate": []int{1, 1}}); err == nil || !strings.Contains(err.Error(), "eDP-1") {
		t.Fatalf("err = %v", err)
	}

	if len(screen.events) != 0 || screen.closed {
		t.Fatalf("events %v, closed %v", screen.events, screen.closed)
	}

	// Its own moves are not someone else's.
	screen.output = "HDMI-A-1"
	act(t, tool, map[string]any{"action": "screenshot"})

	for range 2 {
		if _, err := act(t, tool, map[string]any{"action": "mouse_move", "coordinate": []int{10, 10}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestADragMovesAllTheWay(t *testing.T) {
	screen := newFakeScreen()
	tool, _ := computerTool(screen)
	act(t, tool, map[string]any{"action": "screenshot"})

	if _, err := act(t, tool, map[string]any{"action": "left_click_drag", "start_coordinate": []int{0, 0}, "coordinate": []int{1279, 799}}); err != nil {
		t.Fatal(err)
	}

	events := screen.events

	if len(events) != 3+dragSteps || events[0] != "move 4000,0" || events[1] != "button 0 true" || events[len(events)-2] != "move 5919,1199" || events[len(events)-1] != "button 0 false" {
		t.Fatalf("events = %v", events)
	}
}

func TestScrollsTurnTheWheelWithModifiersHeld(t *testing.T) {
	screen := newFakeScreen()
	tool, _ := computerTool(screen)
	act(t, tool, map[string]any{"action": "screenshot"})

	for _, args := range []map[string]any{
		{"action": "scroll", "scroll_direction": "down"},
		{"action": "scroll", "scroll_direction": "left", "scroll_amount": 2, "text": "shift"},
	} {
		if _, err := act(t, tool, args); err != nil {
			t.Fatal(err)
		}
	}

	want := "scroll 0,3; press 1+ true; scroll -2,0; press 1+ false"

	if got := strings.Join(screen.events, "; "); got != want {
		t.Fatalf("events = %s", got)
	}

	if _, err := act(t, tool, map[string]any{"action": "scroll", "scroll_direction": "sideways"}); err == nil {
		t.Fatal("a scroll with no direction was done")
	}
}

func TestKeysArePressedInTurn(t *testing.T) {
	screen := newFakeScreen()
	tool, _ := computerTool(screen)
	act(t, tool, map[string]any{"action": "screenshot"})

	if _, err := act(t, tool, map[string]any{"action": "key", "text": "ctrl+a BackSpace"}); err != nil {
		t.Fatal(err)
	}

	want := "press 2+a true; press 2+a false; press 0+BackSpace true; press 0+BackSpace false"

	if got := strings.Join(screen.events, "; "); got != want {
		t.Fatalf("events = %s", got)
	}

	for _, args := range []map[string]any{
		{"action": "key", "text": "notakey"},
		{"action": "hold_key", "text": "shift"},
		{"action": "left_click", "coordinate": []int{1, 1}, "text": "ctrl+a"},
		{"action": "left_click", "coordinate": []int{1280, 1}},
		{"action": "zoom", "region": []int{10, 10, 5, 20}},
		{"action": "fly"},
	} {
		if _, err := act(t, tool, args); err == nil {
			t.Errorf("%v was done", args)
		}
	}

	if screen.closed {
		t.Fatal("a call the model got wrong closed the screen")
	}
}

func TestAZoomShowsTheScreensOwnPixels(t *testing.T) {
	tool, _ := computerTool(newFakeScreen())
	act(t, tool, map[string]any{"action": "screenshot"})

	result, err := act(t, tool, map[string]any{"action": "zoom", "region": []int{0, 0, 640, 400}})

	if err != nil {
		t.Fatal(err)
	}

	if width, height := size(t, result); width != 960 || height != 600 {
		t.Fatalf("zoom shown at %d×%d", width, height)
	}
}

func TestAScreenThatFailsIsOpenedAgain(t *testing.T) {
	screen := newFakeScreen()
	tool, opened := computerTool(screen)
	screen.fail = errors.New("the compositor went away")

	if _, err := act(t, tool, map[string]any{"action": "screenshot"}); err == nil {
		t.Fatal("no error")
	}

	screen.fail = nil

	if _, err := act(t, tool, map[string]any{"action": "screenshot"}); err != nil {
		t.Fatal(err)
	}

	if !screen.closed || *opened != 2 {
		t.Fatalf("closed %v, opened %d times", screen.closed, *opened)
	}
}
