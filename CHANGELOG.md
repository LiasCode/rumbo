# Changelog

What changes from one release of rumbo to the next, for the people who use
it. The commits hold the reasons.

## Unreleased

### Fixes

- The example front end, `examples/frontend.py`, answers that it speaks
  version 3 of the front end protocol, the one rumbo speaks since 0.12.0,
  so rumbo no longer refuses it. The protocol's documentation said 2 in
  the same place, and says 3 now.

## 0.13.0 (2026-11-27)

### Features

- rumbo comes with a skill, `rumbo`, that tells the model where rumbo's
  documentation is in its repository, so it can answer questions about
  rumbo and help write plugins, providers, front ends, and extensions. It
  is written to `~/.rumbo/builtin/skills/`; a skill of yours called
  `rumbo` replaces it.

### Fixes

- With `anthropic`, the model is given a tool's whole schema, as with
  `openai` and `chatgpt`, instead of only its properties and which are
  required. A tool of an MCP server whose schema refers to definitions of
  its own (`$defs` and `$ref`), or allows no fields but its own, is now
  described to the model as the server wrote it.

## 0.12.0 (2026-11-26)

### Windows

- **rumbo runs on Windows.** The releases have a zip for it, with
  `rumbo.exe`. It passes the checks there and on macOS, but has been used
  every day only on Linux: expect rough edges, and please report them. There the model runs commands with PowerShell, through a
  tool called `powershell` in place of `bash`, so an extension that takes
  its place there says `replaces=powershell`. `/login` opens your
  browser. The computer tool, Ctrl-V for an image or files, and what
  rumbo's processes use are not available on Windows yet, and say so;
  `/attach` gives a file. A plugin built with Go builds into a directory
  (`-o bin/`), so that Windows gets the `.exe` it runs, and a front end of
  your own reads the protocol there from the two handles
  `RUMBO_FRONTEND_HANDLES` names, since Windows passes no file descriptors
  3 and 4.

### One directory for everything

- **rumbo keeps everything in `~/.rumbo/`**: `config.yaml`, your
  `AGENTS.md` and skills, credentials, plugins, sessions, and pasted
  images, on every system. `RUMBO_HOME` puts it elsewhere. What you had
  is not moved: to keep it, move `~/.config/rumbo/` (`~/Library/Application
  Support/rumbo/` on macOS) and `~/.local/share/rumbo/` into `~/.rumbo/`.

### Files in what you ask

- **You give the model files of any kind, not only images.** `/attach
  <path>` in the terminal UI puts a file with what you send next, a task
  or what you tell a run. The model is told the file's path and reads it
  with its tools when it needs to, so nothing of the file is kept in the
  session or sent again with every request.
- Ctrl-V still pastes the clipboard's image, now as a file: it is saved
  as it is in `~/.rumbo/pasted/`, and the model looks
  at it with `read`, in a turn of its own.
- **Ctrl-V also gives the files you copied in a file manager**, of any
  kind, on Wayland and X11, and **a file you drop on the terminal is
  given** instead of its path being typed. A path you paste as text is
  typed as before unless it is all there is in the paste and the file
  exists.
- The files given show over the composer and under your message, each
  with an icon for its kind: an image, a PDF, a spreadsheet, code, an
  archive, sound, video, or any other file.
- A session from before still shows the model the images it holds; the
  terminal UI no longer names them when it shows that session.
- **Front ends: the protocol is version 3.** `images` is gone from `run`,
  `steer`, and the `objective` and `steer` events; `files`, a list of
  paths where the host runs, takes its place. A front end saves what has
  no path, such as a pasted image, where it chooses, and gives the path.

## 0.11.0 (2026-11-25)

### Terminal UI

- The bar at the top is gone, so the conversation has its row. How many
  runs work in other sessions shows under the composer, on the right.
- The sidebar starts put away; Ctrl-B shows it.
- Under the composer, beside the model, are the Git branch checked out
  and the folder, under your home as `~/`. The branch is read again when
  a run ends and when the terminal comes back to the front.
- The conversation and the composer take the whole width, short of its
  sides, instead of a column of at most 100.
- The line under a finished task puts its tokens in and out first, then
  what the cache gave, with its share of what went in, as
  `12.3s · 1.1k in · 810 out · 26.5k cached (96%) · 1 turn`.
- A session you continue says how long each of its earlier tasks took
  too, under each, and so does `/export`.
- In the conversation (Shift-Tab), j and k choose too, K and J jump
  between your messages, gg and G go to the start and the end, c copies
  what is chosen, and b branches the session from your message that
  task started with: it chooses that message first, and asks you to
  agree, saying what the branch keeps.
- In the sidebar, j, k, gg, and G choose too, and d deletes the session
  chosen, once you agree. The sessions are a row apart, each with how
  long ago it was used, as `5m`, `3h`, or `2d`.

- Copying works in a tmux that does not pass on what programs copy, as
  with `set-clipboard external`, and in terminals that do not take OSC
  52: it also goes through the desktop's clipboard (`wl-copy`, `xclip`
  or `xsel`, `pbcopy`), or else through tmux's buffer. What is copied
  says where it went.

- **The command palette is gone**: everything is a command typed in the
  composer, and Ctrl-P starts one, as a slash on an empty line does.
- **Fewer commands, in your words**, each with its arguments completed:
  `/new`, `/sessions` to list them and `/resume <session>` to go to one,
  `/rename`, `/branch [message]`, `/compact`, `/delete [session]`,
  `/copy [code|id]`, `/export`, `/status` and `/changes` for the panels,
  `/model [provider/model] [option=value…]`, `/login` and `/logout`
  for the provider or an extension, `/providers` and `/extensions` to
  list, show, set up, add, and remove them, `/settings` to see what
  applies and change rumbo's settings, `/rumbos [close]`, `/help
  [command]`, and `/quit`. They take the place of `/use`, `/session-set`,
  `/config`, `/system-set`, `/skills`, `/processes`, every `/provider-…`
  and `/extension-…`, and the functions of the provider and of the
  extensions, which are called after their name: `/providers <name>
  <function>`, `/extensions <name> <function>`.

- While a command is typed, what it still takes shows after the cursor,
  dimmed, as `/resume <session>`; the menu shows an example of the
  command chosen; a command typed wrong says which one you meant; `/help`
  lists the commands by what they are about; and an empty conversation
  shows the few commands to start with.

- `/providers` and `/extensions` complete more: what a provider's or an
  extension's function takes, from what that program lists; the changes
  you have not given yet; and, for `add`, how the programs there are are
  started, such as `{self} extend mcp`, and an extension's `config=` and
  `replaces=`.

- `/extensions <name> tools=` chooses an MCP server's tools from all
  it has, item by item: the list as it is chosen now first, then each one
  not in it yet, with what it does; a comma goes on with another.
- How a run stands, its spinner and its time, shows over the composer,
  outside it, instead of in its last row.

### Extensions

- The `mcp` extension lists every tool its server has with a function of
  its own, `tools`, for a front end to choose among, and says to sign in
  with its `login` rather than naming a command of the terminal UI's.

### Front ends

- What rumbo answers no longer names functions a front end may not
  offer as commands, such as `provider-show` or `extension-add`: it says
  what to do instead.
- A session's history says how long each run took: `ended` has `took`,
  in milliseconds, which is absent when the session does not know.

## 0.10.0 (2026-11-25)

A rumbo is one session. Each `rumbo` you start works in a session of its
own, with its own provider and extensions, in the directory it was started
in, and no longer serves the others of a project: another session is
another `rumbo`. The front end protocol is at version 2 for it.

### Sessions

- **One rumbo, one session.** A running rumbo no longer opens another
  session, starts a new one, or runs in two at once. Start another
  `rumbo` for a new session, `rumbo -c` for the latest of the directory,
  or `rumbo -s <session>` for any; several run side by side, each a
  process of its own. `-single` is gone, since it is how rumbo works now.
- The commands that reached other sessions from inside one are gone:
  `/sessions`, `/projects`, `/open`, `/new`, and `/prune`, and `/delete`
  is the terminal UI's own, for the session shown. From the command
  line, `rumbo sessions` lists them, `rumbo sessions rm` deletes them, and
  `rumbo sessions prune` deletes those not used in a while. `/status` and
  `/compact` are of the session you are in.
- `/branch` makes the branch and says which session it is; the rumbo that
  made it stays in its session, and another goes on in the branch: the
  terminal UI starts it and shows it, and elsewhere `rumbo -s <session>`
  does. A branch is titled "Branch", then the title of the session it
  came from, so a list tells them apart.
- `rumbo sessions -json` lists the sessions for a program to read: each
  with its directory, its title, how its last run ended, and whether a
  rumbo holds it now.
- A session's root is the directory rumbo is started in, and rumbo no
  longer looks for the git repository that holds it. Started in a
  directory inside a repository, it reads the `AGENTS.md` or `CLAUDE.md`
  and the `.agents/skills/` of that directory alone, not those of the
  directories above it, and `-c` and `rumbo sessions` find only the
  sessions started there: start rumbo at the top of the repository to work
  with its guide. The sessions you have are kept, each in the directory it
  was started in, so one started inside a repository is no longer listed
  from its top; `rumbo sessions -all` lists them all, and `-s <session>`
  continues any.

### Terminal UI

- Each session in the sidebar is a rumbo of its own, which the terminal
  UI starts when you first show the session, or a new one with Ctrl-N,
  and ends when you quit. The runs of the sessions you leave go on as
  before, and the sidebar marks with a ring the sessions a rumbo is still
  open for. It lists the sessions started in the folder rumbo was started
  in, not those of the whole repository.
- The rumbos of the sessions you left with nothing to do are closed past
  the two shown last, so they do not go on costing memory; a session
  closed that way is as it was, and showing it again starts its rumbo.
  "Close the rumbos not in use", in the palette, closes them all.
- `/rumbos` says what the rumbo of each session open uses: its processes,
  their memory, and their processor time.
- `/delete` deletes the session shown, once you agree, and "Delete this
  session" in the palette does the same. It takes the place of Delete in
  the sidebar. The session rumbo was started in is held until you quit,
  and is deleted from the command line, with `rumbo sessions rm`.

### Front ends

- **The front end protocol is at version 2**, and a front end that speaks
  version 1 is refused when it starts, saying so. A `run` and a `history`
  name no session, a run starts no new one, and the host has no
  `sessions`, `projects`, `open`, `new`, `delete`, or `prune`: a front end
  starts a `rumbo serve` for each session it shows, and groups them by
  the `dir` each says. `initialize` and the state of every answer say what
  the session is called, as `title`, and `branch` answers with the session
  it made. The provider and extension protocols are as they were.
- `{self}` stands for the rumbo executable as any word of a program's
  command, not only the first, so a front end rumbo starts can be told
  which rumbo to start for the other sessions it shows, as
  `[my-frontend, --rumbo, "{self}"]`.
- A front end can ask a host which processes it has running and what each
  uses, with the new `processes` request: the host, its front end, its
  providers and extensions, what those started, and what the model left
  running in the background, each with its memory and its processor time.
  One that holds several rumbos can so say what they cost, and close those
  nobody is using.

### Features

- `/processes` shows the processes the rumbo you are in has running, each
  under the one that started it, with the memory and the processor time it
  uses, and what they use together.
- Extensions are managed from any front end, as providers are:
  `/extensions` lists them, `/extension-add` registers one, `/extension-show`
  says what it gives the model and what that costs, `/extension-set` changes
  its configuration or what it replaces, and `/extension-rm` removes it. A
  change applies to the runs that start after it, and the terminal UI offers
  the commands of an extension, such as `/docs-login`, as soon as it is
  added.
- In the terminal UI, a parameter that can be left out can be typed by
  name, as `name=value`: `/extension-add docs
  config={"url":"https://example.com/mcp"} {self} extend mcp`.

### Going back to an earlier rumbo

- rumbo 0.9.0 and earlier list a session by the git repository it was
  started in, which a session this version made, renamed, or changed the
  provider of does not say: they show it with `rumbo sessions -all`, and
  continue it with `-s <session>`.

## 0.9.0 (2026-11-24)

### Features

- Extensions give the model tools of your own. An extension is any program
  that declares its tools and answers the calls to them on its stdin and
  stdout: `rumbo extension add notes python3 examples/extension.py`, and the
  runs that start from then on have them, in every project. `rumbo
  extension show` says the tools one declares and what declaring them adds
  to every request, and `list`, `set`, and `rm` work as they do for a
  provider. rumbo keeps one process of each while it runs, so an extension
  can hold a connection from one call to the next. The specification is
  `docs/extension-protocol.md`.
- MCP servers work through the `mcp` extension that comes with rumbo:
  `rumbo extension add files {self} extend mcp npx -y
  @modelcontextprotocol/server-filesystem ~/notes` for one started with a
  command, or `-config '{"url":"https://..."}'` for one reached over HTTP.
  `tools=a,b` gives the model only those tools of the server, and
  `prefix=` renames them.
- An MCP server that wants a sign-in gets one in your browser: registered
  as `docs`, it offers `/docs-login` in the terminal UI, and once you are
  signed in its tools are the model's from the next run, with
  `/docs-logout` to forget it. The sign-in is kept with the other
  credentials, readable only by you, and renewed as it expires; over ssh
  you paste the address the browser ended at, as with ChatGPT. For a
  server that takes a token of your own instead, `token_env` names the
  environment variable that holds it.
- An extension can take the place of one of rumbo's own tools: `rumbo
  extension add -replaces bash sandbox <command>`, or `rumbo extension set
  sandbox replaces=bash`, and the model's `bash` is the extension's, to run
  commands in a container or to refuse some of them. Without it, an
  extension that declares a tool rumbo has is refused, so none replaces
  one by its name alone.
- An extension can tell the model something when a run starts, in a
  section of its own, such as the notes it keeps: the first time, and again
  only when it changed. Removing the extension takes its section away.
- An extension can call rumbo's own tools while it answers a call, so one
  that replaces `bash` passes on what it lets through; is told the session
  each call is for; and can ask to be told what the runs do, to notify you
  when one ends, count what it cost, or keep a record.
- An extension can talk with you. A tool of its own asks you something
  while it works, such as whether a command may run: `examples/guard.py`
  asks before any `rm -rf`, in a dialog of the terminal UI that Escape
  declines, and refuses it where nobody is there to answer, as in a script.
  A call shows what it says it is doing on its line. And an extension
  offers commands of its own, named after it, such as `/notes-forget`,
  which say things and ask as a provider's `/login` does.

- Plugins bring extensions, providers, front ends, and skills at once:
  `rumbo plugin add <directory or Git address>` adds one, after starting
  what it brings, and `rumbo plugin show`, `update`, and `rm` say what it
  brings, bring it up to date, and take it out. What it brings is set like
  anything else (`rumbo extension set`), and `config.yaml` keeps only where
  the plugin comes from. A plugin whose extension takes the place of one of
  rumbo's own tools says so and is added only with `-replaces <tool>`.
  `examples/plugin` is one to start from.
- What comes with rumbo besides the host is a plugin too, `rumbo`, built
  into it and always there: the providers `anthropic`, `openai`, and
  `chatgpt`, the terminal UI, and the extension for MCP servers. `rumbo
  plugin show rumbo` says what it brings, and `rumbo provider list` and
  `rumbo config` say `plugin rumbo` where they said `built in`. What you
  set for them is kept and read as before; only the `plain` front end is
  still built in.
- A plugin can be built where it is added, so its author publishes its
  source and not a program for each system: `build: [go, build, -o,
  bin/greeter, ./cmd/greeter]` in its `plugin.yaml` runs in its directory
  when you add it and when `rumbo plugin update` brings something new, and
  you see what it prints. An update whose build fails goes back to what
  the plugin was, and builds that again.

### Terminal UI

- What an extension prints is marked `[extension]`, and what its commands
  answer `[extension <name>]`.

### Going back to an earlier rumbo

- A `config.yaml` with `extensions:` or `plugins:` in it is refused by
  rumbo 0.8.1 and earlier, which do not know them: take those sections out
  first, or keep this version.

## 0.8.1 (2026-11-23)

### Fixes

- A slip in `rumbo config`, such as `rumbo confg`, says which command you
  meant, as one in the other commands does, instead of going to the model
  as a task.
- A field the terminal UI's configuration does not have, such as a
  misspelled `colr_host`, stops it and says how to take it out, as a value
  it cannot use does, instead of being ignored. `plain`, which has no
  configuration, does the same with any field.
- A field written by hand in a provider's configuration in `config.yaml`
  that the provider has no setting of, such as a misspelled
  `contxt_window`, is refused when the provider starts, saying where it is
  and how to take it out, instead of being ignored, as one set with
  `rumbo provider set` already was. `rumbo provider show` marks it.
- What you asked comes back once in the terminal UI's Up and Ctrl-R, also
  when you asked it again with another image.
- The evaluation runner leaves your global instructions and skills out on
  Linux and macOS without changing where providers look for credentials.

## 0.8.0 (2026-11-23)

### Terminal UI

- **The terminal UI takes the whole screen**, as rumbo-gui takes a
  window, and no longer draws inline: the project's sessions are in a
  sidebar, the conversation in a column of its own, which PgUp, PgDn, and
  the mouse's wheel scroll, and the composer under it shows the model, how
  full the context is, and what the session spent. The conversation no
  longer stays in the terminal once it quits; `/export` writes it to a
  file.
- Each session keeps what it showed, and a run goes on in its session
  while you look at another, which the sidebar marks while it works and
  when it ends. The mouse, or Tab and the arrows, show a session, and
  Delete deletes the one chosen; Ctrl-N starts a new session, Ctrl-B puts
  the sidebar away, and Escape cancels the run.
- Ctrl-P opens a palette of every command, which typing narrows. One that
  takes something asks for it in a form that lists the values it takes,
  one that cannot be undone asks you to agree in a dialog rather than to
  type yes, and a provider's question, such as a sign-in code, opens in
  one too, leaving what you were typing as it was. The palette also
  renames, branches, compacts, and deletes the session.
- A click on the model under the composer, or the palette's Model and
  options, chooses the provider, the model, and the options of the
  session.
- Ctrl-Alt-B shows the session's details beside the conversation, with its
  context, tokens, and skills, and Ctrl-G what changed in the project's
  folder, as Git sees it.
- Shift-Tab goes up into the conversation: Enter, or a click, on a tool
  call shows all of it, on your message changes it or asks it again in a
  branch that starts before it, and on anything else copies it.
- Dragging the mouse over the conversation or a panel selects its text,
  and letting go copies it. A code block shows its language and its code
  alone, so what you select of it is the code.
- Keys that changed: Ctrl-P and Ctrl-N no longer go back and forward
  through what you entered, which Up and Down still do; Ctrl-B no longer
  moves the cursor; and Ctrl-L is gone.

### Images in what you ask

- **Ctrl-V in the terminal UI pastes the image in the clipboard**, which
  goes to the model with what you send next, a task or what you tell a
  run, or alone. A terminal pastes text only, so rumbo reads the clipboard
  itself: with `wl-paste` on Wayland, `xclip` on X11, and AppKit on macOS.
  The images wait in the composer, and Backspace on an empty composer
  takes the last one away.
- A front end can give images with a task or a steer, in the new `images`
  field of `run` and `steer`, and a session's history gives them back with
  what was asked. They are made smaller when they are large, kept in the
  session, and come back with the summary when the conversation is
  compacted.
- Every built-in provider sends them: Anthropic's API, ChatGPT, and both of
  OpenAI's APIs.

### How the model works

- **The model asks what only you can decide**: what you want, a
  preference, or which of two readings of a task you mean, when a wrong
  guess would not be cheap to undo. It asks once, with few questions, each
  with the answer it recommends, and before it starts when the goal itself
  is unclear; what it can find out, it looks up. It confirms before
  something hard to undo or that reaches other people, such as deleting
  data, publishing, sending, or paying, unless you asked for exactly that.
- It writes to you in the language you write in, as briefly or as formally
  as you do, and says so when you are wrong.
- Your instructions win over rumbo's: what you say in the conversation
  first, then your `AGENTS.md`.
- It checks what it did against what you asked, with what its tools show,
  and its closing report says what it assumed and what it checked.

### Releases

- A release's number says what it brings: one with only fixes raises the
  last number, as 0.8.1 would, and one with something new the middle one.
  Until 1.0, a change you may have to adapt to, such as a renamed setting,
  raises the middle number too.

### Fixes

- The choice selected in the terminal UI's command menu shows in reverse
  across its whole row, and no longer stops where its description starts.
- The computer tool no longer ends a program made with GPUI, such as
  rumbo-gui, when it starts typing in a desktop with no keyboard of its
  own, such as a headless Sway: its keyboard has its keymap from the start.
- `chatgpt` reads much more of each request from OpenAI's cache. The plan's
  server ignored the name of the conversation rumbo gave it for its cache
  and made up another for every request, so most requests, in some
  sessions nearly all, missed the cache and counted against your ChatGPT
  plan in full.

## 0.7.0 (2026-11-21)

The model can look at images and use your screen. `read` shows it an image
file, and the computer tool, which you turn on, lets it take screenshots and
use the pointer and the keyboard, for the programs a shell cannot drive.
Every built-in provider sends the model the images a tool shows.

### Tools

- `read` shows the model an image file, PNG, JPEG, GIF, or WebP, made
  smaller when it is large, instead of refusing it as binary.
- **The computer tool**, off unless you turn it on with `rumbo system set
  computer=on`: the model takes screenshots of your desktop and uses its
  pointer and keyboard, for programs a shell cannot drive. It works on
  wlroots compositors such as Hyprland and Sway, with `grim` installed, and
  in X11 sessions, with `xdotool` and `maim` or ImageMagick. It is written
  for macOS too, which has not been tried yet: there, allow the program
  that runs rumbo under Screen Recording, Accessibility, and Automation of
  System Events. It shows the monitor in use, and does nothing more once
  you move the pointer yourself or turn to another monitor, until the model
  takes a new screenshot.
- Images are kept in the session and sent again with every later request,
  as any tool result is: a screenshot costs about 1,400 tokens each time.

### Providers

- A tool's result can carry images, in the new `images` field of a
  `tool_result` block. The built-in providers send them to Anthropic's API,
  to ChatGPT, and to OpenAI's Responses API; Chat Completions takes images
  only in a message of the user's, so the openai provider sends them in one
  right after the results.

## 0.6.0 (2026-11-20)

Configuration is one thing to learn. Everything rumbo keeps is in files you
can read: what you chose in one `config.yaml`, and each session in a
directory of its own. Every setting is changed the same way, `name=value`,
is checked before it is kept, and `rumbo config` shows all of it with where
each value comes from. The model a new session starts with is now each
provider's own. Sessions and settings stored by 0.5.0 and earlier are not
carried over.

### Configuration

- Every setting is changed as `name=value`, and an empty value gives it
  back to its default: `rumbo system set max_turns=90`, `rumbo provider set
  anthropic effort=high`, `rumbo frontend set tui theme=plain`, and in the
  terminal UI `/system-set`, `/provider-set`, and `/session-set`, whose
  menus offer the values each setting takes. Several changes go in one
  command line, and none is kept unless all are right.
- **The model is now each provider's own**, and `default_model` is gone:
  `rumbo provider set anthropic model=claude-opus-5-5`. A new session starts
  with the default provider and its model, and `rumbo -p chatgpt` starts
  with chatgpt's, without `-m`. A provider with no model set still opens the
  terminal UI, where `/use` chooses one; a task is refused until there is
  one, saying how to choose it. `rumbo provider models <name>` lists the
  models a provider offers.
- A value is checked before it is kept. A provider is asked what it has: a
  name it does not have, such as a misspelled `contxt_window`, a value of
  another type, or one not among those it lists, such as `api=bogus`, is
  refused with what it takes, and the provider is started with the change,
  which is not kept if it does not start. Values are read as the setting's
  type asks, so `effort=3` is the text "3". The options a session chooses,
  with `-o` or `/session-set`, are checked the same way. A front end cannot
  be started without its terminal, so what it is given is checked when it
  next starts.
- `rumbo config`, and `/config` in the terminal UI, show what is set and
  what applies, each value with where it comes from: the session,
  `config.yaml`, rumbo's default, or how rumbo has a built-in provider. It
  starts nothing. `rumbo provider show <name>`, and `/provider-show`, start a
  provider and show each of its settings, its own defaults included, with
  what each is and the values it takes; in the terminal UI, without a name,
  the session's provider, with what the session chose over the rest.
- `rumbo system` shows the system settings; `rumbo system list`, `get`, and
  `unset`, `rumbo provider config` and `options`, and `rumbo frontend
  config` say what took their place.
- The terminal UI has fewer commands: `/system`, `/system-unset`,
  `/system-keys`, `/session-options`, `/option-values`, `/provider-config`,
  `/provider-options`, and its own `/options` are gone, into `/config`,
  `/system-set`, `/session-set`, `/provider-set`, and `/provider-show`. Its
  status bar says when there is no model yet. A color of its configuration
  can be written as a number, such as `color_code: 214`.

### Storage

- What you choose is kept in `~/.config/rumbo/config.yaml`
  (`~/Library/Application Support/rumbo/config.yaml` on macOS): the system
  settings under `system`, and what you chose for each provider and front
  end under `providers` and `frontends`, a provider's model beside its
  configuration. It holds only what you set, never a default, and you can
  edit it. rumbo changes it one value at a time and leaves the rest, your
  comments included, as you wrote it. A name it does not know there, or a
  value of the wrong shape, stops it and says the line.
- Each session is a directory, `~/.local/share/rumbo/sessions/<id>/`:
  `session.json` says what it is and what it chose, and `entries.jsonl`
  holds its history, one entry to a line, as `rumbo log` prints it.
- **Sessions and settings from 0.5.0 and earlier are not read.** rumbo no
  longer uses SQLite, and `~/.local/share/rumbo/rumbo.db` is left where it
  is, untouched. Set your defaults and register your own providers again.
  To keep the record of an earlier session, print it with the version that
  made it: `rumbo log <session>`.
- `RUMBO_DB` is gone. `RUMBO_CONFIG` names the configuration file, and
  `RUMBO_DATA` the directory the sessions are kept in.

### Providers

- A provider has one configuration, and the value you set for one of its
  options is a setting of it like any other: `rumbo provider set anthropic
  effort=high` gives every session that effort unless it chooses another
  with `-o` or `/session-set`.
- Only what you choose for a provider or a front end is kept. Its defaults
  are no longer copied next to it the first time it starts, so a default a
  new version changes reaches you unless you chose otherwise. An option
  nobody chose is no longer sent with each request as its empty default.
- The built-in providers and front ends are always there, without being
  registered. `rumbo provider rm openai` removes what you set for it and
  leaves it as rumbo has it; a provider you add under the same name takes
  its place.

### Protocols

- A provider can describe the fields of its configuration, as `fields`,
  the way it describes its options, each with its default. rumbo shows them
  and checks against them what a person sets. The built-in providers
  describe theirs.
- A function of the host can be marked `lists`: it is there to list the
  values a parameter takes, and a front end need not offer it as a command.
- The `model` a front end is told of at `initialize` is empty while
  nothing chose one.

## 0.5.0 (2026-11-16)

The model is told of your skills. rumbo says how each session stands,
renames and prunes sessions, lists those of every project, and prints the
request any turn sent. It tells you when a request misses the provider's
cache, and summarizes a conversation at 80% of the model's context however
large it is.

### Host

- The model is told of your skills, in the Agent Skills format: each
  `SKILL.md` in `~/.config/rumbo/skills/<name>/` (`~/Library/Application
  Support/rumbo/skills/<name>/` on macOS), and in a project's
  `.agents/skills/<name>/`. It gets the name and the description of each,
  and reads the skill itself when a task needs it. A project's skill
  replaces one of yours with the same name. `rumbo skills`, or `/skills`
  in the terminal UI, lists those the model is told of where you are, and
  says why one is left out, such as a frontmatter without a description.

### Sessions

- rumbo says when a request misses the provider's cache: when the provider
  read less of it than the request before left in its cache, it tells you
  how much it read, and how much counted as new input instead. A pause
  longer than the cache keeps a conversation, a few minutes with Claude,
  is the usual cause. Opening a session shows its earlier misses too.
- A conversation is summarized at 80% of the model's context however large
  it is, and `rumbo system list` shows `compact_at` as `80%`. It used to be
  summarized at 150,000 tokens at most, which was 15% of a context of a
  million tokens, such as Claude Opus's. `rumbo system set compact_at 15%`
  brings that back for such a model.
- `rumbo sessions` says how each session stands: how its last run ended,
  or that a rumbo process is using it, its turns, and the tokens they took.
  `-all` lists the sessions of every project. Its output now has a header
  and aligned columns, with the title last. The list of `/open` says how
  each session stands and its turns too.
- Sessions can be renamed, with `/rename` or `rumbo sessions rename`, and
  deleted several at once, with `rumbo sessions rm`, or by age, with
  `/prune 30d` or `rumbo sessions prune 30d`. Both list the sessions
  first, and delete them when given `yes`: `/prune 30d yes`, or
  `rumbo sessions prune -yes 30d`. A session in use by a rumbo process is
  kept.
- `rumbo request <session> [turn]` prints the request a turn sent the
  provider, exactly as it was sent, rebuilt from the session.
- Each response records the output limit its request set, how long the
  provider took, and the version of rumbo; each tool result, how long the
  tool took. `rumbo log` shows them.
- A host whose session was deleted by another rumbo process says so when
  given a task, instead of failing with a database error.

### Front ends

- A front end can show the sessions of every project: `projects` lists the
  projects that have sessions, most recently used first, and `sessions`
  lists another project's with its `project`. In the terminal UI they are
  `/projects` and `/sessions <project>`.
- The input tokens the terminal UI and `plain` count now include those the
  provider wrote to its cache, as `/status` does. With Claude, which writes
  nearly all of each request there, `in` showed almost nothing, and a
  cache miss did not move it.
- When it quits, the terminal UI leaves the command that continues its
  session, `rumbo -s <session>`, under the conversation.

## 0.4.0 (2026-11-15)

A new built-in provider, `chatgpt`, uses your ChatGPT plan instead of an
API key. Requests to OpenAI's Responses API read much more from its
cache, and the `effort` in a provider's configuration now applies.

### Providers

- A new built-in provider, `chatgpt`, uses your ChatGPT plan instead of an
  API key: `/login` signs in with ChatGPT in your browser, and over ssh you
  paste the address the browser ends at. Its usage counts against your
  plan; `/account` and a usage limit say where to manage it. rumbo
  registers it at its next start, and `rumbo provider rm chatgpt` removes
  it for good.
- `chatgpt`, and `openai` with `api` set to `responses`, read much more of
  each request from OpenAI's cache: every request of a session now asks for
  the server that cached its start. Before, about half of the requests of a
  long run missed the cache and were paid for, or counted against a
  ChatGPT plan, in full.
- The `effort` in a provider's configuration (`rumbo provider config
  <name> effort=...`) now applies in `anthropic`, `openai`, and `chatgpt`.
  It was never sent: the empty option every request carries when nothing
  is chosen replaced it. An effort chosen as an option, or in a session,
  still wins over it.

## 0.3.0 (2026-11-15)

The terminal UI is new. It draws inline, in the terminal's own scrollback,
shows what the model writes as Markdown and each tool call as a block,
opens pickers for sessions, models, and option values, brings back what
you asked in any session, and can copy an answer and export a session.
Sessions can branch.

### Terminal UI

- It draws inline, below what the terminal showed before, instead of
  taking the whole screen: the conversation goes into the terminal's
  scrollback, and only the part still changing is drawn again.
- A slash opens a menu of the commands with what each does, and then of
  what each takes, with what describes every value, such as a session's
  title. It narrows as you type, by value or description; Tab or Enter take
  a choice, and Enter on a command that needs nothing more sends it.
- The input takes several lines: Alt-Enter, Shift-Enter, or Ctrl-J starts
  a new one, and pasted text keeps its line breaks.
- Up and Down bring back what you asked in any session of the project, not
  only in this start, and Ctrl-R searches it. It comes from the sessions
  themselves, so deleting a session removes it from there too; commands
  are remembered until the terminal UI quits.
- What the model writes shows as Markdown: headings, lists, quotes, code,
  bold, italics, links, and tables lined up in columns.
- A tool call shows with a spinner and its time while it works, then with
  how it went: an edit with the lines it changed, a command with the start
  of its output, or its exit status and the end of its output when it
  failed.
- A run ends with how long it took, its turns, and its tokens.
- Opening, branching to, or continuing a session (`/open`, `/branch`, `-c`,
  `-s`) shows what it holds, and the status bar counts that session's
  context and tokens.
- Enter on a command that still needs something opens the menu of what it
  takes: `/open` lists the sessions, and `/use openai` the models of
  `openai`. After `/session-options `, the menu offers the values of the
  provider's options, with those in force marked.
- The status bar shows how full the context is as a bar, yellow past half
  and red past four fifths, and how long a run has worked, and leaves out
  what does not fit, the least needed first.
- A run in the background that ends says what it took and how to open its
  session. A run that ends, or a provider that asks something, while the
  terminal is not in front rings the bell and sends a desktop notification
  (OSC 9), in terminals that report their focus.
- `/copy` puts the model's last answer in the clipboard, and `/copy code`
  its last code block, through the terminal (OSC 52), so it works over ssh.
- `/export [path]` writes the session as Markdown, to `rumbo-<session>.md`
  in its directory unless a path is given, and never over a file.
- Its colors can be chosen in its configuration: `rumbo frontend config tui
  theme=plain` shows none, `color_code=#d7875f` changes one, and `NO_COLOR`
  is followed under the default theme, `auto`.

### Sessions

- A session can branch: `/branch` goes on in a new session that starts
  from the current one as it is, or as it was before one of your messages,
  numbered as `/branch-points` lists them. The first session stays as it
  was, and can keep working at the same time. The files are not part of a
  session, so a branch finds them as they are.
- `/sessions`, `/status`, and `rumbo sessions` say which session a branch
  came from.

### Protocols

Both stay at version 1, and what is new in them is optional.

- A front end can ask the host, with `recall`, what the person asked in the
  project's sessions, newest first, to bring it back to its input.
- A function that lists a parameter's values is called with what was given
  for the parameters before it that it takes, so the values of one can
  depend on another.
- The host lists a provider's models, `provider-models`, and the values its
  options take, `option-values`, asking it; `use`, `session-options`, and
  `provider-options` list their values with them.
- For front ends written in Go, `protocol.DialFrontendConfigured` answers
  with the start configuration in force.

## 0.2.0 (2026-11-15)

Configuration is simpler: the system settings, and over them what each
session chooses. The built-in providers sign in with an API key, and
`openai` speaks OpenAI's Responses API. Some commands and settings have new
names; what you stored is carried over, but for values stored for one
project.

### Settings

Settings are now kept apart by what they act on: the system, a session, or
a provider.

- `rumbo config` is now `rumbo system`, for the system settings:
  `default_provider` and `default_model`, which new sessions start with,
  `frontend`, `max_turns`, `max_tokens`, and `compact_at`. The `model`
  setting, which held both the provider and the model, is carried over to
  the first two once.
- Settings are the same everywhere: there are no project settings any more.
  Those stored for one project are no longer read, and `rumbo system list`
  shows them.
- `compact_at` takes a share of the model's context, such as `80%`, or `0%`
  for never. One stored as a number of tokens stops rumbo, saying how to
  change it.
- `-p` chooses the provider and `-m` the model alone, which may hold a
  slash: `rumbo -p local -m qwen3-coder`. `-m provider/model` no longer
  works. What `-p`, `-m`, and `-o` choose is the session's, kept from one
  start to the next, as `/use` and `/session-options` make it.
- A provider's options are its own, kept with its registration, no longer
  the `options.<provider>` setting: `rumbo provider options <name>
  effort=high`, or `/provider-options` in the terminal UI. Those already
  stored are carried over.
- Every field of a provider's or a front end's configuration, and every
  option of a provider, now has a value you can see in
  `rumbo provider show` and `rumbo provider list`: the one you gave, or the
  program's own default, which rumbo keeps the first time the program
  starts. `rumbo provider add` starts the provider, so one that does not
  work is not registered.
- In the terminal UI, `/help` lists the commands by what they act on, and
  some have new names: `/system`, `/system-set`, `/system-unset`, and
  `/system-keys` were `/settings`, `/setting`, `/unset`, and
  `/setting-keys`; `/provider-add`, `/provider-config`, `/provider-show`,
  and `/provider-rm` were `/register`, `/configure`, `/inspect`, and
  `/unregister`. On the command line, `rumbo provider set` and
  `rumbo frontend set` are now `rumbo provider config` and
  `rumbo frontend config`.
- A session keeps the provider options chosen in it, as it keeps its model:
  `/session-options effort=high`. Changing its provider with `/use` leaves
  them behind. The terminal UI's `/set`, whose choices lasted until it
  closed, is gone.
- A system setting or a provider's option you store applies to the next
  run, without starting rumbo again. The default provider and model, and the
  front end, still apply from the next start.

### Providers

- The built-in providers sign in with an API key: `/login` asks for it,
  checks it, and keeps it in `~/.config/rumbo/credentials/`, readable only
  by you; `/logout` removes it, and `/account` says which credential is in
  use. A key in the environment still comes first.
- The built-in `openai` provider offers an `effort` option, sent as
  `reasoning_effort` only when it is chosen: `/session-options effort=high`,
  `-o effort=high`, or `effort` in its configuration. The values depend on
  the model. Like `anthropic`, it now refuses options it does not describe.
- The built-in `openai` provider can speak OpenAI's Responses API, which
  its reasoning models need to use tools with an effort, and the `openai`
  rumbo registers on a new installation does. One registered before keeps
  Chat Completions until `rumbo provider config openai api=responses`. It asks
  the server to store nothing, and gives a model its own reasoning back,
  encrypted.
- In the terminal UI, what a provider's function answers in words shows
  without quotes.

### Protocols

Both stay at version 1, and what is new in them is optional.

- Each function of the host says what it acts on, in `group`: `system`,
  `session`, or `provider`.
- A provider's or a front end's answer to `initialize` may carry `config`,
  its configuration in force, with its default for every field it was not
  given. rumbo keeps it with the registration.
- Every option a provider describes has a `default`, which may be empty
  when that means the model decides. `rumbo provider add` says which ones a
  provider gives none for.

## 0.1.0 (2026-11-15)

The first release.

### Host

- Works on a task until the model stops calling tools, with four tools:
  `read`, `write`, `edit`, and `bash`, with a timeout or in the background.
- Keeps each session as an append-only list of entries in SQLite, from which
  every request sent to the model can be rebuilt, and keeps the start of
  each request stable so providers can cache it.
- Takes what you tell a run while it works, and resumes a run that failed,
  was cancelled, or was cut short (`-r`).
- Summarizes a conversation before it outgrows the model's context.
- Works in several sessions of a project at once, or in one with `-single`,
  and locks each session it works in.
- Gives the model the environment and the `AGENTS.md` or `CLAUDE.md` files
  of the project and of the user.
- Stores settings globally or per project, and checks each when it is
  stored.

### Providers

- The Agent Provider Protocol, version 1.
- Built in: `anthropic` for the Claude API, and `openai` for any server
  compatible with OpenAI's Chat Completions.
- Providers describe their models, options, and functions, and their
  functions can talk with the person while they work.

### Front ends

- The Agent Frontend Protocol, version 1, with the host's own functions
  described as commands and a session's history told in events.
- Built in: `tui` for terminals and `plain` for pipes and scripts.
- `rumbo serve`, for a front end that starts rumbo itself.

### Command line

- `rumbo version`.
- A lone word close to a command, such as `rumbo versoin`, is refused
  instead of being sent to the model as a task; `rumbo -- word` sends it.
