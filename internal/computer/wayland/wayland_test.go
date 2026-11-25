//go:build linux

package wayland

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// request is a request the fake compositor received.
type request struct {
	iface  string
	opcode uint16
	args   *Event
	fds    []int
}

// compositor is a fake compositor: it offers a few globals, answers syncs,
// says where its one monitor is, and keeps what it was sent.
type compositor struct {
	t        *testing.T
	conn     *net.UnixConn
	mu       sync.Mutex
	objects  map[uint32]string
	received []request
	// refuse makes it answer the next sync with a protocol error.
	refuse bool
}

var offered = []Global{
	{Name: 1, Interface: "wl_seat", Version: 9},
	{Name: 2, Interface: "wl_output", Version: 4},
	{Name: 3, Interface: "zxdg_output_manager_v1", Version: 3},
	{Name: 4, Interface: "zwlr_virtual_pointer_manager_v1", Version: 2},
	{Name: 5, Interface: "zwp_virtual_keyboard_manager_v1", Version: 1},
}

func fakeCompositor(t *testing.T) *compositor {
	t.Helper()

	// A socket's path is short: 103 bytes on macOS, where t.TempDir's path
	// is longer than that by itself.
	dir, err := os.MkdirTemp("", "wl")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { os.RemoveAll(dir) })

	path := filepath.Join(dir, "w")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { listener.Close() })
	t.Setenv("WAYLAND_DISPLAY", path)

	fake := &compositor{t: t, objects: map[uint32]string{1: "wl_display"}}

	go func() {
		conn, err := listener.AcceptUnix()

		if err != nil {
			return
		}

		fake.conn = conn
		fake.serve()
	}()

	return fake
}

func (c *compositor) send(object uint32, opcode uint16, message *Message) {
	data := make([]byte, 8+len(message.data))
	binary.LittleEndian.PutUint32(data[0:], object)
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data))<<16|uint32(opcode))
	copy(data[8:], message.data)
	c.conn.Write(data)
}

func (c *compositor) serve() {
	var pending []byte
	// The files that came are queued, and each goes to the next request
	// that carries one, as libwayland does: a read may bring several
	// requests, and their files with them.
	var files []int

	for {
		buffer := make([]byte, 4096)
		oob := make([]byte, 128)
		count, oobCount, _, _, err := c.conn.ReadMsgUnix(buffer, oob)

		if err != nil {
			return
		}

		if messages, err := syscall.ParseSocketControlMessage(oob[:oobCount]); err == nil {
			for _, message := range messages {
				rights, _ := syscall.ParseUnixRights(&message)
				files = append(files, rights...)
			}
		}

		pending = append(pending, buffer[:count]...)

		for len(pending) >= 8 {
			size := int(binary.LittleEndian.Uint32(pending[4:]) >> 16)

			if len(pending) < size {
				break
			}

			object := binary.LittleEndian.Uint32(pending)
			opcode := uint16(binary.LittleEndian.Uint32(pending[4:]))
			var fds []int

			// Of the requests here, only a keymap carries a file.
			if c.carriesFile(object, opcode) && len(files) > 0 {
				fds, files = files[:1], files[1:]
			}

			c.handle(object, opcode, append([]byte(nil), pending[8:size]...), fds)
			pending = pending[size:]
		}
	}
}

func (c *compositor) carriesFile(object uint32, opcode uint16) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.objects[object] == "zwp_virtual_keyboard_v1" && opcode == keyboardKeymap
}

func (c *compositor) handle(object uint32, opcode uint16, body []byte, fds []int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	iface := c.objects[object]
	c.received = append(c.received, request{iface: iface, opcode: opcode, args: &Event{data: body}, fds: fds})

	args := &Event{data: body}

	switch {
	case iface == "wl_display" && opcode == displayGetRegistry:
		registry := args.Uint()
		c.objects[registry] = "wl_registry"

		for _, global := range offered {
			c.send(registry, registryGlobal, NewMessage().Uint(global.Name).String(global.Interface).Uint(global.Version))
		}

	case iface == "wl_display" && opcode == displaySync:
		callback := args.Uint()

		if c.refuse {
			c.send(1, displayError, NewMessage().Uint(callback).Uint(3).String("not today"))
			return
		}

		c.send(callback, callbackDone, NewMessage().Uint(0))

	case iface == "wl_registry" && opcode == registryBind:
		args.Uint()
		bound := args.String()
		args.Uint()
		c.objects[args.Uint()] = bound

	case iface == "zxdg_output_manager_v1" && opcode == xdgOutputGet:
		id := args.Uint()
		c.send(id, xdgOutputLogicalPosition, NewMessage().Int(1920).Int(0))
		c.send(id, xdgOutputLogicalSize, NewMessage().Int(1280).Int(800))
		c.send(id, xdgOutputName, NewMessage().String("HDMI-A-1"))

	case iface == "zwlr_virtual_pointer_manager_v1" && opcode == pointerCreate:
		args.Uint()
		c.objects[args.Uint()] = "zwlr_virtual_pointer_v1"

	case iface == "zwp_virtual_keyboard_manager_v1" && opcode == keyboardCreate:
		args.Uint()
		c.objects[args.Uint()] = "zwp_virtual_keyboard_v1"
	}
}

// sent returns the requests sent to objects of an interface.
func (c *compositor) sent(iface string) []request {
	c.mu.Lock()
	defer c.mu.Unlock()

	var found []request

	for _, received := range c.received {
		if received.iface == iface {
			found = append(found, received)
		}
	}

	return found
}

func TestTheRegistryAndTheMonitorsAreRead(t *testing.T) {
	fakeCompositor(t)

	conn, err := Dial()

	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	if global, ok := conn.Global("zwp_virtual_keyboard_manager_v1"); !ok || global.Name != 5 {
		t.Fatalf("global = %+v, %v", global, ok)
	}

	outputs, err := conn.Outputs()

	if err != nil {
		t.Fatal(err)
	}

	if len(outputs) != 1 || outputs[0] != (Output{Name: "HDMI-A-1", X: 1920, Y: 0, Width: 1280, Height: 800}) {
		t.Fatalf("outputs = %+v", outputs)
	}
}

func TestThePointerMovesAcrossTheWholeLayout(t *testing.T) {
	fake := fakeCompositor(t)
	conn, err := Dial()

	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	pointer, err := conn.NewPointer()

	if err != nil {
		t.Fatal(err)
	}

	if err := pointer.MoveTo(10, 20, 3200, 800); err != nil {
		t.Fatal(err)
	}

	if err := pointer.Scroll(0, -2); err != nil {
		t.Fatal(err)
	}

	if err := pointer.Nudge(-1, 0); err != nil {
		t.Fatal(err)
	}

	sent := fake.sent("zwlr_virtual_pointer_v1")

	if len(sent) != 7 || sent[0].opcode != pointerMotionAbsolute || sent[1].opcode != pointerFrame {
		t.Fatalf("sent %+v", sent)
	}

	nudge := sent[5].args
	nudge.Uint()

	if dx, dy := nudge.Int(), nudge.Int(); sent[5].opcode != pointerMotion || dx != -256 || dy != 0 || sent[6].opcode != pointerFrame {
		t.Fatalf("nudge = %d %d", dx, dy)
	}

	motion := sent[0].args
	motion.Uint()

	if x, y, width, height := motion.Uint(), motion.Uint(), motion.Uint(), motion.Uint(); x != 10 || y != 20 || width != 3200 || height != 800 {
		t.Fatalf("motion = %d %d %d %d", x, y, width, height)
	}

	discrete := sent[3].args
	discrete.Uint()

	if axis, value, steps := discrete.Uint(), discrete.Int(), discrete.Int(); sent[3].opcode != pointerAxisDiscrete || axis != 0 || value != -2*wheelStep*256 || steps != -2 {
		t.Fatalf("scroll = %d %d %d", axis, value, steps)
	}
}

func TestTheKeymapTravelsAsAFile(t *testing.T) {
	fake := fakeCompositor(t)
	conn, err := Dial()

	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	if _, err := conn.NewKeyboard("xkb_keymap {};"); err != nil {
		t.Fatal(err)
	}

	// The keymap comes with the keyboard, before the compositor is waited
	// for.
	sent := fake.sent("zwp_virtual_keyboard_v1")

	if len(sent) != 1 || len(sent[0].fds) != 1 {
		t.Fatalf("sent %+v", sent)
	}

	args := sent[0].args

	if format, size := args.Uint(), args.Uint(); format != keymapFormatXKB || size != uint32(len("xkb_keymap {};"))+1 {
		t.Fatalf("format %d, size %d", format, size)
	}

	file := os.NewFile(uintptr(sent[0].fds[0]), "keymap")
	defer file.Close()

	content := make([]byte, 64)
	count, _ := file.ReadAt(content, 0)

	if string(content[:count]) != "xkb_keymap {};\x00" {
		t.Fatalf("keymap = %q", content[:count])
	}
}

func TestAProtocolErrorIsReturned(t *testing.T) {
	fake := fakeCompositor(t)
	conn, err := Dial()

	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	fake.mu.Lock()
	fake.refuse = true
	fake.mu.Unlock()

	if err := conn.Roundtrip(); err == nil || !strings.Contains(err.Error(), "not today") {
		t.Fatalf("err = %v", err)
	}

	if err := conn.Send(1, displaySync, NewMessage().Uint(conn.NewID())); err == nil {
		t.Fatal("a refused connection kept sending")
	}
}

func TestStringsArePaddedToFourBytes(t *testing.T) {
	message := NewMessage().String("abc").String("abcd")

	if len(message.data) != 4+4+4+8 {
		t.Fatalf("length = %d", len(message.data))
	}

	event := &Event{data: message.data}

	if first, second := event.String(), event.String(); first != "abc" || second != "abcd" {
		t.Fatalf("read %q %q", first, second)
	}
}
