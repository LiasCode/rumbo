# rumbo

A minimal coding agent harness. You give it an objective; it drives a model
until the objective is done and the project's checks pass.

- **Small on purpose.** Four tools (`read`, `write`, `edit`, `bash`), a short
  constant system prompt, one loop.
- **Context without asking.** The harness collects the directory tree, git
  state, project instructions, and checks with ordinary tools and hands them to
  the model up front. Later turns only carry what changed.
- **Done means verified.** When the model stops, the harness runs the
  project's checks and sends failures back.
- **Reproducible history.** A session is an append-only list of entries in
  SQLite. Every request sent to the model can be rebuilt from them.
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
rumbo                                        # open the terminal UI and type objectives
rumbo "fix the lint errors" > answer.txt     # not a terminal: plain output, exit code
rumbo sessions                               # sessions of this directory
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
and an input line. Enter sends an objective, Ctrl-C cancels the run or quits
when idle, Ctrl-D quits. Elsewhere it starts `plain`, which writes the model's
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
`rumbo config` lists the keys; `-p` scopes a value to the current project.

```sh
rumbo config set -p checks '["go vet ./...", "go test ./..."]'
```

## Documentation

- [docs/architecture.md](docs/architecture.md): the parts and their boundaries
- [docs/protocol.md](docs/protocol.md): the Agent Provider Protocol
- [docs/frontend-protocol.md](docs/frontend-protocol.md): the Agent Frontend Protocol
- [docs/glossary.md](docs/glossary.md): terms
