// Package signin holds what signing in with OAuth in the person's browser
// takes, whoever signs in: values that cannot be guessed, a listener on the
// loopback for the browser to come back to, and opening the browser. What
// is asked of which server, and what is made of its answer, is the caller's.
package signin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// Random is 32 random bytes, as base64url without padding.
func Random() string {
	value := make([]byte, 32)
	_, _ = rand.Read(value)

	return base64.RawURLEncoding.EncodeToString(value)
}

// Listener waits on the loopback for the browser to come back from signing
// in. Redirect is the address it comes back to, and Result gives what it
// came back with.
type Listener struct {
	Redirect string
	Result   chan url.Values
	server   *http.Server
}

// Listen starts waiting for the browser at path, on a port of the loopback
// the system chooses, before the browser is sent anywhere. Only an answer
// that carries state is taken.
func Listen(path, state string) (*Listener, error) {
	socket, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		return nil, fmt.Errorf("the sign-in could not listen for the browser: %w", err)
	}

	host := socket.Addr().String()
	waiting := &Listener{Redirect: "http://" + host + path, Result: make(chan url.Values, 1)}

	waiting.server = &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")

			if r.Method != http.MethodGet || r.Host != host || r.URL.Path != path {
				http.NotFound(w, r)
				return
			}

			// Something else on the loopback cannot end this sign-in.
			if !SameState(r.URL.Query(), state) {
				http.Error(w, "This is not the sign-in rumbo started. Go back to the browser tab that it opened.", http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Back to rumbo</title><body style="font:17px system-ui;max-width:32rem;margin:18vh auto"><h1>Back to rumbo</h1><p>Go back to the terminal and press Enter, and rumbo finishes signing in. You can close this tab.</p>`)

			select {
			case waiting.Result <- r.URL.Query():
			default:
			}
		}),
	}

	go waiting.server.Serve(socket)

	return waiting, nil
}

// Close stops listening.
func (l *Listener) Close() {
	_ = l.server.Close()
}

// SameState reports whether an answer carries the state of this sign-in,
// once.
func SameState(query url.Values, state string) bool {
	return len(query["state"]) == 1 && subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) == 1
}

// OpenBrowser opens address in the person's browser, where there is one to
// open: on Linux, only in a graphical session, so a browser that would run
// in the terminal is never started.
func OpenBrowser(address string) error {
	var command *exec.Cmd

	switch {
	case runtime.GOOS == "darwin":
		command = exec.Command("open", address)
	case os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "":
		command = exec.Command("xdg-open", address)
	default:
		return errors.New("there is no browser here to open")
	}

	if err := command.Start(); err != nil {
		return err
	}

	go command.Wait()

	return nil
}
