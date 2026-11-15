# To do

rumbo is a general agent harness: it is not tied to programming, it does not
judge the model's work, and it sends no context beyond the environment and
the user's instructions.

What is left on our side: the loop, configuration, and sessions.

Items are numbered so they can be referred to. Within a block they are in the
order to do them. Remove an item when it is done.

Measure every change to the loop, the prompt, or the tools with the
evaluation set before and after it: `go run ./evals -model <provider>/<model>`.

## Order

1. The remaining blocks, in order.
2. Subagents (57), which the owner postponed.

## Decisions for the owner

- **File restore points** (35): whether sessions should be able to undo
  changes to files.
- **Windows** (39): whether it is in scope.

## 1. Loop

### Context

58. **Old tool results stay in the context until a compaction.** A result
    read once is paid for on every later request. Dropping old ones needs the
    provider's help, because editing earlier turns invalidates the reasoning
    blocks that came after.

### Tools

48. **Background processes end with the host.** A process started with
    `background` is stopped when rumbo exits, so it does not survive between
    one-shot runs.
### Provider options

52. **Host functions take only text.** Their parameters are an object of
    text values, and they cannot report progress while they work, which
    `compact` would use.

### Terminal UI

53. **Completion shows values, not what they are.** After `/open`, Tab lists
    session ids without their titles, because the status bar has one line.
54. **The input is a single line.** Its history lasts as long as the
    terminal UI.

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

## 3. Sessions

30. **Reproduce a request.** `log` prints entries. A command should print the
    exact request of turn N, which is the proof that history is reproducible.
31. **Entry metadata.** Duration, rumbo version, and request parameters are
    not stored.
32. **Useful listing.** `sessions` shows only the current project, and no
    turns, tokens, or state (finished, failed, unfinished).
33. **Rename and prune.** A session can be deleted, one at a time and from a
    front end only. There is no renaming, no deleting by age, and no
    `rumbo sessions rm`.
35. **File state.** History reproduces what the model was sent, not the
    files: no restore points and no undo.
36. **Branches.** There is no way to go back to an earlier point and continue
    from there.

## 4. Quality

39. **Windows.** Not supported.
