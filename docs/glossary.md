# Glossary

**Objective.** What the user asks for in one message. A run works on one
objective.

**Run.** The work from an objective until the agent stops and the checks pass
(or fail for good). A session holds one or more runs.

**Session.** A conversation bound to a project directory, stored as a list of
entries.

**Entry.** One immutable record in a session. Kinds: `system` (prompt and tool
declarations in force), `context` (ambient sections that changed), `user` (an
objective), `assistant` (a model response), `tool_result`, `harness` (a
message from the harness to the model, such as a failed check), and `note`.

**Note.** An entry that records what happened to a run without being part of
the conversation: how it ended (`done`, `failed`, `cancelled`), a retried
request, checks that passed. The model never sees notes.

**Unfinished run.** A run whose last note is not `done`, or that has none
because the process died. It can be resumed.

**Resume.** To continue an unfinished run without a new objective. The model
is told the run was interrupted, and calls that never finished are closed
with an error result.

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
a tool call, a check result, token usage, a log line.

**Program.** A registered provider or front end: a name, the command that
starts it, and its configuration.

**Tool.** An action the model can request: `read`, `write`, `edit`, `bash`.

**Ambient context.** Information the harness collects on its own and gives the
model without being asked.

**Collector.** The source of one section of ambient context.

**Section.** A named piece of ambient context, such as `tree` or `git`. A new
version of a section replaces the previous one.

**Checks.** Commands that must succeed for the work to count as done. Detected
from the project or set with the `checks` setting. They run when the agent
stops and the project's files have changed since the run started.

**Project.** The directory tree the work belongs to, identified by its root:
the top of the git repository, or the directory itself outside one.

**Snapshot.** A fingerprint of the project's files, by path, size, and
modification time. Two equal snapshots mean nothing was touched in between.

**Opaque block.** Provider-owned data inside an assistant message that the
host stores and replays without reading.

**Observer.** The interface through which the loop reports a run. The front
end connection implements it by sending events.
