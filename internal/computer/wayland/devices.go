package wayland

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// The protocols this file speaks, by the names their globals have. The
// opcodes below are the order of the requests and events in each
// protocol's XML.
const (
	seatInterface            = "wl_seat"
	outputInterface          = "wl_output"
	xdgOutputManager         = "zxdg_output_manager_v1"
	virtualPointerManager    = "zwlr_virtual_pointer_manager_v1"
	virtualKeyboardManager   = "zwp_virtual_keyboard_manager_v1"
	outputName               = 4 // wl_output.name, since version 4
	xdgOutputGet             = 1 // zxdg_output_manager_v1.get_xdg_output
	xdgOutputLogicalPosition = 0
	xdgOutputLogicalSize     = 1
	xdgOutputName            = 3
	pointerCreate            = 0 // zwlr_virtual_pointer_manager_v1.create_virtual_pointer
	pointerMotionAbsolute    = 1
	pointerButton            = 2
	pointerFrame             = 4
	pointerAxisSource        = 5
	pointerAxisDiscrete      = 7
	keyboardCreate           = 0 // zwp_virtual_keyboard_manager_v1.create_virtual_keyboard
	keyboardKeymap           = 0
	keyboardKey              = 1
	keyboardModifiers        = 2
	keymapFormatXKB          = 1
	axisSourceWheel          = 0
	// wheelStep is how far one notch of a wheel scrolls, in the units of
	// the protocol, as libinput reports a mouse's.
	wheelStep = 15
)

// Output is a monitor where the compositor lays it out, in the logical
// pixels windows and the pointer use.
type Output struct {
	Name   string
	X, Y   int
	Width  int
	Height int
}

// Outputs returns the compositor's monitors and where each one is.
func (c *Conn) Outputs() ([]Output, error) {
	global, ok := c.Global(xdgOutputManager)

	if !ok {
		return nil, errors.New("the compositor does not say where its monitors are (it offers no xdg-output)")
	}

	manager, _, err := c.Bind(global, 3)

	if err != nil {
		return nil, err
	}

	globals := c.Globals(outputInterface)
	outputs := make([]Output, len(globals))

	for index, global := range globals {
		output := &outputs[index]
		id, _, err := c.Bind(global, 4)

		if err != nil {
			return nil, err
		}

		c.Handle(id, func(opcode uint16, event *Event) {
			if opcode == outputName && output.Name == "" {
				output.Name = event.String()
			}
		})

		logical := c.NewID()

		c.Handle(logical, func(opcode uint16, event *Event) {
			switch opcode {
			case xdgOutputLogicalPosition:
				output.X, output.Y = int(event.Int()), int(event.Int())
			case xdgOutputLogicalSize:
				output.Width, output.Height = int(event.Int()), int(event.Int())
			case xdgOutputName:
				output.Name = event.String()
			}
		})

		if err := c.Send(manager, xdgOutputGet, NewMessage().Uint(logical).Uint(id)); err != nil {
			return nil, err
		}
	}

	if err := c.Roundtrip(); err != nil {
		return nil, err
	}

	return outputs, nil
}

// seat binds the compositor's seat, which the virtual devices join.
func (c *Conn) seat() (uint32, error) {
	global, ok := c.Global(seatInterface)

	if !ok {
		return 0, errors.New("the compositor offers no seat")
	}

	id, _, err := c.Bind(global, 1)

	return id, err
}

// Pointer is a virtual pointer: what it does, the compositor takes as a
// mouse's doing.
type Pointer struct {
	conn *Conn
	id   uint32
}

// NewPointer makes a virtual pointer, which wlroots compositors offer.
func (c *Conn) NewPointer() (*Pointer, error) {
	global, ok := c.Global(virtualPointerManager)

	if !ok {
		return nil, errors.New("the compositor offers no virtual pointer")
	}

	seat, err := c.seat()

	if err != nil {
		return nil, err
	}

	manager, _, err := c.Bind(global, 1)

	if err != nil {
		return nil, err
	}

	pointer := &Pointer{conn: c, id: c.NewID()}

	if err := c.Send(manager, pointerCreate, NewMessage().Uint(seat).Uint(pointer.id)); err != nil {
		return nil, err
	}

	return pointer, c.Roundtrip()
}

// MoveTo puts the pointer at x, y of an area width×height, which the
// compositor stretches over all of its monitors together.
func (p *Pointer) MoveTo(x, y, width, height int) error {
	message := NewMessage().Uint(now()).Uint(uint32(x)).Uint(uint32(y)).Uint(uint32(width)).Uint(uint32(height))

	if err := p.conn.Send(p.id, pointerMotionAbsolute, message); err != nil {
		return err
	}

	return p.frame()
}

// Button presses or releases a button, by its Linux input code, such as
// 0x110 for the left one.
func (p *Pointer) Button(code uint32, pressed bool) error {
	state := uint32(0)

	if pressed {
		state = 1
	}

	if err := p.conn.Send(p.id, pointerButton, NewMessage().Uint(now()).Uint(code).Uint(state)); err != nil {
		return err
	}

	return p.frame()
}

// Scroll turns a wheel on an axis, 0 vertical and 1 horizontal, by steps
// notches: positive is down, or right.
func (p *Pointer) Scroll(axis uint32, steps int) error {
	if err := p.conn.Send(p.id, pointerAxisSource, NewMessage().Uint(axisSourceWheel)); err != nil {
		return err
	}

	message := NewMessage().Uint(now()).Uint(axis).Fixed(float64(steps * wheelStep)).Int(int32(steps))

	if err := p.conn.Send(p.id, pointerAxisDiscrete, message); err != nil {
		return err
	}

	return p.frame()
}

func (p *Pointer) frame() error {
	if err := p.conn.Send(p.id, pointerFrame, NewMessage()); err != nil {
		return err
	}

	return p.conn.Roundtrip()
}

// Keyboard is a virtual keyboard, with a keymap of its own: its keys mean
// what that keymap says, whatever the layouts of the person's keyboards.
type Keyboard struct {
	conn *Conn
	id   uint32
}

// NewKeyboard makes a virtual keyboard. It types nothing until it has a
// keymap.
func (c *Conn) NewKeyboard() (*Keyboard, error) {
	global, ok := c.Global(virtualKeyboardManager)

	if !ok {
		return nil, errors.New("the compositor offers no virtual keyboard")
	}

	seat, err := c.seat()

	if err != nil {
		return nil, err
	}

	manager, _, err := c.Bind(global, 1)

	if err != nil {
		return nil, err
	}

	keyboard := &Keyboard{conn: c, id: c.NewID()}

	if err := c.Send(manager, keyboardCreate, NewMessage().Uint(seat).Uint(keyboard.id)); err != nil {
		return nil, err
	}

	return keyboard, c.Roundtrip()
}

// Keymap gives the keyboard a keymap in XKB's text format.
func (k *Keyboard) Keymap(keymap string) error {
	// The keymap travels as a file the compositor maps, removed from its
	// directory at once, so only the open file is left, and it goes when
	// both sides close it.
	file, err := os.CreateTemp(os.Getenv("XDG_RUNTIME_DIR"), "rumbo-keymap-*")

	if err != nil {
		return fmt.Errorf("making room for the keymap: %w", err)
	}

	defer file.Close()

	os.Remove(file.Name())

	// The compositor reads the keymap as a C string, so it ends in a zero.
	data := append([]byte(keymap), 0)

	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("writing the keymap: %w", err)
	}

	message := NewMessage().Uint(keymapFormatXKB).FD(int(file.Fd())).Uint(uint32(len(data)))

	if err := k.conn.Send(k.id, keyboardKeymap, message); err != nil {
		return err
	}

	return k.conn.Roundtrip()
}

// Key presses or releases a key, by its code in the keymap less 8, which is
// how the protocol numbers keys.
func (k *Keyboard) Key(code uint32, pressed bool) error {
	state := uint32(0)

	if pressed {
		state = 1
	}

	if err := k.conn.Send(k.id, keyboardKey, NewMessage().Uint(now()).Uint(code).Uint(state)); err != nil {
		return err
	}

	return k.conn.Roundtrip()
}

// Modifiers says which modifiers are held, as a mask of XKB's real
// modifiers: 1 Shift, 4 Control, 8 Mod1, 64 Mod4.
func (k *Keyboard) Modifiers(depressed uint32) error {
	if err := k.conn.Send(k.id, keyboardModifiers, NewMessage().Uint(depressed).Uint(0).Uint(0).Uint(0)); err != nil {
		return err
	}

	return k.conn.Roundtrip()
}

// now is the time an input event carries, in milliseconds, which clients
// compare to tell a double click from two.
func now() uint32 {
	return uint32(time.Now().UnixMilli())
}
