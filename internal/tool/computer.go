package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"strings"
	"sync"
	"time"

	"github.com/LiasCode/rumbo/internal/computer"
	"github.com/LiasCode/rumbo/internal/picture"
	"github.com/LiasCode/rumbo/protocol"
)

// What the computer tool shows and how it waits.
const (
	// screenWidth and screenHeight bound a screenshot. Models are shown a
	// screen of about this size, and point at it best, so a larger one is
	// made smaller, and the model's coordinates are made larger to match.
	screenWidth  = 1280
	screenHeight = 800
	// answerDelay is how long an action is given to show on the screen
	// before the screenshot that answers it.
	answerDelay = 700 * time.Millisecond
	// The longest wait and the longest a key may be held.
	maxWait = 30 * time.Second
	maxHold = 10 * time.Second
	// defaultScroll is how many notches a scroll turns when none is given.
	defaultScroll = 3
	// clickGap is the pause between the clicks of a double or triple click,
	// well inside the time a desktop counts them together in.
	clickGap = 60 * time.Millisecond
	// dragSteps is how many moves a drag makes between its ends, since a
	// program follows a drag by the pointer's moves.
	dragSteps = 12
	// moved is how far the pointer may be from where the tool left it, in
	// the desktop's pixels, before someone else is taken to have moved it.
	moved = 4
)

// Computer lets the model see the screen of the machine rumbo runs on and
// use its pointer and keyboard, as a person at it would.
type Computer struct {
	display *display
}

// display is the screen the computer tool acts on. It is opened at its first
// use, and kept while the host lasts.
type display struct {
	mu   sync.Mutex
	open func(context.Context) (computer.Screen, error)
	// delay is how long an action waits before its screenshot.
	delay  time.Duration
	screen computer.Screen
	// view is the last screenshot, whose pixels the model's coordinates are.
	view *view
	// left is where the pointer was when the tool last acted or looked, when
	// the desktop says where it is.
	left *computer.Point
}

// view is what a screenshot showed: where the monitor is, and the size it
// was shown at.
type view struct {
	area          computer.Rect
	width, height int
	output        string
}

// inputError is a call that was not carried out, because the model got it
// wrong or the screen is not as it was shown, as opposed to a screen that
// failed; the screen stays open after it.
type inputError struct {
	message string
}

func (e inputError) Error() string {
	return e.message
}

func wrong(format string, args ...any) error {
	return inputError{message: fmt.Sprintf(format, args...)}
}

// Def implements Tool.
func (Computer) Def() protocol.ToolDef {
	return protocol.ToolDef{
		Name:        "computer",
		Description: "See and use the screen of the machine you run on, as a person at it would: take screenshots, move the pointer, click, drag, scroll, type, and press keys. Coordinates are pixels of the last screenshot. Every action but screenshot, zoom, and cursor_position answers with a screenshot taken after it. Start the programs to work with through bash.",
		Schema: schema(`{
			"action": {"type": "string", "enum": ["screenshot", "zoom", "cursor_position", "mouse_move", "left_click", "right_click", "middle_click", "double_click", "triple_click", "left_click_drag", "left_mouse_down", "left_mouse_up", "scroll", "type", "key", "hold_key", "wait"]},
			"coordinate": {"type": "array", "items": {"type": "integer"}, "minItems": 2, "maxItems": 2, "description": "[x, y] in the last screenshot: where to move, click, or scroll, and where left_click_drag ends. Without it, a click or scroll acts where the pointer is"},
			"start_coordinate": {"type": "array", "items": {"type": "integer"}, "minItems": 2, "maxItems": 2, "description": "[x, y] where left_click_drag starts"},
			"text": {"type": "string", "description": "type: the text. key: keys as xdotool names them, such as Return, ctrl+s, or ctrl+a BackSpace to press one and then the other. hold_key: the keys to hold. A click or scroll: modifiers to hold, such as shift"},
			"scroll_direction": {"type": "string", "enum": ["up", "down", "left", "right"]},
			"scroll_amount": {"type": "integer", "description": "Notches of the wheel to turn; 3 when not given"},
			"duration": {"type": "number", "description": "Seconds that wait waits (1 when not given) or hold_key holds"},
			"region": {"type": "array", "items": {"type": "integer"}, "minItems": 4, "maxItems": 4, "description": "zoom: [x0, y0, x1, y1] of the last screenshot, to see at the screen's full resolution"}
		}`, "action"),
	}
}

// computerInput is what a call of the computer tool asks.
type computerInput struct {
	Action          string  `json:"action"`
	Coordinate      []int   `json:"coordinate"`
	StartCoordinate []int   `json:"start_coordinate"`
	Text            string  `json:"text"`
	ScrollDirection string  `json:"scroll_direction"`
	ScrollAmount    int     `json:"scroll_amount"`
	Duration        float64 `json:"duration"`
	Region          []int   `json:"region"`
}

// Run implements Tool.
func (c Computer) Run(ctx context.Context, _ string, args json.RawMessage) (Result, error) {
	var input computerInput

	if err := decode(args, &input); err != nil {
		return Result{}, err
	}

	return c.display.run(ctx, input)
}

func (d *display) run(ctx context.Context, input computerInput) (Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.screen == nil {
		screen, err := d.open(ctx)

		if err != nil {
			return Result{}, err
		}

		d.screen = screen
	}

	result, err := d.act(ctx, input)

	// A screen that failed, such as a compositor that restarted, is opened
	// again on the next call.
	var mistake inputError

	if err != nil && !errors.As(err, &mistake) {
		d.close()
	}

	return result, err
}

func (d *display) close() {
	if d.screen != nil {
		d.screen.Close()
	}

	d.screen = nil
	d.view = nil
	d.left = nil
}

// actions are the tool's actions, each with whether it only looks at the
// screen.
var actions = map[string]bool{
	"screenshot": true, "zoom": true, "cursor_position": true, "wait": true,
	"mouse_move": false, "left_click": false, "right_click": false, "middle_click": false,
	"double_click": false, "triple_click": false, "left_click_drag": false,
	"left_mouse_down": false, "left_mouse_up": false, "scroll": false,
	"type": false, "key": false, "hold_key": false,
}

// guard refuses to act on a screen that is not as the model last saw it:
// before a screenshot, on a monitor other than the screenshot's, or after
// someone else moved the pointer. A person who takes the computer back
// stops the model at once, instead of having it type into what they are
// doing.
func (d *display) guard(ctx context.Context) error {
	if d.view == nil {
		return wrong("take a screenshot first: the computer tool acts only on a screen the model has seen")
	}

	output, err := d.screen.Output(ctx)

	if err != nil {
		return err
	}

	if shown := d.view.output; output != shown {
		d.view, d.left = nil, nil

		return wrong("nothing was done: the monitor in use is now %s, not the last screenshot's %s. Take a screenshot to see it", output, shown)
	}

	if d.left == nil {
		return nil
	}

	at, err := d.screen.Pointer(ctx)

	if err != nil {
		return nil
	}

	if abs(at.X-d.left.X) > moved || abs(at.Y-d.left.Y) > moved {
		d.view, d.left = nil, nil

		return wrong("nothing was done: the pointer is not where the last action left it, so someone else is using the computer. Take a screenshot to see the screen as it is now, and go on only if the task still needs it")
	}

	return nil
}

// remember notes where the pointer is now, which guard compares with.
func (d *display) remember(ctx context.Context) {
	if at, err := d.screen.Pointer(ctx); err == nil {
		d.left = &at
	} else {
		d.left = nil
	}
}

func abs(value int) int {
	if value < 0 {
		return -value
	}

	return value
}

// act carries out one action. Those that change what is on the screen
// answer with a screenshot taken once the screen has had time to show it.
func (d *display) act(ctx context.Context, input computerInput) (Result, error) {
	screen := d.screen
	var did string

	looks, known := actions[input.Action]

	if !known {
		return Result{}, wrong("unknown action %q", input.Action)
	}

	if !looks {
		if err := d.guard(ctx); err != nil {
			return Result{}, err
		}
	}

	switch input.Action {
	case "screenshot":
		d.remember(ctx)

		return d.screenshot(ctx, "")

	case "zoom":
		return d.zoom(ctx, input.Region)

	case "cursor_position":
		return d.cursor(ctx)

	case "mouse_move":
		at, err := d.point(input.Coordinate, "coordinate")

		if err != nil {
			return Result{}, err
		}

		if err := screen.Move(ctx, at); err != nil {
			return Result{}, err
		}

		did = fmt.Sprintf("moved the pointer to %s", position(input.Coordinate))

	case "left_click", "right_click", "middle_click", "double_click", "triple_click":
		button, count := computer.Left, 1

		switch input.Action {
		case "right_click":
			button = computer.Right
		case "middle_click":
			button = computer.Middle
		case "double_click":
			count = 2
		case "triple_click":
			count = 3
		}

		if err := d.click(ctx, input, button, count); err != nil {
			return Result{}, err
		}

		did = fmt.Sprintf("%s%s", strings.ReplaceAll(input.Action, "_", " "), where(input.Coordinate))

	case "left_click_drag":
		if err := d.drag(ctx, input); err != nil {
			return Result{}, err
		}

		did = fmt.Sprintf("dragged from %s to %s", position(input.StartCoordinate), position(input.Coordinate))

	case "left_mouse_down", "left_mouse_up":
		if err := d.moveIfGiven(ctx, input.Coordinate); err != nil {
			return Result{}, err
		}

		down := input.Action == "left_mouse_down"

		if err := screen.Button(ctx, computer.Left, down); err != nil {
			return Result{}, err
		}

		did = fmt.Sprintf("%s%s", strings.ReplaceAll(input.Action, "_", " "), where(input.Coordinate))

	case "scroll":
		if err := d.scroll(ctx, input); err != nil {
			return Result{}, err
		}

		amount := input.ScrollAmount

		if amount == 0 {
			amount = defaultScroll
		}

		did = fmt.Sprintf("scrolled %s %d notch(es)%s", input.ScrollDirection, amount, where(input.Coordinate))

	case "type":
		if input.Text == "" {
			return Result{}, wrong("type needs the text to type in text")
		}

		if err := screen.Type(ctx, input.Text); err != nil {
			return Result{}, err
		}

		did = fmt.Sprintf("typed %d character(s)", len([]rune(input.Text)))

	case "key":
		sequence, err := computer.ParseSequence(input.Text)

		if err != nil {
			return Result{}, inputError{message: err.Error()}
		}

		for index, keys := range sequence {
			if index > 0 {
				if err := pause(ctx, clickGap); err != nil {
					return Result{}, err
				}
			}

			if err := press(ctx, screen, keys, 0); err != nil {
				return Result{}, err
			}
		}

		did = fmt.Sprintf("pressed %s", input.Text)

	case "hold_key":
		keys, err := computer.ParseKeys(input.Text)

		if err != nil {
			return Result{}, inputError{message: err.Error()}
		}

		hold := time.Duration(input.Duration * float64(time.Second))

		if hold <= 0 || hold > maxHold {
			return Result{}, wrong("hold_key needs a duration of more than 0 and at most %d seconds", int(maxHold.Seconds()))
		}

		if err := press(ctx, screen, keys, hold); err != nil {
			return Result{}, err
		}

		did = fmt.Sprintf("held %s for %g second(s)", input.Text, input.Duration)

	case "wait":
		wait := time.Duration(input.Duration * float64(time.Second))

		if input.Duration == 0 {
			wait = time.Second
		}

		if wait < 0 || wait > maxWait {
			return Result{}, wrong("wait waits at most %d seconds", int(maxWait.Seconds()))
		}

		if err := pause(ctx, wait); err != nil {
			return Result{}, err
		}

		return d.screenshot(ctx, fmt.Sprintf("waited %g second(s)", wait.Seconds()))

	}

	// Where the action left the pointer, before the screen has had time to
	// answer, so a move by someone else while it does is noticed.
	d.remember(ctx)

	if err := pause(ctx, d.delay); err != nil {
		return Result{}, err
	}

	return d.screenshot(ctx, did)
}

// screenshot takes a picture of the monitor in use, shown at most at
// screenWidth×screenHeight, and makes it the one coordinates are in.
func (d *display) screenshot(ctx context.Context, did string) (Result, error) {
	shot, err := d.screen.Capture(ctx)

	if err != nil {
		return Result{}, err
	}

	bounds := shot.Image.Bounds()
	width, height := picture.Fit(bounds.Dx(), bounds.Dy(), screenWidth, screenHeight, 0)
	shown, err := picture.PNG(picture.Scale(shot.Image, width, height))

	if err != nil {
		return Result{}, err
	}

	d.view = &view{area: shot.Area, width: width, height: height, output: shot.Output}
	about := fmt.Sprintf("[screenshot of %s, %s, %d×%d]", shot.Output, d.screen.Desktop(), width, height)

	if did != "" {
		about = did + ". " + about
	}

	return Result{Text: about, Images: []protocol.Image{shown}}, nil
}

// zoom shows part of the last screenshot at the monitor's own resolution,
// taken again, so small text can be read. Coordinates stay those of the
// last screenshot.
func (d *display) zoom(ctx context.Context, region []int) (Result, error) {
	if d.view == nil {
		return Result{}, wrong("take a screenshot first: a zoom's region is in the last screenshot's pixels")
	}

	if len(region) != 4 || region[0] >= region[2] || region[1] >= region[3] {
		return Result{}, wrong("zoom needs region as [x0, y0, x1, y1], with x0 < x1 and y0 < y1")
	}

	if region[0] < 0 || region[1] < 0 || region[2] > d.view.width || region[3] > d.view.height {
		return Result{}, wrong("region %v is outside the last screenshot, which is %d×%d", region, d.view.width, d.view.height)
	}

	shot, err := d.screen.Capture(ctx)

	if err != nil {
		return Result{}, err
	}

	if shot.Output != d.view.output {
		return Result{}, wrong("the monitor in use is now %s, not the last screenshot's %s: take a screenshot first", shot.Output, d.view.output)
	}

	bounds := shot.Image.Bounds()
	crop := image.Rect(
		bounds.Min.X+region[0]*bounds.Dx()/d.view.width,
		bounds.Min.Y+region[1]*bounds.Dy()/d.view.height,
		bounds.Min.X+region[2]*bounds.Dx()/d.view.width,
		bounds.Min.Y+region[3]*bounds.Dy()/d.view.height,
	)

	part, ok := shot.Image.(interface {
		SubImage(image.Rectangle) image.Image
	})

	if !ok {
		return Result{}, errors.New("this screenshot cannot be cut")
	}

	cut := part.SubImage(crop)
	width, height := picture.Fit(crop.Dx(), crop.Dy(), screenWidth, screenHeight, 0)
	shown, err := picture.PNG(picture.Scale(cut, width, height))

	if err != nil {
		return Result{}, err
	}

	about := fmt.Sprintf("[zoom of %v of the last screenshot, shown at %d×%d; coordinates are still the last screenshot's]", region, width, height)

	return Result{Text: about, Images: []protocol.Image{shown}}, nil
}

func (d *display) cursor(ctx context.Context) (Result, error) {
	if d.view == nil {
		if _, err := d.screenshot(ctx, ""); err != nil {
			return Result{}, err
		}
	}

	at, err := d.screen.Pointer(ctx)

	if err != nil {
		return Result{}, err
	}

	area := d.view.area

	if !area.Contains(at) {
		return text(fmt.Sprintf("the pointer is off the monitor of the last screenshot (%s)", d.view.output)), nil
	}

	x := (at.X - area.X) * d.view.width / area.Width
	y := (at.Y - area.Y) * d.view.height / area.Height

	return text(fmt.Sprintf("the pointer is at [%d, %d] of the last screenshot", x, y)), nil
}

// point turns coordinates of the last screenshot into a place on the
// screen.
func (d *display) point(coordinate []int, name string) (computer.Point, error) {
	if len(coordinate) != 2 {
		return computer.Point{}, wrong("%s needs to be [x, y]", name)
	}

	if d.view == nil {
		return computer.Point{}, wrong("take a screenshot first: coordinates are in the last screenshot's pixels")
	}

	x, y := coordinate[0], coordinate[1]

	if x < 0 || y < 0 || x >= d.view.width || y >= d.view.height {
		return computer.Point{}, wrong("%s %v is outside the last screenshot, which is %d×%d", name, coordinate, d.view.width, d.view.height)
	}

	// The middle of the screenshot's pixel, on the screen.
	area := d.view.area

	return computer.Point{
		X: area.X + (2*x+1)*area.Width/(2*d.view.width),
		Y: area.Y + (2*y+1)*area.Height/(2*d.view.height),
	}, nil
}

// moveIfGiven moves the pointer to coordinate, when there is one.
func (d *display) moveIfGiven(ctx context.Context, coordinate []int) error {
	if len(coordinate) == 0 {
		return nil
	}

	at, err := d.point(coordinate, "coordinate")

	if err != nil {
		return err
	}

	return d.screen.Move(ctx, at)
}

// held reads the modifiers a click or a scroll holds.
func held(text string) (computer.Keys, error) {
	if text == "" {
		return computer.Keys{}, nil
	}

	keys, err := computer.ParseKeys(text)

	if err != nil {
		return computer.Keys{}, inputError{message: err.Error()}
	}

	if keys.Key != "" {
		return computer.Keys{}, wrong("the text of a click or a scroll names modifiers to hold, such as shift or ctrl+alt, not %q", keys.Key)
	}

	return keys, nil
}

// withModifiers runs act with the modifiers held, and releases them however
// it ends.
func (d *display) withModifiers(ctx context.Context, modifiers computer.Keys, act func() error) error {
	if modifiers.Modifiers == 0 {
		return act()
	}

	if err := d.screen.Press(ctx, modifiers, true); err != nil {
		return err
	}

	err := act()

	if release := d.screen.Press(ctx, modifiers, false); err == nil {
		err = release
	}

	return err
}

func (d *display) click(ctx context.Context, input computerInput, button computer.Button, count int) error {
	modifiers, err := held(input.Text)

	if err != nil {
		return err
	}

	if err := d.moveIfGiven(ctx, input.Coordinate); err != nil {
		return err
	}

	return d.withModifiers(ctx, modifiers, func() error {
		for index := range count {
			if index > 0 {
				if err := pause(ctx, clickGap); err != nil {
					return err
				}
			}

			if err := d.screen.Button(ctx, button, true); err != nil {
				return err
			}

			if err := d.screen.Button(ctx, button, false); err != nil {
				return err
			}
		}

		return nil
	})
}

func (d *display) drag(ctx context.Context, input computerInput) error {
	from, err := d.point(input.StartCoordinate, "start_coordinate")

	if err != nil {
		return err
	}

	to, err := d.point(input.Coordinate, "coordinate")

	if err != nil {
		return err
	}

	if err := d.screen.Move(ctx, from); err != nil {
		return err
	}

	if err := d.screen.Button(ctx, computer.Left, true); err != nil {
		return err
	}

	// The button comes up however the drag ends, so it is not left held.
	moved := func() error {
		for step := 1; step <= dragSteps; step++ {
			at := computer.Point{
				X: from.X + (to.X-from.X)*step/dragSteps,
				Y: from.Y + (to.Y-from.Y)*step/dragSteps,
			}

			if err := d.screen.Move(ctx, at); err != nil {
				return err
			}

			if err := pause(ctx, 15*time.Millisecond); err != nil {
				return err
			}
		}

		return nil
	}()

	if err := d.screen.Button(ctx, computer.Left, false); moved == nil {
		moved = err
	}

	return moved
}

func (d *display) scroll(ctx context.Context, input computerInput) error {
	modifiers, err := held(input.Text)

	if err != nil {
		return err
	}

	amount := input.ScrollAmount

	if amount == 0 {
		amount = defaultScroll
	}

	if amount < 0 {
		return wrong("scroll_amount is a number of notches, more than 0")
	}

	var dx, dy int

	switch input.ScrollDirection {
	case "up":
		dy = -amount
	case "down":
		dy = amount
	case "left":
		dx = -amount
	case "right":
		dx = amount
	default:
		return wrong("scroll needs scroll_direction: up, down, left, or right")
	}

	if err := d.moveIfGiven(ctx, input.Coordinate); err != nil {
		return err
	}

	return d.withModifiers(ctx, modifiers, func() error {
		return d.screen.Scroll(ctx, dx, dy)
	})
}

// press presses keys and releases them after hold, releasing them however
// the wait ends.
func press(ctx context.Context, screen computer.Screen, keys computer.Keys, hold time.Duration) error {
	if err := screen.Press(ctx, keys, true); err != nil {
		return err
	}

	err := pause(ctx, hold)

	// Released even when the run was cancelled, so no key is left held.
	if release := screen.Press(context.WithoutCancel(ctx), keys, false); err == nil {
		err = release
	}

	return err
}

// pause waits, or less if the run is cancelled.
func pause(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func position(coordinate []int) string {
	if len(coordinate) != 2 {
		return "?"
	}

	return fmt.Sprintf("[%d, %d]", coordinate[0], coordinate[1])
}

func where(coordinate []int) string {
	if len(coordinate) == 0 {
		return " where the pointer was"
	}

	return " at " + position(coordinate)
}
