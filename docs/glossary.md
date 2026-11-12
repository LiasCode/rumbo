# Glossary

**Objective.** What the user asks for in one message. A run works on one
objective.

**Run.** The work from an objective until the model stops calling tools, or
the run fails or is cancelled. A session holds one or more runs.

**Session.** A conversation bound to a project directory, stored as a list of
entries.

**Entry.** One immutable record in a session. Kinds: `system` (prompt and tool
declarations in force), `context` (ambient sections that changed), `user` (an
objective), `assistant` (a model response), `tool_result`, `harness` (a
message from the harness to the model, such as being told a run was
interrupted), `compaction`, and `note`.

**Note.** An entry that records what happened to a run without being part of
the conversation: how it ended (`done`, `failed`, `cancelled`), or a retried
request. The model never sees notes.

**Unfinished run.** A run whose last note is not `done`, or that has none
because the process died. It can be resumed.

**Resume.** To continue an unfinished run without a new objective. The model
is told the run was interrupted, and calls that never finished are closed
with an error result.

**Context window.** How many tokens a model's context holds. The provider
reports it, and the host summarizes the conversation before it fills.

**Compaction.** Replacing the conversation the model sees with a summary of
it, when the context is nearly full. It is an entry like any other: what came
before stays stored and the request is rebuilt from the summary on.

**Transient failure.** A provider error that can succeed if the same request
is sent again later. The host retries it; any other failure ends the run.

**Transcript.** The interpretation of a session's entries as the request sent
to the model.

**Turn.** One request to the provider and its response.

**Provider.** A program that produces assistant messages by speaking the Agent
Provider Protocol. It owns the connection to a model and its credentials.

**Description.** What a provider says about itself: how its model is
chosen, the options it accepts, and the functions it can be asked. The host
passes it on without interpreting it.

**Option.** A value a provider accepts with a request, beyond the model,
such as how thorough to be. Which options exist is the provider's decision.

**Function.** An operation offered through `invoke`: by a provider, such as
listing its models, or by the host, such as changing session or quitting. A
front end learns both from descriptions and knows neither beforehand.

**Agent Provider Protocol.** The JSON-RPC contract between the host and a
provider.

**Host.** The rumbo process that owns the session, the tools, and the loop.
It sits between a front end and a provider and speaks a protocol to each.

**Front end.** The program a person uses to give objectives and follow the
work: a terminal UI, a plain printer, an editor extension. It speaks the Agent
Frontend Protocol with the host.

**Agent Frontend Protocol.** The JSON-RPC contract between a front end and
the host.

**Event.** A notification from the host to the front end about a run: text,
a tool call, token usage, a log line.

**Program.** A registered provider or front end: a name, the command that
starts it, and its configuration.

**Tool.** An action the model can request: `read`, `write`, `edit`, `bash`.

**Ambient context.** What the harness tells the model without being asked:
the environment it runs in and the user's instructions.

**Instructions.** The `AGENTS.md` or `CLAUDE.md` files of the project and of
the user. They are where the user says how work should be done.

**Collector.** The source of one section of ambient context.

**Section.** A named piece of ambient context: `environment` or
`instructions`. A new
version of a section replaces the previous one.

**Project.** The directory tree the work belongs to, identified by its root:
the top of the git repository, or the directory itself outside one.

**Opaque block.** Provider-owned data inside an assistant message that the
host stores and replays without reading.

**Observer.** The interface through which the loop reports a run. The front
end connection implements it by sending events.
