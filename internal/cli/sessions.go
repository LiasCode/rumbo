package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"rumbo/internal/agent"
	"rumbo/internal/ambient"
	"rumbo/internal/frontend"
	"rumbo/internal/frontends/render"
	"rumbo/internal/provider"
	"rumbo/internal/store"
	"rumbo/internal/tool"
	"rumbo/protocol"
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

// choice is the provider a session uses, its model, and the provider's
// options in force there.
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

	// What the run does not choose keeps the value in force in the session.
	if chosen.options, err = mergeOptions(chosen.options, params.Options); err != nil {
		return id, err
	}

	if params.Model != "" {
		chosen.model = params.Model
	}

	session, err := h.made(target, params.Objective)

	if err != nil {
		return session.ID, err
	}

	run.Attach(session.ID)

	worker := h.agent(pool, chosen, target.tools, run)
	worker.Inbox = run

	if params.Resume {
		return session.ID, worker.Resume(ctx, session)
	}

	return session.ID, worker.Run(ctx, session, params.Objective)
}

// agent builds the loop for one piece of work in a session.
func (h *host) agent(pool *provider.Pool, chosen choice, tools *tool.Set, observer agent.Observer) *agent.Agent {
	return &agent.Agent{
		Store:        h.database,
		Provider:     pool,
		Observer:     observer,
		Tools:        tools.Tools(),
		Collectors:   ambient.Defaults(),
		ProviderName: chosen.provider,
		Model:        chosen.model,
		Options:      chosen.options,
		MaxTokens:    h.settings.MaxTokens,
		MaxTurns:     h.settings.MaxTurns,
		Compaction:   h.settings.Compaction,
		RetryWaits:   agent.DefaultRetryWaits,
	}
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

	// The session keeps its provider from one start of rumbo to the next.
	if err := h.database.SetSessionModel(created.ID, target.provider, target.model); err != nil {
		return created, err
	}

	created.Provider, created.Model = target.provider, target.model
	target.session = &created
	h.places[created.ID] = target

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

// known returns the place of a session of the project, by id. A session
// this host has not worked in yet uses the provider stored with it, or
// that of the current one when it has none stored. The caller holds the
// lock.
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
		options, err := storedOptions(h.database, h.root, session.Provider)

		if err != nil {
			return nil, err
		}

		known.choice = choice{provider: session.Provider, model: session.Model, options: options}
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
