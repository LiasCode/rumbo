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

Result: `{"protocol":1,"name":"my-frontend"}`.

### `run` (request, front end to host)

```json
{"jsonrpc":"2.0","id":1,"method":"run","params":{"objective":"add a --json flag"}}
```

Starts a run on `objective`. Instead of an objective, `{"resume": true}`
continues the session's last run where it stopped: one that failed, was
cancelled, or was cut short by the host dying. It is an error when the last
run finished.

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
JSON-RPC error.

### `cancel` (notification, front end to host)

```json
{"jsonrpc":"2.0","method":"cancel"}
```

Stops the active run. Its `run` request is still answered, with the
`cancelled` status. The session stays usable.

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
| `turn` | `usage` | One model response finished, with its token counts. |
| `log` | `text` | A diagnostic from the provider, or something the host did on its own, such as retrying a request. |

A front end ignores event types and fields it does not know. New ones may be
added without a new protocol version.

## What the host guarantees

- **It never uses the terminal.** Everything the user should see is an event
  or a result. A front end can own the screen completely.
- **It never acts on the terminal's interrupt.** Ctrl-C is the front end's to
  interpret; `cancel` is how to stop a run.
- **Everything is recorded.** The session is stored by the host as it
  happens, so a front end keeps no state it would need to restore.
