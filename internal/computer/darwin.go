package computer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// quartz is the script that posts Quartz events and reads the display, for
// osascript to run as JavaScript. Its first argument says what to do. The
// numbers are Quartz's: event types, mouse buttons, and the click-state
// field (1).
const quartz = `ObjC.import('CoreGraphics');
function run(argv) {
	var n = function (i) { return Number(argv[i]); };
	switch (argv[0]) {
	case 'bounds':
		var b = $.CGDisplayBounds($.CGMainDisplayID());
		return [b.origin.x, b.origin.y, b.size.width, b.size.height].join(' ');
	case 'pointer':
		var p = $.CGEventGetLocation($.CGEventCreate(null));
		return p.x + ' ' + p.y;
	case 'mouse':
		var m = $.CGEventCreateMouseEvent(null, n(1), $.CGPointMake(n(2), n(3)), n(4));
		$.CGEventSetIntegerValueField(m, 1, n(5));
		$.CGEventSetFlags(m, n(6));
		$.CGEventPost(0, m);
		return '';
	case 'scroll':
		$.CGEventPost(0, $.CGEventCreateScrollWheelEvent2(null, 1, 2, n(1), n(2), 0));
		return '';
	case 'key':
		var k = $.CGEventCreateKeyboardEvent(null, n(1), argv[2] === 'down');
		$.CGEventSetFlags(k, n(3));
		$.CGEventPost(0, k);
		return '';
	case 'type':
		Application('System Events').keystroke(argv[1]);
		return '';
	}
	throw new Error('unknown operation ' + argv[0]);
}`

// Quartz's numbers for the pointer: the button, and the event types for it
// going down, coming up, and moving while it is held.
var quartzButtons = map[Button]struct{ button, down, up, dragged int }{
	Left:   {0, 1, 2, 6},
	Right:  {1, 3, 4, 7},
	Middle: {2, 25, 26, 27},
}

const quartzMoved = 5

// quartzFlags are the flags of the modifiers on an event.
var quartzFlags = map[Modifier]int{Shift: 0x20000, Control: 0x40000, Alt: 0x80000, Super: 0x100000}

// quartzModifierKeys are the key codes of the modifiers themselves.
var quartzModifierKeys = map[Modifier]int{Shift: 56, Control: 59, Alt: 58, Super: 55}

// quartzKeys are the key codes of a US keyboard, by X keysym name or
// character. A layout that differs moves the characters, not the keys.
var quartzKeys = map[string]int{
	"a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7, "c": 8, "v": 9,
	"b": 11, "q": 12, "w": 13, "e": 14, "r": 15, "y": 16, "t": 17, "1": 18, "2": 19,
	"3": 20, "4": 21, "6": 22, "5": 23, "=": 24, "9": 25, "7": 26, "-": 27, "8": 28,
	"0": 29, "]": 30, "o": 31, "u": 32, "[": 33, "i": 34, "p": 35, "l": 37, "j": 38,
	"'": 39, "k": 40, ";": 41, "\\": 42, ",": 43, "/": 44, "n": 45, "m": 46, ".": 47,
	"`": 50, " ": 49, "space": 49,
	"Return": 36, "KP_Enter": 76, "Tab": 48, "BackSpace": 51, "Escape": 53, "Delete": 117,
	"Insert": 114, "Home": 115, "End": 119, "Page_Up": 116, "Page_Down": 121,
	"Left": 123, "Right": 124, "Down": 125, "Up": 126, "Caps_Lock": 57,
	"F1": 122, "F2": 120, "F3": 99, "F4": 118, "F5": 96, "F6": 97, "F7": 98, "F8": 100,
	"F9": 101, "F10": 109, "F11": 103, "F12": 111, "F13": 105, "F14": 107, "F15": 113,
	"F16": 106, "F17": 64, "F18": 79, "F19": 80, "F20": 90,
}

// quartzShifted are the characters a US keyboard types with Shift, and the
// key that has them.
var quartzShifted = map[rune]string{
	'!': "1", '@': "2", '#': "3", '$': "4", '%': "5", '^': "6", '&': "7", '*': "8",
	'(': "9", ')': "0", '_': "-", '+': "=", '{': "[", '}': "]", '|': "\\", ':': ";",
	'"': "'", '<': ",", '>': ".", '?': "/", '~': "`",
}

// doubleClick is how close in time two presses must be to count as one
// double click, as macOS counts a mouse's.
const doubleClick = 500 * time.Millisecond

// darwin is macOS. screencapture takes the screenshots of the main display,
// and Quartz events posted through osascript move the pointer and type.
// Both need the person to allow the program that runs rumbo: Screen
// Recording for one, Accessibility for the other.
type darwin struct {
	held    Modifier
	pressed map[Button]bool
	// at is where the pointer is, which every pointer event carries; known
	// says it was asked or set.
	at    Point
	known bool
	// Presses are counted as a mouse's are, for double and triple clicks.
	lastPress time.Time
	lastAt    Point
	clicks    int
}

func openDarwin(_ context.Context) (Screen, error) {
	for _, program := range []string{"screencapture", "osascript"} {
		if _, err := exec.LookPath(program); err != nil {
			return nil, missing(program, "the computer tool acts through", "macOS")
		}
	}

	return &darwin{pressed: map[Button]bool{}}, nil
}

func (d *darwin) Desktop() string {
	return "macOS"
}

func (d *darwin) Close() error {
	return nil
}

// run runs one operation of the Quartz script.
func (d *darwin) run(ctx context.Context, args ...string) (string, error) {
	out, err := output(ctx, "osascript", append([]string{"-l", "JavaScript", "-e", quartz}, args...)...)

	if err != nil {
		if strings.Contains(err.Error(), "assistive") || strings.Contains(err.Error(), "not allowed") {
			return "", errors.New("macOS does not let the program that runs rumbo control the computer: allow it in System Settings, Privacy & Security, Accessibility, and try again")
		}

		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}

func (d *darwin) Output(context.Context) (string, error) {
	return "main display", nil
}

func (d *darwin) Capture(ctx context.Context) (Shot, error) {
	file, err := os.CreateTemp("", "rumbo-screen-*.png")

	if err != nil {
		return Shot{}, err
	}

	file.Close()
	defer os.Remove(file.Name())

	// -x is silent, -C shows the pointer, and -D 1 is the main display.
	if _, err := output(ctx, "screencapture", "-x", "-C", "-t", "png", "-D", "1", file.Name()); err != nil {
		return Shot{}, err
	}

	data, err := os.ReadFile(file.Name())

	if err != nil {
		return Shot{}, err
	}

	img, err := png.Decode(bytes.NewReader(data))

	if err != nil {
		return Shot{}, fmt.Errorf("reading screencapture's screenshot: %w", err)
	}

	bounds, err := d.run(ctx, "bounds")

	if err != nil {
		return Shot{}, err
	}

	var numbers []int

	for _, field := range strings.Fields(bounds) {
		value, _ := strconv.ParseFloat(field, 64)
		numbers = append(numbers, int(value))
	}

	if len(numbers) != 4 {
		return Shot{}, fmt.Errorf("the main display's bounds are unreadable: %q", bounds)
	}

	// The display is placed in points; on a Retina display the picture has
	// two pixels to each.
	return Shot{Image: img, Area: Rect{X: numbers[0], Y: numbers[1], Width: numbers[2], Height: numbers[3]}, Output: "main display"}, nil
}

func (d *darwin) mouse(ctx context.Context, kind int, button int) error {
	_, err := d.run(ctx, "mouse", strconv.Itoa(kind), strconv.Itoa(d.at.X), strconv.Itoa(d.at.Y), strconv.Itoa(button), strconv.Itoa(max(d.clicks, 1)), strconv.Itoa(flags(d.held)))

	return err
}

func (d *darwin) Move(ctx context.Context, p Point) error {
	d.at, d.known = p, true

	// Moving with a button held is dragging, which is another event.
	for button, held := range d.pressed {
		if held {
			codes := quartzButtons[button]

			return d.mouse(ctx, codes.dragged, codes.button)
		}
	}

	return d.mouse(ctx, quartzMoved, 0)
}

func (d *darwin) Button(ctx context.Context, button Button, down bool) error {
	codes := quartzButtons[button]

	if !d.known {
		at, err := d.Pointer(ctx)

		if err != nil {
			return err
		}

		d.at, d.known = at, true
	}

	if !down {
		d.pressed[button] = false

		return d.mouse(ctx, codes.up, codes.button)
	}

	if time.Since(d.lastPress) < doubleClick && d.lastAt == d.at {
		d.clicks++
	} else {
		d.clicks = 1
	}

	d.lastPress, d.lastAt = time.Now(), d.at
	d.pressed[button] = true

	return d.mouse(ctx, codes.down, codes.button)
}

func (d *darwin) Scroll(ctx context.Context, dx, dy int) error {
	// Quartz turns the wheel up and left for positive values.
	_, err := d.run(ctx, "scroll", strconv.Itoa(-dy), strconv.Itoa(-dx))

	return err
}

func (d *darwin) Press(ctx context.Context, keys Keys, down bool) error {
	modifiers := keys.Modifiers
	key := keys.Key

	if character, _ := utf8.DecodeRuneInString(key); utf8.RuneCountInString(key) == 1 {
		if base, ok := quartzShifted[character]; ok {
			key, modifiers = base, modifiers|Shift
		} else if unicode.IsUpper(character) {
			key, modifiers = string(unicode.ToLower(character)), modifiers|Shift
		}
	}

	code, ok := quartzKeys[key]

	if key != "" && !ok {
		return fmt.Errorf("on macOS the key %q has no key code here; type it with the type action instead", keys.Key)
	}

	var steps []func() error

	for _, modifier := range modifierOrder {
		if modifiers&modifier != 0 {
			steps = append(steps, func() error {
				if down {
					d.held |= modifier
				} else {
					d.held &^= modifier
				}

				return d.key(ctx, quartzModifierKeys[modifier], down)
			})
		}
	}

	if key != "" {
		steps = append(steps, func() error { return d.key(ctx, code, down) })
	}

	if !down {
		for left, right := 0, len(steps)-1; left < right; left, right = left+1, right-1 {
			steps[left], steps[right] = steps[right], steps[left]
		}
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}

	return nil
}

func (d *darwin) key(ctx context.Context, code int, down bool) error {
	state := "up"

	if down {
		state = "down"
	}

	_, err := d.run(ctx, "key", strconv.Itoa(code), state, strconv.Itoa(flags(d.held)))

	return err
}

func (d *darwin) Type(ctx context.Context, text string) error {
	// System Events types a line's characters in whatever layout is in use;
	// the line ends are Return.
	for index, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if index > 0 {
			for _, down := range []bool{true, false} {
				if err := d.key(ctx, quartzKeys["Return"], down); err != nil {
					return err
				}
			}
		}

		if line == "" {
			continue
		}

		if _, err := d.run(ctx, "type", line); err != nil {
			return err
		}
	}

	return nil
}

func (d *darwin) Pointer(ctx context.Context) (Point, error) {
	out, err := d.run(ctx, "pointer")

	if err != nil {
		return Point{}, err
	}

	fields := strings.Fields(out)

	if len(fields) != 2 {
		return Point{}, fmt.Errorf("the pointer's place is unreadable: %q", out)
	}

	x, _ := strconv.ParseFloat(fields[0], 64)
	y, _ := strconv.ParseFloat(fields[1], 64)

	return Point{X: int(x), Y: int(y)}, nil
}

// flags is the modifiers held, as Quartz marks them on an event.
func flags(held Modifier) int {
	out := 0

	for modifier, flag := range quartzFlags {
		if held&modifier != 0 {
			out |= flag
		}
	}

	return out
}
