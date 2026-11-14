# rumbo: agent guide

Read [docs/architecture.md](docs/architecture.md) first. The rules below are
the ones a change must not break.

## Invariants

- **Entries are append-only.** Never update or delete an entry of a session.
  The one way history goes away is a user deleting a whole session.
- **Only `transcript.Build` decides what the model sees.** Do not add, drop,
  or rewrite messages between the store and the provider. Anything meant for
  the model becomes an entry.
- **Keep the prefix stable.** No timestamps, random values, or map-ordered
  output in the system prompt, tool declarations, or anything else that lands
  early in a request. A changed prefix silently disables provider caching.
- **Parts meet at interfaces.** `agent` depends on `provider.Provider`,
  `tool.Tool`, `ambient.Collector`, `store.Store`, `agent.Observer`, and
  `agent.Inbox`, never on an implementation. Wiring happens only in
  `internal/cli`.
- **Built-in providers and front ends get no shortcut.** They are separate
  processes reached through the protocols, like the ones users write. The
  host never renders anything and never calls a model API.
- **The host does not touch the terminal.** No prints to stdout or stderr
  once a front end is attached, and no signal handling beyond ignoring the
  interrupt, and shutting down on SIGTERM and SIGHUP as a disconnect does.
  What the user must see is an event.
- **The protocols are public contracts.** Changes to `protocol/` are
  additive. An incompatible change needs a new protocol version and an update
  to `docs/protocol.md` or `docs/frontend-protocol.md` in the same change.
- **Stored data keeps working.** Schema changes add tables or columns; they
  never rewrite what a user's database already holds. A change to the schema
  is a new entry at the end of `migrations` in `internal/store/sqlite.go`,
  never an edit to an earlier one.
- **Settings are read once**, in `internal/cli/config.go`, and passed down.

## What the harness is not

rumbo is a general agent harness. These are decisions, not gaps:

- **It knows no kind of work.** Nothing in the host is about programming or
  any other domain: no project detection, no language-specific behaviour.
  What a task needs comes from the user and the user's instruction files.
- **It does not judge the model's work.** A run ends when the model stops
  calling tools. The host runs no checks and sends the model back for
  nothing. Measured over the evaluation set, checks never changed an outcome
  and cost a great deal of machinery.
- **It sends no context unasked** beyond the environment and the user's
  instructions. The model looks at the project with its tools when it needs
  to.
- **It has no permission system.** The agent acts with the rights of the
  user who started it. Limiting what it may write or run is out of scope;
  someone who needs that runs rumbo inside a sandbox of their choice.

## Minimalism

A new tool, setting, entry kind, or protocol field must pay for itself: every
tool and every line of system prompt is paid for in tokens on each request,
by every task, whether it helps that task or not.

## Commands

```sh
gofmt -l .          # must print nothing
go vet ./...
go test ./...
go build -o rumbo ./cmd/rumbo
```

## Measuring changes

A change to the loop, the system prompt, the tools, or the ambient context
changes how the model behaves, and tests cannot show that. Run the evaluation
set before and after such a change and compare pass rate, turns, and tokens:

```sh
go run ./evals -selftest                          # the tasks themselves are sound
go run ./evals -model anthropic/claude-opus-5-5   # needs a credential; costs tokens
```

Never run the second command without the owner asking for it. See
[evals/README.md](evals/README.md).

Compare runs made on the same day, with `-runs 3` or more. Figures from an
earlier day are not a baseline: the same code, sending byte-identical
requests, has measured about a third more output tokens a few days apart.
When two results differ, find out what was sent before explaining it; a
provider that only records the request settles that without a model.

Tests never call a real model: the agent uses a scripted `Provider`, and the
built-in providers are tested against local HTTP servers.

The TUI has no automated test. After changing `internal/frontends/tui`, run
it in tmux and look at it (`tmux capture-pane -p`): a run, typing, cancel with
Ctrl-C, quit, and the shell being usable afterwards.
