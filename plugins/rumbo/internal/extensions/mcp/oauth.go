package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/LiasCode/rumbo/plugins/rumbo/internal/credentials"
	"github.com/LiasCode/rumbo/plugins/rumbo/internal/signin"
	"github.com/LiasCode/rumbo/protocol"
)

// Signing in follows MCP's authorization for servers reached over HTTP: the
// server says, in its 401 and in what it publishes about itself, which
// authorization server protects it; rumbo registers itself there as a
// client, and the person signs in with it in their browser, with PKCE and a
// loopback redirect. What that gives is a grant of tokens for the server,
// which the adapter renews as they expire. See
// https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization.

const (
	callbackPath = "/callback"
	// renewBefore is how long before a token expires it is renewed.
	renewBefore = time.Minute
)

// unauthorized is a server's refusal for lack of a token it takes. hint is
// what it said of where to sign in, when it said.
type unauthorized struct {
	status string
	hint   string
}

func (u *unauthorized) Error() string {
	return "the MCP server refused the request (" + u.status + "): it wants a sign-in"
}

// resourceMetadata finds, in what a server says with a 401, the address of
// what it publishes about its protection.
var resourceMetadata = regexp.MustCompile(`resource_metadata="([^"]+)"`)

// signIn signs in to one MCP server and keeps what that gives.
type signIn struct {
	// server is the MCP server's address, which the grant is for and is
	// kept by.
	server string
	// clientID is the client the user registered with the authorization
	// server by hand, for one that registers none itself.
	clientID string
	store    credentials.Store
	client   *http.Client
	open     func(address string) error
	now      func() time.Time

	// mu guards the token last read from the store, and when it expires, so
	// that not every request reads the file.
	mu      sync.Mutex
	current string
	expires time.Time
}

func newSignIn(server, clientID string) *signIn {
	return &signIn{
		server:   server,
		clientID: clientID,
		store:    credentials.Default("mcp"),
		client:   &http.Client{Timeout: 30 * time.Second},
		open:     signin.OpenBrowser,
		now:      time.Now,
	}
}

// endpoints are where an authorization server signs in, issues tokens, and
// registers clients, and the scopes the MCP server asks for.
type endpoints struct {
	Authorization string   `json:"authorization_endpoint"`
	Token         string   `json:"token_endpoint"`
	Registration  string   `json:"registration_endpoint"`
	Challenges    []string `json:"code_challenge_methods_supported"`
	scopes        []string
}

// token implements bearer: the token a sign-in gave, renewed when it is
// about to expire, or none when nobody signed in.
func (s *signIn) token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fresh := func() bool { return s.expires.IsZero() || s.now().Before(s.expires.Add(-renewBefore)) }

	if s.current != "" && fresh() {
		return s.current, nil
	}

	grant, err := s.store.Grant(s.server)

	if err != nil || grant == nil {
		return "", err
	}

	if s.current, s.expires = grant.AccessToken, grant.ExpiresAt; fresh() {
		return s.current, nil
	}

	return s.renewed(ctx)
}

// refused implements bearer: the server did not take the token, so it is
// renewed, which says why when it cannot be.
func (s *signIn) refused(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current == "" {
		return errors.New("nobody is signed in")
	}

	_, err := s.renewed(ctx)

	return err
}

// renewed renews the token last read and keeps the new one. The caller
// holds the lock.
func (s *signIn) renewed(ctx context.Context) (string, error) {
	stale := s.current
	s.current, s.expires = "", time.Time{}

	renewed, err := s.renew(ctx, stale)

	if err != nil {
		return "", err
	}

	if grant, err := s.store.Grant(s.server); err == nil && grant != nil {
		s.current, s.expires = grant.AccessToken, grant.ExpiresAt
	}

	return renewed, nil
}

// forget drops the token read from the store, which changed under it.
func (s *signIn) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.current, s.expires = "", time.Time{}
}

// renew trades the grant's refresh token for a new access token, unless
// another process already did since stale was read. A grant that cannot be
// renewed is forgotten, so the person is asked to sign in again.
func (s *signIn) renew(ctx context.Context, stale string) (string, error) {
	var renewed string

	err := s.store.Locked(func() error {
		grant, err := s.store.Grant(s.server)

		if err != nil {
			return err
		}

		if grant == nil {
			return errors.New("nobody is signed in")
		}

		if grant.AccessToken != stale {
			renewed = grant.AccessToken
			return nil
		}

		// Nothing renews it, so keeping it would only fail every time.
		if grant.RefreshToken == "" {
			_ = s.store.SetGrant(s.server, nil)

			return errors.New("it expired, and the server gave nothing to renew it with")
		}

		clientID, _, err := s.store.Registration(s.server)

		if err != nil {
			return err
		}

		at, err := s.discover(ctx, "")

		if err != nil {
			return err
		}

		issued, err := s.exchange(ctx, at, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {grant.RefreshToken},
			"client_id":     {clientID},
			"resource":      {s.server},
		})

		if err != nil {
			var refused *oauthError

			// The server no longer takes the grant: keeping it would only
			// fail the same way every time.
			if errors.As(err, &refused) {
				_ = s.store.SetGrant(s.server, nil)
			}

			return err
		}

		// A server that rotates refresh tokens gives a new one.
		if issued.RefreshToken == "" {
			issued.RefreshToken = grant.RefreshToken
		}

		renewed = issued.AccessToken

		return s.store.SetGrant(s.server, issued.grant(s.now()))
	})

	if err != nil {
		return "", fmt.Errorf("the sign-in could not be renewed: %w", err)
	}

	return renewed, nil
}

// signedIn reports whether a grant is kept for the server.
func (s *signIn) signedIn() bool {
	grant, err := s.store.Grant(s.server)

	return err == nil && grant != nil
}

// logout forgets the grant, and reports whether there was one.
func (s *signIn) logout() (bool, error) {
	was := s.signedIn()
	s.forget()

	return was, s.store.SetGrant(s.server, nil)
}

// login signs the person in, in their browser, talking with them through
// dialog. hint is what the server said of where to sign in, when it did.
func (s *signIn) login(ctx context.Context, hint string, dialog protocol.Dialog) error {
	at, err := s.discover(ctx, hint)

	if err != nil {
		return err
	}

	state, verifier := signin.Random(), signin.Random()
	waiting, err := signin.Listen(callbackPath, state)

	if err != nil {
		return err
	}

	defer waiting.Close()

	clientID, err := s.registered(ctx, at, waiting.Redirect)

	if err != nil {
		return err
	}

	challenge := sha256.Sum256([]byte(verifier))

	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {waiting.Redirect},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"resource":              {s.server},
	}

	if len(at.scopes) > 0 {
		query.Set("scope", strings.Join(at.scopes, " "))
	}

	separator := "?"

	if strings.Contains(at.Authorization, "?") {
		separator = "&"
	}

	address := at.Authorization + separator + query.Encode()
	dialog.Say("Sign in to " + s.server + " in your browser, at:\n" + address)

	if err := s.open(address); err != nil {
		dialog.Say("rumbo could not open a browser here; open that address in one")
	}

	answer, err := dialog.Ask(ctx, "Press Enter once the browser says to go back to rumbo. If it runs on another computer, paste here the address it ended at.", true)

	if err != nil {
		return err
	}

	back, found := pasted(answer, state)

	if !found {
		select {
		case back = <-waiting.Result:
		case <-time.After(3 * time.Second):
			return errors.New("the browser did not come back to rumbo; try again, and over ssh, paste the address the browser ended at")
		}
	}

	if failure := back.Get("error"); failure != "" {
		return fmt.Errorf("the sign-in failed: %s %s", failure, back.Get("error_description"))
	}

	if len(back["code"]) != 1 {
		return errors.New("the server did not finish the sign-in; try again")
	}

	issued, err := s.exchange(ctx, at, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {back.Get("code")},
		"redirect_uri":  {waiting.Redirect},
		"client_id":     {clientID},
		"code_verifier": {verifier},
		"resource":      {s.server},
	})

	if err != nil {
		return err
	}

	s.forget()

	return s.store.Locked(func() error {
		if err := s.store.SetRegistration(s.server, clientID, ""); err != nil {
			return err
		}

		return s.store.SetGrant(s.server, issued.grant(s.now()))
	})
}

// pasted reads the address the browser ended at, when it was pasted
// instead of reaching the loopback: a browser on another computer, as over
// ssh. Only one that carries the state of this sign-in is taken.
func pasted(text, state string) (url.Values, bool) {
	address, err := url.Parse(strings.TrimSpace(text))

	if err != nil || address.Path != callbackPath || !signin.SameState(address.Query(), state) {
		return nil, false
	}

	return address.Query(), true
}

// registered returns the client rumbo is to the authorization server: the
// one the user registered by hand, or one the server registers now for the
// address the browser comes back to, which changes with every sign-in.
func (s *signIn) registered(ctx context.Context, at endpoints, redirect string) (string, error) {
	if s.clientID != "" {
		return s.clientID, nil
	}

	if at.Registration == "" {
		return "", errors.New("the authorization server registers no clients by itself; register rumbo with it by hand, with a redirect to http://127.0.0.1 on any port, and set client_id to what it gives")
	}

	asked, _ := json.Marshal(map[string]any{
		"client_name":                "rumbo",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, at.Registration, strings.NewReader(string(asked)))

	if err != nil {
		return "", err
	}

	request.Header.Set("Content-Type", "application/json")

	response, err := s.client.Do(request)

	if err != nil {
		return "", fmt.Errorf("rumbo could not be registered with the authorization server: %w", err)
	}

	defer response.Body.Close()

	var given struct {
		ClientID string `json:"client_id"`
	}

	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&given); err != nil || given.ClientID == "" {
		return "", fmt.Errorf("the authorization server did not register rumbo (%s)", response.Status)
	}

	return given.ClientID, nil
}

// tokens is what the token endpoint answers.
type tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// grant is the tokens as they are kept.
func (t tokens) grant(now time.Time) *credentials.Grant {
	kept := &credentials.Grant{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, Scopes: strings.Fields(t.Scope)}

	// A token that does not say when it expires is used until refused.
	if t.ExpiresIn > 0 {
		kept.ExpiresAt = now.Add(time.Duration(t.ExpiresIn) * time.Second)
	}

	return kept
}

// oauthError is a token endpoint's refusal, in OAuth's own words.
type oauthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *oauthError) Error() string {
	return strings.TrimSpace("the authorization server refused: " + e.Code + " " + e.Description)
}

// exchange asks the token endpoint for tokens.
func (s *signIn) exchange(ctx context.Context, at endpoints, form url.Values) (tokens, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, at.Token, strings.NewReader(form.Encode()))

	if err != nil {
		return tokens{}, err
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	response, err := s.client.Do(request)

	if err != nil {
		return tokens{}, fmt.Errorf("the authorization server could not be reached: %w", err)
	}

	defer response.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))

	if response.StatusCode != http.StatusOK {
		var refused oauthError

		if json.Unmarshal(body, &refused) == nil && refused.Code != "" {
			return tokens{}, &refused
		}

		return tokens{}, fmt.Errorf("the authorization server answered %s", response.Status)
	}

	var issued tokens

	if err := json.Unmarshal(body, &issued); err != nil || issued.AccessToken == "" {
		return tokens{}, errors.New("the authorization server answered without a token")
	}

	return issued, nil
}

// discover finds where to sign in for the server: what the server
// publishes about its protection names its authorization server, which
// publishes its endpoints. hint is the address of the first, when the
// server gave it with its 401.
func (s *signIn) discover(ctx context.Context, hint string) (endpoints, error) {
	server, err := url.Parse(s.server)

	if err != nil {
		return endpoints{}, err
	}

	var protection struct {
		Servers []string `json:"authorization_servers"`
		Scopes  []string `json:"scopes_supported"`
	}

	candidates := []string{wellKnown(server, "oauth-protected-resource", true), wellKnown(server, "oauth-protected-resource", false)}

	if hint != "" {
		candidates = append([]string{hint}, candidates...)
	}

	for _, candidate := range candidates {
		if s.getJSON(ctx, candidate, &protection) == nil && len(protection.Servers) > 0 {
			break
		}
	}

	// A server that publishes nothing of its protection is its own
	// authorization server, as MCP had it before.
	issuer := server.Scheme + "://" + server.Host

	if len(protection.Servers) > 0 {
		issuer = protection.Servers[0]
	}

	authorization, err := url.Parse(issuer)

	if err != nil {
		return endpoints{}, fmt.Errorf("the authorization server's address, %q: %w", issuer, err)
	}

	candidates = []string{
		wellKnown(authorization, "oauth-authorization-server", true),
		wellKnown(authorization, "openid-configuration", true),
		strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration",
	}

	for _, candidate := range candidates {
		var found endpoints

		if s.getJSON(ctx, candidate, &found) != nil || found.Authorization == "" || found.Token == "" {
			continue
		}

		if len(found.Challenges) > 0 && !strings.Contains(strings.Join(found.Challenges, " "), "S256") {
			return endpoints{}, errors.New("the authorization server does not take PKCE with S256, which signing in needs")
		}

		for _, address := range []string{found.Authorization, found.Token, found.Registration} {
			if err := safe(address); err != nil {
				return endpoints{}, err
			}
		}

		found.scopes = protection.Scopes

		return found, nil
	}

	return endpoints{}, fmt.Errorf("%s does not say where to sign in: neither it nor %s publishes OAuth metadata; if it takes a token of your own, token_env names the environment variable that holds it", s.server, issuer)
}

// wellKnown is where a server publishes name: under /.well-known/ at its
// root, followed by its own path when inPath.
func wellKnown(server *url.URL, name string, inPath bool) string {
	address := server.Scheme + "://" + server.Host + "/.well-known/" + name

	if path := strings.Trim(server.Path, "/"); inPath && path != "" {
		address += "/" + path
	}

	return address
}

// safe refuses an endpoint a token or a code would cross the network to in
// the clear: only https, or the loopback.
func safe(address string) error {
	if address == "" {
		return nil
	}

	parsed, err := url.Parse(address)

	if err != nil {
		return fmt.Errorf("the authorization server's endpoint %q: %w", address, err)
	}

	loopback := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"

	if parsed.Scheme != "https" && !loopback {
		return fmt.Errorf("the authorization server's endpoint %s is not https, so signing in would not be safe", address)
	}

	return nil
}

func (s *signIn) getJSON(ctx context.Context, address string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)

	if err != nil {
		return err
	}

	request.Header.Set("Accept", "application/json")

	response, err := s.client.Do(request)

	if err != nil {
		return err
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", address, response.Status)
	}

	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(into)
}
