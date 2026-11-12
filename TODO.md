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

1. Permissions (18), once the owner decides the model.
2. Steering (19) and the remaining blocks.

## Decisions for the owner

- **Permission model** (18): confirm each action, an allow list, or a sandbox.
- **File restore points** (35): whether sessions should be able to undo
  changes to files.
- **Windows** (39): whether it is in scope.

## 1. Loop

### Correctness

42. **Context size is a setting, not a fact.** `compact_at` is a fixed number
    of tokens because the host does not know the model's window. It is right
    for 200K models and early for larger ones. Needs the provider to report
    the window, which is protocol work.
43. **Compact on overflow.** A provider error saying the prompt is too long
    ends the run instead of compacting and trying again.
40. **Provider process death.** When the provider exits mid-session the run
    fails and the host does not start it again; only a new `rumbo` does.
41. **Host shutdown on SIGTERM.** A killed host leaves the provider to exit by
    itself on end of input, and does not stop a running tool.

### Context

13. **Bound the instructions.** A very large `AGENTS.md` is injected whole.
44. **Saved output is never removed.** Long command output is saved to the
    system's temporary directory and left there.

### Tools

48. **Background processes end with the host.** A process started with
    `background` is stopped when rumbo exits, so it does not survive between
    one-shot runs, and its log file is left in the temporary directory.
18. **Permissions.** There are none: the agent can write outside the project
    and run any command.

### Provider options

49. **Choices made in a front end are not stored.** `/set` in the terminal UI
    lasts until it closes; only `rumbo config` and `-o` reach the database.
50. **Functions are request and response only.** A provider cannot hold a
    dialogue through one, which a browser login for a subscription needs.
52. **Host functions take only text.** Their parameters are an object of
    text values, and they cannot report progress while they work, which
    `compact` would use.

### Steering

19. **Messages during a run.** The loop accepts no correction from the user
    between turns; it can only be cancelled.
20. **Changing model mid-session.** It works, but is untested with reasoning
    blocks left by another provider.

## 2. Configuration

22. **Validate on save.** `config set` accepts any value; the error appears
    when running.
23. **`config unset`.** A setting cannot be removed.
24. **Show the effective value.** `config get` looks at one scope. It does not
    say what will apply or where it comes from.
25. **Configurable limits.** Tool output size and timeouts are fixed in the
    code.
27. **Edit a registered program.** Changing a provider's config means running
    `add` again in full.
28. **Built-in programs.** Deleted ones come back on the next start; they
    cannot be disabled.

## 3. Sessions

30. **Reproduce a request.** `log` prints entries. A command should print the
    exact request of turn N, which is the proof that history is reproducible.
31. **Entry metadata.** Duration, rumbo version, and request parameters are
    not stored.
32. **Useful listing.** `sessions` shows only the current project, and no
    turns, tokens, or state (finished, failed, unfinished).
33. **Delete, rename, prune.** The database only grows.
34. **Accumulated usage.** No token totals per session.
35. **File state.** History reproduces what the model was sent, not the
    files: no restore points and no undo.
36. **Branches.** There is no way to go back to an earlier point and continue
    from there.

## 4. Quality

37. **Missing tests.** `cli` and configuration have none.
38. **`rumbo --version`** and a release build.
39. **Windows.** Not supported.
