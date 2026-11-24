# Architecture

rumbo is a loop around a model, and the things the loop depends on. Each
dependency is an interface, so any of them can be replaced without touching the
others. Three of them cross a process boundary through a protocol, which is
what lets users bring their own: the front end a person uses, the provider
that reaches the model, and the extensions that give the model more tools.

![The front end process speaks the Agent Frontend Protocol with the host process, rumbo, whose agent loop hears from an Inbox, reports to an Observer, and depends on Provider, Tool, Collector, and Store; the Provider speaks the Agent Provider Protocol with the provider process, and Tool and Collector the Agent Extension Protocol with the extension processes: the user's, a plugin's, or the mcp of rumbo's own plugin.](architecture.svg)

The host is the only part that is always rumbo. It owns one session, the
tools, and the loop; it shows nothing and knows no model API. It is not tied
to one kind of work: what a task is about comes from the user and from the
user's instructions, not from the host.

## The parts

**Agent loop** (`internal/agent`). Records the objective, asks the provider
for a message, runs the tool calls in it, and repeats until the model stops
calling tools. Before each request it records what the user said to the run
meanwhile, from its `Inbox`. Around that it retries requests that fail for a
passing reason, continues responses cut by the output limit, compacts a full
context, and records how the run ended. The loop knows only interfaces.

**Provider** (`internal/provider`, `protocol`). The only way to reach a model.
The host starts provider processes and speaks the Agent Provider Protocol to
each. A process answers one request at a time, so the host keeps a pool of
them per provider and starts another when every one is busy; any of them can
answer any request, since every request carries the whole conversation.
A session uses the provider chosen for it, and keeps it from one start of
rumbo to the next. The providers that come with rumbo, `anthropic`,
`openai`, and `chatgpt`, are its own plugin's (`plugins/rumbo`), served by
the same binary through `rumbo provide <name>`, and get no shortcut: they
cross the same protocol as a provider a user writes. Credentials belong to
the provider, which keeps them where it
chooses; the host keeps none, and relays one a person types at a
provider's question without recording it. A provider also describes
itself: the options it accepts and the functions it can be asked, such as
listing its models, and its default for each option and each field of its
configuration, which the host shows and does not store. The host stores
the chosen values, sends them with every request, and relays function
calls, and what a function says to the person and asks of them
while it works, without knowing what any of them mean.

**Tools** (`internal/tool`). What the model can do: read, write, and edit
files, and run commands. The set is small because every declared tool costs
tokens on every request, and a shell already reaches everything else. A
result is text, and may show the model images as well: `read` shows an
image file as an image, with text that says what it is for a provider that
cannot send it. `internal/picture` prepares every image the model is shown,
a tool's or one a person gives with a task, at a size a model sees whole
and in a form every model API reads. The tools keep a little state: a record of the files the model has seen,
so it cannot replace a file whole without knowing what is in it, the
processes left running in the background, and the files the tools save
for the model. All of it is the host's, and goes when it exits.

**Extensions** (`internal/extension`, `protocol`). How a user gives the model
tools rumbo does not have. An extension is a program the user registers,
which declares tools and runs the calls to them, over the Agent Extension
Protocol. The host declares its tools after its own, in an order that does
not change, and bounds their results as it bounds its own; it knows nothing
else of them. A tool called as one of the host's takes its place when the
user says so, which is how someone runs commands in a sandbox, or limits
them, without the host having a notion of either. An extension may also
have sections of ambient context, which the model is told as it is told
the host's own; call the host's own tools while it answers a call, so a
replacement wraps the tool it replaces; and be told the events a front end
is told. Those are all the ways it reaches a run: it decides what its tools
return and its sections say, and nothing else. It reaches the person too,
through the front end and without the host reading any of it: a call says
what it is doing and asks them something, such as whether to go on, and
its functions are commands in any front end, as a provider's are. One
process of each lasts as long as the host, so what an extension holds,
such as a connection, is there for the next call. The extensions are the user's, the same in every project,
and a run has those registered when it starts: one that does not start, or
whose tools the model cannot be given, stops the run instead of leaving it
with tools missing. rumbo's own plugin has an extension (`plugins/rumbo`),
served by the same binary through `rumbo extend mcp` and added once for
each server, which gives the model the tools of a Model Context Protocol
server. It is where MCP ends: the host
knows only its own protocol, so what comes after MCP is another extension.

**Screen** (`internal/computer`). The desktop rumbo runs in, for the
computer tool, which is declared only when the user turns on the `computer`
setting: a shell starts programs, but cannot show the model what they draw
or press their buttons. It finds out which desktop it is in and acts
through what that desktop offers: on a wlroots compositor such as Hyprland
or Sway, the compositor's virtual pointer and keyboard (spoken by a small
Wayland client of its own, `internal/computer/wayland`) and grim's
screenshots; on X11, xdotool, and maim or ImageMagick for screenshots; on
macOS, Quartz events through osascript and screencapture. On a wlroots
compositor its keyboard is a US one whatever the person's layout, each
key with the code of the key it is, and a character such a keyboard lacks
is typed on a key given it for the moment. The tool shows the model
screenshots of at most 1280×800 and maps the model's coordinates to the
screen, and it acts only on the screen the model saw last: when the monitor
in use changes, or someone else moves the pointer, it does nothing until
the model looks again.

**Ambient context** (`internal/ambient`). The little the model is told
without asking: the environment it runs in, the user's instruction files,
and the user's skills. Of a skill the model is told only what it is for and
where it is; it reads the skill when a task needs it. Each collector
produces a named section. Everything else about a task the model finds out
with its tools, when it needs it.

**Plugins** (`internal/cli/plugins.go`). How a user gets, at once, what
someone else put together: a plugin is a directory, or a Git repository
rumbo keeps a copy of, whose manifest names the providers, front ends, and
extensions it brings, and the command that builds them where it is added,
and which keeps skills. It adds no part of its own:
its programs are the same programs, there without being registered as the
built-in ones are, and its skills are told with the user's. The
configuration file keeps only where a plugin comes from, and which tools of
the host's own the user agreed its extensions replace.
rumbo's own plugin (`plugins/rumbo`) is one more, there before any the
user adds: what comes with rumbo beyond the host, its providers, its
terminal front end, and its extension for MCP servers. Its manifest and its
code are built into the executable, which serves its programs, so it has
no directory, no entry in the configuration file, and no way to be taken
out or updated apart from rumbo. The host reaches its code only through
the package at its root, which starts its programs; everything else of it
is internal to it. What the host keeps of its own is its tools and `plain`,
the front end for pipes and scripts.
[plugins.md](plugins.md) says what is in one and how to write it.

**Store** (`internal/store`). Settings, registered programs, sessions, and
entries, all in files a person can read. What the user chose is one file,
`config.yaml`, next to their instructions and skills: they can edit it, and
rumbo changes it one value at a time, leaving the rest as it was, comments
included. It holds no default, so a value that is there is one somebody set.
Each session is a directory of its own in the user's data: what the session
is, and its entries, one to a line, only ever appended. The copies of the
plugins added from an address are kept there too.

**Transcript** (`internal/transcript`). Defines what entries mean and rebuilds
the model request from them. This is the single place that decides what the
model sees. It also tells a session's history to front ends, in the events
its runs sent, and what the person asked, which front ends bring back.

**Front end** (`internal/frontend`, `protocol`). The only way a person
reaches the loop. The host sends it events and receives objectives, steers,
and cancellations over the Agent Frontend Protocol. Inside the host, the
connection gives each run an `Observer` and an `Inbox`, so what a run does
reaches the front end naming the run and its session. The front ends that
come with rumbo, `tui`, its own plugin's, and `plain`, which it has built in
(`internal/frontends`), are served by the same binary through `rumbo attach
<name>` and cross the same protocol as one a user writes. A front end either
is started by the host, which leaves it the terminal, or starts the host
itself with `rumbo serve`, as an editor would, once for each session it
shows. `tui` does both: one host starts it, and it starts the hosts of the
other sessions the person opens, lists the sessions with `rumbo sessions
-json`, and closes the hosts left unused. What a person can do beyond giving a task, such as changing provider
or compacting, the host offers as described functions, so a front end has commands for them without knowing
them, exactly as it does for a provider's.

**Processes** (`internal/processes`). What a host costs the machine: the
host, the programs it started, and theirs, each with the memory it holds
and the processor time it has used, read from the system's `ps`. The host
says which of them is its front end, a provider's, an extension's, or left
running by the model, and a process under one of those is counted as its.
A front end that holds a host for each session asks each of them, as a
request of the protocol or as the host's function a person reads.

**Command line** (`internal/cli`). Parses arguments, reads the settings, and
wires the parts. It is the only place that knows the implementations. What
rumbo starts with is read when it starts, and what a run uses when the run
starts, so a stored change applies to the next run.
Configuration has three levels, kept apart. The system settings are
rumbo's own, the same everywhere. A session keeps what it chose, which wins
over them there: its provider, its model, and values of the provider's
options. A provider has the model a new session that uses it starts with,
and one configuration: what it starts with, and the values of its options,
which it gets with every request under those a session chose. The programs
rumbo has built in, and those plugins bring, its own among them, are there
without being registered; what the user chooses for one
is all that is kept of it. Every level is changed the same way, as
`name=value`, where an empty value gives a setting back to the level under
it (`changes.go`), and every value is checked before it is kept: a
provider's against what the provider describes, and by starting it with
the change; and what is written in the file by hand when the provider
starts. What a system setting is, what a provider's options are, and
what a registered program is each have one owner there (`settings.go`,
`options.go`, `programs.go`), and so has what a person is shown of all of
it, each value with where it comes from (`view.go`). The commands and the
host's functions, each named for the level it acts on, are two ways into
the same code, so configuring from a terminal and from a front end cannot
drift apart.

## Rules the design depends on

**History is append-only.** Entries are never edited or deleted. Changes to
the system prompt or to the ambient context are new entries. Going back to
an earlier point makes a branch: a new session that starts with a copy of
the entries up to it, so its first request is one the provider has seen.

**The request is a pure function of the entries.** `transcript.Build` is the
only code that turns a session into a request. Nothing else is added on the
way to the provider, so a stored session reproduces every request exactly,
and `rumbo request` prints any of them. Anything that should influence the
model must be an entry first.

**The prefix is stable.** The system prompt is constant, tool declarations
are deterministic, and ambient context is sent once and then only as changed
sections. Each request therefore extends the previous one, which is what lets
providers cache the prefix. The host checks that they do: a response that
read less from the cache than the request before left there is a cache miss,
and the person is told, since the rest is paid for again.

**The harness adds as little as it can.** The system prompt is short,
and the only context sent unasked is the environment, the user's
instructions, what each of the user's skills is for, and the sections of
the extensions the user registered. Whatever else the
harness sent would be paid for on every
request, by every task, whether it helped that task or not.

**A full context is compacted, not trimmed.** The provider reports how many
tokens the model's context holds, and the conversation is summarized at 80%
of that, or at 150,000 tokens when the provider does not say. The
`compact_at` setting changes the share. When the limit is reached the model is asked to
summarize, and a compaction entry makes the next request start from the
ambient context and that summary alone. A request that does not fit anyway is
recovered the same way, without the step that tipped it over. No recent turns are kept alongside it: turns replayed after a
rewritten beginning are what model APIs reject, and a summary written for the
purpose carries the work better than a cut. The entries before the compaction
stay in the session; only the request changes.

**Every run leaves its ending in the session.** A run closes with a note:
done, failed, or cancelled. A run without one was killed. That is what makes
resuming possible, and why a failure is never only on the screen.

**A rumbo process is one session.** A host starts in a new session or
continues one, works in it one run at a time, and ends with it: it does not
list the others, move to another, or run in two. So each session has its
own settings as they were read, its own provider and extension processes,
and its own tools, and what goes wrong in one stays there. A front end that
shows several sessions starts a host for each, groups them as it likes, by
their roots for one, and closes those it does not need: what many
processes cost is its to manage. The host holds a lock on its session from
the first work in it until it ends, so two processes cannot interleave
entries into a history that neither of them sent.

**Tool output is bounded.** A result stays in the context and is paid for on
every later request, so results are kept small: a file is read in windows,
and long command output is reduced to its beginning and end, with the whole
of it saved to a file the model can search while the host lasts.

**The host is driven only through the protocol.** It ignores the terminal's
interrupt and writes nothing to the terminal. A front end decides what Ctrl-C
means and cancels with a message; diagnostics, including the provider's
stderr, reach the user as events. A request to stop the process, SIGTERM or
SIGHUP, is not the front end's to interpret: the host ends as it does when
the front end disconnects, with its runs cancelled and recorded, and its
tools and providers stopped.

**The model decides when the work is done.** A run ends when the model stops
calling tools. The harness does not judge the result; checking the work is
part of the task, and the user's instructions say how.

**A session's root is where it was started.** The directory rumbo is started
in is the session's root: where it works, what it is listed and continued
by, and the one place besides the user's own whose instructions and skills
the model is told. Nothing above it is looked at, the top of a git
repository included, so what a session is does not depend on what holds its
directory. Settings are not the directory's: they are the same everywhere.
