# Glossary

**Objective.** What the user asks for in one message. A run works on one
objective.

**Run.** The work from an objective until the agent stops and the checks pass
(or fail for good). A session holds one or more runs.

**Session.** A conversation bound to a project directory, stored as a list of
entries.

**Entry.** One immutable record in a session. Kinds: `system` (prompt and tool
declarations in force), `context` (ambient sections that changed), `user` (an
objective), `assistant` (a model response), `tool_result`, and `harness` (a
message from the harness to the model, such as a failed check).

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
from the project or set with the `checks` setting.

**Opaque block.** Provider-owned data inside an assistant message that the
host stores and replays without reading.

**Observer.** The interface through which the loop reports a run. The front
end connection implements it by sending events.
