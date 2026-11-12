# Architecture

rumbo is a loop around a model, and the things the loop depends on. Each
dependency is an interface, so any of them can be replaced without touching the
others. Two of them cross a process boundary through a protocol, which is what
lets users bring their own: the front end a person uses, and the provider that
reaches the model.

```
 front end process  (built-in tui or plain, or written by the user)
   ^
   |  Agent Frontend Protocol: JSON-RPC, tasks in, events out
   v
 +------------------- host process (rumbo) --------------------+
 |  Observer                                                   |
 |     ^                                                       |
 |  +--+-------- agent loop (internal/agent) -----------+      |
 |  |              |               |                    |      |
 | Provider       Tool         Collector              Store    |
 |  |           (actions)   (ambient context)       (history)  |
 +--+----------------------------------------------------------+
    |  Agent Provider Protocol: JSON-RPC, conversation in, message out
    v
 provider process  (built-in anthropic or openai, or written by the user)
```

The host is the only part that is always rumbo. It owns the session, the
tools, and the loop; it shows nothing and knows no model API. It is not tied
to one kind of work: what a task is about comes from the user and from the
user's instructions, not from the host.

## The parts

**Agent loop** (`internal/agent`). Records the objective, asks the provider
for a message, runs the tool calls in it, and repeats until the model stops
calling tools. Around that it retries requests that fail for a passing
reason, continues responses cut by the output limit, compacts a full context,
and records how the run ended. The loop knows only interfaces.

**Provider** (`internal/provider`, `protocol`). The only way to reach a model.
The host starts a provider process and speaks the Agent Provider Protocol to
it. The built-in providers (`internal/providers`) are served by the same
binary through `rumbo provide <name>` and get no shortcut: they cross the same
protocol as a provider a user writes. Credentials belong to the provider; the
host never sees them. A provider also describes itself: the options it
accepts and the functions it can be asked, such as listing its models. The
host stores the chosen values, sends them with every request, and relays
function calls, without knowing what any of them mean.

**Tools** (`internal/tool`). What the model can do: read, write, and edit
files, and run commands. The set is small because every declared tool costs
tokens on every request, and a shell already reaches everything else. The
tools share a little state for the life of the host: which files the model
has seen, so it cannot replace one whole without knowing what is in it, and
which processes it left running in the background, so they are stopped when
the host exits.

**Ambient context** (`internal/ambient`). The little the model is told
without asking: the environment it runs in, and the user's instruction files.
Each collector produces a named section. Everything else about a task the
model finds out with its tools, when it needs it.

**Store** (`internal/store`). Settings, registered programs, sessions, and
entries, in one SQLite file.

**Transcript** (`internal/transcript`). Defines what entries mean and rebuilds
the model request from them. This is the single place that decides what the
model sees.

**Project** (`internal/project`). Says where the project a directory belongs
to starts.

**Front end** (`internal/frontend`, `protocol`). The only way a person
reaches the loop. The host sends it events and receives objectives and
cancellations over the Agent Frontend Protocol. Inside the host, the
connection is the loop's `Observer`. The built-in front ends
(`internal/frontends`) are served by the same binary through `rumbo attach
<name>` and cross the same protocol as one a user writes. A front end either
is started by the host, which leaves it the terminal, or starts the host
itself with `rumbo serve`, as an editor would. What a person can do beyond
giving a task, such as changing session or provider, the host offers as
described functions, so a front end has commands for them without knowing
them, exactly as it does for a provider's.

**Command line** (`internal/cli`). Parses arguments, reads settings once, and
wires the parts. It is the only place that knows the implementations.

## Rules the design depends on

**History is append-only.** Entries are never edited or deleted. Changes to
the system prompt or to the ambient context are new entries.

**The request is a pure function of the entries.** `transcript.Build` is the
only code that turns a session into a request. Nothing else is added on the
way to the provider, so a stored session reproduces every request exactly.
Anything that should influence the model must be an entry first.

**The prefix is stable.** The system prompt is constant, tool declarations
are deterministic, and ambient context is sent once and then only as changed
sections. Each request therefore extends the previous one, which is what lets
providers cache the prefix.

**The harness adds as little as it can.** The system prompt is a few lines,
and the only context sent unasked is the environment and the user's
instructions. Whatever else the harness sent would be paid for on every
request, by every task, whether it helped that task or not.

**A full context is compacted, not trimmed.** When the conversation reaches
the `compact_at` size, the model is asked to summarize it, and a compaction
entry makes the next request start from the ambient context and that summary
alone. No recent turns are kept alongside it: turns replayed after a
rewritten beginning are what model APIs reject, and a summary written for the
purpose carries the work better than a cut. The entries before the compaction
stay in the session; only the request changes.

**Every run leaves its ending in the session.** A run closes with a note:
done, failed, or cancelled. A run without one was killed. That is what makes
resuming possible, and why a failure is never only on the screen.

**One process per session.** The host holds a lock on the session it works
in, so two processes cannot interleave entries into a history that neither of
them sent.

**Tool output is bounded.** A result stays in the context and is paid for on
every later request, so results are kept small: a file is read in windows,
and long command output is reduced to its beginning and end, with the whole
of it saved to a file the model can search.

**The host is driven only through the protocol.** It ignores the terminal's
interrupt and writes nothing to the terminal. A front end decides what Ctrl-C
means and cancels with a message; diagnostics, including the provider's
stderr, reach the user as events.

**The model decides when the work is done.** A run ends when the model stops
calling tools. The harness does not judge the result; checking the work is
part of the task, and the user's instructions say how.

**A project is its root.** Settings and sessions belong to the top of the git
repository, and are found from any directory inside it. A session still works
in the directory it was started in.
