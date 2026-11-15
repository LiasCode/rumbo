package chatgpt

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/LiasCode/rumbo/internal/providers/credentials"
)

// Signing in follows OpenAI's Sign in with ChatGPT for open-source apps: an
// OAuth authorization in the person's browser, with PKCE and a loopback
// redirect, during which OpenAI registers this installation as a client of
// its own. What it gives is a grant of tokens for one account, which the
// provider renews as they expire. See
// https://developers.openai.com/siwc/token-sharing-open-source.

const (
	// resource is what the tokens are for, which every request names.
	resource = "https://api.openai.com/v1"
	scopes   = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	// planScope is the permission to use the person's ChatGPT plan, which a
	// sign-in without it does not give.
	planScope = "chatgpt.tokens.use.direct"
	// newClient asks OpenAI to register this installation as a client.
	newClient    = "dynamic_agent_client"
	appName      = "rumbo"
	callbackPath = "/auth/callback"
)

// endpoints are where an issuer signs in, issues and renews tokens, keeps
// its keys, and takes tokens back.
type endpoints struct {
	Issuer        string `json:"issuer"`
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	Keys          string `json:"jwks_uri"`
	Revocation    string `json:"revocation_endpoint"`
}

// auth signs in to an issuer and renews what it gave.
type auth struct {
	issuer string
	client *http.Client

	mu   sync.Mutex
	said *endpoints
}

// discover asks the issuer where its endpoints are, once, and refuses one
// that points anywhere but the issuer.
func (a *auth) discover(ctx context.Context) (endpoints, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.said != nil {
		return *a.said, nil
	}

	var said endpoints

	if err := a.getJSON(ctx, a.issuer+"/.well-known/openid-configuration", &said); err != nil {
		return said, fmt.Errorf("ChatGPT's sign-in configuration could not be read: %w", err)
	}

	origin, _ := url.Parse(a.issuer)
	required := []string{said.Authorization, said.Token, said.Keys}

	// Taking tokens back is the one an issuer may not have.
	if said.Revocation != "" {
		required = append(required, said.Revocation)
	}

	for _, address := range required {
		parsed, err := url.Parse(address)

		if address == "" || err != nil || parsed.Scheme != origin.Scheme || parsed.Host != origin.Host {
			return endpoints{}, errors.New("ChatGPT's sign-in configuration could not be verified")
		}
	}

	if said.Issuer != a.issuer {
		return endpoints{}, errors.New("ChatGPT's sign-in configuration could not be verified")
	}

	a.said = &said

	return said, nil
}

// attempt is one sign-in: the values that tie the browser's answer to it.
type attempt struct {
	state, nonce, verifier string
}

func newAttempt() attempt {
	return attempt{state: random(), nonce: random(), verifier: random()}
}

// random is 32 random bytes, as base64url without padding.
func random() string {
	value := make([]byte, 32)
	_, _ = rand.Read(value)

	return base64.RawURLEncoding.EncodeToString(value)
}

// newHostID names this installation to OpenAI, without saying anything of
// the person or the computer.
func newHostID() string {
	value := make([]byte, 16)
	_, _ = rand.Read(value)

	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80

	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

// authorization is the address the browser opens to sign in. A client
// registered before is asked again, and the account signed in before is
// offered first.
func (a attempt) authorization(at endpoints, redirect, clientID, hostID, email string) string {
	challenge := sha256.Sum256([]byte(a.verifier))

	query := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirect},
		"scope":                 {scopes},
		"resource":              {resource},
		"state":                 {a.state},
		"nonce":                 {a.nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"ext_agent_host_id":     {hostID},
	}

	if clientID == newClient {
		query.Set("agent_name_hint", appName)
	}

	if email != "" {
		query.Set("login_hint", email)
	}

	return at.Authorization + "?" + query.Encode()
}

// callback is what the browser comes back with.
type callback struct {
	code, clientID string
	err            error
}

// listener waits on the loopback for the browser to come back from signing
// in.
type listener struct {
	redirect string
	result   chan callback
	server   *http.Server
}

// listen starts waiting for the browser, on a port of the loopback the
// system chooses, before the browser is sent anywhere.
func listen(state string) (*listener, error) {
	socket, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		return nil, fmt.Errorf("the sign-in could not listen for the browser: %w", err)
	}

	host := socket.Addr().String()
	waiting := &listener{redirect: "http://" + host + callbackPath, result: make(chan callback, 1)}

	waiting.server = &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")

			if r.Method != http.MethodGet || r.Host != host || r.URL.Path != callbackPath {
				http.NotFound(w, r)
				return
			}

			// Something else on the loopback cannot end this sign-in.
			if !sameState(r.URL.Query(), state) {
				http.Error(w, "This is not the sign-in rumbo started. Go back to the browser tab that it opened.", http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Back to rumbo</title><body style="font:17px system-ui;max-width:32rem;margin:18vh auto"><h1>Back to rumbo</h1><p>Go back to the terminal and press Enter, and rumbo finishes signing in. You can close this tab.</p>`)

			select {
			case waiting.result <- answered(r.URL.Query()):
			default:
			}
		}),
	}

	go waiting.server.Serve(socket)

	return waiting, nil
}

func (l *listener) close() {
	_ = l.server.Close()
}

// sameState reports whether an answer carries the state of this sign-in,
// once.
func sameState(query url.Values, state string) bool {
	return len(query["state"]) == 1 && subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) == 1
}

// answered reads what the browser came back with.
func answered(query url.Values) callback {
	if failure := query.Get("error"); failure != "" {
		if failure == "access_denied" {
			return callback{err: errors.New("the sign-in was declined in the browser; nothing changed")}
		}

		return callback{err: fmt.Errorf("the sign-in failed: %s %s", failure, query.Get("error_description"))}
	}

	if len(query["code"]) != 1 || len(query["client_id"]) > 1 {
		return callback{err: errors.New("ChatGPT did not finish the sign-in; /login tries again")}
	}

	return callback{code: query.Get("code"), clientID: query.Get("client_id")}
}

// pasted reads the address the browser ended at, when it was pasted
// instead of reaching the loopback: a browser on another computer, as over
// ssh.
func pasted(text, state string) (callback, bool) {
	address, err := url.Parse(strings.TrimSpace(text))

	if err != nil || address.Path != callbackPath {
		return callback{}, false
	}

	query := address.Query()

	if !sameState(query, state) {
		return callback{err: errors.New("the address pasted is not of this sign-in; /login tries again")}, true
	}

	return answered(query), true
}

// issued is the client OAuth registered, as the browser said it: the one
// asked for again, or a new one. One that does not match is refused.
func issued(back callback, saved string) (string, error) {
	clientID := back.clientID

	if clientID == "" {
		clientID = saved
	}

	valid := clientID != "" && clientID != newClient && len(clientID) <= 200 && strings.Trim(clientID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == ""

	if !valid || saved != "" && back.clientID != "" && back.clientID != saved {
		return "", errors.New("ChatGPT did not finish registering rumbo; /login tries again")
	}

	return clientID, nil
}

// tokens is what the token endpoint answers.
type tokens struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	IDToken      string          `json:"id_token"`
	TokenType    string          `json:"token_type"`
	ExpiresIn    int64           `json:"expires_in"`
	Scope        *string         `json:"scope"`
	RefreshAfter json.RawMessage `json:"earliest_refresh_at"`
	Error        string          `json:"error"`
	Description  string          `json:"error_description"`
}

// oauthError is an error the token endpoint answered with, by its code.
type oauthError struct {
	code, description string
}

func (e *oauthError) Error() string {
	return strings.TrimSpace("ChatGPT refused the sign-in: " + e.code + " " + e.description)
}

// expired reports whether err says the grant can no longer be renewed, so
// only signing in again helps.
func expired(err error) bool {
	failure, ok := errors.AsType[*oauthError](err)

	return ok && slices.Contains([]string{
		"invalid_grant", "invalid_refresh_token", "token_expired",
		"refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused",
	}, failure.code)
}

// exchange asks the token endpoint for tokens.
func (a *auth) exchange(ctx context.Context, form url.Values) (tokens, error) {
	var answer tokens

	at, err := a.discover(ctx)

	if err != nil {
		return answer, err
	}

	form.Set("resource", resource)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, at.Token, strings.NewReader(form.Encode()))

	if err != nil {
		return answer, err
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	response, err := a.client.Do(request)

	if err != nil {
		return answer, err
	}

	defer response.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))

	if json.Unmarshal(raw, &answer) != nil && response.StatusCode == http.StatusOK {
		return answer, errors.New("ChatGPT answered the sign-in with something it could not read")
	}

	if response.StatusCode != http.StatusOK || answer.Error != "" {
		if answer.Error == "" {
			answer.Error = response.Status
		}

		return answer, &oauthError{code: answer.Error, description: answer.Description}
	}

	if !strings.EqualFold(answer.TokenType, "bearer") || answer.AccessToken == "" || answer.ExpiresIn <= 0 || answer.RefreshToken == "" {
		return answer, errors.New("ChatGPT answered the sign-in without the tokens it should; /login tries again")
	}

	return answer, nil
}

// grant is what tokens give, for subject, with the scopes they say, or the
// ones before when they say none.
func (t tokens) grant(subject, email string, before []string, now time.Time) credentials.Grant {
	granted := before

	if t.Scope != nil {
		granted = strings.Fields(*t.Scope)
	}

	kept := credentials.Grant{
		Subject:      subject,
		Email:        email,
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(t.ExpiresIn) * time.Second).UTC(),
		Scopes:       granted,
		IDToken:      t.IDToken,
	}

	// The earliest renewal comes as seconds or as a date.
	var seconds int64
	var date time.Time

	switch {
	case json.Unmarshal(t.RefreshAfter, &seconds) == nil && seconds > 0:
		kept.RefreshAfter = time.Unix(seconds, 0).UTC()
	case json.Unmarshal(t.RefreshAfter, &date) == nil:
		kept.RefreshAfter = date.UTC()
	}

	return kept
}

// renew asks for new tokens with the refresh token of grant, which they
// replace. When they come with a proof of identity, it must be the same
// account's.
func (a *auth) renew(ctx context.Context, clientID string, grant credentials.Grant) (credentials.Grant, error) {
	answer, err := a.exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {grant.RefreshToken},
	})

	if err != nil {
		return grant, err
	}

	renewed := answer.grant(grant.Subject, grant.Email, grant.Scopes, time.Now())

	if renewed.IDToken == "" {
		renewed.IDToken = grant.IDToken
		return renewed, nil
	}

	who, err := a.verify(ctx, answer.IDToken, clientID, "")

	// Keys that cannot be fetched say nothing against the account, which a
	// refresh token cannot change.
	if errors.Is(err, errKeysUnavailable) {
		return renewed, nil
	}

	if err != nil || who.subject != grant.Subject {
		return grant, &oauthError{code: "invalid_grant", description: "the renewed sign-in is not of the same account"}
	}

	return renewed, nil
}

// revoke takes the refresh token back, so the sign-in cannot be renewed.
func (a *auth) revoke(ctx context.Context, clientID, refreshToken string) error {
	at, err := a.discover(ctx)

	if err != nil || at.Revocation == "" {
		return errors.New("there is nowhere to take the sign-in back")
	}

	form := url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {clientID}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, at.Revocation, strings.NewReader(form.Encode()))

	if err != nil {
		return err
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := a.client.Do(request)

	if err != nil {
		return err
	}

	response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return errors.New(response.Status)
	}

	return nil
}

// identity is who a proof of identity says the account is.
type identity struct {
	subject, email string
}

// errKeysUnavailable says the issuer's keys could not be fetched, which
// leaves a proof of identity unchecked rather than wrong.
var errKeysUnavailable = errors.New("ChatGPT's keys could not be fetched to check who signed in; try again shortly")

// verify checks a proof of identity: signed with one of the issuer's keys,
// by the issuer, for this client, not expired, and for this sign-in when a
// nonce is given.
func (a *auth) verify(ctx context.Context, token, clientID, nonce string) (identity, error) {
	invalid := errors.New("ChatGPT's proof of who signed in could not be verified; /login tries again")
	parts := strings.Split(token, ".")

	if len(parts) != 3 {
		return identity{}, invalid
	}

	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}

	if decodeSegment(parts[0], &header) != nil || header.Algorithm != "RS256" {
		return identity{}, invalid
	}

	key, err := a.key(ctx, header.KeyID)

	if err != nil {
		return identity{}, err
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])

	if err != nil {
		return identity{}, invalid
	}

	signed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))

	if rsa.VerifyPKCS1v15(key, crypto.SHA256, signed[:], signature) != nil {
		return identity{}, invalid
	}

	var claims struct {
		Issuer     string          `json:"iss"`
		Audience   json.RawMessage `json:"aud"`
		Authorized string          `json:"azp"`
		Expires    *int64          `json:"exp"`
		Issued     *int64          `json:"iat"`
		Subject    string          `json:"sub"`
		Nonce      string          `json:"nonce"`
		Email      string          `json:"email"`
	}

	if decodeSegment(parts[1], &claims) != nil {
		return identity{}, invalid
	}

	var audiences []string

	if json.Unmarshal(claims.Audience, &audiences) != nil {
		var single string
		_ = json.Unmarshal(claims.Audience, &single)
		audiences = []string{single}
	}

	const tolerance = 5

	switch {
	case claims.Issuer != a.issuer,
		!slices.Contains(audiences, clientID),
		len(audiences) > 1 && claims.Authorized != clientID,
		claims.Authorized != "" && claims.Authorized != clientID,
		claims.Expires == nil || time.Now().Unix() > *claims.Expires+tolerance,
		claims.Issued == nil,
		claims.Subject == "",
		nonce != "" && claims.Nonce != nonce:
		return identity{}, invalid
	}

	return identity{subject: claims.Subject, email: claims.Email}, nil
}

// key returns the issuer's public key with id, fetching its keys.
func (a *auth) key(ctx context.Context, id string) (*rsa.PublicKey, error) {
	at, err := a.discover(ctx)

	if err != nil {
		return nil, errKeysUnavailable
	}

	var set struct {
		Keys []struct {
			Type     string `json:"kty"`
			ID       string `json:"kid"`
			Modulus  string `json:"n"`
			Exponent string `json:"e"`
		} `json:"keys"`
	}

	if a.getJSON(ctx, at.Keys, &set) != nil {
		return nil, errKeysUnavailable
	}

	for _, key := range set.Keys {
		// A token that names no key can only be of a set with one.
		named := key.ID == id || id == "" && len(set.Keys) == 1

		if key.Type != "RSA" || !named {
			continue
		}

		modulus, errModulus := base64.RawURLEncoding.DecodeString(key.Modulus)
		exponent, errExponent := base64.RawURLEncoding.DecodeString(key.Exponent)

		if errModulus != nil || errExponent != nil || len(exponent) > 4 {
			continue
		}

		return &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(new(big.Int).SetBytes(exponent).Int64())}, nil
	}

	return nil, errors.New("ChatGPT's proof of who signed in names no key it has; /login tries again")
}

// decodeSegment reads one part of a token.
func decodeSegment(segment string, into any) error {
	raw, err := base64.RawURLEncoding.DecodeString(segment)

	if err != nil {
		return err
	}

	return json.Unmarshal(raw, into)
}

// getJSON reads a JSON document.
func (a *auth) getJSON(ctx context.Context, address string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)

	if err != nil {
		return err
	}

	request.Header.Set("Accept", "application/json")

	response, err := a.client.Do(request)

	if err != nil {
		return err
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return errors.New(response.Status)
	}

	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(into)
}

// openBrowser opens address in the person's browser, where there is one to
// open: on Linux, only in a graphical session, so a browser that would run
// in the terminal is never started.
func openBrowser(address string) error {
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
