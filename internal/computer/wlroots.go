package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/LiasCode/rumbo/internal/computer/wayland"
)

// Linux input codes of the pointer's buttons.
var buttonCodes = map[Button]uint32{Left: 0x110, Right: 0x111, Middle: 0x112}

// typeDelay is the pause between the keys of what is typed, so a program
// that reads its input slowly gets every key.
const typeDelay = 2 * time.Millisecond

// wlroots is a Wayland compositor that offers wlroots' virtual pointer and
// the virtual keyboard, as Hyprland, Sway, river, and labwc do. It moves the
// pointer and types through those, and takes screenshots with grim.
type wlroots struct {
	desktop  string
	conn     *wayland.Conn
	pointer  *wayland.Pointer
	keyboard *wayland.Keyboard
	// uploaded is the keymap the keyboard has, as text.
	uploaded string
	// held are the modifiers the keyboard holds down now.
	held Modifier
	// outputs are the monitors as last asked, which the pointer's
	// coordinates span.
	outputs []wayland.Output
	// at is where the pointer was last put.
	at *Point
}

func openWayland(_ context.Context) (Screen, error) {
	desktop := waylandDesktop()
	conn, err := wayland.Dial()

	if err != nil {
		return nil, fmt.Errorf("WAYLAND_DISPLAY is set, but its compositor cannot be reached: %w", err)
	}

	pointer, pointerErr := conn.NewPointer()
	keyboard, keyboardErr := conn.NewKeyboard()

	if pointerErr != nil || keyboardErr != nil {
		conn.Close()

		return nil, fmt.Errorf("%s does not let programs move the pointer or type: it offers no virtual pointer and keyboard (the zwlr_virtual_pointer_manager_v1 and zwp_virtual_keyboard_manager_v1 protocols). The computer tool works on wlroots compositors such as Hyprland and Sway, in X11 sessions, and on macOS", desktop)
	}

	if _, err := exec.LookPath("grim"); err != nil {
		conn.Close()

		return nil, missing("grim", "takes screenshots", desktop)
	}

	return &wlroots{desktop: desktop, conn: conn, pointer: pointer, keyboard: keyboard}, nil
}

// waylandDesktop names the compositor rumbo runs in.
func waylandDesktop() string {
	switch {
	case os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "":
		return "Hyprland (Wayland)"
	case os.Getenv("SWAYSOCK") != "":
		return "Sway (Wayland)"
	}

	return desktopName() + " (Wayland)"
}

func (w *wlroots) Desktop() string {
	return w.desktop
}

func (w *wlroots) Close() error {
	return w.conn.Close()
}

// layout asks the compositor where its monitors are. A connection of its
// own asks it, so monitors plugged in since are there too.
func (w *wlroots) layout() ([]wayland.Output, error) {
	conn, err := wayland.Dial()

	if err != nil {
		return nil, err
	}

	defer conn.Close()

	outputs, err := conn.Outputs()

	if err != nil {
		return nil, err
	}

	if len(outputs) == 0 {
		return nil, errors.New("the compositor has no monitor")
	}

	w.outputs = outputs

	return outputs, nil
}

func (w *wlroots) Output(ctx context.Context) (string, error) {
	outputs, err := w.layout()

	if err != nil {
		return "", err
	}

	return w.current(ctx, outputs).Name, nil
}

func (w *wlroots) Capture(ctx context.Context) (Shot, error) {
	outputs, err := w.layout()

	if err != nil {
		return Shot{}, err
	}

	chosen := w.current(ctx, outputs)
	data, err := output(ctx, "grim", "-c", "-t", "ppm", "-o", chosen.Name, "-")

	if err != nil {
		return Shot{}, err
	}

	img, err := decodePPM(data)

	if err != nil {
		return Shot{}, fmt.Errorf("reading grim's screenshot: %w", err)
	}

	return Shot{Image: img, Area: Rect{X: chosen.X, Y: chosen.Y, Width: chosen.Width, Height: chosen.Height}, Output: chosen.Name}, nil
}

// current is the monitor in use: the one the compositor has focused, when
// it says, or else the one the pointer was put on, or else the first.
func (w *wlroots) current(ctx context.Context, outputs []wayland.Output) wayland.Output {
	focused := ""

	switch {
	case os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "":
		focused = hyprlandFocused(ctx)
	case os.Getenv("SWAYSOCK") != "":
		focused = swayFocused(ctx)
	}

	for _, candidate := range outputs {
		if focused != "" && candidate.Name == focused {
			return candidate
		}
	}

	if w.at != nil {
		for _, candidate := range outputs {
			if (Rect{candidate.X, candidate.Y, candidate.Width, candidate.Height}).Contains(*w.at) {
				return candidate
			}
		}
	}

	return outputs[0]
}

// hyprlandFocused is the monitor Hyprland has focused.
func hyprlandFocused(ctx context.Context) string {
	data, err := output(ctx, "hyprctl", "monitors", "-j")

	if err != nil {
		return ""
	}

	var monitors []struct {
		Name    string `json:"name"`
		Focused bool   `json:"focused"`
	}

	_ = json.Unmarshal(data, &monitors)

	for _, monitor := range monitors {
		if monitor.Focused {
			return monitor.Name
		}
	}

	return ""
}

// swayFocused is the monitor Sway has focused.
func swayFocused(ctx context.Context) string {
	data, err := output(ctx, "swaymsg", "-t", "get_outputs", "-r")

	if err != nil {
		return ""
	}

	var outputs []struct {
		Name    string `json:"name"`
		Focused bool   `json:"focused"`
	}

	_ = json.Unmarshal(data, &outputs)

	for _, candidate := range outputs {
		if candidate.Focused {
			return candidate.Name
		}
	}

	return ""
}

func (w *wlroots) Move(_ context.Context, p Point) error {
	outputs := w.outputs

	if len(outputs) == 0 {
		var err error

		if outputs, err = w.layout(); err != nil {
			return err
		}
	}

	// The compositor stretches the pointer's area over the box that holds
	// every monitor.
	box := bounds(outputs)

	if err := w.pointer.MoveTo(p.X-box.X, p.Y-box.Y, box.Width, box.Height); err != nil {
		return err
	}

	w.at = &p

	return nil
}

// bounds is the smallest area that holds every monitor.
func bounds(outputs []wayland.Output) Rect {
	left, top := outputs[0].X, outputs[0].Y
	right, bottom := left+outputs[0].Width, top+outputs[0].Height

	for _, candidate := range outputs[1:] {
		left, top = min(left, candidate.X), min(top, candidate.Y)
		right, bottom = max(right, candidate.X+candidate.Width), max(bottom, candidate.Y+candidate.Height)
	}

	return Rect{X: left, Y: top, Width: right - left, Height: bottom - top}
}

func (w *wlroots) Button(_ context.Context, button Button, down bool) error {
	return w.pointer.Button(buttonCodes[button], down)
}

func (w *wlroots) Scroll(_ context.Context, dx, dy int) error {
	if dy != 0 {
		if err := w.pointer.Scroll(0, dy); err != nil {
			return err
		}
	}

	if dx != 0 {
		return w.pointer.Scroll(1, dx)
	}

	return nil
}

// use gives the keyboard the US keymap, with extra characters on keys of
// their own when there are any, unless it has that keymap already.
func (w *wlroots) use(extra map[string]uint32) error {
	text := keymap(extra)

	if text == w.uploaded {
		return nil
	}

	if err := w.keyboard.Keymap(text); err != nil {
		return err
	}

	w.uploaded = text

	return nil
}

// key presses or releases one key. A modifier's key also changes the
// modifiers clients are told are held.
func (w *wlroots) key(code uint32, down bool) error {
	if err := w.keyboard.Key(code, down); err != nil {
		return err
	}

	for modifier, key := range modifierKeys {
		if key.code != code {
			continue
		}

		if down {
			w.held |= modifier
		} else {
			w.held &^= modifier
		}

		return w.keyboard.Modifiers(mask(w.held))
	}

	return nil
}

func (w *wlroots) Press(_ context.Context, keys Keys, down bool) error {
	modifiers := keys.Modifiers
	var extra map[string]uint32
	var codes []uint32

	if keys.Key != "" {
		name := keysym(keys.Key)
		found, ok := usStroke(name)

		if !ok {
			extra = placeExtra([]string{name})
			found = stroke{code: extra[name]}
		}

		// A character on a key's second level is typed with Shift, as
		// ctrl++ is ctrl+shift+= on a US keyboard.
		if found.shift {
			modifiers |= Shift
		}

		codes = append(codes, found.code)
	}

	for index := len(modifierOrder) - 1; index >= 0; index-- {
		if modifier := modifierOrder[index]; modifiers&modifier != 0 {
			codes = append([]uint32{modifierKeys[modifier].code}, codes...)
		}
	}

	if err := w.use(extra); err != nil {
		return err
	}

	if !down {
		// Released in the other order: the key, then its modifiers.
		for left, right := 0, len(codes)-1; left < right; left, right = left+1, right-1 {
			codes[left], codes[right] = codes[right], codes[left]
		}
	}

	for _, code := range codes {
		if err := w.key(code, down); err != nil {
			return err
		}
	}

	return nil
}

// mask is the modifiers held, as XKB's real modifiers.
func mask(held Modifier) uint32 {
	var out uint32

	for modifier, key := range modifierKeys {
		if held&modifier != 0 {
			out |= key.mask
		}
	}

	return out
}

// keyStep is one key going down or coming up.
type keyStep struct {
	code uint32
	down bool
}

// tap types one key, with Shift when it needs it.
func (w *wlroots) tap(found stroke) error {
	steps := []keyStep{{found.code, true}, {found.code, false}}

	if found.shift {
		shift := modifierKeys[Shift].code
		steps = []keyStep{{shift, true}, {found.code, true}, {found.code, false}, {shift, false}}
	}

	for _, step := range steps {
		if err := w.key(step.code, step.down); err != nil {
			return err
		}

		time.Sleep(typeDelay)
	}

	return nil
}

func (w *wlroots) Type(_ context.Context, text string) error {
	names := []string{}

	for _, character := range strings.ReplaceAll(text, "\r\n", "\n") {
		names = append(names, keysym(string(character)))
	}

	for len(names) > 0 {
		if found, ok := usStroke(names[0]); ok {
			if err := w.use(nil); err != nil {
				return err
			}

			if err := w.tap(found); err != nil {
				return err
			}

			names = names[1:]

			continue
		}

		// The characters the US keyboard lacks that come next, as many
		// different ones as a keymap has keys for.
		run := 0
		different := map[string]bool{}

		for run < len(names) {
			if _, ok := usStroke(names[run]); ok {
				break
			}

			if !different[names[run]] && len(different) == extraKeys {
				break
			}

			different[names[run]] = true
			run++
		}

		extra := placeExtra(names[:run])

		if err := w.use(extra); err != nil {
			return err
		}

		for _, name := range names[:run] {
			if err := w.tap(stroke{code: extra[name]}); err != nil {
				return err
			}
		}

		names = names[run:]
	}

	return nil
}

func (w *wlroots) Pointer(ctx context.Context) (Point, error) {
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" {
		data, err := output(ctx, "hyprctl", "cursorpos", "-j")

		if err == nil {
			var at struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			}

			if json.Unmarshal(data, &at) == nil {
				return Point{X: int(at.X), Y: int(at.Y)}, nil
			}
		}
	}

	if w.at != nil {
		return *w.at, nil
	}

	return Point{}, ErrUnknown
}
