# rumbo: agent guide

Read [docs/architecture.md](docs/architecture.md) first. The rules below are
the ones a change must not break.

[docs/use-cases.md](docs/use-cases.md) says what the host, providers,
front ends, and extensions must, can, and must not do. A change that adds, removes, or
changes one of those updates it in the same change.

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
- **rumbo's own providers, front ends, and extensions get no shortcut.**
  They are separate processes reached through the protocols, like the ones
  users write. All but `plain` are its own plugin's (`plugins/rumbo`),
  which the host reads through the plugin's manifest, as any plugin's, and
  whose code it reaches only through the package at the plugin's root; the
  rest of the plugin is internal to it. The host never renders anything,
  never calls a model API, and knows nothing of MCP, which only
  `plugins/rumbo/internal/extensions/mcp` speaks.
- **An extension reaches a run only through its tools and its sections.**
  The host declares the tools an extension declares, after its own and in
  an order that does not change, passes it the calls to them, and tells the
  model what its sections say when a run starts (`internal/extension`). An
  extension may call rumbo's own tools and talk with the person while it
  answers a call, offer the person functions, and be told the events a
  front end is told, but nothing it does changes a message, another
  extension's tool, or when a run ends, and no run waits for a listener.
  What it asks the person and is answered is recorded nowhere. One of its tools takes the place of rumbo's own only where the
  user said so (`replaces`), never by its name alone. A run never starts
  with some of its extensions' tools missing.
- **A plugin adds no kind of thing.** It brings providers, front ends,
  extensions, and skills, which are the same as any other once it is added:
  there without an entry, as the built-in programs are, through the one
  place that says what is there without being registered (`provided`, in
  `programs.go`). `config.yaml` keeps of a plugin where it comes from and
  the replacements the user agreed to, nothing its manifest says. What a
  plugin's extension replaces is agreed to by name when the plugin is
  added, and an update that asks for more is refused.
- **The host does not touch the terminal.** No prints to stdout or stderr
  once a front end is attached, and no signal handling beyond ignoring the
  interrupt, and shutting down on SIGTERM and SIGHUP as a disconnect does.
  What the user must see is an event.
- **The protocols are public contracts.** Changes to `protocol/` are
  additive. An incompatible change needs a new protocol version and an update
  to `docs/protocol.md`, `docs/frontend-protocol.md`, or
  `docs/extension-protocol.md` in the same change. Each protocol has a
  version of its own (`Version`, the provider's, `FrontendVersion`, and
  `ExtensionVersion`), so a new one of one refuses nothing of the others.
- **A rumbo process is one session.** A host starts in a new session or
  continues one, and works in no other: it has no function that lists the
  others, moves to one, or runs in two, and no run names a session. What a
  session needs, its providers, its extensions, and its tools, is its
  process's own. A front end that wants another session starts another
  rumbo, and what many processes cost is the front end's to manage. The
  other sessions are reached from the command line (`rumbo sessions`).
- **A session's root is the directory it was started in.** Nothing looks
  above it: not for a git repository, and not for instructions or skills.
  Sessions are listed and continued by that directory, and a front end
  that groups them does it by their roots.
- **Stored data keeps working.** Everything rumbo stores is a file a person
  can read: `config.yaml`, and a directory for each session
  (`internal/store`). A change to what a file holds adds a key or a field
  that may be absent, so the files users already have are read as they are.
  Never rename one or give it another meaning.
- **`config.yaml` holds only what the user chose.** Never write a default
  there, or what a program says about itself: a default lives in the code
  that owns it, and a built-in program is there without an entry. rumbo
  changes the one value a command names and leaves the rest of the file,
  comments included, as the person wrote it.
- **Settings are read in one place**, `internal/cli/config.go`: what rumbo
  starts with when it starts, and what a run uses when the run starts. They
  are passed down, and a run keeps what it started with.
- **Configuration has three levels, kept apart.** A system setting is
  rumbo's own, the same everywhere (`settings.go`); a session keeps what it
  chose, which wins over the system's in it; a provider has its model and
  one configuration, which holds the values of its options too, and a
  session's win over those (`programs.go`, `options.go`). Nothing is set
  per project. Something new goes at one level, and a host function names
  it in its `Group`.
- **Every setting is changed the same way**: `name=value`, where an empty
  value gives it back to the level under it, through `changes.go`, and
  checked before it is kept; a front end's, which needs its terminal to
  start, by the front end when it next starts. A new command or host
  function that sets something takes that grammar, and shows what it
  changed one line to a change; what is shown of the configuration goes
  through `view.go`, each value with where it comes from.
- **Users read CHANGELOG.md.** A change they would notice adds a line to its
  `Unreleased` section, in their terms; the reason goes in the commit.

## What the harness is not

rumbo is a general agent harness. These are decisions, not gaps:

- **It knows no kind of work.** Nothing in the host is about programming or
  any other domain: no project detection, no language-specific behaviour.
  What a task needs comes from the user and the user's instruction files.
- **It does not judge the model's work.** A run ends when the model stops
  calling tools. The host runs no checks and sends the model back for
  nothing. Measured over the evaluation set, checks never changed an outcome
  and cost a great deal of machinery.
- **It sends no context unasked** beyond the environment, the user's
  instructions, what each of the user's skills is for, and the sections of
  the extensions the user registered, which are theirs to ask for. The
  model looks at the project, and reads a skill, with its tools when it
  needs to.
- **It has no permission system.** The agent acts with the rights of the
  user who started it. Limiting what it may write or run is out of scope;
  someone who needs that runs rumbo inside a sandbox of their choice, or
  has an extension of theirs replace the tool to limit.
- **It uses a subscription only where its maker allows it.** `chatgpt`
  signs in with ChatGPT as OpenAI offers open-source apps. Anthropic's terms
  keep a Claude subscription to its own apps, so no provider signs in to
  one, or passes for Claude Code; `anthropic` takes an API key.
- **It does not track files.** A session keeps the conversation, not the
  disk: there are no restore points, and a branch starts from an earlier
  point of the conversation with the files as they are.

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

`make check` runs them all.

## Releasing

The commits since the last tag decide the version, so their Conventional
Commits types matter: a breaking change (`!` or a `BREAKING CHANGE:`
footer) raises the major number, or the minor one before 1.0; `feat` the
minor; `fix` and `perf` the patch; other types release nothing.

1. `make next` says which version that is, and the commits that decide it.
2. `make release` runs the checks, puts `## X.Y.Z (YYYY-MM-DD)` under
   `## Unreleased` in CHANGELOG.md, commits it as `chore: release X.Y.Z`,
   and tags the commit `vX.Y.Z` (`scripts/bump`). It refuses uncommitted
   changes and an empty `Unreleased` section.
3. `git push --atomic origin main vX.Y.Z`. The release workflow runs the
   checks, builds the archives with `scripts/release.sh`, and publishes them
   with that section of the changelog as their notes. It refuses a tree
   without a `LICENSE`, or a changelog without that section.

1.0, or any version the commits do not give, is still released by hand:
the same changelog heading, commit, and tag.

`scripts/release.sh X.Y.Z <directory>` builds the same archives locally.

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
When two results differ, find out what was sent before explaining it:
`rumbo request <session> <turn>` prints a stored turn's request exactly,
and a provider that only records the request settles it without a model.

A change for a provider's cache is measured on the provider's server too,
the same task with and without it on the same day: a request shows what
was sent, not what the server did with it. The ChatGPT plan's server once
ignored a cache key that every rebuilt request carried.

Tests never call a real model: the agent uses a scripted `Provider`, and the
providers of rumbo's own plugin are tested against local HTTP servers.

The TUI's state is tested by driving it with messages (`app_test.go`'s
stage), but nothing tests what a terminal draws of it. After changing
`plugins/rumbo/internal/frontends/tui`, run it in tmux and look at it
(`tmux capture-pane -p`): a run, typing, cancel with Ctrl-C, quit, and the
shell being usable afterwards; a session continued with `-s`, which shows
what it holds; another session shown from the sidebar and a new one with
Ctrl-N, a run left working in one while another shows, `/rumbos`, and no
`rumbo serve` left once it quits (`pgrep -af "[r]umbo serve"`, since a
pattern that matches the shell that runs it kills that shell with
`pkill`); a command from Ctrl-P with a form and one with a
confirmation; the picker changing the model and an option, and a run that
uses them; a provider's function that asks a secret, answered and given up
with Escape; both panels, Ctrl-G on a folder with edits and on one outside
Git, and Ctrl-Alt-B; a tool call shown whole and back; and text selected
by dragging the pointer. `tmux send-keys` takes some words as key names,
`delete` among them: send text with `-l`; it sends an Escape and the key
after it at once, which reads as Alt and that key, so give them apart; and
the pointer is sent as the terminal reports it, `-l` with `\e[<0;X;YM` for the left
button going down on column X and row Y, counted from 1, `\e[<32;X;YM`
for it moving held, and `\e[<0;X;Ym` for it coming up.

The computer tool is tested against a fake screen, and its Wayland client
against a fake compositor. After changing `internal/computer`, also try it
on a real desktop, driven by a provider that follows a script, and see what
reached a test page that records its events. A Sway started with
`WLR_BACKENDS=headless` shows nothing on the person's desktop, and a rootful
`Xwayland :N` gives X11 a server of its own; neither can touch what the
person is doing. On the person's Hyprland, the tool acts on whatever has the
focus, and its pointer is theirs too: give what it acts on a headless output
far from their monitors, check that the test window opened there, since a
program can ignore where it was asked to open, and stop at the first
screenshot of any other monitor. A refusal because the pointer moved is
the person using their mouse, which is what it is for. In a browser,
Shift-click after a click elsewhere selects from there, and what is typed
next is dropped: click plainly before typing.
