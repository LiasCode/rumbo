---
name: rumbo
description: How rumbo, the harness this agent runs in, works, and how to extend it. Use when the user asks about rumbo itself, its commands, settings, sessions, or design, or wants to write a plugin, a provider, a front end, an extension, or a skill for it.
---

# rumbo

rumbo is the harness this agent runs in: a host that runs the agent loop,
providers that reach a model, front ends that show the work, and extensions
that give the model more tools, each a program of its own speaking a
protocol. A plugin brings several of them, and skills, at once.

This file only says where to look. The documentation lives in the
repository, https://github.com/LiasCode/rumbo; fetch the file a task needs
from its raw address (for example with `curl -fsSL`), and only that one.
The links point at `main`; `rumbo version` says which release is installed,
and the same paths under `https://raw.githubusercontent.com/LiasCode/rumbo/vX.Y.Z/`
give the documentation of that release.

Raw files are under `https://raw.githubusercontent.com/LiasCode/rumbo/main/`:

| To | Read |
| --- | --- |
| Understand the parts and the rules the design keeps | `docs/architecture.md` |
| Know what the host, providers, front ends, and extensions must and can do, and the commands | `docs/use-cases.md` |
| Look up a term | `docs/glossary.md` |
| Write a plugin, or replace a tool of the host's | `docs/plugins.md`, then `examples/plugin/` |
| Write a provider | `docs/protocol.md`, then `examples/provider.py` |
| Write a front end | `docs/frontend-protocol.md`, then `examples/frontend.py` |
| Write an extension | `docs/extension-protocol.md`, then `examples/extension.py` and `examples/guard.py` |
| Install it, and the first steps | `README.md` |

Go types of the protocols are in `protocol/`; to browse a directory, use
`https://github.com/LiasCode/rumbo/tree/main/<path>`.

On the user's machine, `rumbo help` lists the commands, and `rumbo skills`
the skills the model is told of and why one is left out.
