package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/LiasCode/rumbo/protocol"
)

// Pool is a Provider made of provider processes, started as requests need
// them. A process answers one request at a time, so each request takes one
// that no other request is using, or starts one. Any process can answer any
// request, since every request carries the whole conversation.
type Pool struct {
	start func(ctx context.Context) (pooled, error)

	mu     sync.Mutex
	idle   []pooled
	closed bool
	// description is what the first process said about the provider.
	description protocol.Description
	described   bool
}

// pooled is what a pool keeps: a provider process that can say it has
// exited, so it is not handed out again.
type pooled interface {
	Provider
	Exited() bool
}

// NewPool returns a pool of processes started from argv with config, their
// stderr going to stderr. None starts until a request needs it.
func NewPool(argv []string, config json.RawMessage, stderr io.Writer) *Pool {
	return &Pool{
		start: func(ctx context.Context) (pooled, error) {
			return Start(ctx, argv, config, stderr)
		},
	}
}

// Complete implements Provider.
func (p *Pool) Complete(ctx context.Context, params protocol.CompleteParams, onDelta func(text string)) (protocol.CompleteResult, error) {
	process, err := p.take(ctx)

	if err != nil {
		return protocol.CompleteResult{}, err
	}

	defer p.give(process)

	return process.Complete(ctx, params, onDelta)
}

// Invoke implements Provider.
func (p *Pool) Invoke(ctx context.Context, name string, params json.RawMessage, dialog protocol.Dialog) (json.RawMessage, error) {
	process, err := p.take(ctx)

	if err != nil {
		return nil, err
	}

	defer p.give(process)

	return process.Invoke(ctx, name, params, dialog)
}

// Description implements Provider. It is empty until a process has
// started, which Ready makes sure of.
func (p *Pool) Description() protocol.Description {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.description
}

// Ready starts a process if none is idle, so a provider that cannot start
// fails before any work depends on it.
func (p *Pool) Ready(ctx context.Context) error {
	process, err := p.take(ctx)

	if err != nil {
		return err
	}

	p.give(process)

	return nil
}

// Close stops the idle processes now, and the busy ones when their request
// ends.
func (p *Pool) Close() error {
	p.mu.Lock()
	idle := p.idle
	p.idle, p.closed = nil, true
	p.mu.Unlock()

	for _, process := range idle {
		_ = process.Close()
	}

	return nil
}

// take hands out an idle process, or starts one when none is idle.
func (p *Pool) take(ctx context.Context) (pooled, error) {
	p.mu.Lock()

	for len(p.idle) > 0 && !p.closed {
		last := len(p.idle) - 1
		process := p.idle[last]
		p.idle = p.idle[:last]

		// One that died while idle is dropped, and the next one is tried.
		if !process.Exited() {
			p.mu.Unlock()
			return process, nil
		}

		_ = process.Close()
	}

	closed := p.closed
	p.mu.Unlock()

	if closed {
		return nil, errors.New("the provider was stopped")
	}

	process, err := p.start(ctx)

	if err != nil {
		return nil, err
	}

	p.mu.Lock()

	if !p.described {
		p.description, p.described = process.Description(), true
	}

	p.mu.Unlock()

	return process, nil
}

// give takes a process back once its request ends. One that exited, or
// whose pool was closed meanwhile, is stopped instead of kept.
func (p *Pool) give(process pooled) {
	p.mu.Lock()
	keep := !p.closed && !process.Exited()

	if keep {
		p.idle = append(p.idle, process)
	}

	p.mu.Unlock()

	if !keep {
		_ = process.Close()
	}
}
