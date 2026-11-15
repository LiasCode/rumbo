package chatgpt

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/internal/providers/credentials"
	"github.com/LiasCode/rumbo/protocol"
)

// fake is OpenAI as the provider meets it: where accounts sign in, and the
// API the plan is used through. One server plays both.
type fake struct {
	t      *testing.T
	key    *rsa.PrivateKey
	server *httptest.Server

	mu sync.Mutex
	// authorized are the authorization requests the browser made.
	authorized []url.Values
	// pending are the codes given out, with what they were asked with.
	pending map[string]url.Values
	// refresh are the refresh tokens given out and not used yet.
	refresh map[string]bool
	// valid are the access tokens the API takes.
	valid   map[string]bool
	issued  int
	revoked []string
	// scope is what a sign-in grants; noPlan leaves out using the plan.
	noPlan bool
	// limited makes every request of the API go over the usage limit.
	limited bool
	// asked are the request bodies of the Responses API, and seen the
	// tokens they came with.
	asked []map[string]any
	seen  []string
}

const (
	clientID = "oaiapp_test"
	subject  = "user-1"
	email    = "person@example.com"
)

func newFake(t *testing.T) *fake {
	key, err := rsa.GenerateKey(rand.Reader, 2048)

	if err != nil {
		t.Fatal(err)
	}

	f := &fake{t: t, key: key, pending: map[string]url.Values{}, refresh: map[string]bool{}, valid: map[string]bool{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)

	return f
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 f.server.URL,
			"authorization_endpoint": f.server.URL + "/authorize",
			"token_endpoint":         f.server.URL + "/token",
			"jwks_uri":               f.server.URL + "/jwks",
			"revocation_endpoint":    f.server.URL + "/revoke",
		})

	case "/jwks":
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes()),
		}}})

	case "/authorize":
		query := r.URL.Query()
		f.authorized = append(f.authorized, query)
		code := fmt.Sprintf("code-%d", len(f.authorized))
		f.pending[code] = query

		back := url.Values{"code": {code}, "state": {query.Get("state")}}

		if query.Get("client_id") == newClient {
			back.Set("client_id", clientID)
		}

		http.Redirect(w, r, query.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)

	case "/token":
		r.ParseForm()
		f.token(w, r.PostForm)

	case "/revoke":
		r.ParseForm()
		f.revoked = append(f.revoked, r.PostForm.Get("token"))

	case "/v1/models":
		if !f.valid[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] {
			http.Error(w, `{"error":{"code":"subscription_sharing_invalid_user","message":"no"}}`, http.StatusUnauthorized)
			return
		}

		w.Write([]byte(`{"models":[{"slug":"gpt-a","display_name":"GPT A","visibility":"list"},{"slug":"gpt-hidden","display_name":"Hidden","visibility":"hide"}]}`))

	case "/v1/responses":
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.seen = append(f.seen, token)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.asked = append(f.asked, body)

		switch {
		case !f.valid[token]:
			http.Error(w, `{"error":{"code":"subscription_sharing_invalid_user","message":"not taken"}}`, http.StatusUnauthorized)
		case f.limited:
			http.Error(w, `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}`, http.StatusTooManyRequests)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n\n")
			fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":10,"output_tokens":1}}}`+"\n\n")
		}

	default:
		http.NotFound(w, r)
	}
}

// token answers the token endpoint: a code for tokens, once, and a refresh
// token for new ones, once.
func (f *fake) token(w http.ResponseWriter, form url.Values) {
	refuse := func(code string) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": code})
	}

	if form.Get("resource") != resource || form.Get("client_id") != clientID {
		refuse("invalid_client")
		return
	}

	nonce := ""

	switch form.Get("grant_type") {
	case "authorization_code":
		asked, found := f.pending[form.Get("code")]
		challenge := sha256.Sum256([]byte(form.Get("code_verifier")))

		if !found || base64.RawURLEncoding.EncodeToString(challenge[:]) != asked.Get("code_challenge") || form.Get("redirect_uri") != asked.Get("redirect_uri") {
			refuse("invalid_grant")
			return
		}

		delete(f.pending, form.Get("code"))
		nonce = asked.Get("nonce")

	case "refresh_token":
		if !f.refresh[form.Get("refresh_token")] {
			refuse("refresh_token_reused")
			return
		}

		delete(f.refresh, form.Get("refresh_token"))

	default:
		refuse("unsupported_grant_type")
		return
	}

	f.issued++
	access, refresh := fmt.Sprintf("access-%d", f.issued), fmt.Sprintf("refresh-%d", f.issued)
	f.valid[access], f.refresh[refresh] = true, true

	scope := "openid profile email offline_access resource.invoke " + planScope

	if f.noPlan {
		scope = "openid profile email offline_access"
	}

	json.NewEncoder(w).Encode(map[string]any{
		"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 3600, "scope": scope,
		"id_token": f.sign(map[string]any{"iss": f.server.URL, "aud": clientID, "sub": subject, "email": email, "nonce": nonce, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}),
	})
}

// sign makes a proof of identity with claims, signed with the fake's key.
func (f *fake) sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1"})
	payload, _ := json.Marshal(claims)
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signed))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])

	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// handler is a provider that signs in to the fake, with a browser that
// follows where it is sent, and keeps its credentials in a file of the
// test's own.
func (f *fake) handler(t *testing.T) *Handler {
	h := New()
	h.store = credentials.Store{Path: filepath.Join(t.TempDir(), "chatgpt.json")}
	h.auth = &auth{issuer: f.server.URL, client: h.client}
	h.api = f.server.URL + "/v1"
	h.open = browse

	return h
}

// browse is a browser that goes where it is sent, and where that sends it.
func browse(address string) error {
	response, err := http.Get(address)

	if err != nil {
		return err
	}

	io.Copy(io.Discard, response.Body)

	return response.Body.Close()
}

// person answers what a function asks with answer, and keeps what it says.
type person struct {
	said   []string
	answer func() string
}

func (p *person) Say(text string) {
	p.said = append(p.said, text)
}

func (p *person) Ask(context.Context, string, bool) (string, error) {
	if p.answer == nil {
		return "", nil
	}

	return p.answer(), nil
}

// Signing in registers rumbo once, keeps a grant to use the plan, and says
// so; signing in again asks for the same registration and account.
func TestSigningInKeepsAGrantToUseThePlan(t *testing.T) {
	f := newFake(t)
	h := f.handler(t)

	text, err := h.login(context.Background(), &person{})

	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(text, "You're using your ChatGPT plan") || !strings.Contains(text, email) || !strings.Contains(text, usageSettings) {
		t.Fatalf("login said %q", text)
	}

	grant, _ := h.store.Grant(resource)
	client, host, _ := h.store.Registration(resource)

	if grant == nil || grant.Subject != subject || grant.Email != email || client != clientID || !strings.HasPrefix(host, "urn:uuid:") {
		t.Fatalf("grant %+v, client %q, host %q", grant, client, host)
	}

	first := f.authorized[0]

	for name, want := range map[string]string{
		"client_id": newClient, "agent_name_hint": appName, "scope": scopes, "resource": resource,
		"response_type": "code", "code_challenge_method": "S256", "ext_agent_host_id": host,
	} {
		if first.Get(name) != want {
			t.Errorf("the first sign-in sent %s=%q, want %q", name, first.Get(name), want)
		}
	}

	if redirect, _ := url.Parse(first.Get("redirect_uri")); redirect.Hostname() != "127.0.0.1" || redirect.Path != callbackPath {
		t.Fatalf("redirect = %s", first.Get("redirect_uri"))
	}

	if _, err := h.login(context.Background(), &person{}); err != nil {
		t.Fatal(err)
	}

	again := f.authorized[1]

	if again.Get("client_id") != clientID || again.Has("agent_name_hint") || again.Get("login_hint") != email || again.Get("ext_agent_host_id") != host {
		t.Fatalf("signing in again sent %v", again)
	}
}

// A browser on another computer cannot come back to this one; the address
// it ended at, pasted, finishes the sign-in.
func TestAPastedAddressFinishesTheSignIn(t *testing.T) {
	f := newFake(t)
	h := f.handler(t)

	var sentTo string

	h.open = func(address string) error {
		sentTo = address
		return errors.New("no browser here")
	}

	answer := func() string {
		stay := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := stay.Get(sentTo)

		if err != nil {
			t.Fatal(err)
		}

		response.Body.Close()

		return response.Header.Get("Location")
	}

	asked := &person{answer: answer}

	if _, err := h.login(context.Background(), asked); err != nil {
		t.Fatal(err)
	}

	if grant, _ := h.store.Grant(resource); grant == nil {
		t.Fatal("the pasted address kept no grant")
	}

	if !strings.Contains(strings.Join(asked.said, "\n"), "could not open a browser") {
		t.Fatalf("said %q", asked.said)
	}

	// An address of another sign-in is refused.
	stranger := &person{answer: func() string { return "http://127.0.0.1:1/auth/callback?code=x&state=other" }}

	if _, err := h.login(context.Background(), stranger); err == nil || !strings.Contains(err.Error(), "not of this sign-in") {
		t.Fatalf("another sign-in's address: %v", err)
	}
}

// A sign-in that does not let rumbo use the plan is no use, and not kept.
func TestASignInWithoutThePlanIsNotKept(t *testing.T) {
	f := newFake(t)
	f.noPlan = true
	h := f.handler(t)

	if _, err := h.login(context.Background(), &person{}); err == nil || !strings.Contains(err.Error(), "did not let rumbo use your ChatGPT plan") {
		t.Fatalf("login: %v", err)
	}

	if grant, _ := h.store.Grant(resource); grant != nil {
		t.Fatalf("kept %+v", grant)
	}
}

// Requests go under the plan's conditions, renewing the sign-in when it is
// about to expire, and once more when ChatGPT does not take it.
func TestRequestsUseThePlanAndRenewTheSignIn(t *testing.T) {
	f := newFake(t)
	h := f.handler(t)
	h.login(context.Background(), &person{})

	params := protocol.CompleteParams{
		Model: "gpt-a", System: "be brief", MaxTokens: 4096,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockText, Text: "hello"}}}},
	}

	// About to expire, it is renewed before the request.
	grant, _ := h.store.Grant(resource)
	grant.ExpiresAt = time.Now().Add(30 * time.Second)
	h.store.SetGrant(resource, grant)

	result, err := h.Complete(context.Background(), params, func(string) {})

	if err != nil || len(result.Message.Content) != 1 || result.Message.Content[0].Text != "Hi" {
		t.Fatalf("result %+v, %v", result, err)
	}

	renewed, _ := h.store.Grant(resource)

	if renewed.AccessToken == grant.AccessToken || renewed.RefreshToken == grant.RefreshToken || f.seen[0] != renewed.AccessToken {
		t.Fatalf("not renewed: %+v, sent %v", renewed, f.seen)
	}

	body := f.asked[0]

	if body["store"] != false || body["stream"] != true || body["instructions"] != "be brief" || body["max_output_tokens"] != nil {
		t.Fatalf("the request = %v", body)
	}

	// Taken back on the server, the token is renewed and the request sent
	// again.
	f.mu.Lock()
	delete(f.valid, renewed.AccessToken)
	f.mu.Unlock()

	if _, err := h.Complete(context.Background(), params, func(string) {}); err != nil {
		t.Fatal(err)
	}

	if len(f.seen) != 3 || f.seen[1] != renewed.AccessToken || f.seen[2] == renewed.AccessToken {
		t.Fatalf("sent with %v", f.seen)
	}
}

// The configured effort applies unless the request chooses one. The host
// sends the option's default, empty, with every request: that is none
// chosen.
func TestTheConfiguredEffortApplies(t *testing.T) {
	f := newFake(t)
	h := f.handler(t)

	if _, err := h.Initialize(context.Background(), protocol.InitializeParams{Config: json.RawMessage(`{"effort":"low"}`)}); err != nil {
		t.Fatal(err)
	}

	h.login(context.Background(), &person{})

	params := protocol.CompleteParams{
		Model:    "gpt-a",
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockText, Text: "hello"}}}},
	}

	for index, want := range []struct{ options, sent string }{
		{``, "low"},
		{`{"effort":""}`, "low"},
		{`{"effort":"high"}`, "high"},
	} {
		params.Options = json.RawMessage(want.options)

		if _, err := h.Complete(context.Background(), params, func(string) {}); err != nil {
			t.Fatal(err)
		}

		if reasoning, _ := f.asked[index]["reasoning"].(map[string]any); reasoning["effort"] != want.sent {
			t.Errorf("options %s: reasoning = %v, want effort %s", want.options, f.asked[index]["reasoning"], want.sent)
		}
	}
}

// A usage limit is the person's to act on: it says where, and is not tried
// again.
func TestAUsageLimitSaysWhereToManageIt(t *testing.T) {
	f := newFake(t)
	h := f.handler(t)
	h.login(context.Background(), &person{})
	f.limited = true

	_, err := h.Complete(context.Background(), protocol.CompleteParams{Model: "gpt-a"}, func(string) {})

	if err == nil || !strings.Contains(err.Error(), "usage limit reached") || !strings.Contains(err.Error(), usageSettings) || protocol.IsTransient(err) {
		t.Fatalf("over the limit: %v, transient %v", err, protocol.IsTransient(err))
	}

	if len(f.asked) != 1 {
		t.Fatalf("sent %d times", len(f.asked))
	}
}

// A sign-in that cannot be renewed is over: it is forgotten, and the error
// says to sign in again.
func TestAnExpiredSignInAsksToSignInAgain(t *testing.T) {
	f := newFake(t)
	h := f.handler(t)
	h.login(context.Background(), &person{})

	grant, _ := h.store.Grant(resource)
	grant.ExpiresAt, grant.RefreshToken = time.Now(), "refresh-used"
	h.store.SetGrant(resource, grant)

	_, err := h.Complete(context.Background(), protocol.CompleteParams{Model: "gpt-a"}, func(string) {})

	if err == nil || !strings.Contains(err.Error(), "/login signs in again") || protocol.IsTransient(err) {
		t.Fatalf("expired: %v", err)
	}

	if kept, _ := h.store.Grant(resource); kept != nil {
		t.Fatalf("still kept: %+v", kept)
	}

	if _, err := h.Complete(context.Background(), protocol.CompleteParams{Model: "gpt-a"}, func(string) {}); !errors.Is(err, errNotSignedIn) {
		t.Fatalf("without a sign-in: %v", err)
	}
}

// The models are those ChatGPT lists for the plan, with their names.
func TestTheModelsAreThoseThePlanLists(t *testing.T) {
	f := newFake(t)
	h := f.handler(t)
	h.login(context.Background(), &person{})

	listed, err := h.models(context.Background())

	if err != nil || len(listed.Values) != 1 || listed.Values[0].Value != "gpt-a" || listed.Values[0].Label != "GPT A" {
		t.Fatalf("models = %+v, %v", listed.Values, err)
	}
}

// Signing out forgets the sign-in here and takes it back on the server,
// and keeps the registration.
func TestSigningOutTakesTheSignInBack(t *testing.T) {
	f := newFake(t)
	h := f.handler(t)
	h.login(context.Background(), &person{})

	grant, _ := h.store.Grant(resource)

	if text, err := h.logout(context.Background()); err != nil || text != "rumbo is signed out of ChatGPT" {
		t.Fatalf("logout = %q, %v", text, err)
	}

	if len(f.revoked) != 1 || f.revoked[0] != grant.RefreshToken {
		t.Fatalf("revoked %v", f.revoked)
	}

	if client, _, _ := h.store.Registration(resource); client != clientID {
		t.Fatalf("the registration went with it: %q", client)
	}

	if text, _ := h.account(); !strings.HasPrefix(text, "not signed in") {
		t.Fatalf("account = %q", text)
	}

	if text, _ := h.logout(context.Background()); text != "rumbo is not signed in to ChatGPT" {
		t.Fatalf("logout again = %q", text)
	}
}

// A proof of identity is taken only signed by the issuer's key, by the
// issuer, for this client and sign-in, and before it expires.
func TestAProofOfIdentityIsVerified(t *testing.T) {
	f := newFake(t)
	a := &auth{issuer: f.server.URL, client: http.DefaultClient}
	claims := func(change func(map[string]any)) string {
		base := map[string]any{"iss": f.server.URL, "aud": clientID, "sub": subject, "nonce": "n", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
		change(base)

		return f.sign(base)
	}

	if who, err := a.verify(context.Background(), claims(func(map[string]any) {}), clientID, "n"); err != nil || who.subject != subject {
		t.Fatalf("a good proof: %+v, %v", who, err)
	}

	for name, token := range map[string]string{
		"another issuer":   claims(func(c map[string]any) { c["iss"] = "https://elsewhere" }),
		"another client":   claims(func(c map[string]any) { c["aud"] = "oaiapp_other" }),
		"another sign-in":  claims(func(c map[string]any) { c["nonce"] = "m" }),
		"expired":          claims(func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }),
		"no subject":       claims(func(c map[string]any) { delete(c, "sub") }),
		"several audience": claims(func(c map[string]any) { c["aud"] = []string{clientID, "other"} }),
		"another key":      strings.Join(strings.Split(claims(func(map[string]any) {}), ".")[:2], ".") + ".c2lnbmF0dXJl",
	} {
		if _, err := a.verify(context.Background(), token, clientID, "n"); err == nil {
			t.Errorf("%s was taken", name)
		}
	}
}

// The host names this installation without saying anything of it.
func TestAHostIDIsAnOpaqueUUID(t *testing.T) {
	id := newHostID()

	if len(id) != len("urn:uuid:")+36 || !strings.HasPrefix(id, "urn:uuid:") || id[9+14] != '4' || newHostID() == id {
		t.Fatalf("host id = %q", id)
	}
}
