# Agent Frontend Protocol

Version 1.

The protocol connects a **front end** to the rumbo host. A front end is what a
person uses: it sends objectives and shows what the agent does. The host owns
the session, the tools, and the loop, and shows nothing itself.

The Go types are in the [`protocol`](../protocol) package (`frontend.go`),
which also contains `DialFrontend`, a ready-made connection for front ends
written in Go. [examples/frontend.py](../examples/frontend.py) is a front end
in Python.

## Transport

Messages are JSON-RPC 2.0 objects, **one per line**, UTF-8. There are two
ways to be connected, with the same messages on both:

| Who starts whom | The front end reads from | The front end writes to |
|---|---|---|
| `rumbo` starts a registered front end | file descriptor 3 | file descriptor 4 |
| The front end starts `rumbo serve` | the host's stdout | the host's stdin |

A registered front end keeps stdin, stdout, and stderr connected to the
terminal, so it can draw and read keys freely. A front end that starts
`rumbo serve` passes it the same flags as `rumbo` (`-c`, `-s`, `-m`) and sets
its working directory to the project.

Closing the connection ends the session: the host cancels a run in progress,
stops the provider, and exits. The exit code of a registered front end becomes
the exit code of `rumbo`.

## Lifecycle

```
front end                                   host
 |<------------------------- initialize ------|
 |-- result --------------------------------->|
 |-- run (id 1) ----------------------------->|
 |<----------------- event (notification) ----|   many
 |<----------------------------- result ------|   when the run ends
 |            ... more runs ...               |
 |-- (closes the connection) ---------------->|
```

## Methods

### `initialize` (request, host to front end)

The host speaks first.

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{
  "protocol": 1,
  "dir": "/home/me/project",
  "session": "4cf0261c",
  "provider": "anthropic",
  "model": "claude-opus-5-5",
  "objective": "fix the failing test",
  "config": {}
}}
```

- `session` is absent for a new session; the first `run` result carries its
  id.
- `objective` is what the user already wrote on the command line, if anything.
  A front end is expected to run it without asking again.
- `resume` is `true` when the user asked to continue the session's unfinished
  run (`rumbo -r`). A front end is expected to send a `run` with `resume`.
- `config` is the JSON registered with the front end (`rumbo frontend add
  -config`). Its meaning is the front end's own.
- `functions` are what the host itself can be asked, whatever the provider.
  See [Functions of the host](#functions-of-the-host).

Result: `{"protocol":1,"name":"my-frontend"}`.

### `run` (request, front end to host)

```json
{"jsonrpc":"2.0","id":1,"method":"run","params":{"objective":"add a --json flag"}}
```

Starts a run on `objective`. Instead of an objective, `{"resume": true}`
continues the session's last run where it stopped: one that failed, was
cancelled, or was cut short by the host dying. It is an error when the last
run finished.

Two optional fields choose, for this run, among what the provider offers:
`model`, and `options`, an object of option values. What is left out keeps
the value in force, which `describe` reports.

The result arrives when the run is over, however it ends:

```json
{"status":"done","session":"4cf0261c"}
```

| `status` | Meaning |
|---|---|
| `done` | The model finished the task. |
| `failed` | The run ended with a problem; `error` explains it. |
| `cancelled` | The front end cancelled it. |

One run is active at a time. A `run` sent during another is answered with a
JSON-RPC error. Other requests can be sent while a run works, without
waiting for its result; each answer carries the id of its request.

### `describe` (request, front end to host)

```json
{"jsonrpc":"2.0","id":2,"method":"describe"}
```

Asks what the provider in use offers and what is in force. It is how a front
end shows a model picker or a settings form for a provider it knows nothing
about.

```json
{"provider": "anthropic",
 "model": "claude-opus-5-5",
 "options": {"effort": "high"},
 "offers": {
   "model": {"type": "string", "values": "models"},
   "options": [{"name": "effort", "type": "string", "enum": ["low", "medium", "high"]}],
   "functions": [{"name": "models", "description": "List the models this account can use"}]}}
```

- `model` and `options` are what a run uses unless it says otherwise: the
  stored settings, with the command line's choices over them.
- `offers` is the provider's own description, passed on unchanged. Its format
  is specified in [the provider protocol](protocol.md#describing-the-provider).
  Any part of it may be absent.

The first `describe` starts the provider, so it can fail for the same reasons
a first run can.

### `invoke` (request, front end to host)

```json
{"jsonrpc":"2.0","id":3,"method":"invoke","params":{"name":"models"}}
```

Calls a function the provider described, with `params` if it takes any. The
host relays the call and the answer without reading either. A function that
an option names in `values` answers with `{"values": [{"value", "label",
"description"}]}`; any other answers with JSON of its own.

With `"target": "host"` the call goes to a function of the host instead; see
[Functions of the host](#functions-of-the-host).

While a run is in progress the provider is busy with it, so `describe` and
the provider's functions are refused with an error. The host's functions
are not: see [Functions of the host](#functions-of-the-host) for which ones
answer during a run.

### `cancel` (notification, front end to host)

```json
{"jsonrpc":"2.0","method":"cancel"}
```

Stops the active run. Its `run` request is still answered, with the
`cancelled` status. The session stays usable.

### `steer` (request, front end to host)

```json
{"jsonrpc":"2.0","id":4,"method":"steer","params":{"text":"use the v2 endpoint instead"}}
```

Tells the active run something while it works. The text reaches the model
before its next request, as the user's words, and the run's objective stays
as it was. If the model was about to finish, the run goes on so the model
reads it.

The result, `{}`, arrives once the text is recorded for the model. An error
means it never will be: no run is in progress, or the run ended first. A
front end can offer the text as a new `run` then.

### `event` (notification, host to front end)

```json
{"jsonrpc":"2.0","method":"event","params":{"type":"text","text":"Let me look"}}
```

What is happening in the active run. `type` selects the fields:

| `type` | Fields | Meaning |
|---|---|---|
| `context` | `sections` | Ambient context sent to the model, by section name. |
| `text` | `text` | A piece of the model's visible answer, in order. |
| `tool_started` | `name`, `args` | The agent calls a tool. |
| `tool_finished` | `name`, `result`, `failed` | The call ended; `result` is what the model receives. |
| `turn` | `usage`, `context`, `window` | One model response finished. `usage` is what it cost; `context` is how many tokens the conversation takes now; `window` is how many the model holds, when the provider said. |
| `log` | `text`, `source` | A diagnostic. `source` is `provider` for the provider's own output and `host` for something the host did on its own, such as retrying a request. |

A front end ignores event types and fields it does not know. New ones may be
added without a new protocol version.

## Functions of the host

Some things a person wants from any front end have nothing to do with the
provider: changing session, changing provider, seeing what a conversation
has cost, quitting. The host offers them as functions, described the same
way a provider describes its own, so a front end can turn them into
commands, menu entries, or buttons without knowing them beforehand. A host
that gains a function gives every front end a new command.

They arrive in `initialize`, as `functions`, each with a `name`, a
`description`, and `params`. A parameter marked `optional` may be left out;
the table below shows those with a `?`. A function with a `confirm` text
cannot be undone: a front end shows that text and calls the function only
after the person agrees. They are called with `invoke` and
`"target": "host"`:

```json
{"jsonrpc":"2.0","id":5,"method":"invoke",
 "params":{"target":"host","name":"use","params":{"provider":"openai","model":"gpt-x"}}}
```

Parameters are an object of text values. Every answer has the same shape:

```json
{"text": "using openai/gpt-x",
 "values": [],
 "state": {"dir": "/home/me/project", "session": "4cf0261c", "provider": "openai", "model": "gpt-x"}}
```

- `text` is the outcome in words, for a person.
- `values` is set by a function that lists something, in the shape a
  provider's value functions use: `value`, `label`, `description`.
- `state` is what the front end is attached to after the call. A function may
  have changed it, so a front end takes its session, provider, and model from
  here instead of remembering them.

The functions of this version:

| Function | Parameters | What it does |
|---|---|---|
| `status` | | The session, the model, the size of the context, and what was spent. |
| `sessions` | | Lists the project's sessions, most recently used first. |
| `open` | `session` | Continues another session of the project. |
| `new` | | Makes the next task start a new session. |
| `delete` | `session` | Deletes a session of the project and its history, for good. Asks to be confirmed. |
| `providers` | | Lists the registered providers. |
| `use` | `provider`, `model` | Changes provider and model for the tasks that follow. The session goes on. |
| `register` | `name`, `command` | Registers a provider. The command is the program and its arguments, separated by spaces. |
| `configure` | `provider`, `change` | Changes one field of a provider's configuration, as `field=value`. An empty value removes it. |
| `inspect` | `provider` | Starts a provider and shows what it offers. Also the test that it works. |
| `unregister` | `provider` | Removes a registered provider. Asks to be confirmed. |
| `settings` | | Shows every setting as it applies to the project, and where each value comes from. |
| `setting` | `key`, `value`, `scope`? | Stores a setting, for the project unless `scope` is `global`. It applies from the next start. |
| `unset` | `key`, `scope`? | Removes a stored setting. |
| `setting-keys` | | Lists the settings there are. |
| `compact` | | Replaces the conversation with a summary now. |
| `quit` | | Stops the provider and ends the host, which closes the connection. A run in progress is cancelled. |

A front end should read the list from `initialize` and not from this table:
the table is what the host offers today.

During a run, the functions that only read or store something answer as
usual: `status`, `sessions`, `providers`, `settings`, `setting-keys`,
`setting`, `unset`, `register`, `unregister`, and `quit`. The others change
the session or the provider, or need a provider, and answer with an error
saying they wait for the run to end.

## What the host guarantees

- **It never uses the terminal.** Everything the user should see is an event
  or a result. A front end can own the screen completely.
- **It never acts on the terminal's interrupt.** Ctrl-C is the front end's to
  interpret; `cancel` is how to stop a run.
- **Everything is recorded.** The session is stored by the host as it
  happens, so a front end keeps no state it would need to restore.
