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

The result may also carry what the provider says about itself. See
[Describing the provider](#describing-the-provider).

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
- `options`, when present, is an object with the values chosen for the
  options the provider described. Answer with an error for a value or a name
  you do not accept: the host does not check them.

Result:

```json
{"message":{"role":"assistant","content":[ ... ]},
 "stop":"end",
 "usage":{"input":1200,"output":85,"cache_read":0,"cache_write":0},
 "context_window":200000}
```

`context_window` is how many tokens the model's context holds. It is
optional, and worth sending when the provider knows it: the host uses it to
summarize the conversation before the context fills, which matters most for
models with small contexts. Without it the host works to a fixed limit.

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

An error with `code` `2` says the request **did not fit** in the model's
context. The host summarizes the conversation, leaving out the last step,
which is what tipped it over, and sends the shorter request. A second such
error in a row ends the run.

### `invoke` (request)

```json
{"jsonrpc":"2.0","id":3,"method":"invoke","params":{"name":"models","params":{}}}
```

Calls one of the functions the provider described. The result is whatever
JSON the function returns; the host passes it to whoever asked, unread. A
provider that described no functions answers with an error.

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

## Describing the provider

A provider can say what may be chosen and asked of it. The host understands
none of it: it hands the description to the front end, sends the chosen
values back with every `complete`, and relays `invoke`. That is how a front
end offers a provider's settings without knowing the provider, and how a
provider adds a setting without anything else changing.

The description travels in the `initialize` result. Every part is optional.

```json
{"protocol": 1, "name": "my-provider",
 "model":   {"type": "string", "values": "models", "description": "Model to use"},
 "options": [
   {"name": "effort", "type": "string", "enum": ["low", "high"], "default": "low",
    "description": "How thorough the model is"},
   {"name": "web", "type": "boolean", "description": "Let the model search the web"}],
 "functions": [
   {"name": "models", "description": "List the available models"}]}
```

**An option** is one value the provider accepts with a request:

| Field | Meaning |
|---|---|
| `name` | The key it has in `options`. |
| `type` | `string`, `number`, or `boolean`. |
| `description` | For a person choosing it. |
| `enum` | The accepted values of a string option, when they are fixed. |
| `values` | The name of a function that returns the accepted values, when they are not. |
| `default` | What applies when nothing is chosen. For display only. |
| `optional` | For a parameter of a function: it may be left out. Optional parameters come last. |

This is a small subset of JSON Schema on purpose: a front end can offer it
without a validator.

**`model`** describes the model the same way, without a `name`. The model is
not an option: it stays the `model` field of `complete`, because every
provider has one and the host records it with each answer.

**A function** has a `name`, a `description`, and `params`, a list of options
describing its arguments. What it does and returns is the provider's own. A
function that cannot be undone adds `confirm`, a sentence saying what it
does; a front end shows it and calls the function only after the person
agrees.

One convention ties the two together. A function that an option names in
`values` returns the accepted values in this shape, so a front end can build
a picker from any provider:

```json
{"values": [{"value": "big-1", "label": "Big 1", "description": "Slow and careful"}]}
```

Only `value` is required.

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

An opaque block is treated as valid only after the exact conversation that
produced it. When the host changes what comes before, it does not send the
block back: after a compaction nothing earlier is replayed at all, and after
a change of system prompt the earlier opaque blocks are left out.

## What the host guarantees

- **Append-only history.** The `messages` of a request extend those of the
  previous request in the session; earlier content never changes. `system` and
  `tools` are stable across a session. A provider can cache the prefix. The
  one exception is a compaction: when the context is nearly full, the host
  starts the conversation over from a summary, and the prefix is new from
  there on.
- **Summaries come through `complete`.** To compact, the host sends the
  conversation with one more instruction and takes the text of the answer as
  the summary. A provider needs nothing special for it.
- **Complete tool results.** A call interrupted before it finished gets an
  error result, so the history is always valid.
