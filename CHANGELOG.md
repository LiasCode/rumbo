# Changelog

What changes from one release of rumbo to the next, for the people who use
it. The commits hold the reasons.

## Unreleased

### Terminal UI

- It draws inline, below what the terminal showed before, instead of
  taking the whole screen: the conversation goes into the terminal's
  scrollback, and only the part still changing is drawn again.
- A slash opens a menu of the commands with what each does, and then of
  what each takes, with what describes every value, such as a session's
  title. It narrows as you type, by value or description; Tab or Enter take
  a choice, and Enter on a command that needs nothing more sends it.
- The input takes several lines: Alt-Enter, Shift-Enter, or Ctrl-J starts
  a new one, and pasted text keeps its line breaks.
- Up and Down bring back what you asked in any session of the project, not
  only in this start, and Ctrl-R searches it. It comes from the sessions
  themselves, so deleting a session removes it from there too; commands
  are remembered until the terminal UI quits.

### Sessions

- A session can branch: `/branch` goes on in a new session that starts
  from the current one as it is, or as it was before one of your messages,
  numbered as `/branch-points` lists them. The first session stays as it
  was, and can keep working at the same time. The files are not part of a
  session, so a branch finds them as they are.
- `/sessions`, `/status`, and `rumbo sessions` say which session a branch
  came from.

### Protocols

The Agent Frontend Protocol stays at version 1, and what is new in it is
optional.

- A front end can ask the host, with `recall`, what the person asked in the
  project's sessions, newest first, to bring it back to its input.

## 0.2.0 (2026-11-15)

Configuration is simpler: the system settings, and over them what each
session chooses. The built-in providers sign in with an API key, and
`openai` speaks OpenAI's Responses API. Some commands and settings have new
names; what you stored is carried over, but for values stored for one
project.

### Settings

Settings are now kept apart by what they act on: the system, a session, or
a provider.

- `rumbo config` is now `rumbo system`, for the system settings:
  `default_provider` and `default_model`, which new sessions start with,
  `frontend`, `max_turns`, `max_tokens`, and `compact_at`. The `model`
  setting, which held both the provider and the model, is carried over to
  the first two once.
- Settings are the same everywhere: there are no project settings any more.
  Those stored for one project are no longer read, and `rumbo system list`
  shows them.
- `compact_at` takes a share of the model's context, such as `80%`, or `0%`
  for never. One stored as a number of tokens stops rumbo, saying how to
  change it.
- `-p` chooses the provider and `-m` the model alone, which may hold a
  slash: `rumbo -p local -m qwen3-coder`. `-m provider/model` no longer
  works. What `-p`, `-m`, and `-o` choose is the session's, kept from one
  start to the next, as `/use` and `/session-options` make it.
- A provider's options are its own, kept with its registration, no longer
  the `options.<provider>` setting: `rumbo provider options <name>
  effort=high`, or `/provider-options` in the terminal UI. Those already
  stored are carried over.
- Every field of a provider's or a front end's configuration, and every
  option of a provider, now has a value you can see in
  `rumbo provider show` and `rumbo provider list`: the one you gave, or the
  program's own default, which rumbo keeps the first time the program
  starts. `rumbo provider add` starts the provider, so one that does not
  work is not registered.
- In the terminal UI, `/help` lists the commands by what they act on, and
  some have new names: `/system`, `/system-set`, `/system-unset`, and
  `/system-keys` were `/settings`, `/setting`, `/unset`, and
  `/setting-keys`; `/provider-add`, `/provider-config`, `/provider-show`,
  and `/provider-rm` were `/register`, `/configure`, `/inspect`, and
  `/unregister`. On the command line, `rumbo provider set` and
  `rumbo frontend set` are now `rumbo provider config` and
  `rumbo frontend config`.
- A session keeps the provider options chosen in it, as it keeps its model:
  `/session-options effort=high`. Changing its provider with `/use` leaves
  them behind. The terminal UI's `/set`, whose choices lasted until it
  closed, is gone.
- A system setting or a provider's option you store applies to the next
  run, without starting rumbo again. The default provider and model, and the
  front end, still apply from the next start.

### Providers

- The built-in providers sign in with an API key: `/login` asks for it,
  checks it, and keeps it in `~/.config/rumbo/credentials/`, readable only
  by you; `/logout` removes it, and `/account` says which credential is in
  use. A key in the environment still comes first.
- The built-in `openai` provider offers an `effort` option, sent as
  `reasoning_effort` only when it is chosen: `/session-options effort=high`,
  `-o effort=high`, or `effort` in its configuration. The values depend on
  the model. Like `anthropic`, it now refuses options it does not describe.
- The built-in `openai` provider can speak OpenAI's Responses API, which
  its reasoning models need to use tools with an effort, and the `openai`
  rumbo registers on a new installation does. One registered before keeps
  Chat Completions until `rumbo provider config openai api=responses`. It asks
  the server to store nothing, and gives a model its own reasoning back,
  encrypted.
- In the terminal UI, what a provider's function answers in words shows
  without quotes.

### Protocols

Both stay at version 1, and what is new in them is optional.

- Each function of the host says what it acts on, in `group`: `system`,
  `session`, or `provider`.
- A provider's or a front end's answer to `initialize` may carry `config`,
  its configuration in force, with its default for every field it was not
  given. rumbo keeps it with the registration.
- Every option a provider describes has a `default`, which may be empty
  when that means the model decides. `rumbo provider add` says which ones a
  provider gives none for.

## 0.1.0 (2026-11-15)

The first release.

### Host

- Works on a task until the model stops calling tools, with four tools:
  `read`, `write`, `edit`, and `bash`, with a timeout or in the background.
- Keeps each session as an append-only list of entries in SQLite, from which
  every request sent to the model can be rebuilt, and keeps the start of
  each request stable so providers can cache it.
- Takes what you tell a run while it works, and resumes a run that failed,
  was cancelled, or was cut short (`-r`).
- Summarizes a conversation before it outgrows the model's context.
- Works in several sessions of a project at once, or in one with `-single`,
  and locks each session it works in.
- Gives the model the environment and the `AGENTS.md` or `CLAUDE.md` files
  of the project and of the user.
- Stores settings globally or per project, and checks each when it is
  stored.

### Providers

- The Agent Provider Protocol, version 1.
- Built in: `anthropic` for the Claude API, and `openai` for any server
  compatible with OpenAI's Chat Completions.
- Providers describe their models, options, and functions, and their
  functions can talk with the person while they work.

### Front ends

- The Agent Frontend Protocol, version 1, with the host's own functions
  described as commands and a session's history told in events.
- Built in: `tui` for terminals and `plain` for pipes and scripts.
- `rumbo serve`, for a front end that starts rumbo itself.

### Command line

- `rumbo version`.
- A lone word close to a command, such as `rumbo versoin`, is refused
  instead of being sent to the model as a task; `rumbo -- word` sends it.
