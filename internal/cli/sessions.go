package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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

// place is a session as this host knows it: the provider it uses, and what
// its tools have seen. A place without a session is where a run goes that
// starts one.
type place struct {
	choice
	session *store.Session
	// dir is where the session works, or will work once it is made.
	dir   string
	tools *tool.Set
	// unlock releases the session while this host holds it.
	unlock func()
}

// choice is the provider a session uses, its model, and the values of the
// provider's options chosen in it. What a run gets is worked out when it
// starts, with the values stored for the provider under these.
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

// id is the place's session id, empty until the session is made.
func (p *place) id() string {
	if p.session == nil {
		return ""
	}

	return p.session.ID
}

// name says which session a place is, for messages.
func (p *place) name() string {
	if p.session == nil {
		return "the new session"
	}

	return "session " + p.session.ID
}

// release gives up the session, if this host holds it.
func (p *place) release() {
	if p.unlock != nil {
		p.unlock()
		p.unlock = nil
	}
}

// accept implements frontend.Handlers.Run. It decides where the run goes and
// whether it may start, in the order run requests arrive.
func (h *host) accept(params protocol.RunParams, run *frontend.Run) (frontend.Work, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	target, err := h.target(params)

	if err != nil {
		return nil, err
	}

	switch {
	case h.single && len(h.busy) > 0:
		return nil, errors.New("a run is already in progress")

	case h.busy[target] != "":
		return nil, fmt.Errorf("%s is busy with %s; cancel it, or tell it what you want with steer", target.name(), h.busy[target])

	case params.Resume && target.session == nil:
		return nil, errors.New("there is no session to resume")
	}

	if err := h.hold(target); err != nil {
		return nil, err
	}

	h.busy[target] = busyRun

	return func(ctx context.Context) (string, error) {
		defer h.finish(target)

		return h.work(ctx, target, params, run)
	}, nil
}

// target is the place a run goes to: the session it names, a new one, or
// the current one. A host that works in one session at a time only takes
// the current one.
func (h *host) target(params protocol.RunParams) (*place, error) {
	named := params.Session != "" && params.Session != h.current.id()

	switch {
	case (params.New || named) && h.single:
		return nil, errors.New("this host works in one session at a time (-single); change the current session with open or new first")

	case params.New:
		return h.fresh(h.current), nil

	case named:
		return h.known(params.Session)
	}

	return h.current, nil
}

// work runs the agent for an accepted run. The provider starts before a new
// session is made for it, so a provider that cannot start leaves nothing
// behind.
func (h *host) work(ctx context.Context, target *place, params protocol.RunParams, run *frontend.Run) (string, error) {
	h.mu.Lock()
	chosen, id := target.choice, target.id()
	h.mu.Unlock()

	pool, err := h.pool(chosen.provider)

	if err != nil {
		return id, err
	}

	if err := pool.Ready(ctx); err != nil {
		return id, err
	}

	if params.Model != "" {
		chosen.model = params.Model
	}

	// What the run does not choose keeps the value in force in the session.
	worker, err := h.agent(pool, chosen, params.Options, target.tools, run)

	if err != nil {
		return id, err
	}

	session, err := h.made(target, params.Objective)

	if err != nil {
		return session.ID, err
	}

	run.Attach(session.ID)
	worker.Inbox = run

	if params.Resume {
		return session.ID, worker.Resume(ctx, session)
	}

	return session.ID, worker.Run(ctx, session, params.Objective)
}

// agent builds the loop for one piece of work in a session, with the
// settings as they stand when it starts: a change to them applies to the
// work that starts after it. extra are option values the work chose for
// itself, over the others.
func (h *host) agent(pool *provider.Pool, chosen choice, extra json.RawMessage, tools *tool.Set, observer agent.Observer) (*agent.Agent, error) {
	settings, err := loadLimits(h.database)

	if err != nil {
		return nil, err
	}

	options, err := h.inForce(chosen)

	if err != nil {
		return nil, err
	}

	if options, err = mergeOptions(options, extra); err != nil {
		return nil, err
	}

	return &agent.Agent{
		Store:        h.database,
		Provider:     pool,
		Observer:     observer,
		Tools:        tools.Tools(),
		Collectors:   ambient.Defaults(),
		ProviderName: chosen.provider,
		Model:        chosen.model,
		Options:      options,
		MaxTokens:    settings.MaxTokens,
		MaxTurns:     settings.MaxTurns,
		Compaction:   settings.Compaction,
		RetryWaits:   agent.DefaultRetryWaits,
	}, nil
}

// inForce returns the option values a run with chosen gets, as the
// settings stand now.
func (h *host) inForce(chosen choice) (json.RawMessage, error) {
	return optionsInForce(h.database, chosen.provider, chosen.options)
}

// keep stores what a place chose in its session, which keeps it from one
// start of rumbo to the next. The caller holds the session.
func (h *host) keep(target *place) error {
	session := target.session

	if err := h.database.SetSessionModel(session.ID, target.provider, target.model); err != nil {
		return err
	}

	if err := h.database.SetSessionOptions(session.ID, target.options); err != nil {
		return err
	}

	session.Provider, session.Model, session.Options = target.provider, target.model, target.options

	return nil
}

// made returns the place's session, making it first when the place has
// none.
func (h *host) made(target *place, objective string) (store.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if target.session != nil {
		return *target.session, nil
	}

	created, err := h.database.CreateSession(target.dir, h.root, render.FirstLine(objective))

	if err != nil {
		return created, err
	}

	target.session = &created
	h.places[created.ID] = target

	// The session keeps its provider, and what was chosen for it, from one
	// start of rumbo to the next.
	if err := h.keep(target); err != nil {
		return created, err
	}

	return created, h.hold(target)
}

// finish marks the end of what worked in a place.
func (h *host) finish(target *place) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.busy, target)
	h.letGo(target)
	h.prune()
}

// history tells a front end what a session holds: the current session's
// unless the params name another of the project.
func (h *host) history(params protocol.HistoryParams) (protocol.HistoryResult, error) {
	told := protocol.HistoryResult{Events: []protocol.Event{}}

	h.mu.Lock()
	target, err := h.chosen(map[string]string{"session": params.Session})

	var session store.Session

	if err == nil && target.session != nil {
		session = *target.session
	}

	h.mu.Unlock()

	// A new session holds nothing until its first run makes it.
	if err != nil || session.ID == "" {
		return told, err
	}

	entries, err := h.database.Entries(session.ID)

	if err != nil {
		return told, err
	}

	events, err := transcript.History(entries)

	if err != nil {
		return told, err
	}

	told.Session = session.ID

	for _, event := range events {
		event.Session = session.ID
		told.Events = append(told.Events, event)
	}

	return told, nil
}

// known returns the place of a session of the project, by id. A session
// this host has not worked in yet uses the provider and the options stored
// with it, or the provider of the current one when it has none stored. The
// caller holds the lock.
func (h *host) known(id string) (*place, error) {
	if found, ok := h.places[id]; ok {
		return found, nil
	}

	session, err := h.database.Session(id)

	if err != nil {
		return nil, fmt.Errorf("session %s: %w", id, err)
	}

	// Settings were read for this project, so the session has to be one of
	// its own.
	if session.Project != h.root {
		return nil, fmt.Errorf("session %s belongs to %s; start rumbo there to work in it", session.ID, session.Project)
	}

	known := h.fresh(h.current)
	known.session, known.dir = &session, session.Dir

	if session.Provider != "" {
		known.choice = choice{provider: session.Provider, model: session.Model, options: session.Options}
	}

	h.places[id] = known

	return known, nil
}

// fresh returns a place for a session still to be made, where from works and
// with its provider.
func (h *host) fresh(from *place) *place {
	return &place{choice: from.choice, dir: from.dir, tools: h.tools.Separate()}
}

// hold takes the place's session for this process, if it has one this host
// does not hold yet. The caller holds the lock.
func (h *host) hold(target *place) error {
	if target.session == nil || target.unlock != nil {
		return nil
	}

	unlock, err := h.database.LockSession(target.session.ID)

	if err != nil {
		return fmt.Errorf("session %s: %w", target.session.ID, err)
	}

	target.unlock = unlock

	return nil
}

// letGo releases a session once it is neither current nor busy, so another
// rumbo process can work in it. The caller holds the lock.
func (h *host) letGo(target *place) {
	if target != h.current && h.busy[target] == "" {
		target.release()
	}
}

// working reports whether anything works in any session.
func (h *host) working() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.busy) > 0
}

// uses reports whether the current session, or one that works, uses the
// provider called name. The caller holds the lock.
func (h *host) uses(name string) bool {
	if h.current.provider == name {
		return true
	}

	for target := range h.busy {
		if target.provider == name {
			return true
		}
	}

	return false
}

// pool returns the processes of the provider called name, which start as
// requests need them.
func (h *host) pool(name string) (*provider.Pool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if pool, found := h.pools[name]; found {
		return pool, nil
	}

	program, err := h.database.Program(store.KindProvider, name)

	if err != nil {
		return nil, fmt.Errorf("provider %q is not registered", name)
	}

	command, err := programCommand(program.Command)

	if err != nil {
		return nil, err
	}

	pool := provider.NewPool(command, program.Config, h.conn.Log())
	h.pools[name] = pool

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

// prune stops the processes of the providers that no session uses now. The
// caller holds the lock.
func (h *host) prune() {
	for name := range h.pools {
		if !h.uses(name) {
			h.drop(name)
		}
	}
}
