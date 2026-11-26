# rumbo

A minimal agent harness, extensible by design.

I built this harness because I wanted the freedom to create and experiment
with new interface ideas, without being constrained by the rigidity of
existing harnesses, while keeping the philosophy of
[pi](https://pi.dev) at its core. I then added what the project itself
demanded: a fully open implementation for providers, an extension system,
and an easy way to distribute and install components (called "plugins")
through simple protocols that are easy to implement.

The result is that you can modify everything around the core loop: use
your own front end, optimize your providers, and build extensions for a
specific market, or create an uncontrollable monster.

![Front ends speak the Agent Frontend Protocol with the rumbo host, which runs the agent loop, keeps each session as an append-only log, and asks a provider for each message through the Agent Provider Protocol; extensions give the model more tools and context through the Agent Extension Protocol.](docs/how-it-works.svg)

- **Like pi**, rumbo gives the model four tools of its own (`read`,
  `write`, `edit`, `bash`), a fifth that uses the screen if you turn it on
  (`rumbo system set computer=on`), and a short system prompt. It has no
  plan mode, no subagents, and no permission prompts: a shell reaches
  everything else, and what a task needs comes from the task and your
  instructions.
- **Like the [Language Server Protocol](https://microsoft.github.io/language-server-protocol/)**,
  its parts are separate programs that speak JSON-RPC and say, on
  `initialize`, what each can do. Front ends, providers, and extensions can
  be written in any language, and the ones that come with rumbo use the
  same protocols as the ones you write; none gets a shortcut.

> [!WARNING]
> This is a work in progress: everything can change and break. rumbo gives
> the model your rights: it reads, writes, and runs commands as you,
> without asking and without a sandbox. Use it at your own risk.

## Install

Download the archive for Linux, macOS, or Windows from the
[releases](https://github.com/LiasCode/rumbo/releases), or:

```sh
go install github.com/LiasCode/rumbo/cmd/rumbo@latest
```

On Windows, the model runs commands with PowerShell, and the computer
tool is not available.

> [!NOTE]
> rumbo is used and tested every day only on Linux. On macOS and Windows it
> passes the checks of continuous integration, but nobody has used it there
> for real yet: expect rough edges, and please report what you find.

## Start

```sh
export ANTHROPIC_API_KEY=...                        # or /login in the terminal UI, or `ant auth login`
rumbo system set default_provider=anthropic         # what new sessions start with
rumbo provider set anthropic model=claude-opus-5-5  # and the model they start with there

rumbo                                            # the terminal UI; /help lists its commands
rumbo "add a --json flag to the export command"  # one task, in this directory
rumbo -c "also document it"                      # continue the latest session
rumbo -r                                         # resume a run that stopped
rumbo "summarize notes.md" > summary.txt         # piped: plain text out, exit code
```

`rumbo help` lists every command and flag, and `rumbo config` shows what is
set, each value with where it comes from.

What you set is kept in `~/.rumbo/config.yaml`. It holds only what
you chose and you can edit it; rumbo changes it one value at a time and
leaves your comments:

```yaml
system:
  default_provider: anthropic
providers:
  anthropic:
    model: claude-opus-5-5
    config:
      effort: high # every session, unless it chooses another with -o
```

Each session is a directory in `~/.rumbo/sessions/`: `session.json` says
what it is, and `entries.jsonl` holds its history, one entry to a line.
Everything rumbo keeps is in `~/.rumbo/` (`%USERPROFILE%\.rumbo\` on
Windows), or in the directory `RUMBO_HOME` names.

## Providers

Three come with rumbo: `anthropic`, `openai`, and `chatgpt`.

Any server compatible with OpenAI's Chat Completions, local ones included,
works through the `openai` one:

```sh
rumbo provider add -config '{"base_url":"http://localhost:11434/v1"}' local '{self}' provide openai
rumbo provider models local                         # the models the server offers
rumbo provider set local model=qwen3-coder context_window=32768   # a server cannot say its context size
rumbo -p local "fix the failing test"               # the session keeps its provider and model
```

`chatgpt` uses your ChatGPT plan instead of an API key: `rumbo -p chatgpt`,
then `/login` signs in and `/use chatgpt` lists the plan's models.
Anthropic's terms do not let other applications use a Claude subscription,
so `anthropic` takes an API key.

## Instructions and skills

The model reads your instructions: the `AGENTS.md` or `CLAUDE.md` file of
the directory you start rumbo in, and yours in `~/.rumbo/`.

It is told of your skills too, in the [Agent Skills](https://agentskills.io)
format: yours in `~/.rumbo/skills/`, and a project's in
`.agents/skills/` of the directory you start rumbo in, which replaces one
of yours with the same name. Only a skill's description is sent with every
request; the model reads the rest when a task needs it. `rumbo skills`
lists them, and says why one is left out.

## Use an MCP server

The tools of a [Model Context Protocol](https://modelcontextprotocol.io)
server become the model's through the `mcp` extension that comes with
rumbo, registered once for each server:

```sh
# one started with a command
rumbo extension add files {self} extend mcp npx -y @modelcontextprotocol/server-filesystem ~/notes

# one reached at a URL; if it wants a sign-in, /login docs in the terminal UI
# signs in with your browser
rumbo extension add -config '{"url":"https://example.com/mcp"}' docs {self} extend mcp
```

Every tool a server has is declared to the model with every request, so a
large server costs tokens in every task: `rumbo extension show files` says
how much, and `rumbo extension set files tools=read_text_file,list_directory`
keeps only those. `rumbo extension` lists the other fields.

## Add a plugin

A plugin brings extensions, providers, front ends, and skills at once: a
directory, or a Git repository, with a `plugin.yaml` that names them.

```sh
rumbo plugin add https://example.com/team-plugin.git
rumbo plugin show team      # what it brings
rumbo plugin update team    # bring it up to date
rumbo plugin rm team
```

What it brings is set as what comes with rumbo is, which is a plugin too
(`rumbo plugin show rumbo`). A plugin runs with your rights, so add one
whose code you trust; when one of its extensions takes the place of a tool
of rumbo's own, adding it says so and asks you to agree. To write one, read
[docs/plugins.md](docs/plugins.md) and start from
[examples/plugin](examples/plugin).

## Bring your own

Each is a program in any language, with an example to start from and a
specification:

| | Speaks the protocol on | Example | Specification |
|---|---|---|---|
| A provider | its stdin and stdout | [provider.py](examples/provider.py) | [protocol.md](docs/protocol.md) |
| An extension | its stdin and stdout | [extension.py](examples/extension.py) | [extension-protocol.md](docs/extension-protocol.md) |
| A front end | file descriptors 3 and 4 (on Windows, the handles `RUMBO_FRONTEND_HANDLES` names), or `rumbo serve`, which it starts itself | [frontend.py](examples/frontend.py) | [frontend-protocol.md](docs/frontend-protocol.md) |

```sh
rumbo provider add mine /path/to/my-provider
rumbo -p mine -m some-model "..."

rumbo extension add notes python3 /path/to/examples/extension.py
rumbo "remember that the deploy is on Friday"

rumbo frontend add mine /path/to/my-frontend
rumbo -f mine
```

An extension gives the model tools, can tell it something when a run
starts, offers you commands, and is told what the runs do. One of its tools
takes the place of one of rumbo's when you say so, which is how you run the
model's commands in a container, or are asked before some of them run, as
[examples/guard.py](examples/guard.py) does around rumbo's own bash:

```sh
rumbo extension add -replaces bash guard python3 /path/to/examples/guard.py
```

## Learn more

- [docs/use-cases.md](docs/use-cases.md): everything the host, providers,
  front ends, and extensions can do.
- [docs/architecture.md](docs/architecture.md) and
  [docs/glossary.md](docs/glossary.md): how the parts fit, and the terms.
- [CHANGELOG.md](CHANGELOG.md): what each release changes.

## License

MIT; see [LICENSE](LICENSE).
