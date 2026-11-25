# Glossary

**Objective.** What the user asks for in one message, in words, and the
files given with them, which the model is told the paths of. A run works on one objective.

**Run.** The work from an objective until the model stops calling tools, or
the run fails or is cancelled. A session holds one or more runs.

**Steer.** What the user says to a run while it works. It reaches the model
before the next request, as the user's words, and leaves the objective as it
was. A steer that comes as the model finishes keeps the run going.

**Session.** A conversation bound to the directory it was started in, its
root, stored as a list of entries. It keeps its provider, its model, and
the values of the provider's options chosen in it. A rumbo process works
in one, and each has a process of its own.

**Branch.** A session that starts with a copy of another one's entries, up
to now or to just before one of the person's messages, and goes on from
there. The session it came from is left as it was. A branch copies the
conversation only; the files are as they are.

**Entry.** One immutable record in a session. Kinds: `system` (prompt and tool
declarations in force), `context` (ambient sections that changed), `user` (an
objective), `steer`, `assistant` (a model response), `tool_result`, `harness` (a
message from the harness to the model, such as being told a run was
interrupted), `compaction`, and `note`.

**Note.** An entry that records what happened to a run without being part of
the conversation: how it ended (`done`, `failed`, `cancelled`), or a retried
request. The model never sees notes.

**Unfinished run.** A run whose last note is not `done`: it failed, was
cancelled, or was interrupted, which leaves no note because its process
died. It can be resumed.

**Resume.** To continue an unfinished run without a new objective. The model
is told the run was interrupted, and calls that never finished are closed
with an error result.

**Context window.** How many tokens a model's context holds. The provider
reports it, and the host summarizes the conversation before it fills.

**Compaction.** Replacing the conversation the model sees with a summary of
it, when the context is nearly full. It is an entry like any other: what came
before stays stored and the request is rebuilt from the summary on.

**Transient failure.** A failed request that can succeed if the same request
is sent again later: an error the provider marks so, or the provider process
dying, since another process takes its place. The host retries it; any other
failure ends the run.

**Transcript.** The interpretation of a session's entries as the request sent
to the model.

**Turn.** One request to the provider and its response: the model's answer,
or a summary. A session's turns are counted from 1.

**Cache miss.** A response whose provider read less from its cache than the
request before left there, as the usage of both says: what the earlier one
read from the cache and wrote to it is the start the next one repeats. Under
another provider or model, or after a compaction or a new system prompt,
the cache holds nothing for the request, and nothing is missed.

**Provider.** A program that produces assistant messages by speaking the Agent
Provider Protocol. It owns the connection to a model and its credentials.

**Description.** What a provider says about itself: how its model is
chosen, the options it accepts, the fields of its configuration, and the
functions it can be asked. The host passes it on, and checks against it a
value a person sets, without knowing what any of it means.

**Option.** A value a provider accepts with a request, beyond the model,
such as how thorough to be. Which options exist is the provider's decision.
The value a user sets for the provider is a field of its configuration; a
session can choose another, which it keeps and which wins in it.

**Setting.** Anything a person can set, at any level: a system setting; a
provider's model, a field of its configuration, or one of its options; a
field of a front end's configuration; or an option a session chooses. Each
is changed the same way, as `name=value`, where an empty value gives it back
to the level under it: the session's to the provider's, the provider's to
its own default.

**Source.** Where the value of a setting in force comes from: the session
that chose it, `config.yaml`, rumbo's own default, how rumbo has a built-in
program or a plugin brings it, or the program's own default, which only the
program knows.

**Configuration file.** `config.yaml`, in rumbo's directory of the user's
configuration: what the user chose, and nothing else. It holds the system
settings, the programs the user registered, and what they chose for each
program. A person can edit it; a default is never written to it.

**System setting.** A setting of rumbo itself, the same everywhere: the
provider a new session starts with, the front end, the turn limit, the
output limit, when to summarize, and whether the model has the computer
tool. It applies to the runs that start after it is stored. What a
session chooses for itself is kept with the session and wins there; what
a provider takes is kept with the provider, and so is the model a new
session that uses it starts with.

**Function.** An operation offered through `invoke`: by a provider, such as
listing its models, by an extension, such as signing in to the service it
reaches, or by the host, such as changing provider or quitting. A front end
learns them from descriptions and knows none beforehand. A function of the
host says what it acts on: the system, its session, the providers, or the
extensions, and one there to list the values a parameter takes says so, for
a front end to offer its values and not it as a command. A function of a
provider or of an extension can talk with the person while it works, saying
things and asking questions, whose answers may be secret.

**Agent Provider Protocol.** The JSON-RPC contract between the host and a
provider.

**Host.** The rumbo process that owns one session, the tools, and the
loop. It sits between a front end and the providers and speaks a protocol
to each. Another session is another host, which a front end starts.

**Front end.** The program a person uses to give objectives and follow the
work: a terminal UI, a plain printer, an editor extension. It speaks the Agent
Frontend Protocol with the host.

**Agent Frontend Protocol.** The JSON-RPC contract between a front end and
the host.

**Event.** A notification from the host to the front end about a run: text,
a tool call, token usage, a log line.

**History.** What a session holds, told to a front end in the same events as
the runs that made it, plus what the person asked and how each run ended. It
lets a front end show a session it continues as if it had followed every
run.

**Recall.** What the person asked in the sessions of a host's directory,
this one and the others, objectives and steers, newest first and each once, which a front end brings back to its
input. It is read from the sessions, so it keeps nothing of its own.

**Program.** A provider, front end, or extension rumbo can start: a name,
the command that starts it, and its configuration, which for a provider
holds the values of its options too; a provider also has the model a new
session that uses it starts with. `plain`, which rumbo has built in, and
those of rumbo's own plugin are always there, and so are those another
plugin brings while the plugin is added; the user registers others.
Only what the user chose is kept of a configuration: what is not chosen is
the program's own default, which the program says when it starts.

**Tool.** An action the model can request: `read`, `write`, `edit`, `bash`,
`computer` when the user turns it on, and those the user's extensions
declare. A tool's result is text, and may show images too.

**Extension.** A program the user registers that gives the model tools of
its own, and sections of ambient context, by speaking the Agent Extension
Protocol. One process of it serves a host while the host lasts.

**Replacement.** A tool of an extension that takes the place of one of
rumbo's own of the same name, because the user said so. A run then has the
extension's tool and not rumbo's.

**Agent Extension Protocol.** The JSON-RPC contract between the host and an
extension. Specified in [extension-protocol.md](extension-protocol.md).

**MCP.** The Model Context Protocol, a contract of others' by which a
server offers tools to any agent. The `mcp` extension of rumbo's own plugin
speaks it with one server and gives the model that server's tools.

**Computer tool.** The tool that sees and uses the desktop rumbo runs in:
screenshots, the pointer, and the keyboard. Its coordinates are pixels of
the last screenshot it showed.

**Ambient context.** What the harness tells the model without being asked:
the environment it runs in, the user's instructions, and the user's skills.

**Instructions.** The `AGENTS.md` or `CLAUDE.md` file of the project and
that of the user. They are where the user says how work should be done.

**Plugin.** A directory that brings programs, providers, front ends, and
extensions, and skills, all at once, named in its `plugin.yaml`. The user
adds it from a directory or from the address of a Git repository. What it
brings is there without being registered, as what rumbo has built in is,
and is built on the user's machine when its manifest says how.
Described in [plugins.md](plugins.md).

**rumbo's own plugin.** The plugin called `rumbo`, built into the
executable with its programs: rumbo's providers, its terminal front end,
and its extension for MCP servers. It is there before any the user adds,
and cannot be taken out (`plugins/rumbo`).

**Skill.** Instructions for one kind of task, kept by the user, a plugin of
theirs, or the project in a directory with a `SKILL.md`, in the
[Agent Skills](https://agentskills.io) format. The model is told only what
each one is for, and reads the rest when a task needs it.

**Collector.** The source of one section of ambient context.

**Section.** A named piece of ambient context: `environment`,
`instructions`, `skills`, or one an extension declares. A new
version of a section replaces the previous one.

**Project.** The directory rumbo is started in, which is the root of the
sessions started there. Nothing above it is part of it: rumbo does not look
for a git repository, or for anything else, in the directories that hold it.

**Opaque block.** Provider-owned data inside an assistant message that the
host stores and replays without reading.

**Observer.** The interface through which the loop reports a run. The front
end connection implements it by sending events.
