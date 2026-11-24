# Agent Extension Protocol

Version 1.

The protocol connects the rumbo host to an **extension**: a program that
gives the model tools of its own, beside the ones rumbo has or in place of
one of them, tells it things without being asked, may be told what the
runs do, and offers the person commands of its own. The host declares the tools to the model as the extension declares
them, passes each call to it, and tells the model what its sections say,
without knowing what any of them mean.

The Go types are in the [`protocol`](../protocol) package, which also
contains `ServeExtension`, a ready-made server for extensions written in Go.
[examples/extension.py](../examples/extension.py) is an extension in Python,
with a tool and a section, and [examples/guard.py](../examples/guard.py) one
that takes the place of rumbo's `bash`.

Everything but `initialize` and `shutdown` is optional: an extension has
what it declares, and the host asks it for nothing else.

## Transport

- The host starts the extension as a child process, when the first run after
  it was registered starts, or when a front end first asks what there is to
  command, and keeps it until the host exits. What the
  process holds, such as a connection or what it learned, lasts from one
  call to the next.
- Messages are JSON-RPC 2.0 objects, **one per line**, UTF-8, on the
  extension's stdin (host to extension) and stdout (extension to host).
- stdout carries only protocol messages. Diagnostics go to stderr, which the
  host shows to the user.
- One process serves the host while it lasts, for its run and for what
  the person asks of the extension meanwhile, so the host may send a
  request while another is in progress. An extension may answer them in
  any order, or one at a time; each response names its request by `id`.

## Methods

### `initialize` (request)

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol":1,"name":"notes","config":{}}}
```

`name` is what the user registered the extension as, which a front end
names its commands after. `config` is what the user chose for the extension (`rumbo extension set`,
or its `config` in rumbo's `config.yaml`), as JSON. Its meaning is the
extension's own. Credentials should not be in it: read them from the
environment or from the extension's own storage. An extension that cannot
work with what it was given answers with an error, and the host does not
keep the change that caused it.

Result:

```json
{"protocol": 1, "name": "notes", "tools": [
  {"name": "remember",
   "description": "Keep a note for later.",
   "schema": {"type": "object",
              "properties": {"text": {"type": "string"}},
              "required": ["text"]}}],
 "sections": ["notes"],
 "events": ["ended"],
 "functions": [{"name": "forget", "description": "Forget every note"}]}
```

`tools`, `sections`, `events`, and `functions` are each optional.

The host refuses an extension that answers with a different protocol
version.

**A tool** is declared to the model as it is here:

| Field | Meaning |
|---|---|
| `name` | What the model calls it: letters, digits, `_`, and `-`, at most 64. No other extension's tool may be called the same. One called as a tool of rumbo's, such as `bash`, takes its place, but only once the user says so (`replaces`); until then the host refuses it. |
| `description` | What it does and when to use it, for the model. |
| `schema` | A JSON Schema object for its arguments. |

The declarations are sent to the model with every request of every run, so
they cost tokens whether a task uses the tools or not: declare few tools,
and describe them briefly. They are read when the extension starts, and a
run keeps the ones it started with.

### `call` (request)

```json
{"jsonrpc":"2.0","id":2,"method":"call","params":{
  "name": "remember",
  "args": {"text": "the deploy is on Friday"},
  "dir": "/home/me/project",
  "session": "517cc41a"}}
```

`args` are the arguments the model gave, valid JSON that the host has not
checked against the schema. `dir` is the directory the session works in,
and `session` the session, for an extension that keeps something for each:
a host is one session, and each has a process of the extension.

Result: `{"text": "kept"}`.

| Field | Meaning |
|---|---|
| `text` | What the model reads. |
| `images` | Pictures the model is shown, each `{"media_type": "image/png", "data": "<base64>"}`: PNG, JPEG, GIF, or WebP. |
| `is_error` | The call failed, and `text` says how to recover. |

A JSON-RPC error is a failed call too, with its message as the text. Either
way the run goes on: the model reads what went wrong and decides what to do.

The host bounds what a result adds to the conversation, as it does for its
own tools: a long text is reduced to its beginning and end, with the whole
of it saved to a file the model can search, and a large image is shown
smaller.

### Talking with the person during a call

While it answers a `call`, an extension reaches the person who follows the
run, when someone does. A `delta` notification with the id of the call says
what the call is doing, and shows on the call's line while it works:

```json
{"jsonrpc":"2.0","method":"delta","params":{"id":2,"text":"uploading"}}
```

An `ask` request, with an id of the extension's own and the id of the call
in `params`, asks them something, such as whether a command may run:

```json
{"jsonrpc":"2.0","id":1,"method":"ask","params":{"id":2,"text":"Run `rm -rf build`?"}}
```

The host answers `{"text": "yes"}` with what the person typed, or with an
error when they declined, when the run was cancelled, or when no one
follows the run, as in a script: the extension decides what no answer
means. `"secret": true` asks for an answer that is not shown while it is
typed. Neither the question nor the answer is recorded, and the model sees
neither, only what the call returns.

### `call` (request, extension to host)

While it answers a `call`, an extension can run a tool of rumbo's own with
a request of its own:

```json
{"jsonrpc":"2.0","id":1,"method":"call","params":{
  "id": 2, "name": "bash", "args": {"command": "ls"}}}
```

`params.id` is the id of the call it is made in, which says where the tool
runs and for which session; `id` is the extension's own, and the host
answers it with a result as a `call` has, `is_error` set when the tool
failed. The tools are the ones of rumbo's the run has: `read`, `write`,
`edit`, `bash`, and `computer` when it is on, also when an extension
replaces one of them. That is what lets a replacement put something before
and after rumbo's tool, to refuse some commands or to record each change,
instead of doing the tool's work again. The request is refused once the
call it names is over.

### `section` (request)

**A section** is ambient context: text the model is told when a run starts,
without asking for it. `sections` in the result of `initialize` names the
ones the extension has, each in lowercase letters, digits, `_`, and `-`,
and none called as one of rumbo's or of another extension. Before each run
starts, the host asks for each:

```json
{"jsonrpc":"2.0","id":3,"method":"section","params":{
  "name": "notes", "dir": "/home/me/project", "session": "517cc41a"}}
```

Result: `{"text": "- the deploy is on Friday"}`.

The model is told a section the first time, and again only when its text
changed, so say the same while nothing changed: a section with the time in
it is told anew with every run. An empty `text` means the section does not
apply, and takes away what was told before. The host cuts a long text.

A section is paid for by every task, whether it helps that task or not. It
is for the little that most tasks need; what only some need is better a
tool the model calls.

Every run waits for the answer, for a few seconds at most. When the
extension answers with an error, or too late, the run starts all the same
and the model goes on being told what it was told last.

### `invoke` (request)

**A function** is for the person, not the model: a command a front end
offers, such as a sign-in. `functions` in the result of `initialize`
describes them as [a provider describes its
own](protocol.md#describing-the-provider): a `name`, in lowercase letters,
digits, `_`, and `-`, a `description`, `params`, and `confirm` for one that
cannot be undone. A front end names each command after the extension, so
`forget` of the extension registered as `notes` is `/notes-forget`.
Functions cost no tokens: the model is not told of them.

```json
{"jsonrpc":"2.0","id":7,"method":"invoke","params":{"name":"forget","params":{}}}
```

The result is JSON of the extension's own, which the person is shown: text
as it is, and `{"values": [...]}` as a list, which is also what a function
that a parameter names in `values` answers with. While it works the
function talks with the person as a call does, with `delta` and `ask`
naming the id of the `invoke`, and `cancel` gives it up. It is in no run,
so it cannot call rumbo's tools.

### `changed` (notification, extension to host)

```json
{"jsonrpc":"2.0","method":"changed"}
```

What the extension declared when it started is no longer so: a sign-in gave
it tools, or took them away. The host shuts the extension down and starts
it again before the next run, to hear what it declares now. A run in
progress keeps the tools it started with.

### `event` (notification)

An extension that names event types in `events`, in the result of
`initialize`, is told what the runs of the host do, as a front end is:

```json
{"jsonrpc":"2.0","method":"event","params":{
  "type": "ended", "run": 4, "session": "517cc41a", "status": "done"}}
```

The events are those of the
[Agent Frontend Protocol](frontend-protocol.md#event-notification-host-to-front-end),
with the same fields, of the host's session. Two of them reach
a front end only in a session's history, and an extension as they happen:
`objective`, when a run starts, with what was asked, and `ended`, with how
it ended. So an extension can notify when a run is done, count what the
turns cost, or keep a record of each tool call.

Events only tell: nothing an extension does with one changes the run, and
the host does not wait for it. One that reads too slowly misses events. The
lines extensions print to stderr are not told to extensions.

### `cancel` (notification)

```json
{"jsonrpc":"2.0","method":"cancel","params":{"id":2}}
```

The run that made the call with that `id` was cancelled. The host has
stopped waiting, and drops whatever answer arrives. An extension that
ignores it only does work nobody reads.

### `shutdown` (request)

The host is exiting, or the extension was removed or changed. Answer `{}`
and exit. The host kills a process that has not exited a few seconds later.

## What may come

The protocol grows without a new version, so an extension written today
goes on working:

- The host may send methods and fields that are not here yet. An extension
  ignores a field, a notification, and an event type it does not know, and
  answers a request it does not know with the JSON-RPC error `-32601`.
- An extension gets a new method only after it declares something new in
  the result of `initialize`, as `sections`, `events`, and `functions` are
  declared.
- The host ignores what it does not know in an extension's answers.

## What the host guarantees

- **One process.** An extension is started once and reused; it is started
  again only when it exited, when it said that what it declares changed, or
  when the user changed its command or its configuration, and then with
  the next run.
- **Stable declarations.** The tools are declared after rumbo's own, by
  extension name and then by tool name, so the start of a request does not
  change from one to the next.
- **No tools missing in silence.** A run does not start when an extension
  does not, or when it declares a tool or a section the model cannot be
  given; the person is told which, and how to remove it.
- **Nothing it did not ask for.** An extension gets the calls to its own
  tools and functions and the requests for its own sections, and the events
  of the types it named. It changes a run only through what its tools
  return and its sections say.
