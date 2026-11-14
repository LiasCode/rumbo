# Use cases

What each of the three parts of rumbo must do, can do, and must not do. The
host is rumbo itself. Providers and front ends are separate programs that
speak a protocol with it, whether they are built in or written by a user.

[architecture.md](architecture.md) explains how the parts fit together,
[protocol.md](protocol.md) and [frontend-protocol.md](frontend-protocol.md)
specify the messages between them, and the code holds the details.

## Host

The host owns the sessions of one project, the tools, and the loop. It shows
nothing and knows no model API.

### Must

- Keep each session as an append-only list of entries. The only way history
  goes away is deleting a whole session.
- Build every request to the model from the session's entries alone, so a
  stored session reproduces every request it sent.
- Keep the start of each request stable, from the system prompt and the tool
  declarations to the ambient context, so providers can cache it.
- Hold a lock on every session it works in, so no other rumbo process writes
  to it at the same time.
- Close every run with a note saying how it ended: done, failed, or
  cancelled.
- Keep tool results small: read files in windows, shorten long output, and
  save the whole of it to a file the model can search.
- Summarize a conversation before it outgrows the model's context, and
  recover the same way when a request does not fit anyway.
- Reach models only through providers, and people only through front ends,
  the built-in ones included, always over the protocols.
- Leave the terminal to the front end: write nothing to it, and ignore the
  interrupt, which is the front end's to interpret.
- End on SIGTERM and SIGHUP as it does when the front end disconnects: its
  runs cancelled and recorded, its tools and providers stopped.
- Stop the processes the model left running in the background, and remove
  the files the tools saved, when it exits.
- Check a setting when it is stored, and refuse to start with one that is
  missing or malformed instead of guessing.
- Keep the protocols compatible: what it adds is optional.
- Keep stored data working: changes to the schema only add.

### Can

**Run tasks**

- Work on an objective until the model stops calling tools.
- Work in several sessions of a project at once, with one run per session,
  or in one session at a time when started with `-single`.
- Start a run in the current session, in another session of the project, or
  in a new one.
- Take what the user tells a run while it works, give it to the model before
  its next request, and keep the run going when the model was about to
  finish.
- Cancel one run, or all of them.
- Resume a run that failed, was cancelled, or was cut short by the host
  dying, telling the model it was interrupted.
- Retry a request that failed for a passing reason, such as a rate limit, an
  overloaded server, or a provider process that died, with growing pauses.
- Continue a response the output limit cut off.
- Stop a run after a number of turns.
- Summarize a session's conversation on request.

**Give the model**

- Four tools: read a file in windows, write a whole file, edit one by exact
  match, and run a bash command, with a timeout or in the background.
- A guard against replacing a file its model has not read, or that changed
  since it did. Each session keeps its own record of what its model read.
- The environment it works in: the working directory, the platform, and the
  date.
- The user's instructions: the `AGENTS.md` or `CLAUDE.md` files from the
  project's root down to the working directory, and the user's own in
  `~/.config/rumbo/`. A long one is cut, saying where the rest is.

**Sessions**

- Find a project by its git root, from any directory inside it.
- List the sessions of a project, marking those working in this host.
- Continue the latest session (`-c`) or a given one (`-s`), or resume its
  unfinished run (`-r`).
- Open another session, start a new one, and delete one with its history.
- Show a session's status: its provider and model, the size of its context,
  what it has spent, and how its last run stands.
- Keep each session's provider and model from one start to the next, and let
  `-m` choose others for one start.
- Print a session's whole record (`rumbo log`).

**Providers**

- Register, configure, show, and remove providers. The built-in `anthropic`
  and `openai` are registered the first time rumbo runs, and `{self}` stands
  for rumbo's own executable in a command.
- Start a provider and show what it offers, which is also the test that it
  works.
- Keep as many processes of a provider as the requests running at once need,
  and replace one that died.
- Change the provider and the model of a session for its next runs, while a
  run in progress keeps the ones it started with.
- Pass a provider's description to the front end, send the chosen options
  with every request, and relay calls to its functions, understanding none of
  them.
- Serve the built-in providers itself (`rumbo provide <name>`), over the same
  protocol as any other.

**Front ends**

- Start a registered front end and give it the terminal, or be started by
  one (`rumbo serve`). Without `-f` or the `frontend` setting it starts `tui`
  in a terminal and `plain` elsewhere.
- Register, show, and remove front ends, and serve the built-in `tui` and
  `plain` itself (`rumbo attach <name>`).
- Describe functions of its own, which any front end offers as commands
  without knowing them: `status`, `sessions`, `open`, `new`, `delete`,
  `providers`, `use`, `register`, `configure`, `inspect`, `unregister`,
  `settings`, `setting`, `unset`, `setting-keys`, `compact`, and `quit`.
- Mark the functions that cannot be undone, so a front end asks the person
  to confirm them.
- Answer its own functions, and the provider's description and functions,
  while runs work. Under `-single`, those that change the session or the
  provider wait for the run instead.
- Report each run as events, each naming its run and session: the model's
  text, the tools it calls and what they return, the context sent, the
  tokens spent, and diagnostics.

**Settings**

- Store settings globally or for one project: the model, the front end, the
  output limit, the turn limit, when to summarize, and the options of each
  provider. List them with where each value in force comes from.
- Take a provider's options for one start (`-o`) or one run, from the front
  end.

### Must not

- Know any kind of work: no project detection, and nothing about
  programming or any other domain.
- Judge the model's work. A run ends when the model stops calling tools,
  and the host runs no checks of its own.
- Send context nobody asked for, beyond the environment and the user's
  instructions.
- Limit what the agent may do. It acts with the rights of the user who
  started it, inside whatever sandbox the user chose.
- Call a model API, render anything, or see a provider's credentials.

## Provider

A provider turns a conversation into the next assistant message. It speaks
the Agent Provider Protocol on its stdin and stdout.

### Must

- Answer `initialize` with protocol version 1 and its name.
- Answer `complete` with the next assistant message, why it stopped (it
  ended, it calls tools, the output limit cut it, or the model refused), and
  what it cost. Every request carries the whole conversation.
- Answer one request at a time. When the host needs more at once, it starts
  more processes.
- Give each tool call the arguments the model produced, as JSON, or as a
  string when they are not valid JSON.
- Mark a failure that can succeed if the request is sent again as transient,
  and a request that did not fit in the model's context as such. Any other
  error ends the run.
- Answer a request it was asked to cancel, normally with an error, within a
  few seconds.
- Answer `shutdown` and exit.
- Write only protocol messages to stdout. Diagnostics go to stderr, which
  reaches the user.
- Hold its own credentials. They never pass through the host.
- Ignore opaque blocks it does not recognize, which is what lets a session
  change provider.

### Can

- Stream the visible text of a response while it is produced.
- Report how many tokens the model's context holds, so the host summarizes
  before it fills.
- Describe how its model is chosen, the options it accepts, and the
  functions it offers. The host and the front ends offer them without
  knowing them.
- Offer functions, such as listing its models. One that cannot be undone
  asks to be confirmed, and one that an option names returns the values the
  option accepts.
- Read the configuration registered with it.
- Keep what it needs to continue a conversation, such as signed reasoning,
  in opaque blocks the host returns unchanged.
- Cache the start of requests, which the host keeps stable.

### Must not

- Rely on anything but the request it is answering: the next request of the
  same session may go to another process.

### Built in

- **`anthropic`** reaches the Claude API with `ANTHROPIC_API_KEY`,
  `ANTHROPIC_AUTH_TOKEN`, or the profile from `ant auth login`. It lists the
  models the account can use, offers an `effort` option, keeps the model's
  thinking in opaque blocks, and reports the size of the model's context.
- **`openai`** reaches any server compatible with OpenAI's Chat Completions,
  at the address its configuration gives, with the key in the variable its
  configuration names (`OPENAI_API_KEY` by default). It lists the server's
  models, and takes the size of the context from its configuration, since a
  server cannot say.

## Front end

A front end is what a person uses to give objectives and follow the work. It
speaks the Agent Frontend Protocol with the host.

### Must

- Answer the host's `initialize` with protocol version 1 and its name.
- Run the objective the command line gave, or resume the unfinished run,
  when `initialize` asks for it.
- Ignore event types and fields it does not know.
- Not reuse the id of a run in progress, since it names the run.
- Decide what Ctrl-C means, and cancel through the protocol.
- Show the confirmation a function asks for, and call the function only
  after the person agrees.
- Wait for an answer when one request has to take effect before another.

### Can

- Start runs in the current session, in another session, or in a new one,
  and choose the model and the options for one run.
- Follow several runs at once, telling their events apart by run and
  session.
- Tell a run something while it works, and offer it as a new task when the
  run ended first.
- Cancel one run, or all of them.
- Ask what the provider offers and call its functions, call the host's
  functions, and build commands, pickers, and completion from their
  descriptions without knowing them beforehand.
- Take what it is attached to, its directory, session, provider, and model,
  from the host's answers instead of remembering it.
- Be started by rumbo, with the terminal and with the protocol on file
  descriptors 3 and 4, or start `rumbo serve` itself and speak on its stdin
  and stdout.
- Read the configuration registered with it.
- End the host by closing the connection.

### Built in

**`tui`**, the terminal UI:

- The conversation scrolls above a status bar and an input line, and stays
  in the terminal's scrollback.
- Enter sends a task. During a run it tells the run what was typed, which
  shows once the model has it, and goes back to the input if the run ended
  first.
- Its commands are the functions the host and the provider describe, and
  its own `/options`, `/set`, and `/help`. Tab completes a command and its
  arguments, and the status bar shows what a command could become.
- Answers, errors, and diagnostics are marked `[host]` or `[provider
  <name>]`, by where they come from.
- `/new` or `/open` during a run leaves the run working in the background.
  The status bar counts those runs, and a line says when each one ends.
- Up and Down, or Ctrl-P and Ctrl-N, bring back what was entered. Ctrl-C
  cancels the run, else clears the input, else quits; Ctrl-D quits; Ctrl-U
  clears the line, Ctrl-W deletes a word, and Ctrl-L redraws. Pasted line
  breaks stay text.
- The status bar shows the provider, the model, what is happening, the
  session, the size of the context, and the tokens spent.
- It gives the terminal back as it found it, when it quits or the host ends.

**`plain`**, for pipes and scripts:

- It takes the objective from the command line or from piped input, or
  reads objectives line by line.
- The model's text goes to stdout; progress, the session, and the tokens
  spent go to stderr.
- Ctrl-C cancels the run. It exits with 0 when the run is done, 1 when it
  failed, and 130 when it was cancelled.
