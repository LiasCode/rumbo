# To do

rumbo is a general agent harness: it is not tied to programming, it does not
judge the model's work, and it sends no context beyond the environment, the
user's instructions, and what the user's skills are for.

What is left on our side: the loop and configuration.

Items are numbered so they can be referred to. Within a block they are in the
order to do them. Remove an item when it is done.

Measure every change to the loop, the prompt, or the tools with the
evaluation set before and after it: `go run ./evals -model <provider>/<model>`.

## Order

1. The remaining blocks, in order.
2. Subagents (57), which the owner postponed.

## Decisions for the owner

- **Windows** (39): whether it is in scope.

## 1. Loop

### Context

58. **Old tool results stay in the context until a compaction.** A result
    read once is paid for on every later request. Dropping old ones needs the
    provider's help, because editing earlier turns invalidates the reasoning
    blocks that came after. Screenshots make it costlier: each is about 1,400
    tokens on every request after it.

### Tools

48. **Background processes end with the host.** A process started with
    `background` is stopped when rumbo exits, so it does not survive between
    one-shot runs.

### Images

60. **Front ends do not see a tool's images.** A tool's events carry its
    text, so a person following a run is not shown the screenshots the
    model is; the images a person gave are in the history.

### Provider options

52. **Host functions take only text.** Their parameters are an object of
    text values, and they cannot report progress while they work, which
    `compact` would use.

### Concurrency

The owner decided what this covers: these, in this order.

57. **Subagents.** The agent cannot hand part of a task to another agent.
    Postponed. The design discussed: a tool, off by default behind a
    setting, that runs a task in a child session of the same host and
    answers with its final message; calls in one response run at once; a
    parent column on sessions.
20. **Changing model mid-session.** It works, but is untested with reasoning
    blocks left by another provider.

## 2. Configuration

25. **Configurable limits.** Tool output size and timeouts are fixed in the
    code.

## 4. Quality

39. **Windows.** Not supported.
61. **The computer tool on macOS** is written, and has not been run on a
    Mac.
