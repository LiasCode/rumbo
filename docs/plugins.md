# Plugins

A **plugin** is a directory that brings, all at once, things rumbo already
has kinds of: extensions, providers, front ends, and skills. It is how what
one person put together reaches another in one step, instead of registering
each program and copying each skill by hand.

A plugin adds no kind of thing, and speaks no protocol. Its programs are
ordinary ones, each speaking the protocol of its kind
([extensions](extension-protocol.md), [providers](protocol.md),
[front ends](frontend-protocol.md)), and its skills are ordinary skills.
Once a plugin is added, what it brought cannot be told from what a person
registered by hand, except that it says where it comes from.

[examples/plugin](../examples/plugin) is a plugin to start from.

## What is in a plugin

```
my-plugin/
├── plugin.yaml              the manifest: what the plugin brings
├── skills/
│   └── review/
│       └── SKILL.md         a skill, in a directory of its own
└── bin/
    └── greeter.py           the code of its programs, wherever it likes
```

Only `plugin.yaml` is required, at the root. Everything else is where the
manifest, or the convention for skills, says it is.

## The manifest

```yaml
name: team
description: The team's greeter, and its way to review a change.

extensions:
  greeter:
    command: [python3, "{plugin}/bin/greeter.py"]
    config:
      greeting: hello
```

| Key | Meaning |
|---|---|
| `name` | What the plugin is called: lowercase letters, digits, `_`, and `-`, starting with a letter. A person has one plugin of a name. |
| `description` | A line for a person, shown by `rumbo plugin list` and `rumbo plugin show`. |
| `build` | The command that builds the plugin's programs, as a list, run in the plugin's directory when it is added and when it is updated. See [Building a plugin](#building-a-plugin). |
| `extensions` | The extensions it brings, by name. |
| `providers` | The providers it brings, by name. |
| `frontends` | The front ends it brings, by name. |

Each program is named as a plugin is, and has:

| Key | Meaning |
|---|---|
| `command` | The program and its arguments, as a list. Required. `{plugin}` in any word stands for the directory the plugin is in, wherever it was added from, and `{self}` as a word for the rumbo executable: the first one for a program rumbo serves itself, or an argument for a program that starts rumbo, as a front end does for each other session it shows. |
| `config` | The configuration the plugin gives the program, which reaches it at `initialize`. What the user sets goes over it, field by field. |
| `replaces` | For an extension only: the tools of rumbo's own, such as `bash`, that it asks to take the place of. See [Replacing a tool](#replacing-a-tool). |

A key rumbo does not know, in the manifest or in a program, is refused, so
that a misspelled one is not ignored. A program cannot have the name of one
rumbo has, built in or in its own plugin, such as the front end `plain` or
the provider `openai`.

**Skills** need nothing in the manifest: each directory in `skills/` that
has a `SKILL.md` is one, in the [Agent Skills](https://agentskills.io)
format, with a `name` and a `description` in its frontmatter.

## What a plugin's programs are, once added

They are there without being registered, as the programs rumbo has built in
are. `rumbo extension list` shows them with the plugin they come from, and
every command that works on a program works on them:

```sh
rumbo extension show greeter              # what it gives the model
rumbo extension set greeter greeting=hi   # yours, over the plugin's
rumbo extension set greeter greeting=     # back to the plugin's
rumbo extension rm greeter                # removes what you set, not the extension
```

The value of a setting comes from the first of these that has one:

1. what the user set, kept in `config.yaml`;
2. what the plugin's manifest gives, in `config`;
3. the program's own default.

`config.yaml` keeps of a plugin only where it comes from, and the
replacements the user agreed to. Nothing the manifest says is copied there,
so a new version of the plugin changes what everyone who set nothing else
gets.

A plugin's skills are told to the model with the others. When two skills
have the same name, the more specific one is told: the project's over the
user's own, and the user's own over a plugin's.

## Replacing a tool

An extension that declares a tool called as one of rumbo's own, such as
`bash`, takes its place only where the user said so. A plugin cannot say so
for them: `replaces` in the manifest asks, and the person agrees by name as
they add the plugin.

```sh
rumbo plugin add ./my-plugin                  # refused: it says what the plugin asks for
rumbo plugin add -replaces bash ./my-plugin   # agreed
```

What was agreed is kept, and an update that asks to replace more is refused
and undone. Say in the plugin's description, or its README, what it
replaces and why, since the person has to type it.

## Building a plugin

A plugin's programs can be written in a language that is compiled, without
publishing a program for each system: `build` says how the plugin is built,
and rumbo builds it on the person's machine.

```yaml
name: team
build: [go, build, -o, bin/greeter, ./cmd/greeter]
extensions:
  greeter:
    command: ["{plugin}/bin/greeter"]
```

- It runs in the plugin's directory, with `{plugin}` and `{self}` as in a
  program's `command`. A list is no shell: for several steps, run a script
  of the plugin's, such as `[sh, "{plugin}/build.sh"]`.
- It runs when the plugin is added, before what it brings is started, and
  when an update brings something new. What it prints goes to the
  person's terminal, after a line that says what is run.
- A build that fails keeps the plugin from being added. An update whose
  build fails, or whose programs do not start after it, goes back to what
  the plugin was and builds that again, since the failed build may have
  changed what it left.
- What it needs to run, such as Go, is the person's to have: say so in
  the plugin's README. A build that does not find it fails, and says why.
- Build into files the repository ignores, in `.gitignore`, so an update
  finds the copy as Git left it.
- A plugin added from a directory is built when it is added, and not
  again: while you work on it, build it yourself.

## Writing a plugin

1. **Make the directory and the manifest.** Start with the name and one
   program:

   ```yaml
   name: team
   description: The team's greeter, and its way to review a change.
   extensions:
     greeter:
       command: [python3, "{plugin}/bin/greeter.py"]
   ```

2. **Write the program**, as any program of its kind is written. For an
   extension, start from [examples/extension.py](../examples/extension.py)
   and the [Agent Extension Protocol](extension-protocol.md). Refer to the
   plugin's own files through `{plugin}`, in the manifest, never through a
   path of your machine.

3. **Add its skills**, each in `skills/<name>/SKILL.md`:

   ```markdown
   ---
   name: review
   description: Review a change the way the team does. Use for any code review.
   ---
   Read the whole diff before commenting. Say what must change first.
   ```

4. **Add it from its directory**, which is the test that it works: rumbo
   starts each provider, and the extensions together with all the ones the
   person already has.

   ```sh
   rumbo plugin add ./my-plugin
   rumbo plugin show team
   ```

   A plugin added from a directory is read as it is each time, so while you
   work on it a change to the manifest, a skill, or a program is there for
   the next run, with nothing to add again.

5. **Try it.** `rumbo extension show greeter` starts the extension and
   lists its tools, with what declaring them adds to every request;
   `rumbo skills` lists the skills the model is told of, and why one is
   left out; then give rumbo a task that uses them.

6. **Publish it** as a Git repository with `plugin.yaml` at its root.
   Others add it by its address, and bring it up to date:

   ```sh
   rumbo plugin add https://example.com/team-plugin.git
   rumbo plugin update team
   ```

## What rumbo checks

Before a plugin is added, and again when it is updated:

- The manifest is read, and refused when it has no valid `name`, a key
  rumbo does not know, a program without a `command`, or `replaces` on a
  program that is not an extension.
- Nothing it brings is already there: no other plugin has its name, and no
  program of it is one another plugin brings, rumbo's own among them, one
  the person registered themselves, or one rumbo has built in.
- What it asks to replace is exactly what the person agreed to.
- Its build, if it has one, succeeds, once everything above has passed.
- What it brings starts: each provider, and every extension with all the
  others, so a tool that two extensions both declare is found then. A front
  end needs the terminal, and is tried when it is first used.

A plugin that does not pass is not added, and a copy made of its repository
is not left behind. An update that does not pass leaves the plugin as it
was.

If a plugin's manifest can no longer be read, because its directory was
moved for one, or a change to it brings what another plugin brings, rumbo
stops and says how to take the plugin out, instead of going on without what
it brought.

## Where a plugin lives

| Added from | Where it is | `rumbo plugin update` |
|---|---|---|
| A directory | The directory itself, by its full path. | Not needed. |
| The address of a Git repository | A copy in rumbo's data, `~/.local/share/rumbo/plugins/<name>`. | Fetches what is new. |

`rumbo plugin rm` takes the plugin out, with what the person had set for
the programs it brought, and removes the copy; a directory it was added
from is left as it is.

## rumbo's own plugin

What comes with rumbo beyond the host is a plugin too, called `rumbo`: the
providers `anthropic`, `openai`, and `chatgpt`, and the front end `tui`.
`rumbo plugin show rumbo` says so. Its manifest,
[plugins/rumbo/plugin.yaml](../plugins/rumbo/plugin.yaml), is built into
the executable with the code of its programs, which the executable serves
itself, so each of its commands starts with `{self}`. It is there before
any plugin you add, with nothing of it in `config.yaml`, and cannot be
taken out or updated apart from rumbo. Its extension for MCP servers is in
it too, but not in its manifest, since it would start with no server: you
add it once for each server, as `{self} extend mcp <the server's command>`.

## What to keep in mind

- **A plugin runs with the rights of whoever adds it.** rumbo neither
  reviews nor confines it, and its build runs as soon as it is added. Say
  what yours does, and add only one whose code you trust.
- **Everything declared costs tokens.** Each tool of an extension, and each
  skill's description, is sent to the model in every task, whether the task
  uses it or not. Bring few, described briefly.
- **There are no versions.** A plugin added from an address is the default
  branch of its repository, and an update brings the latest. Keep that
  branch working.
- **Do not count on another plugin.** A plugin cannot ask for one. And a
  program named as one another plugin brings keeps whichever is added
  second from being added, so name your programs after your plugin.
