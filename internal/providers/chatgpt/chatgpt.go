// Package chatgpt is the built-in provider that uses the person's ChatGPT
// plan: they sign in with ChatGPT, and requests go to OpenAI's Responses API
// with the tokens that gives, drawing on what the plan includes rather than
// on an API key. OpenAI offers this to open-source apps that run where the
// person does, under the conditions its preview sets: nothing stored on the
// server, the answer streamed, and no limit on the output.
package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/LiasCode/rumbo/internal/providers/credentials"
	"github.com/LiasCode/rumbo/internal/providers/openai"
	"github.com/LiasCode/rumbo/protocol"
)

// usageSettings is where the person reviews what the plan has used, and the
// limits they set for each app.
const usageSettings = "https://chatgpt.com/settings/usage"

// Config is what the provider accepts on initialize.
type Config struct {
	// ContextWindow is the size of the model's context, in tokens, which
	// the model list does not say.
	ContextWindow int64 `json:"context_window"`
	// Effort is the reasoning effort sent with every request that chooses
	// none as an option. Empty sends none.
	Effort string `json:"effort"`
}

// Handler implements protocol.Handler on the person's ChatGPT plan.
type Handler struct {
	config Config
	// store keeps the sign-in, which every request reads, so a renewal one
	// process of this provider made applies in the others at once.
	store     credentials.Store
	auth      *auth
	api       string
	client    *http.Client
	open      func(address string) error
	responses *openai.Responses
}

// New returns a handler that is ready after Initialize.
func New() *Handler {
	// A redirect could carry the conversation somewhere else.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	h := &Handler{
		store:  credentials.Default("chatgpt"),
		auth:   &auth{issuer: "https://auth.openai.com", client: client},
		api:    resource,
		client: client,
		open:   openBrowser,
	}

	h.responses = &openai.Responses{Post: h.post, Failure: failure, Unlimited: true}

	return h
}

// Initialize implements protocol.Handler.
func (h *Handler) Initialize(_ context.Context, params protocol.InitializeParams) (protocol.InitializeResult, error) {
	if len(params.Config) > 0 {
		if err := json.Unmarshal(params.Config, &h.config); err != nil {
			return protocol.InitializeResult{}, fmt.Errorf("invalid config: %w", err)
		}
	}

	config, _ := json.Marshal(h.config)

	return protocol.InitializeResult{Protocol: protocol.Version, Name: "chatgpt", Config: config, Description: description}, nil
}

// description is what this provider lets the person choose and ask.
var description = protocol.Description{
	Model: &protocol.Option{Type: protocol.OptionString, Values: "models", Description: "A model your ChatGPT plan can use"},
	Options: []protocol.Option{{
		Name:        "effort",
		Type:        protocol.OptionString,
		Description: "How much a reasoning model thinks, such as low, medium, and high, as the model names them. Empty, the effort in the provider's configuration applies, or else the model's own default.",
		Default:     json.RawMessage(`""`),
	}},
	Functions: []protocol.Function{
		{Name: "models", Description: "List the models your ChatGPT plan can use"},
		{Name: "login", Description: "Sign in with ChatGPT, so requests use your ChatGPT plan"},
		{
			Name:        "logout",
			Description: "Sign out of ChatGPT",
			Confirm:     "This signs rumbo out of ChatGPT on this computer, and requests stop using your plan until you sign in again.",
		},
		{Name: "account", Description: "Say which ChatGPT account requests use, and where to manage its usage"},
	},
}

// InvokeDialog implements protocol.DialogInvoker.
func (h *Handler) InvokeDialog(ctx context.Context, name string, _ json.RawMessage, dialog protocol.Dialog) (any, error) {
	switch name {
	case "models":
		return h.models(ctx)
	case "login":
		return h.login(ctx, dialog)
	case "logout":
		return h.logout(ctx)
	case "account":
		return h.account()
	}

	return nil, fmt.Errorf("unknown function %q", name)
}

// login signs in with ChatGPT in the person's browser. The browser comes
// back to a listener on this computer; one on another computer, as over
// ssh, ends at an address the person pastes instead.
func (h *Handler) login(ctx context.Context, dialog protocol.Dialog) (string, error) {
	at, err := h.auth.discover(ctx)

	if err != nil {
		return "", err
	}

	savedClient, hostID, err := h.store.Registration(resource)

	if err != nil {
		return "", err
	}

	// The host is this installation to OpenAI, the same on every sign-in.
	if hostID == "" {
		hostID = newHostID()

		if err := h.store.SetRegistration(resource, savedClient, hostID); err != nil {
			return "", err
		}
	}

	before, err := h.store.Grant(resource)

	if err != nil {
		return "", err
	}

	email, asked := "", savedClient

	if before != nil {
		email = before.Email
	}

	if asked == "" {
		asked = newClient
	}

	try := newAttempt()
	waiting, err := listen(try.state)

	if err != nil {
		return "", err
	}

	defer waiting.close()

	address := try.authorization(at, waiting.redirect, asked, hostID, email)
	dialog.Say("Continue with ChatGPT in your browser, at:\n" + address)

	if err := h.open(address); err != nil {
		dialog.Say("rumbo could not open a browser here; open that address in one")
	}

	answer, err := dialog.Ask(ctx, "Press Enter once the browser says to go back to rumbo. If it runs on another computer, paste here the address it ended at.", true)

	if err != nil {
		return "", err
	}

	back, found := pasted(answer, try.state)

	if !found {
		select {
		case back = <-waiting.result:
		case <-time.After(3 * time.Second):
			return "", errors.New("the browser did not come back to rumbo; /login tries again, and over ssh, paste the address the browser ended at")
		}
	}

	if back.err != nil {
		return "", back.err
	}

	clientID, err := issued(back, savedClient)

	if err != nil {
		return "", err
	}

	// A code that fails after OpenAI registered rumbo leaves the
	// registration, so trying again is not a new one.
	if err := h.store.SetRegistration(resource, clientID, hostID); err != nil {
		return "", err
	}

	answered, err := h.auth.exchange(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {back.code},
		"code_verifier": {try.verifier},
		"redirect_uri":  {waiting.redirect},
	})

	if err != nil {
		return "", err
	}

	if answered.IDToken == "" {
		return "", errors.New("ChatGPT did not say who signed in; /login tries again")
	}

	who, err := h.auth.verify(ctx, answered.IDToken, clientID, try.nonce)

	if err != nil {
		return "", err
	}

	grant := answered.grant(who.subject, who.email, nil, time.Now())

	// A sign-in that does not let rumbo use the plan is no use to it, and
	// is not kept.
	if !slices.Contains(grant.Scopes, planScope) {
		return "", errors.New("you signed in, but did not let rumbo use your ChatGPT plan, so nothing was kept; /login asks again")
	}

	if err := h.store.SetGrant(resource, &grant); err != nil {
		return "", err
	}

	text := "You're using your ChatGPT plan: eligible usage in rumbo uses your ChatGPT plan"

	if who.email != "" {
		text += ", signed in as " + who.email
	}

	if before != nil && before.Subject != who.subject {
		text += ", in place of the account signed in before"
	}

	return text + fmt.Sprintf(". Manage usage in your ChatGPT settings: %s. The sign-in is kept in %s, which only you can read.", usageSettings, h.store.Path), nil
}

// logout forgets the sign-in here, and asks OpenAI to take it back.
func (h *Handler) logout(ctx context.Context) (string, error) {
	grant, err := h.store.Grant(resource)

	if err != nil {
		return "", err
	}

	if grant == nil {
		return "rumbo is not signed in to ChatGPT", nil
	}

	clientID, _, err := h.store.Registration(resource)

	if err != nil {
		return "", err
	}

	if err := h.store.SetGrant(resource, nil); err != nil {
		return "", err
	}

	if h.auth.revoke(ctx, clientID, grant.RefreshToken) != nil {
		return "rumbo is signed out of ChatGPT here, but ChatGPT did not confirm it took the sign-in back; disconnect rumbo in your ChatGPT settings", nil
	}

	return "rumbo is signed out of ChatGPT", nil
}

// account says which account requests use, without showing its tokens.
func (h *Handler) account() (string, error) {
	grant, err := h.store.Grant(resource)

	if err != nil {
		return "", err
	}

	if grant == nil {
		return "not signed in to ChatGPT; /login signs in, so requests use your ChatGPT plan", nil
	}

	who := grant.Email

	if who == "" {
		who = "an account without an email"
	}

	return fmt.Sprintf("signed in to ChatGPT as %s; requests use your ChatGPT plan. Manage usage in your ChatGPT settings: %s", who, usageSettings), nil
}

// models lists what the plan can use, as ChatGPT lists them.
func (h *Handler) models(ctx context.Context) (protocol.ValuesResult, error) {
	var result protocol.ValuesResult

	response, err := h.send(ctx, http.MethodGet, "/models", nil)

	if err != nil {
		return result, err
	}

	defer response.Body.Close()

	var listed struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}

	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&listed); err != nil {
		return result, fmt.Errorf("invalid model list: %w", err)
	}

	for _, model := range listed.Models {
		if model.Visibility == "list" && model.Slug != "" {
			result.Values = append(result.Values, protocol.Value{Value: model.Slug, Label: model.DisplayName})
		}
	}

	return result, nil
}

// options are the values a request may carry, as described above.
type options struct {
	Effort string `json:"effort"`
}

// Complete implements protocol.Handler.
func (h *Handler) Complete(ctx context.Context, params protocol.CompleteParams, emit func(text string)) (protocol.CompleteResult, error) {
	// An option chosen for the request wins over the provider's configuration.
	var chosen options

	if len(params.Options) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(params.Options))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&chosen); err != nil {
			return protocol.CompleteResult{}, fmt.Errorf("invalid options: %w", err)
		}
	}

	// An empty effort is none chosen: the host sends the option's default,
	// "", with every request, which would otherwise hide the configured one.
	if chosen.Effort == "" {
		chosen.Effort = h.config.Effort
	}

	result, err := h.responses.Complete(ctx, params, chosen.Effort, emit)
	result.ContextWindow = h.config.ContextWindow

	return result, err
}

// post sends a request of the Responses API.
func (h *Handler) post(ctx context.Context, body []byte) (*http.Response, error) {
	return h.send(ctx, http.MethodPost, "/responses", body)
}

// send makes a request with the sign-in, renewed when it is about to
// expire, and once more if ChatGPT does not take it. It returns the
// successful response, whose body the caller closes.
func (h *Handler) send(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	token, err := h.token(ctx, "")

	if err != nil {
		return nil, err
	}

	response, err := h.sendWith(ctx, method, path, body, token)

	if status, ok := errors.AsType[*statusError](err); ok && status.code == http.StatusUnauthorized {
		if token, err = h.token(ctx, token); err != nil {
			return nil, err
		}

		response, err = h.sendWith(ctx, method, path, body, token)
	}

	return response, err
}

// statusError is a request ChatGPT answered with a failure.
type statusError struct {
	code   int
	reason string
	err    error
}

func (e *statusError) Error() string {
	return e.err.Error()
}

func (e *statusError) Unwrap() error {
	return e.err
}

func (h *Handler) sendWith(ctx context.Context, method, path string, body []byte, token string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, h.api+path, bytes.NewReader(body))

	if err != nil {
		return nil, err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")

	if path == "/responses" {
		request.Header.Set("Accept", "text/event-stream")
	}

	response, err := h.client.Do(request)

	// A request that never got an answer is worth sending again, unless it
	// was the host that cancelled it.
	if err != nil && ctx.Err() == nil {
		return nil, protocol.Transient(err)
	}

	if err != nil {
		return nil, err
	}

	if response.StatusCode == http.StatusOK {
		return response, nil
	}

	defer response.Body.Close()

	detail, _ := io.ReadAll(io.LimitReader(response.Body, 4000))
	code, message := said(detail)
	failed := failure(code, message)

	if code == "" {
		failed = fmt.Errorf("ChatGPT answered %s: %s", response.Status, message)
	}

	// What fails for the moment is worth trying again, but for a usage
	// limit, which is the person's to act on.
	status := response.StatusCode
	passing := status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500

	if passing && code != "subscription_sharing_usage_limit_exceeded" && !protocol.IsTransient(failed) && !protocol.IsOverflow(failed) {
		failed = protocol.Transient(failed)
	}

	return nil, &statusError{code: status, reason: code, err: failed}
}

// said reads the code and the message of a failure ChatGPT answered with,
// in either of the shapes it uses.
func said(body []byte) (code, message string) {
	var answer struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Param   string `json:"param"`
		} `json:"error"`
		Detail string `json:"detail"`
	}

	if json.Unmarshal(body, &answer) == nil {
		switch {
		case answer.Error != nil && answer.Error.Param != "":
			return answer.Error.Code, answer.Error.Message + " (" + answer.Error.Param + ")"
		case answer.Error != nil:
			return answer.Error.Code, answer.Error.Message
		case answer.Detail != "":
			return "", answer.Detail
		}
	}

	return "", strings.TrimSpace(string(body))
}

// failure is what a failure of the plan means: one the person has to act
// on, one worth trying again, or what it means to any Responses server.
func failure(code, message string) error {
	switch code {
	case "subscription_sharing_usage_limit_exceeded":
		return fmt.Errorf("usage limit reached: review your plan or rumbo's limit in your ChatGPT settings, %s", usageSettings)
	case "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
		return protocol.Transient(fmt.Errorf("ChatGPT could not check the plan's usage now: %s", message))
	case "subscription_sharing_user_not_eligible":
		return fmt.Errorf("your ChatGPT plan cannot be used here, for this account, workspace, or policy: %s", message)
	case "subscription_sharing_unsupported_capability":
		return fmt.Errorf("your ChatGPT plan does not take part of this request: %s", message)
	case "subscription_sharing_invalid_user":
		return fmt.Errorf("ChatGPT did not take the sign-in; /login signs in again: %s", message)
	}

	return openai.StreamFailure(code, message)
}

// errNotSignedIn says there is no sign-in to make requests with.
var errNotSignedIn = errors.New("rumbo is not signed in to ChatGPT; /login signs in, so requests use your ChatGPT plan")

// token returns the access token of the next request, renewing the sign-in
// first when it is about to expire, or when the token that failed is still
// the one stored. Only one process renews it at a time, and one that finds
// it renewed by another takes that.
func (h *Handler) token(ctx context.Context, failed string) (string, error) {
	grant, err := h.store.Grant(resource)

	if err != nil {
		return "", err
	}

	if grant == nil {
		return "", errNotSignedIn
	}

	fresh := func(grant *credentials.Grant) bool {
		return grant.AccessToken != failed && time.Until(grant.ExpiresAt) > time.Minute
	}

	if fresh(grant) {
		return grant.AccessToken, nil
	}

	var token string

	err = h.store.Locked(func() error {
		grant, err := h.store.Grant(resource)

		switch {
		case err != nil:
			return err
		case grant == nil:
			return errNotSignedIn
		case fresh(grant):
			token = grant.AccessToken
			return nil
		}

		// One the server does not take a renewal of yet is used while it
		// lasts.
		if time.Now().Before(grant.RefreshAfter) {
			if time.Now().Before(grant.ExpiresAt) && grant.AccessToken != failed {
				token = grant.AccessToken
				return nil
			}

			return protocol.Transient(errors.New("the ChatGPT sign-in cannot be renewed yet; trying again shortly"))
		}

		clientID, _, err := h.store.Registration(resource)

		if err != nil {
			return err
		}

		renewed, err := h.auth.renew(ctx, clientID, *grant)

		// A sign-in that cannot be renewed is over; anything else, such as
		// the network, leaves it to try again.
		if expired(err) {
			_ = h.store.SetGrant(resource, nil)
			return errors.New("the ChatGPT sign-in has expired or was taken back; /login signs in again")
		}

		if err != nil {
			return protocol.Transient(fmt.Errorf("the ChatGPT sign-in could not be renewed: %w", err))
		}

		token = renewed.AccessToken

		return h.store.SetGrant(resource, &renewed)
	})

	return token, err
}
