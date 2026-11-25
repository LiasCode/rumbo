package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/LiasCode/rumbo/internal/agent"
	"github.com/LiasCode/rumbo/internal/ambient"
	"github.com/LiasCode/rumbo/internal/frontend"
	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/internal/provider"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/tool"
	"github.com/LiasCode/rumbo/internal/transcript"
	"github.com/LiasCode/rumbo/protocol"
)

// choice is the provider a session uses, its model, and the values of the
// provider's options chosen in it. What a run gets is worked out when it
// starts, with the values in the provider's configuration under these.
type choice struct {
	provider string
	model    string
	options  json.RawMessage
}

// What can keep a session busy.
const (
	busyRun     = "a run"
	busyCompact = "a compaction"
)

// id is the session's id, empty until the session is made. The caller
// holds the lock.
func (h *host) id() string {
	if h.session == nil {
		return ""
	}

	return h.session.ID
}

// name says which session the host's is, for messages. The caller holds
// the lock.
func (h *host) name() string {
	if h.session == nil {
		return "the new session"
	}

	return "session " + h.session.ID
}

// accept implements frontend.Handlers.Run. It decides whether the run may
// start, in the order run requests arrive: the session has one at a time.
func (h *host) accept(params protocol.RunParams, run *frontend.Run) (frontend.Work, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch {
	case h.busy != "":
		return nil, fmt.Errorf("%s is busy with %s; cancel it, or tell it what you want with steer", h.name(), h.busy)

	case params.Resume && h.session == nil:
		return nil, errors.New("there is no session to resume")

	case h.model == "" && params.Model == "":
		return nil, fmt.Errorf("no model chosen for %[1]s: choose one for this session, or set the model %[1]s starts with", h.provider)
	}

	if err := h.hold(); err != nil {
		return nil, err
	}

	h.busy = busyRun

	return func(ctx context.Context) (string, error) {
		defer h.finish()

		return h.work(ctx, params, run)
	}, nil
}

// work runs the agent for an accepted run. The provider starts before a new
// session is made for it, so a provider that cannot start leaves nothing
// behind.
func (h *host) work(ctx context.Context, params protocol.RunParams, run *frontend.Run) (string, error) {
	h.mu.Lock()
	chosen, id := h.choice, h.id()
	h.mu.Unlock()

	pool, err := h.ready(ctx, chosen.provider)

	if err != nil {
		return id, err
	}

	if params.Model != "" {
		chosen.model = params.Model
	}

	options, err := h.inForce(chosen, pool.Description())

	if err != nil {
		return id, err
	}

	// What the run does not choose keeps the value in force in the session.
	if options, err = mergeOptions(options, params.Options); err != nil {
		return id, err
	}

	worker, err := h.agent(ctx, pool, chosen, options, run)

	if err != nil {
		return id, err
	}

	title := params.Objective

	// A task of images alone is named by them.
	if strings.TrimSpace(title) == "" && len(params.Images) > 0 {
		title = "[image]"
	}

	session, err := h.made(title)

	if err != nil {
		return session.ID, err
	}

	run.Attach(session.ID)
	worker.Inbox = run

	// A tool reaches the person who follows the run through it.
	ctx = tool.WithPerson(ctx, run)

	if params.Resume {
		return session.ID, worker.Resume(ctx, session)
	}

	return session.ID, worker.Run(ctx, session, params.Objective, params.Images...)
}

// agent builds the loop for one piece of work in the session, with the
// settings as they stand when it starts: a change to them applies to the
// work that starts after it. options are the option values the work gets
// with every request.
func (h *host) agent(ctx context.Context, pool *provider.Pool, chosen choice, options json.RawMessage, observer agent.Observer) (*agent.Agent, error) {
	settings, err := loadLimits(h.database)

	if err != nil {
		return nil, err
	}

	declared := h.tools.Tools()

	if settings.Computer {
		declared = append(declared, h.tools.Computer())
	}

	extensions, err := extensionPrograms(h.database)

	if err != nil {
		return nil, err
	}

	// An extension's tool can call the tools of rumbo's own the run has,
	// the ones it replaces among them.
	rumbo := reserved()
	rumbo.Own = slices.Clone(declared)

	// skills are where the user's plugins keep theirs.
	var skills []string

	offers, err := h.extensions.Start(ctx, extensions, rumbo)

	if err == nil {
		skills, err = skillPlaces(h.database)
	}

	if err != nil {
		return nil, fmt.Errorf(`%w; "rumbo extension rm <name>" takes one out`, err)
	}

	// A tool of rumbo's that an extension replaces is not declared. Theirs
	// come after rumbo's own, so one that is added or removed leaves the
	// start of the declarations as it was.
	declared = slices.DeleteFunc(declared, func(own tool.Tool) bool { return slices.Contains(offers.Replaced, own.Def().Name) })
	declared = append(declared, offers.Tools...)

	return &agent.Agent{
		Store:        h.database,
		Provider:     pool,
		Observer:     observer,
		Tools:        declared,
		Collectors:   append(ambient.Defaults(skills...), offers.Collectors...),
		ProviderName: chosen.provider,
		Model:        chosen.model,
		Options:      options,
		MaxTokens:    settings.MaxTokens,
		Version:      release(),
		MaxTurns:     settings.MaxTurns,
		Compaction:   settings.Compaction,
		RetryWaits:   agent.DefaultRetryWaits,
	}, nil
}

// inForce returns the option values a run with chosen gets, as its
// provider's configuration stands now. described is what the provider says
// it offers, which is what tells its options from the rest of its
// configuration.
func (h *host) inForce(chosen choice, described protocol.Description) (json.RawMessage, error) {
	program, err := findProgram(h.database, store.KindProvider, chosen.provider)

	if err != nil {
		return nil, err
	}

	return optionsInForce(program.Config, described.Options, chosen.options)
}

// keep stores what the session chose in it, which keeps it from one start
// of rumbo to the next. The caller holds the session.
func (h *host) keep() error {
	session := h.session

	if err := h.database.SetSessionModel(session.ID, h.provider, h.model); err != nil {
		return err
	}

	if err := h.database.SetSessionOptions(session.ID, h.options); err != nil {
		return err
	}

	session.Provider, session.Model, session.Options = h.provider, h.model, h.options

	return nil
}

// made returns the host's session, making it first when its first run has
// not yet.
func (h *host) made(objective string) (store.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.session != nil {
		return *h.session, nil
	}

	created, err := h.database.CreateSession(h.dir, render.FirstLine(objective))

	if err != nil {
		return created, err
	}

	h.session = &created

	// The session keeps its provider, and what was chosen for it, from one
	// start of rumbo to the next.
	if err := h.keep(); err != nil {
		return created, err
	}

	return created, h.hold()
}

// finish marks the end of what worked in the session.
func (h *host) finish() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.busy = ""
	h.prune()
}

// history tells a front end what the session holds.
func (h *host) history() (protocol.HistoryResult, error) {
	told := protocol.HistoryResult{Events: []protocol.Event{}}

	h.mu.Lock()
	id := h.id()
	h.mu.Unlock()

	// A new session holds nothing until its first run makes it.
	if id == "" {
		return told, nil
	}

	entries, err := h.database.Entries(id)

	if err != nil {
		return told, err
	}

	events, err := transcript.History(entries)

	if err != nil {
		return told, err
	}

	told.Session = id

	for _, event := range events {
		event.Session = id
		told.Events = append(told.Events, event)
	}

	return told, nil
}

// recallLimit bounds how much of what the person asked a front end is told.
const recallLimit = 1000

// recall tells a front end what the person asked in the sessions started
// in the host's directory, most recent first and each once, so it can bring
// it back. The same words given with other images are one text to bring
// back.
func (h *host) recall() (protocol.RecallResult, error) {
	recalled := protocol.RecallResult{Asked: []string{}}
	entries, err := h.database.Recent(h.dir, []string{transcript.KindUser, transcript.KindSteer}, recallLimit)

	if err != nil {
		return recalled, err
	}

	told := map[string]bool{}

	for _, entry := range entries {
		var text transcript.Text

		if json.Unmarshal(entry.Payload, &text) == nil && text.Text != "" && !told[text.Text] {
			told[text.Text] = true
			recalled.Asked = append(recalled.Asked, text.Text)
		}
	}

	return recalled, nil
}

// hold takes the session for this process, if it is made and this host does
// not hold it yet, and keeps it until the host ends. The caller holds the
// lock.
func (h *host) hold() error {
	if h.session == nil || h.unlock != nil {
		return nil
	}

	// Another rumbo process may have deleted the session since this one
	// read it, and nothing should be recorded in it then.
	if _, err := h.database.Session(h.session.ID); errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("session %s was deleted by another rumbo process; start rumbo again to work in a new one", h.session.ID)
	} else if err != nil {
		return err
	}

	unlock, err := h.database.LockSession(h.session.ID)

	if err != nil {
		return fmt.Errorf("session %s: %w", h.session.ID, err)
	}

	h.unlock = unlock

	return nil
}

// pool returns the processes of the provider called name, which start as
// requests need them.
func (h *host) pool(name string) (*provider.Pool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if pool, found := h.pools[name]; found {
		return pool, nil
	}

	program, err := findProgram(h.database, store.KindProvider, name)

	if err != nil {
		return nil, err
	}

	command, err := programCommand(program.Command)

	if err != nil {
		return nil, err
	}

	pool := provider.NewPool(command, program.Config, h.conn.Log())
	h.pools[name] = pool

	return pool, nil
}

// ready returns the processes of the provider called name with one of them
// started, so a provider that cannot start fails before any work depends on
// it, and so does one given a field it has no setting of.
func (h *host) ready(ctx context.Context, name string) (*provider.Pool, error) {
	pool, err := h.pool(name)

	if err != nil {
		return nil, err
	}

	if err := pool.Ready(ctx); err != nil {
		return nil, err
	}

	known := &settingsOf{offers: pool.Description(), inForce: objectFields(pool.Config())}

	if err := checkChosen(h.database, name, known); err != nil {
		return nil, err
	}

	return pool, nil
}

// drop stops the processes of the provider called name, which the next
// request starts again, and reports whether there were any. Those busy
// with a request stop when it ends. The caller holds the lock.
func (h *host) drop(name string) bool {
	pool, found := h.pools[name]

	if found {
		_ = pool.Close()
		delete(h.pools, name)
	}

	return found
}

// prune stops the processes of the providers the session does not use
// now. The caller holds the lock.
func (h *host) prune() {
	for name := range h.pools {
		if name != h.provider {
			h.drop(name)
		}
	}
}
