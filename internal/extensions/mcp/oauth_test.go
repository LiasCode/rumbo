package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/internal/credentials"
	"github.com/LiasCode/rumbo/protocol"
)

// protected is an MCP server that takes a token an authorization server of
// its own gives, to whoever signs in: it publishes where, registers
// clients, and agrees to every sign-in the browser is sent to.
type protected struct {
	*httptest.Server

	mu sync.Mutex
	// valid are the access tokens it takes, and issued how many it gave.
	valid  map[string]bool
	issued int
	// challenges are the PKCE challenges of the codes given, by code.
	challenges map[string]string
	// renewable is whether it still takes its refresh tokens.
	renewable bool
	// asked is what the last authorization was asked with.
	asked url.Values
}

func newProtected(t *testing.T) *protected {
	t.Helper()

	p := &protected{valid: map[string]bool{}, challenges: map[string]string{}, renewable: true}
	mux := http.NewServeMux()

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		taken := p.valid[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		p.mu.Unlock()

		if !taken {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, p.URL))
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		var asked message

		_ = json.NewDecoder(r.Body).Decode(&asked)

		answer := respond(asked)

		if answer == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	})

	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": p.URL + "/mcp", "authorization_servers": []string{p.URL}, "scopes_supported": []string{"tools"}})
	})

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint":           p.URL + "/authorize",
			"token_endpoint":                   p.URL + "/token",
			"registration_endpoint":            p.URL + "/register",
			"code_challenge_methods_supported": []string{"S256"},
		})
	})

	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var asked struct {
			Redirects []string `json:"redirect_uris"`
		}

		_ = json.NewDecoder(r.Body).Decode(&asked)

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "client-of-" + asked.Redirects[0]})
	})

	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		p.mu.Lock()
		p.asked = query
		p.issued++
		code := fmt.Sprintf("code-%d", p.issued)
		p.challenges[code] = query.Get("code_challenge")
		p.mu.Unlock()

		http.Redirect(w, r, query.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(query.Get("state")), http.StatusFound)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		p.mu.Lock()
		defer p.mu.Unlock()

		refuse := func(why string) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": why})
		}

		switch r.Form.Get("grant_type") {
		case "authorization_code":
			proof := sha256.Sum256([]byte(r.Form.Get("code_verifier")))

			if p.challenges[r.Form.Get("code")] != base64.RawURLEncoding.EncodeToString(proof[:]) {
				refuse("the verifier is not the challenge's")
				return
			}

		case "refresh_token":
			if !p.renewable || !strings.HasPrefix(r.Form.Get("refresh_token"), "refresh-") {
				refuse("the refresh token is not taken")
				return
			}
		}

		p.issued++
		access := fmt.Sprintf("access-%d", p.issued)
		p.valid[access] = true

		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": fmt.Sprintf("refresh-%d", p.issued), "expires_in": 3600})
	})

	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)

	return p
}

// revoke makes the server refuse every access token it gave.
func (p *protected) revoke() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.valid = map[string]bool{}
}

// talker is the person a function talks with: it keeps what it was told,
// and answers what it is asked with what answer gives.
type talker struct {
	said   []string
	answer func(question string) string
}

func (t *talker) Say(text string) {
	t.said = append(t.said, text)
}

func (t *talker) Ask(_ context.Context, question string, _ bool) (string, error) {
	return t.answer(question), nil
}

// started is the adapter for the protected server, as the host starts it:
// registered as docs, with its credentials kept in a directory of the test.
func started(t *testing.T, server *protected) (*Adapter, protocol.ExtensionInitializeResult, *told) {
	t.Helper()

	heard := &told{}
	adapter := New(nil, "test", heard)
	t.Cleanup(adapter.Close)

	config := fmt.Sprintf(`{"url":%q}`, server.URL+"/mcp")

	result, err := adapter.Initialize(context.Background(), protocol.InitializeParams{Config: json.RawMessage(config), Name: "docs"})

	if err != nil {
		t.Fatal(err)
	}

	return adapter, result, heard
}

// A server that wants a sign-in gives no tools until the person signs in
// with the command the adapter offers meanwhile; then the host starts it
// again, and it has the server's tools and a command to sign out. A token
// the server stops taking is renewed without anyone noticing, and one that
// cannot be renewed ends the sign-in, which the model is told how to mend.
func TestAServerIsSignedInToWithOAuth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	server := newProtected(t)
	adapter, declared, heard := started(t, server)

	if len(declared.Tools) != 0 || len(declared.Functions) != 1 || declared.Functions[0].Name != "login" || !heard.has("/docs-login signs in") {
		t.Fatalf("signed out, it declared %+v and said %q", declared, heard.text.String())
	}

	// The browser is sent to the authorization server, which sends it back.
	adapter.signing.open = func(address string) error {
		response, err := http.Get(address)

		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}

		return err
	}

	person := &talker{answer: func(string) string { return "" }}

	answer, err := adapter.InvokeDialog(context.Background(), "login", nil, person)

	if err != nil || !strings.Contains(answer.(string), "signed in to "+server.URL+"/mcp") {
		t.Fatalf("login = %v, err = %v", answer, err)
	}

	select {
	case <-adapter.Changes():
	default:
		t.Fatal("the host was not told that what the adapter declares changed")
	}

	asked := server.asked

	if asked.Get("resource") != server.URL+"/mcp" || asked.Get("scope") != "tools" || asked.Get("code_challenge_method") != "S256" || !strings.HasPrefix(asked.Get("client_id"), "client-of-http://127.0.0.1:") {
		t.Fatalf("the authorization was asked with %v", asked)
	}

	// Where the system keeps configuration: XDG_CONFIG_HOME on Linux, and
	// under HOME on macOS, both in the test's own directory.
	config, err := os.UserConfigDir()

	if err != nil || !strings.HasPrefix(config, home) {
		t.Fatalf("the configuration directory is %q, outside the test's: %v", config, err)
	}

	kept := filepath.Join(config, "rumbo", "credentials", "mcp.json")

	if info, err := os.Stat(kept); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the sign-in is kept in %v, %v", info, err)
	}

	// Started again, it has the server's tools.
	adapter, declared, _ = started(t, server)

	if len(declared.Tools) != len(serverTools) || len(declared.Functions) != 1 || declared.Functions[0].Name != "logout" {
		t.Fatalf("signed in, it declared %d tools and %+v", len(declared.Tools), declared.Functions)
	}

	if said := call(t, adapter, "echo", `{"signed":"in"}`); said.Text != `{"signed":"in"}` {
		t.Fatalf("echo = %+v", said)
	}

	// The server stops taking the token, which is renewed and sent again.
	server.revoke()

	if said := call(t, adapter, "echo", `{"renewed":true}`); said.Text != `{"renewed":true}` {
		t.Fatalf("after a renewal, echo = %+v", said)
	}

	// Nothing renews it any more: the sign-in is over, and forgotten.
	server.revoke()
	server.renewable = false

	_, err = adapter.Call(context.Background(), protocol.CallParams{Name: "echo", Args: json.RawMessage(`{}`)}, nil)

	if err == nil || !strings.Contains(err.Error(), "the user signs in again with /docs-login") || adapter.signing.signedIn() {
		t.Fatalf("with the sign-in over: %v", err)
	}

	select {
	case <-adapter.Changes():
	default:
		t.Fatal("the host was not told the tools are gone")
	}
}

// Over ssh the browser runs on another computer and cannot come back, so
// the person pastes the address it ended at; and signing out forgets the
// grant.
func TestASignInTakesAPastedAddressAndIsForgotten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	server := newProtected(t)
	adapter, _, _ := started(t, server)

	adapter.signing.open = func(string) error { return fmt.Errorf("no browser here") }

	// The person opens the address elsewhere, and pastes where it ended.
	person := &talker{}
	person.answer = func(string) string {
		_, address, _ := strings.Cut(person.said[0], "at:\n")
		elsewhere := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

		response, err := elsewhere.Get(address)

		if err != nil {
			t.Error(err)
			return ""
		}

		defer response.Body.Close()

		return response.Header.Get("Location")
	}

	if _, err := adapter.InvokeDialog(context.Background(), "login", nil, person); err != nil {
		t.Fatal(err)
	}

	if len(person.said) != 2 || !strings.Contains(person.said[1], "could not open a browser") || !adapter.signing.signedIn() {
		t.Fatalf("said %q; signed in %v", person.said, adapter.signing.signedIn())
	}

	// An address of another sign-in is not taken for this one.
	person.answer = func(string) string { return "http://127.0.0.1:1/callback?code=stolen&state=other" }

	if _, err := adapter.InvokeDialog(context.Background(), "login", nil, person); err == nil || !strings.Contains(err.Error(), "the browser did not come back") {
		t.Fatalf("a foreign address: %v", err)
	}

	answer, err := adapter.InvokeDialog(context.Background(), "logout", nil, person)

	if err != nil || !strings.Contains(answer.(string), "signed out of") || adapter.signing.signedIn() {
		t.Fatalf("logout = %v, err = %v", answer, err)
	}
}

// A server that needs no sign-in is not offered one, and a token in the
// environment is not renewed: the server that refuses it says so.
func TestNoSignInWhereThereIsNoneToDo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RUMBO_TEST_TOKEN", "stale")

	server := newProtected(t)
	adapter := New(nil, "test", io.Discard)
	config := fmt.Sprintf(`{"url":%q,"token_env":"RUMBO_TEST_TOKEN"}`, server.URL+"/mcp")

	_, err := adapter.Initialize(context.Background(), protocol.InitializeParams{Config: json.RawMessage(config)})

	if err == nil || !strings.Contains(err.Error(), "it did not take the token RUMBO_TEST_TOKEN holds") {
		t.Fatalf("a token the server refuses: %v", err)
	}

	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var asked message

		_ = json.NewDecoder(r.Body).Decode(&asked)

		if answer := respond(asked); answer != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(answer)
		}
	}))

	defer open.Close()

	adapter = New(nil, "test", io.Discard)

	declared, err := adapter.Initialize(context.Background(), protocol.InitializeParams{Config: json.RawMessage(fmt.Sprintf(`{"url":%q}`, open.URL))})

	if err != nil || len(declared.Functions) != 0 || len(declared.Tools) != len(serverTools) {
		t.Fatalf("declared %+v, err = %v", declared, err)
	}
}

// A sign-in that expired while nobody looked, with nothing to renew it
// with, does not keep the adapter from starting: it says so, forgets it,
// and offers the sign-in again.
func TestAnExpiredSignInIsOfferedAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	server := newProtected(t)
	adapter, _, _ := started(t, server)

	expired := &credentials.Grant{AccessToken: "access-old", ExpiresAt: time.Now().Add(-time.Hour)}

	if err := adapter.signing.store.SetGrant(server.URL+"/mcp", expired); err != nil {
		t.Fatal(err)
	}

	adapter, declared, heard := started(t, server)

	if len(declared.Functions) != 1 || declared.Functions[0].Name != "login" || !heard.has("is over: the sign-in could not be renewed: it expired") || adapter.signing.signedIn() {
		t.Fatalf("declared %+v; said %q", declared.Functions, heard.text.String())
	}
}
