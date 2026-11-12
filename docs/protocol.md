# Agent Provider Protocol

Version 1.

The protocol connects the rumbo host to a **provider**: a program that turns a
conversation into the next assistant message. It plays the role the Language
Server Protocol plays for editors: the host does not know how a provider
reaches its model, and a provider written once works with any host.

The Go types are in the [`protocol`](../protocol) package, which also contains
`Serve`, a ready-made server for providers written in Go.
[examples/provider.py](../examples/provider.py) is a provider in Python.

## Transport

- The host starts the provider as a child process.
- Messages are JSON-RPC 2.0 objects, **one per line**, UTF-8, on the
  provider's stdin (host to provider) and stdout (provider to host).
- stdout carries only protocol messages. Diagnostics go to stderr, which the
  host shows to the user.
- The host sends one `complete` at a time.

## Lifecycle

```
host                                   provider
 |-- initialize ------------------------->|
 |<------------------------- result ------|
 |-- complete (id 2) -------------------->|
 |<-------------- delta (notification) ---|   zero or more
 |<------------------------- result ------|
 |            ... more completes ...      |
 |-- shutdown --------------------------->|
 |<------------------------- result ------|   then the provider exits
```

## Methods

### `initialize` (request)

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol":1,"config":{}}}
```

`config` is the JSON registered with the provider (`rumbo provider add
-config`). Its meaning is the provider's own. Credentials should not be in it:
read them from the environment or from the provider's own storage.

Result: `{"protocol":1,"name":"my-provider"}`. The host refuses a provider
that answers with a different protocol version.

### `complete` (request)

```json
{"jsonrpc":"2.0","id":2,"method":"complete","params":{
  "model": "some-model",
  "system": "You are a coding agent...",
  "messages": [ ... ],
  "tools": [{"name":"bash","description":"...","schema":{ JSON Schema }}],
  "max_tokens": 0
}}
```

- `messages` is the whole conversation, every time. Roles alternate between
  `user` and `assistant`, starting with `user`.
- `max_tokens` of `0` or absent means the provider's default.

Result:

```json
{"message":{"role":"assistant","content":[ ... ]},
 "stop":"end",
 "usage":{"input":1200,"output":85,"cache_read":0,"cache_write":0}}
```

`stop` is one of:

| Value | Meaning |
|---|---|
| `end` | The model finished its message. |
| `tool_calls` | The message contains tool calls for the host to run. |
| `max_tokens` | The output limit cut the message. |
| `refusal` | The model declined the request. |

A failure is a JSON-RPC error; its `message` is shown to the user.

An error with `code` `1` is **transient**: a rate limit, an overloaded
server, a dropped connection. The host waits and sends the same request
again, a few times, with growing pauses. Use it only for failures that can
succeed unchanged later; any other code ends the run at once.

```json
{"jsonrpc":"2.0","id":2,"error":{"code":1,"message":"429 rate limited"}}
```

### `delta` (notification, provider to host)

```json
{"jsonrpc":"2.0","method":"delta","params":{"id":2,"text":"Let me look"}}
```

Visible text of the running completion `id`, for display. Optional. The
`complete` result is the authoritative message.

### `cancel` (notification, host to provider)

```json
{"jsonrpc":"2.0","method":"cancel","params":{"id":2}}
```

Abort completion `id`. The provider must still answer the request, normally
with an error. The host kills a provider that does not answer within a few
seconds.

### `shutdown` (request)

Answer with `{}` and exit.

## Content blocks

A message's `content` is a list of blocks.

| `type` | Fields | Appears in |
|---|---|---|
| `text` | `text` | both roles |
| `tool_call` | `id`, `name`, `args` (JSON) | assistant |
| `tool_result` | `id` (of the call), `text`, `is_error` | user |
| `opaque` | `data` (any JSON) | assistant |

**`tool_call`.** `args` should be the JSON object the model produced. If the
model produced something that is not valid JSON, send it as a JSON string: the
host rejects it and tells the model.

**`tool_result`.** Every `tool_call` of an assistant message has a
`tool_result` in the next user message, before any text.

**`opaque`.** State the provider needs back to continue the conversation, such
as signed reasoning. The host stores it and returns it in place, unread and
unchanged. A provider ignores opaque blocks it does not recognize, which is
what makes switching providers mid-session safe.

## What the host guarantees

- **Append-only history.** The `messages` of a request extend those of the
  previous request in the session; earlier content never changes. `system` and
  `tools` are stable across a session. A provider can cache the prefix.
- **Complete tool results.** A call interrupted before it finished gets an
  error result, so the history is always valid.
