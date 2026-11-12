# rumbo

A minimal agent harness. You give it a task; it drives a model, through a
small set of tools, until the task is done.

- **Small on purpose.** Four general tools (`read`, `write`, `edit`, `bash`),
  a short constant system prompt, one loop. The model is told where it is
  running and what your standing instructions are, and finds out the rest
  itself.
- **Careful with tokens.** Each request extends the previous one, so the
  provider's cache does the work. Tool output is bounded, and a full context
  is summarized and continued.
- **Reproducible history.** A session is an append-only list of entries in
  SQLite. Every request sent to the model can be rebuilt from them, and an
  interrupted run can be resumed.
- **Bring your own model.** Models are reached through the Agent Provider
  Protocol: JSON-RPC over stdio, implementable in any language. Use your API
  key, your subscription, or a local server.
- **Bring your own interface.** Front ends are reached the same way, through
  the Agent Frontend Protocol. A simple terminal UI is built in.

## Build

```sh
go build -o rumbo ./cmd/rumbo
```

## Use

```sh
rumbo config set model anthropic/claude-opus-5-5
rumbo "add a --json flag to the export command"

rumbo -c "also document it in the README"   # continue the latest session here
rumbo -r                                     # resume a run that failed or was cancelled
rumbo                                        # open the terminal UI and type tasks
rumbo "summarize notes.md" > summary.txt     # not a terminal: plain output, exit code
rumbo sessions                               # sessions of this project
rumbo log <session>                          # the full record, as JSON lines
```

Built-in providers:

| Name | Talks to | Credentials |
|---|---|---|
| `anthropic` | Claude API | `ANTHROPIC_API_KEY`, or the profile from `ant auth login` |
| `openai` | Any OpenAI-compatible Chat Completions server | the variable named by `api_key_env` (default `OPENAI_API_KEY`) |

A local server is the `openai` provider with another address:

```sh
rumbo provider add -config '{"base_url":"http://localhost:11434/v1"}' local '{self}' provide openai
rumbo -m local/qwen3-coder "fix the failing test"
```

## Models and options

`model` is `<provider>/<model>`. What else can be chosen is up to each
provider, which describes its own options and can list its own models; rumbo
passes them along without knowing what they mean.

```sh
rumbo -o effort=high "review this module"                  # for one run
rumbo config set options.anthropic '{"effort":"high"}'     # stored, for every run
```

In the terminal UI, `/options` shows what the provider offers and what is in
force, including its models, and `/set <name> <value>` changes a choice for
the runs that follow (`/set model <name>` for the model).

## Instructions

rumbo gives the model the `AGENTS.md` (or `CLAUDE.md`) files it finds from the
root of the project down to the directory you are in, and the one in
`~/.config/rumbo/`. That is where to say how you want work done: conventions,
commands to run before finishing, things never to touch.

## Write your own provider

A provider is a program that answers `initialize` and `complete` on stdin and
stdout. [examples/provider.py](examples/provider.py) is a complete one, and
[docs/protocol.md](docs/protocol.md) is the specification.

```sh
rumbo provider add mine /path/to/my-provider
rumbo -m mine/some-model "..."
```

## Front ends

In a terminal rumbo starts `tui`: the conversation scrolls above a status bar
and an input line. Enter sends a task, Ctrl-C cancels the run or quits when
idle, Ctrl-D quits. Elsewhere it starts `plain`, which writes the model's
text to stdout and progress to stderr. `-f <name>` or the `frontend` setting
picks one.

A front end is a program that exchanges JSON lines with the host.
[examples/frontend.py](examples/frontend.py) is a complete one, and
[docs/frontend-protocol.md](docs/frontend-protocol.md) is the specification.

```sh
rumbo frontend add mine /path/to/my-frontend
rumbo -f mine
```

A program that would rather start rumbo itself, such as an editor extension,
runs `rumbo serve` and speaks the same protocol on its stdin and stdout.

## Settings

Stored in SQLite (`$RUMBO_DB`, default `~/.local/share/rumbo/rumbo.db`).
`rumbo config` lists the keys; `-p` scopes a value to the current project,
which is the git repository the current directory is in. Project settings and
sessions are found from any of its subdirectories.

```sh
rumbo config set -p model anthropic/claude-sonnet-5-5
```

## Documentation

- [docs/architecture.md](docs/architecture.md): the parts and their boundaries
- [docs/protocol.md](docs/protocol.md): the Agent Provider Protocol
- [docs/frontend-protocol.md](docs/frontend-protocol.md): the Agent Frontend Protocol
- [docs/glossary.md](docs/glossary.md): terms
