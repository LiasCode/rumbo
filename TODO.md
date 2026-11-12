# To do

What is left on our side: the loop, configuration, and sessions. Work on the
protocols and on external interfaces waits until this is done.

Items are numbered so they can be referred to. Within a block they are in the
order to do them. Remove an item when it is done.

## Order

1. Items 1 and 2: everything else depends on what they show.
2. Loop correctness (3 to 8) together with 21 and 29: what daily use needs.
3. The remaining blocks.

## Decisions for the owner

- **Permission model** (18): confirm each action, an allow list, or a sandbox.
- **File restore points** (35): whether sessions should be able to undo
  changes to files.
- **Windows** (39): whether it is in scope.

## 0. Before anything else

1. **Run against a real model.** Everything is verified only with simulated
   providers. Needs a credential.
2. **A small evaluation set.** Five to ten real, repeatable tasks, so each
   change to the loop is measured.

## 1. Loop

### Correctness

3. **Context limit.** There is no compaction and no warning: a long session
   fails when it passes the model's window.
4. **Resume an interrupted run.** After an error or a cancel, the only way on
   is a new objective. There is no "continue where you stopped".
5. **Transient provider errors.** A network or rate-limit failure ends the
   run. Retry with a wait.
6. **Response cut by `max_tokens`.** It ends the run with an error. It should
   continue by itself.
7. **Limit of a run.** No maximum of turns or tokens: a model that loops never
   stops.
8. **Two processes on one session.** There is no lock, and entries interleave.

### Verification

9. **Detect real changes.** Any `bash` call counts as a change, so an `ls`
   triggers the checks.
10. **Fuller project checks.** Only Go, Rust, and npm scripts are detected.
    No tests, and the time limit is fixed at ten minutes.
11. **Record verification.** A passing check leaves no entry in the session;
    a failing one does.

### Context

12. **Refresh during a run.** The tree and git state are collected only when
    an objective starts, not after the agent's own changes.
13. **Bound the instructions.** A very large `AGENTS.md` is injected whole.
14. **Recoverable truncated output.** What is cut from a tool result is lost.
    Keep it and make it readable.

### Tools

15. **Safe editing.** `edit` and `write` do not check whether the file changed
    on disk since the model read it.
16. **Read-only tools in parallel.** Every call runs in sequence.
17. **Long processes in `bash`.** A development server blocks until the
    timeout. Needs background execution.
18. **Permissions.** There are none: the agent can write outside the project
    and run any command.

### Steering

19. **Messages during a run.** The loop accepts no correction from the user
    between turns; it can only be cancelled.
20. **Changing model mid-session.** It works, but is untested with reasoning
    blocks left by another provider.

## 2. Configuration

21. **Project root.** Project settings and `-c` use the exact directory, so
    they find nothing from a subdirectory. Resolve to the git root.
22. **Validate on save.** `config set` accepts any value; the error appears
    when running.
23. **`config unset`.** A setting cannot be removed.
24. **Show the effective value.** `config get` looks at one scope. It does not
    say what will apply or where it comes from.
25. **Configurable limits.** Tool output size, tree budget, and timeouts are
    fixed in the code.
26. **Schema version.** The database has no version number. Migrations will
    need one.
27. **Edit a registered program.** Changing a provider's config means running
    `add` again in full.
28. **Built-in programs.** Deleted ones come back on the next start; they
    cannot be disabled.

## 3. Sessions

29. **Record errors and cancels.** A provider failure or a Ctrl-C leaves no
    trace in the session.
30. **Reproduce a request.** `log` prints entries. A command should print the
    exact request of turn N, which is the proof that history is reproducible.
31. **Entry metadata.** Duration, rumbo version, and request parameters are
    not stored.
32. **Useful listing.** `sessions` shows only the current directory, sorts by
    creation, and shows no last activity, turns, or tokens.
33. **Delete, rename, prune.** The database only grows.
34. **Accumulated usage.** No token totals per session.
35. **File state.** History reproduces what the model was sent, not the
    files: no restore points and no undo.
36. **Branches.** There is no way to go back to an earlier point and continue
    from there.

## 4. Quality

37. **Missing tests.** `store`, `verify`, `cli`, and configuration have none.
38. **`rumbo --version`** and a release build.
39. **Windows.** Not supported.
