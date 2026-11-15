# Changelog

What changes from one release of rumbo to the next, for the people who use
it. The commits hold the reasons.

## Unreleased

### Settings

Settings are now kept apart by what they act on: the system, a session, or
a provider.

- `rumbo config` is now `rumbo system`, for the system settings: the model
  new sessions start with, the front end, the output limit, the turn limit,
  and when to summarize.
- A provider's options are its own, no longer the `options.<provider>`
  setting: `rumbo provider options <name> effort=high`, or
  `/provider-options` in the terminal UI. Those already stored stay in
  force.
- In the terminal UI, `/help` lists the commands by what they act on, and
  some have new names: `/system`, `/system-set`, `/system-unset`, and
  `/system-keys` were `/settings`, `/setting`, `/unset`, and
  `/setting-keys`; `/provider-add`, `/provider-set`, `/provider-show`, and
  `/provider-rm` were `/register`, `/configure`, `/inspect`, and
  `/unregister`.
- `/system-set` and `/system-unset` act for every project unless you add
  `project`, as `rumbo system` does without `-p`. Before, they acted for the
  project unless told `global`.
- A session keeps the provider options chosen in it, as it keeps its model:
  `/session-options effort=high`. Changing its provider with `/use` leaves
  them behind. The terminal UI's `/set`, whose choices lasted until it
  closed, is gone.
- A system setting or a provider's option you store applies to the next
  run, without starting rumbo again. The model and the front end still apply
  from the next start.
- Options given with `-o` apply in every session that uses the provider
  rumbo was started with.
- Front end protocol: each function of the host says what it acts on, in
  `group`.

### Providers

- The built-in providers sign in with an API key: `/login` asks for it,
  checks it, and keeps it in `~/.config/rumbo/credentials/`, readable only
  by you; `/logout` removes it, and `/account` says which credential is in
  use. A key in the environment still comes first.
- In the terminal UI, what a provider's function answers in words shows
  without quotes.

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
