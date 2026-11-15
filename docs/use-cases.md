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
- Record nothing a provider's function asks or is told. A secret answer goes
  from the front end to the provider, and nowhere else.
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
- Branch a session: go on in a new one that starts with a copy of it, as it
  is or as it was before one of the person's messages. The session it came
  from stays as it is, and can go on working at the same time; the files
  are not the session's, and stay as they are.
- Show a session's status: its provider and model, the size of its context,
  what it has spent and how much of its input the provider read from its
  cache, and how its last run stands.
- Keep each session's provider and model, and the values of the provider's
  options chosen in it, from one start to the next, over the system
  settings. `-p`, `-m`, and `-o` choose them for the session, as `use` and
  `session-options` do. A session that changes provider leaves its options
  behind.
- Print a session's whole record (`rumbo log`).
- Tell a front end what a session holds, in the events its runs sent, with
  what the person asked and how each run ended, so it can show a session it
  opens.

**Providers**

- Register, configure, show, and remove providers. A provider is started
  when it is added, which is the test that it works. The built-in
  `anthropic` and `openai` are registered the first time rumbo runs, and
  `{self}` stands for rumbo's own executable in a command.
- Keep with each registered provider and front end a value for every field
  of its configuration, and for every option of a provider: the one the
  user gave, or else the program's own default, stored the first time the
  program says it.
- Start a provider and show what it offers, which is also the test that it
  works.
- List the models a provider offers and the values its options take,
  asking it, so a front end can offer them as choices: the models after
  the provider `use` names, and the `name=value` changes of
  `session-options` and `provider-options`, with those in force marked.
- Keep as many processes of a provider as the requests running at once need,
  and replace one that died.
- Change the provider and the model of a session for its next runs, while a
  run in progress keeps the ones it started with.
- Store the option values each provider gets with every request, apart
  from its configuration. A run gets those, the ones chosen in its session
  over them, and those its front end chose for it over both.
- Pass a provider's description to the front end, send the chosen options
  with every request, and relay calls to its functions, understanding none of
  them.
- Relay what a provider's function says to the person and asks of them while
  it works, and the answers back, and give the function up when the person
  does.
- Serve the built-in providers itself (`rumbo provide <name>`), over the same
  protocol as any other.

**Front ends**

- Start a registered front end and give it the terminal, or be started by
  one (`rumbo serve`). Without `-f` or the `frontend` setting it starts `tui`
  in a terminal and `plain` elsewhere.
- Register, show, and remove front ends, and serve the built-in `tui` and
  `plain` itself (`rumbo attach <name>`).
- Describe functions of its own, which any front end offers as commands
  without knowing them, each saying what it acts on. For the system:
  `system`, `system-set`, `system-unset`, `system-keys`, and `quit`. For a
  session: `status`, `sessions`, `open`, `new`, `branch`,
  `branch-points`, `delete`, `use`, `session-options`, and `compact`. For
  the providers: `providers`, `provider-models`, `option-values`,
  `provider-add`, `provider-config`, `provider-options`, `provider-show`,
  and `provider-rm`.
- Mark the functions that cannot be undone, so a front end asks the person
  to confirm them.
- Answer its own functions, and the provider's description and functions,
  while runs work. Under `-single`, those that change the session or the
  provider wait for the run instead.
- Report each run as events, each naming its run and session: the model's
  text, the tools it calls and what they return, the context sent, the
  tokens spent, and diagnostics.

**System settings**

- Store the system settings, rumbo's own and the same everywhere: the
  provider and the model a new session starts with, the front end, the turn
  limit, the output limit, and the share of the model's context at which to
  summarize. List them with where each value in force comes from
  (`rumbo system`), and the values an earlier version stored for one
  project, which are no longer read.
- Apply a stored system setting, or a provider's option, to the runs that
  start after it is stored, without starting again. The default provider
  and model, and the front end, apply from the next start.

### Must not

- Know any kind of work: no project detection, and nothing about
  programming or any other domain.
- Judge the model's work. A run ends when the model stops calling tools,
  and the host runs no checks of its own.
- Send context nobody asked for, beyond the environment and the user's
  instructions.
- Limit what the agent may do. It acts with the rights of the user who
  started it, inside whatever sandbox the user chose.
- Track the files the model changes. A session keeps the conversation, not
  the disk.
- Call a model API, render anything, or see a provider's credentials.

## Provider

A provider turns a conversation into the next assistant message. It speaks
the Agent Provider Protocol on its stdin and stdout.

### Must

- Answer `initialize` with protocol version 1, its name, and its
  configuration in force, with a default for every field it was not given.
- Give a default for every option it describes. It may be empty when that
  means the model decides.
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
- Hold its own credentials, and keep them where it chooses. The host keeps
  none: one a person types at the provider's question it relays without
  recording.
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
- Talk with the person while a function works: say things, and ask
  questions, marking an answer secret when it is one, such as the code a
  sign-in gives. The function takes as long as the person does.
- Read the configuration registered with it.
- Keep what it needs to continue a conversation, such as signed reasoning,
  in opaque blocks the host returns unchanged.
- Cache the start of requests, which the host keeps stable.

### Must not

- Rely on anything but the request it is answering: the next request of the
  same session may go to another process.

### Built in

- **`anthropic`** reaches the Claude API with `ANTHROPIC_API_KEY`,
  `ANTHROPIC_AUTH_TOKEN`, the key its `login` stored, or the profile from
  `ant auth login`, in that order. It lists the models the account can use,
  offers an `effort` option, keeps the model's thinking in opaque blocks,
  and reports the size of the model's context.
- **`openai`** reaches any server compatible with OpenAI's Chat Completions,
  at the address its configuration gives, with the key in the variable its
  configuration names (`OPENAI_API_KEY` by default), or else the key its
  `login` stored for that address. It lists the server's models, offers an
  `effort` option, sent as `reasoning_effort` only when it is chosen, and
  takes the size of the context from its configuration, since a server
  cannot say. With `api` set to `responses` in its configuration, as the
  `openai` registered the first time rumbo runs has it, it speaks OpenAI's
  Responses API instead, which its reasoning models need to use tools with
  an effort: nothing is stored on the server, and a model's encrypted
  reasoning goes back to that model alone, in opaque blocks.
- Both offer `login`, which asks for an API key, checks it with a request
  that costs nothing, and keeps it for the server in the user's
  configuration directory (`rumbo/credentials/`), readable only by the user;
  `logout`, which removes it; and `account`, which says which credential is
  in use and where it comes from, without showing it.
- **`chatgpt`** uses the person's ChatGPT plan instead of an API key, as
  OpenAI offers to open-source apps that run where the person does. Its
  `login` signs in with ChatGPT in the browser, which comes back to a
  listener on the loopback, or, from another computer, to an address the
  person pastes; OpenAI registers this installation as its client on the
  way. It keeps what that gives with the other credentials, renews it as it
  expires, one process at a time, and forgets it when it can no longer be
  renewed. Its requests go to OpenAI's Responses API as the plan asks:
  nothing stored, the answer streamed, and no limit on the output. It lists
  the models the plan can use, offers an `effort` option, and says where to
  manage the plan's usage, in `account` and when a usage limit is reached.
  `logout` forgets the sign-in and takes it back on OpenAI's side.
- Anthropic's terms do not let other applications use a Claude
  subscription, so no built-in provider does; `anthropic` takes an API key.

## Front end

A front end is what a person uses to give objectives and follow the work. It
speaks the Agent Frontend Protocol with the host.

### Must

- Answer the host's `initialize` with protocol version 1, its name, and its
  start configuration in force, with a default for every field it was not
  given.
- Run the objective the command line gave, or resume the unfinished run,
  when `initialize` asks for it.
- Ignore event types and fields it does not know.
- Not reuse the id of a run in progress, since it names the run.
- Decide what Ctrl-C means, and cancel through the protocol.
- Show the confirmation a function asks for, and call the function only
  after the person agrees.
- Answer a question a provider's function asks through it, with what the
  person typed or with an error, and neither show nor keep a secret answer.
- Wait for an answer when one request has to take effect before another.

### Can

- Start runs in the current session, in another session, or in a new one,
  and choose the model and the options for one run.
- Follow several runs at once, telling their events apart by run and
  session.
- Show what a session held before it was opened, from its history.
- Bring back to its input what the person asked in any of the project's
  sessions, which the host recalls.
- Tell a run something while it works, and offer it as a new task when the
  run ended first.
- Cancel one run, or all of them.
- Ask what the provider offers and call its functions, call the host's
  functions, and build commands, pickers, and completion from their
  descriptions without knowing them beforehand. A function that lists a
  parameter's values is given what was typed for the parameters before it
  that it takes.
- Show what a provider's function says while it works, ask the person what
  it asks, and give the function up.
- Take what it is attached to, its directory, session, provider, and model,
  from the host's answers instead of remembering it.
- Be started by rumbo, with the terminal and with the protocol on file
  descriptors 3 and 4, or start `rumbo serve` itself and speak on its stdin
  and stdout.
- Read the configuration registered with it.
- End the host by closing the connection.

### Built in

**`tui`**, the terminal UI:

- It draws inline, below what the terminal showed before: the
  conversation goes into the terminal's own scrollback, and a live area
  under it shows the line the model is still writing, the menu while a
  command is typed, the input, and the status bar.
- What the model writes shows as the Markdown it is: headings, lists,
  quotes, code, bold, italics, links, and tables with their columns lined
  up. Code keeps its text, so it can be copied as it is.
- A tool call shows in the live area while it works, with its time, and
  then as a block that says how it went: an edit with the lines it changed,
  a command with the start of what it printed, or, when it failed, with its
  status and the end of what it printed. A blank line sets the calls apart
  from the text around them.
- A run ends with a line that says how it ended, how long it took, and its
  turns and tokens.
- A session opened, branched to, or continued shows what it holds first,
  told as its runs were shown.
- Enter sends a task. During a run it tells the run what was typed, which
  shows once the model has it, and goes back to the input if the run ended
  first.
- Its commands are the functions the host and the provider describe, and
  its own, which come before one of theirs of the same name: `/options`,
  `/help`, `/copy`, and `/export`. `/help` lists the host's by what they
  act on: the system, the session, and the providers.
- `/copy` puts the model's last answer in the clipboard, and `/copy code`
  only its last code block, with OSC 52, so it also works over ssh, in the
  terminals that take it; in tmux, with `set-clipboard on`.
- `/export` writes the current session as Markdown, from its history: what
  was asked, what the model wrote, each tool call with the change it made
  or the start of what it returned, and how each run ended. It writes to
  the path given, from the session's directory, or to `rumbo-<session>.md`
  there, and never over a file that exists.
- A slash opens a menu of the commands, with what each does, and after a
  command, of what its next parameter takes, with what the host or the
  provider says of each value: a session's title, the text of a message to
  branch before. It narrows as one types, by value or by what describes
  it; Up and Down choose, Tab or Enter take, and Escape closes it. Enter on
  a command that needs nothing more sends it, and on one that still needs
  something opens the menu of what it takes next, or says what that is:
  `/open` lists the sessions, and `/use openai` the models of `openai`.
  After `/session-options `, the menu offers the values of the provider's
  options as `name=value`, with those in force marked.
- Answers, errors, and diagnostics are marked `[host]` or `[provider
  <name>]`, by where they come from.
- `/new` or `/open` during a run leaves the run working in the background.
  The status bar counts those runs, and a line says when each one ends.
- What a provider's function says shows marked as the provider's. Its
  question takes the next line typed; a secret one shows as dots and is kept
  nowhere, not even in the history.
- The input takes several lines: Enter sends it, and Alt-Enter,
  Shift-Enter, or Ctrl-J starts a new line. Pasted text keeps its line
  breaks. It grows to ten rows, or a third of a short screen, and then
  scrolls with the cursor.
- Up and Down move through the rows typed, and past them through what was
  entered, as Ctrl-P and Ctrl-N do; Ctrl-R searches it. It starts with what
  the host recalls of the project's sessions, and adds what is entered
  while it runs, commands too, which it keeps nowhere; answers to a
  question or a confirmation are not in it.
- Ctrl-C gives up a provider's function in progress, else cancels the run,
  else clears the input, else quits; Ctrl-D quits on an empty input, and
  Ctrl-L starts the screen again, the conversation kept in the scrollback.
- The input edits where the cursor is, with readline's keys: Left, Right,
  Home, and End, or Ctrl-B, Ctrl-F, Ctrl-A, and Ctrl-E, move in it, and by
  words with Ctrl or Alt and the arrows, or Alt-B and Alt-F; Delete or
  Ctrl-D deletes under the cursor, and Ctrl-W, Ctrl-U, and Ctrl-K delete a
  word back, to the start of the line, and to its end.
- The status bar shows what is happening, with the time a run has worked,
  the provider and the model, the session, and the runs in the
  background; and, on the right, how full the context is, as a bar that
  turns yellow past half and red past four fifths, and the tokens the
  session has spent. What does not fit leaves, the least needed first.
- A run in the background that ends says so, with what it took and how to
  open its session. When a run ends, or a provider's function asks
  something, while the terminal is not in front, it rings the terminal's
  bell and sends a desktop notification, in the terminals that report
  their focus and take OSC 9; in tmux, with `focus-events` on.
- Its start configuration chooses its colors: `theme` is `auto`, which
  shows them unless `NO_COLOR` is set, `color`, or `plain`, which shows
  none; and `color_host`, `color_provider`, `color_code`, `color_success`,
  `color_failure`, `color_warning`, and `color_progress` each take a name,
  such as `cyan` or `bright-blue`, a number of the 256 colors, `#rrggbb`,
  or `default`. It says the configuration in force, with its defaults, to
  be kept with its registration, and one it cannot use stops it with what
  is wrong, before it takes the terminal.
- It gives the terminal back as it found it, when it quits or the host ends.

**`plain`**, for pipes and scripts:

- It takes the objective from the command line or from piped input, or
  reads objectives line by line.
- The model's text goes to stdout; progress, the session, and the tokens
  spent go to stderr.
- Ctrl-C cancels the run. It exits with 0 when the run is done, 1 when it
  failed, and 130 when it was cancelled.
