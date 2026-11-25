//go:build linux

// Package wayland is the least of a Wayland client that the computer tool
// needs: a connection to the compositor, its registry, and the messages of
// a few protocols, written by hand from their XML. It draws nothing and
// owns no surface.
package wayland

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// display is the object every connection starts with.
const display uint32 = 1

// Requests and events of wl_display, wl_registry, and wl_callback.
const (
	displaySync        = 0
	displayGetRegistry = 1
	displayError       = 0
	registryBind       = 0
	registryGlobal     = 0
	callbackDone       = 0
)

// roundtripTimeout bounds how long the compositor may take to answer.
const roundtripTimeout = 5 * time.Second

// Global is an interface the compositor offers.
type Global struct {
	Name      uint32
	Interface string
	Version   uint32
}

// Handler receives the events of one object.
type Handler func(opcode uint16, event *Event)

// Conn is a connection to the compositor. It is not safe for concurrent use.
type Conn struct {
	socket   *net.UnixConn
	next     uint32
	handlers map[uint32]Handler
	registry uint32
	globals  []Global
	// failed is the protocol error the compositor reported, after which the
	// connection is of no use.
	failed  error
	pending []byte
}

// Dial connects to the compositor of WAYLAND_DISPLAY and reads its
// registry.
func Dial() (*Conn, error) {
	name := os.Getenv("WAYLAND_DISPLAY")

	if name == "" {
		name = "wayland-0"
	}

	if !filepath.IsAbs(name) {
		runtime := os.Getenv("XDG_RUNTIME_DIR")

		if runtime == "" {
			return nil, errors.New("XDG_RUNTIME_DIR is not set, so the compositor's socket cannot be found")
		}

		name = filepath.Join(runtime, name)
	}

	socket, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: name, Net: "unix"})

	if err != nil {
		return nil, fmt.Errorf("connecting to the compositor: %w", err)
	}

	conn := &Conn{socket: socket, next: 2, handlers: map[uint32]Handler{}}
	conn.handlers[display] = conn.displayEvent
	conn.registry = conn.NewID()

	conn.handlers[conn.registry] = func(opcode uint16, event *Event) {
		if opcode == registryGlobal {
			conn.globals = append(conn.globals, Global{Name: event.Uint(), Interface: event.String(), Version: event.Uint()})
		}
	}

	if err := conn.Send(display, displayGetRegistry, NewMessage().Uint(conn.registry)); err != nil {
		socket.Close()
		return nil, err
	}

	if err := conn.Roundtrip(); err != nil {
		socket.Close()
		return nil, err
	}

	return conn, nil
}

// Close ends the connection, which destroys every object made on it.
func (c *Conn) Close() error {
	return c.socket.Close()
}

// Global returns the interface the compositor offers under a name, and
// whether it offers one.
func (c *Conn) Global(name string) (Global, bool) {
	for _, global := range c.globals {
		if global.Interface == name {
			return global, true
		}
	}

	return Global{}, false
}

// Globals returns every interface the compositor offers under a name, such
// as one wl_output for each monitor.
func (c *Conn) Globals(name string) []Global {
	var found []Global

	for _, global := range c.globals {
		if global.Interface == name {
			found = append(found, global)
		}
	}

	return found
}

// NewID returns the id of an object to make.
func (c *Conn) NewID() uint32 {
	id := c.next
	c.next++

	return id
}

// Handle gives the events of an object to handler.
func (c *Conn) Handle(object uint32, handler Handler) {
	c.handlers[object] = handler
}

// Bind makes an object of a global, at the lower of version and the
// version the compositor offers, and returns its id and that version.
func (c *Conn) Bind(global Global, version uint32) (uint32, uint32, error) {
	version = min(version, global.Version)
	id := c.NewID()
	message := NewMessage().Uint(global.Name).String(global.Interface).Uint(version).Uint(id)

	return id, version, c.Send(c.registry, registryBind, message)
}

// Send sends a request to an object.
func (c *Conn) Send(object uint32, opcode uint16, message *Message) error {
	if c.failed != nil {
		return c.failed
	}

	size := 8 + len(message.data)
	data := make([]byte, size)
	binary.LittleEndian.PutUint32(data[0:], object)
	binary.LittleEndian.PutUint32(data[4:], uint32(size)<<16|uint32(opcode))
	copy(data[8:], message.data)

	var rights []byte

	if len(message.fds) > 0 {
		rights = syscall.UnixRights(message.fds...)
	}

	if _, _, err := c.socket.WriteMsgUnix(data, rights, nil); err != nil {
		return fmt.Errorf("writing to the compositor: %w", err)
	}

	return nil
}

// Roundtrip waits until the compositor has handled every request sent so
// far, giving its events to their handlers on the way. It returns the
// protocol error the compositor reported, if it did.
func (c *Conn) Roundtrip() error {
	callback := c.NewID()
	done := false

	c.handlers[callback] = func(opcode uint16, _ *Event) {
		if opcode == callbackDone {
			done = true
		}
	}

	defer delete(c.handlers, callback)

	if err := c.Send(display, displaySync, NewMessage().Uint(callback)); err != nil {
		return err
	}

	deadline := time.Now().Add(roundtripTimeout)

	for !done && c.failed == nil {
		if err := c.read(deadline); err != nil {
			return err
		}
	}

	return c.failed
}

// read takes what the compositor sent and gives each whole event to its
// handler.
func (c *Conn) read(deadline time.Time) error {
	if err := c.socket.SetReadDeadline(deadline); err != nil {
		return err
	}

	buffer := make([]byte, 64*1024)
	oob := make([]byte, syscall.CmsgSpace(28*4))
	count, oobCount, _, _, err := c.socket.ReadMsgUnix(buffer, oob)

	if err != nil {
		var timeout net.Error

		if errors.As(err, &timeout) && timeout.Timeout() {
			return errors.New("the compositor did not answer in time")
		}

		return fmt.Errorf("reading from the compositor: %w", err)
	}

	// No event this client asks for carries a file, so any that arrives is
	// closed rather than left open.
	if messages, err := syscall.ParseSocketControlMessage(oob[:oobCount]); err == nil {
		for _, message := range messages {
			if fds, err := syscall.ParseUnixRights(&message); err == nil {
				for _, fd := range fds {
					syscall.Close(fd)
				}
			}
		}
	}

	c.pending = append(c.pending, buffer[:count]...)

	for len(c.pending) >= 8 {
		object := binary.LittleEndian.Uint32(c.pending[0:])
		word := binary.LittleEndian.Uint32(c.pending[4:])
		size := int(word >> 16)

		if size < 8 {
			return errors.New("the compositor sent a malformed event")
		}

		if len(c.pending) < size {
			break
		}

		event := &Event{data: c.pending[8:size]}

		if handler, ok := c.handlers[object]; ok {
			handler(uint16(word&0xffff), event)
		}

		c.pending = c.pending[size:]
	}

	return nil
}

func (c *Conn) displayEvent(opcode uint16, event *Event) {
	if opcode == displayError {
		object, code, message := event.Uint(), event.Uint(), event.String()
		c.failed = fmt.Errorf("the compositor refused a request (object %d, error %d): %s", object, code, message)
	}
}

// Message is the arguments of a request, in order.
type Message struct {
	data []byte
	fds  []int
}

// NewMessage returns a request without arguments.
func NewMessage() *Message {
	return &Message{}
}

// Uint adds an unsigned integer, an object, or a new id.
func (m *Message) Uint(value uint32) *Message {
	m.data = binary.LittleEndian.AppendUint32(m.data, value)

	return m
}

// Int adds a signed integer.
func (m *Message) Int(value int32) *Message {
	return m.Uint(uint32(value))
}

// Fixed adds a number in the protocol's 24.8 fixed point.
func (m *Message) Fixed(value float64) *Message {
	return m.Int(int32(math.Round(value * 256)))
}

// String adds a string, terminated and padded as the protocol wants.
func (m *Message) String(value string) *Message {
	m.Uint(uint32(len(value) + 1))
	m.data = append(m.data, value...)
	m.data = append(m.data, 0)

	for len(m.data)%4 != 0 {
		m.data = append(m.data, 0)
	}

	return m
}

// FD adds a file descriptor, which travels beside the bytes.
func (m *Message) FD(fd int) *Message {
	m.fds = append(m.fds, fd)

	return m
}

// Event is the arguments of an event, read in order.
type Event struct {
	data []byte
}

// Uint reads an unsigned integer or an object.
func (e *Event) Uint() uint32 {
	if len(e.data) < 4 {
		return 0
	}

	value := binary.LittleEndian.Uint32(e.data)
	e.data = e.data[4:]

	return value
}

// Int reads a signed integer.
func (e *Event) Int() int32 {
	return int32(e.Uint())
}

// String reads a string.
func (e *Event) String() string {
	size := int(e.Uint())

	if size == 0 || size > len(e.data) {
		return ""
	}

	value := string(e.data[:size-1])
	padded := (size + 3) &^ 3
	e.data = e.data[min(padded, len(e.data)):]

	return value
}
