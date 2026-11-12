# Evaluation set

Eight small tasks that rumbo is run on, to measure a change instead of
guessing at it. Each task targets one thing the harness is meant to get right.

| Task | What it measures |
|---|---|
| `fix-bug` | Finds and fixes a defect from a failing test, without editing the test. |
| `add-flag` | Adds a feature and keeps the existing behaviour. |
| `stay-in-scope` | Does exactly what was asked in code that invites unrequested cleanup. |
| `signature-change` | Follows a change through every caller in other files and packages. |
| `follow-instructions` | Applies the project's `AGENTS.md`, which the harness provides as context. |
| `no-checks` | Verifies its own work when the harness has no checks to run. |
| `large-file` | Edits one line of a file much larger than a read window. |
| `answer-only` | Answers a question without changing anything. |

## Running

From the repository root:

```sh
go run ./evals -selftest                           # no model needed
go run ./evals -model anthropic/claude-opus-5-5    # the whole set
go run ./evals -model anthropic/claude-opus-5-5 -task scope -runs 3
go run ./evals -model anthropic/claude-opus-5-5 -out results.jsonl
go run ./evals -model anthropic/claude-opus-5-5 -set compact_at=3000
```

`-set key=value` applies a rumbo setting to every run. It is how a mechanism
that small tasks never reach on their own gets exercised: with `compact_at`
set that low, every task is summarized and continued mid-way, and still has
to pass.

A real run calls the model and costs tokens. Model output varies between
runs: use `-runs` before reading much into a single pass or failure.

Each run gets a fresh copy of the task's project and a database of its own,
and ignores the machine's global instructions, so results depend only on the
task and the code. A provider you registered is carried over from your own
database. Checks are whatever the harness detects for the project, as for a
new user.

## Reading the report

- **result**: whether the task's check accepted the final state.
- **exit**: rumbo's exit code. `0` done, `1` failed, `130` cancelled by the
  time limit.
- **turns**, **tools**: model responses and tool calls. Fewer for the same
  result means less wandering.
- **fixes**: times the harness sent the agent back because its checks failed.
- **compact**: times the conversation was replaced by a summary.
- **input**, **cached**, **output**: tokens. `input` is what was paid in full;
  `cached` is what the provider read from its cache.

The working directory of a failed run is kept, and its path printed. It holds
the project as the agent left it (`repo/`), the answer (`output.txt`), the
progress (`progress.txt`), and the session (`eval.db`, readable with
`RUMBO_DB=<path> rumbo log <session>`).

## Adding a task

A task is a directory under `tasks/`:

| File | Purpose |
|---|---|
| `repo/` | The project the agent works in. |
| `objective.txt` | What the agent is asked, worded as a user would. |
| `check.sh` | Run in the project afterwards; exits zero when the result is right. |
| `solution.sh` | A reference solution, run in the project. Used only by `-selftest`. |
| `setup.sh` | Optional. Run in the project before it is committed, to generate files. |

Checks and solutions get two variables: `RUMBO_EVAL_TASK`, the task's
directory, and `RUMBO_EVAL_OUTPUT`, the file with the agent's answer. The
starting commit is tagged `eval-base`, so a check can see everything the agent
changed with `git diff eval-base`, even if the agent made commits.

Rules for a good task:

- It measures one thing, and the check's first comment says which.
- The check judges the result, not the route: what the files and the program
  do, not which tools were called.
- The check also rejects the wrong shortcuts, such as editing the test.
- Anything the check needs that would give the answer away stays outside
  `repo/` (see the `hidden_test.go.txt` files).
- `go run ./evals -selftest` passes: the check fails on the untouched project
  and accepts the reference solution.
