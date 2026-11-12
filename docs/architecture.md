# Architecture

rumbo is a loop around a model, and the things the loop depends on. Each
dependency is an interface, so any of them can be replaced without touching the
others. Two of them cross a process boundary through a protocol, which is what
lets users bring their own: the front end a person uses, and the provider that
reaches the model.

```
 front end process  (built-in tui or plain, or written by the user)
   ^
   |  Agent Frontend Protocol: JSON-RPC, objectives in, events out
   v
 +--------------------- host process (rumbo) ----------------------+
 |  Observer                                                       |
 |     ^                                                           |
 |  +--+---------- agent loop (internal/agent) ------------+       |
 |  |             |              |             |           |       |
 | Provider      Tool         Collector       Store      verify    |
 |  |          (actions)  (ambient context) (history)   (checks)   |
 +--+--------------------------------------------------------------+
    |  Agent Provider Protocol: JSON-RPC, conversation in, message out
    v
 provider process  (built-in anthropic or openai, or written by the user)
```

The host is the only part that is always rumbo. It owns the session, the
tools, and the loop; it shows nothing and knows no model API.

## The parts

**Agent loop** (`internal/agent`). Records the objective, asks the provider
for a message, runs the tool calls in it, and repeats. When the model stops
calling tools, the loop runs the checks; a failure goes back to the model with
the objective restated. The loop knows only interfaces.

**Provider** (`internal/provider`, `protocol`). The only way to reach a model.
The host starts a provider process and speaks the Agent Provider Protocol to
it. The built-in providers (`internal/providers`) are served by the same
binary through `rumbo provide <name>` and get no shortcut: they cross the same
protocol as a provider a user writes. Credentials belong to the provider; the
host never sees them.

**Tools** (`internal/tool`). What the model can do. The set is small because
every declared tool costs tokens on every request, and bash already reaches
everything else.

**Ambient context** (`internal/ambient`). What the harness gathers without
being asked: environment, project instructions, directory tree, git state, and
the checks. Programming always needs these, so the harness collects them with
plain tools instead of letting the model spend turns on it. Each collector
produces a named section.

**Store** (`internal/store`). Settings, registered providers, sessions, and
entries, in one SQLite file.

**Transcript** (`internal/transcript`). Defines what entries mean and rebuilds
the model request from them. This is the single place that decides what the
model sees.

**Verify** (`internal/verify`). Detects and runs the project's checks.

**Front end** (`internal/frontend`, `protocol`). The only way a person
reaches the loop. The host sends it events and receives objectives and
cancellations over the Agent Frontend Protocol. Inside the host, the
connection is the loop's `Observer`. The built-in front ends
(`internal/frontends`) are served by the same binary through `rumbo attach
<name>` and cross the same protocol as one a user writes. A front end either
is started by the host, which leaves it the terminal, or starts the host
itself with `rumbo serve`, as an editor would.

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

**Tool output is bounded.** Results are cut to a fixed size, and the cut tells
the model how to get the rest.

**The host is driven only through the protocol.** It ignores the terminal's
interrupt and writes nothing to the terminal. A front end decides what Ctrl-C
means and cancels with a message; diagnostics, including the provider's
stderr, reach the user as events.

**Stopping is a claim, not a fact.** The model declares the work finished by
not calling tools. The harness checks the claim.
