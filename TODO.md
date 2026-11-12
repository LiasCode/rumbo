# To do

rumbo is a general agent harness: it is not tied to programming, it does not
judge the model's work, and it sends no context beyond the environment and
the user's instructions.

What is left on our side: the loop, configuration, and sessions. Work on the
protocols and on external interfaces waits until this is done.

Items are numbered so they can be referred to. Within a block they are in the
order to do them. Remove an item when it is done.

Measure every change to the loop, the prompt, or the tools with the
evaluation set before and after it: `go run ./evals -model <provider>/<model>`.

## Order

1. Concurrency (19, 56, 57).
2. The remaining blocks.

## Decisions for the owner

- **File restore points** (35): whether sessions should be able to undo
  changes to files.
- **Windows** (39): whether it is in scope.

## 1. Loop

### Correctness

40. **Provider process death.** When the provider exits mid-session the run
    fails and the host does not start it again; only a new `rumbo` does.
41. **Host shutdown on SIGTERM.** A killed host leaves the provider to exit by
    itself on end of input, and does not stop a running tool.

### Context

58. **Old tool results stay in the context until a compaction.** A result
    read once is paid for on every later request. Dropping old ones needs the
    provider's help, because editing earlier turns invalidates the reasoning
    blocks that came after.
44. **Saved output is never removed.** Long command output is saved to the
    system's temporary directory and left there.

### Tools

48. **Background processes end with the host.** A process started with
    `background` is stopped when rumbo exits, so it does not survive between
    one-shot runs, and its log file is left in the temporary directory.
### Provider options

49. **Keeping a choice takes a second step.** `/set` in the terminal UI lasts
    until it closes. Storing it is a separate `/setting model ...` or
    `/setting options.<provider> {...}`.
55. **Stored settings apply from the next start.** The host reads them once,
    so `/setting max_turns 40` does nothing to the session that stored it.
50. **Functions are request and response only.** A provider cannot hold a
    dialogue through one, which a browser login for a subscription needs.
52. **Host functions take only text.** Their parameters are an object of
    text values, and they cannot report progress while they work, which
    `compact` would use.

### Terminal UI

53. **Completion shows values, not what they are.** After `/open`, Tab lists
    session ids without their titles, because the status bar has one line.
54. **The input line is a single line with no cursor movement or history.**

### Concurrency

The owner decided what this covers: these three, in this order.

19. **Talking to the host while it works.** During a run every request is
    refused but cancel. `status` and `sessions` should answer, and a message
    should reach the agent before its next turn.
56. **Several sessions in one host.** A host serves one session at a time, so
    an interface with tabs, or several agents at once, needs a process each.
57. **Subagents.** The agent cannot hand part of a task to another agent.
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
34. **Accumulated usage.** No token totals per session.
35. **File state.** History reproduces what the model was sent, not the
    files: no restore points and no undo.
36. **Branches.** There is no way to go back to an earlier point and continue
    from there.

## 4. Quality

37. **Missing tests.** `cli` and configuration have none.
38. **`rumbo --version`** and a release build.
39. **Windows.** Not supported.
