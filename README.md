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

## Before you run it

rumbo gives the model your rights. It reads and writes files and runs
commands as the user who started it, without asking first, and nothing
limits where: there is no approval step and no sandbox.

- **What the model reads can steer it.** A project's `AGENTS.md`, its files,
  and the output of its commands all reach the model. Work on a repository
  you do not trust inside a container or a virtual machine.
- **Sessions keep what the model saw**, including command output, which can
  hold secrets. They are stored in `~/.local/share/rumbo/rumbo.db`; treat
  that file as you would your shell history.
- **Providers and front ends are programs you register**, and they run with
  your rights too. Register only those you trust.

rumbo itself contacts no server, with no telemetry and no update checks:
only the providers you use, and the commands the model runs, reach the
network.

## Install

Download the archive for your platform, Linux or macOS, from the
[releases](https://github.com/LiasCode/rumbo/releases), and put `rumbo` on
your `PATH`. Or, with Go:

```sh
go install github.com/LiasCode/rumbo/cmd/rumbo@latest
```

To build from a clone: `go build -o rumbo ./cmd/rumbo`. `rumbo version` says
which build you have. Windows is not supported.

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

A session keeps the provider and model it was started with, or the ones
`/use` chose for it, so `rumbo -c` goes on with them whatever the `model`
setting says now. `-m` chooses others for one start.

```sh
rumbo -o effort=high "review this module"                  # for one run
rumbo config set options.anthropic '{"effort":"high"}'     # stored, for every run
```

In the terminal UI, `/options` shows what the provider offers and what is in
force, including its models, and `/set <name> <value>` changes a choice for
the runs that follow (`/set model <name>` for the model).

Other commands come from the host: `/status`, `/sessions`, `/open <session>`,
`/new`, `/delete <session>`, `/providers`, `/use <provider> <model>`,
`/compact`, and `/quit`. A command that cannot be undone, such as `/delete`,
asks for a `yes` first.

What a command acts on is always marked: `[host]` is rumbo itself, the same
with any provider, and `[provider <name>]` is what reaches the model. Answers,
errors, and diagnostics carry the mark of where they came from.
`/help` lists every command, the provider's included. Tab completes a command
and then its arguments: sessions after `/open`, providers after `/use`,
options and their values after `/set`. While a command is being typed, the
status bar shows what it could become.

The terminal UI knows none of this in advance; it builds the commands and
their completions from what the host and the provider describe, and so can
any front end.

## Instructions

rumbo gives the model the `AGENTS.md` (or `CLAUDE.md`) files it finds from the
root of the project down to the directory you are in, and the one in
`~/.config/rumbo/`. That is where to say how you want work done: conventions,
commands to run before finishing, things never to touch.

## Write your own provider

A provider is a program that answers `initialize` and `complete` on stdin and
stdout. [examples/provider.py](examples/provider.py) is a complete one, and
[docs/protocol.md](docs/protocol.md) is the specification.

A provider can offer functions, which every front end shows as commands. A
function can talk with the person while it works, as a login does when it
says where to sign in and asks for the code it gets back; the example's
`greet` shows how.

```sh
rumbo provider add mine /path/to/my-provider
rumbo -m mine/some-model "..."
```

## Front ends

In a terminal rumbo starts `tui`: the conversation scrolls above a status bar
and an input line. Enter sends a task; during a run it tells the run what you
typed, which the model reads before its next turn. Up and Down, or Ctrl-P and
Ctrl-N, bring back what you entered before. The line edits as in a shell:
Left, Right, Home, and End move, with Ctrl or Alt by words, and Ctrl-W,
Ctrl-U, and Ctrl-K delete a word, to the start, and to the end. Ctrl-C
cancels the run or quits when idle, Ctrl-D quits on an empty line; quitting
cancels what still works. `/new` or
`/open` during a run leaves it working in the background: the status bar
counts those runs, and a line says when each ends. Elsewhere rumbo starts
`plain`, which writes the model's text to stdout and progress to stderr.
`-f <name>` or the `frontend` setting picks one.

A front end is a program that exchanges JSON lines with the host.
[examples/frontend.py](examples/frontend.py) is a complete one, and
[docs/frontend-protocol.md](docs/frontend-protocol.md) is the specification.

```sh
rumbo frontend add mine /path/to/my-frontend
rumbo -f mine
```

A program that would rather start rumbo itself, such as an editor extension,
runs `rumbo serve` and speaks the same protocol on its stdin and stdout.

One host works in several sessions of the project at once, each with a
provider of its own, so a front end with tabs needs one rumbo. `-single`
keeps it to one session at a time.

## Settings

Stored in SQLite (`$RUMBO_DB`, default `~/.local/share/rumbo/rumbo.db`).
`rumbo config` lists the keys; `-p` scopes a value to the current project,
which is the git repository the current directory is in. Project settings and
sessions are found from any of its subdirectories.

```sh
rumbo config list                                     # every setting, its value here, and where it comes from
rumbo config set -p model anthropic/claude-sonnet-5-5
rumbo config unset -p model
```

A value is checked when it is stored, so a mistyped provider or number is an
error then and not when the next task starts.

`compact_at` says when a long conversation is summarized to free the model's
context. Unset, it is 80% of the context the provider reports, and at most
150,000 tokens. It takes a number of tokens, a share such as `90%`, or `0`
for never. The `openai` provider cannot ask a server for its context size, so
say it in its configuration when it is small:

```sh
rumbo provider set local context_window=32768
```

Providers and front ends are registered programs:

```sh
rumbo provider add mine /path/to/my-provider          # register
rumbo provider show mine                              # start it and see what it offers; also the test that it works
rumbo provider set mine base_url=http://host:8080     # change one field of its configuration
rumbo provider rm mine
```

The same can be done from a front end, through functions of the host:
`/settings`, `/setting`, `/unset`, `/register`, `/inspect`, `/configure`, and
`/unregister` in the terminal UI.

## Versions

rumbo follows semantic versioning, and [CHANGELOG.md](CHANGELOG.md) says
what each release changes. Until 1.0, a release may still change commands,
flags, and settings, and the changelog says how. Two things hold already:

- **The protocols are at version 1.** A release only adds to them: new
  fields and messages are optional, and a provider or front end written for
  version 1 keeps working. Anything incompatible would be version 2.
- **Stored data keeps working.** A release adds to the database and never
  rewrites what it holds, so sessions and settings carry over.

## Documentation

- [docs/architecture.md](docs/architecture.md): the parts and their boundaries
- [docs/use-cases.md](docs/use-cases.md): what the host, providers, and front ends must and can do
- [docs/protocol.md](docs/protocol.md): the Agent Provider Protocol
- [docs/frontend-protocol.md](docs/frontend-protocol.md): the Agent Frontend Protocol
- [docs/glossary.md](docs/glossary.md): terms

## License

MIT; see [LICENSE](LICENSE).
