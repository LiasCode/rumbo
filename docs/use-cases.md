# Use cases

What each of the four parts of rumbo must do, can do, and must not do. The
host is rumbo itself. Providers, front ends, and extensions are separate
programs that speak a protocol with it, whether they come with rumbo or are
written by a user.

[architecture.md](architecture.md) explains how the parts fit together,
[protocol.md](protocol.md), [frontend-protocol.md](frontend-protocol.md),
and [extension-protocol.md](extension-protocol.md) specify the messages
between them, [plugins.md](plugins.md) says how a plugin brings several of
them at once, and the code holds the details.

## Host

The host owns one session, the tools, and the loop. It shows nothing and
knows no model API.

### Must

- Keep each session as an append-only list of entries. The only way history
  goes away is deleting a whole session.
- Build every request to the model from the session's entries alone, so a
  stored session reproduces every request it sent.
- Keep the start of each request stable, from the system prompt and the tool
  declarations to the ambient context, so providers can cache it.
- Be one session and no more: another session is another rumbo process,
  with its own providers and extensions, which a front end starts.
- Hold a lock on its session from the first work in it, so no other rumbo
  process writes to it.
- Close every run with a note saying how it ended: done, failed, or
  cancelled.
- Keep tool results small: read files in windows, shorten long output, and
  save the whole of it to a file the model can search.
- Summarize a conversation before it outgrows the model's context, and
  recover the same way when a request does not fit anyway.
- Reach models only through providers, and people only through front ends,
  rumbo's own included, always over the protocols.
- Leave the terminal to the front end: write nothing to it, and ignore the
  interrupt, which is the front end's to interpret.
- End on SIGTERM and SIGHUP as it does when the front end disconnects: its
  runs cancelled and recorded, its tools and providers stopped.
- Stop the processes the model left running in the background, and remove
  the files the tools saved, when it exits.
- Record nothing a provider's function asks or is told. A secret answer goes
  from the front end to the provider, and nowhere else.
- Check a setting when it is stored, and refuse to start with one that is
  missing, malformed, or misspelled in the configuration file instead of
  guessing, saying where in the file. A setting of a provider, or an option
  a session chooses, is checked against what the provider describes, and a
  provider's configuration by starting the provider with it, before it is
  kept. A field written by hand in a provider's configuration that the
  provider has no setting of is refused when the provider starts, and the
  provider does no work until it is taken out.
- Take every change to a setting the same way, at every level, as
  `name=value`, where an empty value gives the setting back to the level
  under it.
- Keep everything it stores in files a person can read: what the user
  chose in `config.yaml`, and each session in a directory of its own.
- Write to `config.yaml` only what the user chose, never a default, and
  leave what it does not change there as the person wrote it.
- Keep the protocols compatible: what it adds is optional.
- Keep stored data working: what a file holds only grows.

### Can

**Run tasks**

- Work on an objective until the model stops calling tools.
- Work in its session one run at a time, and refuse a run while another
  works or the session is being compacted.
- Take what the user tells a run while it works, give it to the model before
  its next request, and keep the run going when the model was about to
  finish.
- Tell the model the paths of the files given with an objective, or with
  what a run is told, after the words. It reads none of them and treats
  every kind alike: the model reads a file with its tools when it needs to.
- Cancel one run, or all of them.
- Resume a run that failed, was cancelled, or was cut short by the host
  dying, telling the model it was interrupted.
- Retry a request that failed for a passing reason, such as a rate limit, an
  overloaded server, or a provider process that died, with growing pauses.
- Tell the person when a request missed the provider's cache: when the
  provider read less of it than the request before left there, under the
  same provider and model and since the last compaction or new system
  prompt.
- Continue a response the output limit cut off.
- Stop a run after a number of turns.
- Summarize a session's conversation on request.

**Give the model**

- Four tools: read a file in windows, or look at an image, write a whole
  file, edit one by exact match, and run a command in the system's shell,
  bash or, on Windows, PowerShell, with a timeout or in the background.
- The computer tool, when the user turns it on: screenshots of the desktop
  rumbo runs in, and its pointer and keyboard, through what that desktop
  offers. It acts only on the screen its model saw last, and does nothing
  once someone else moves the pointer.
- A guard against replacing a file its model has not read, or that changed
  since it did. What the model read is the session's own record, which
  lasts as long as the host.
- The environment it works in: the working directory, the platform, and the
  date.
- The user's instructions: the `AGENTS.md` or `CLAUDE.md` file of the
  working directory, and the user's own in `~/.rumbo/`. A long one
  is cut, saying where the rest is.
- The user's skills: the name, the description, and the path of each
  `SKILL.md` of rumbo's own plugin, written to `~/.rumbo/builtin/skills/`,
  which points the model at rumbo's documentation in its repository; in
  the `skills/<name>/` of each plugin the user added, in
  `~/.rumbo/skills/<name>/`, where one of the same name replaces a
  plugin's, and in `.agents/skills/<name>/` of the working directory, where
  one of the same name replaces the user's. The model reads
  a skill when a task needs it. One without a description in its
  frontmatter is left out, and a long description is cut; a value with a
  colon in it that YAML rejects unquoted is read anyway.
- List the skills the model is told of in a session's directory, and those
  left out, with why: one of the same name replaced it, or its frontmatter
  is missing, unclosed, not valid YAML, or without a description
  (`skills`, and `rumbo skills` in the working directory).

**Sessions**

- Take the directory it is started in as the root of the session it
  starts, and the one a session it continues was started in as that
  session's.
- Start a new session, continue the latest one of the directory (`-c`) or
  a given one (`-s`), or resume its unfinished run (`-r`).
- List, from the command line, the sessions of the directory, or of every
  one (`rumbo sessions -all`), with how each one stands: how its last run
  ended, done, failed, cancelled, or interrupted when its process died, or
  that a rumbo process is using it; its turns, and the tokens they took.
  With `-json`, for a front end to read, which starts a rumbo for the one
  a person chooses.
- Give its session a title of the person's own (`rename`), or any stored
  one from the command line (`rumbo sessions rename`).
- Delete sessions with their history, from the command line: those the
  person names (`rumbo sessions rm`), or those not used in a given time
  (`rumbo sessions prune`), which lists them first and deletes them only
  with `-yes`. A session a rumbo process is using is kept, and a host
  whose session another process deleted says so instead of working in it.
- Branch its session (`branch`): make a new one that starts with a copy of
  it, as it is or as it was before one of the person's messages, titled
  "Branch" and that one's title, and say which it is, for another rumbo
  to go on in (`-s`). The host stays in its session, which is left as it
  is; the files are not the session's, and stay as they are.
- Show its session's status: its provider and model, the size of its context,
  what it has spent and how much of its input the provider read from its
  cache, and how its last run stands.
- Keep each session's provider and model, and the values of the provider's
  options chosen in it, from one start to the next, over the system
  settings. `-p`, `-m`, and `-o` choose them for the session, as `use` and
  `session-set` do. A new session starts with the default provider and the
  model set for it, and a session that changes provider takes the model set
  for the new one and leaves its options behind.
- Start without a model, when the provider has none set and the command
  line gives none, so the person can choose one in the front end; a run is
  refused until there is one, saying how to choose it.
- Keep a session where a person can read it, in
  `~/.rumbo/sessions/<id>/`: `session.json`, what it is and what
  it chose, and `entries.jsonl`, its entries, one to a line. A line a
  process died writing is no entry.
- Print a session's whole record (`rumbo log`), and the exact request any
  of its turns sent the provider, rebuilt from it (`rumbo request`).
- Record with each response the output limit its request set, how long the
  provider took, and the rumbo that sent it; and with each tool result how
  long the tool took. None of it is sent to the model.
- Tell a front end what its session holds, in the events its runs sent,
  with what the person asked and how each run ended, so it can show a
  session it continues; and what the session is called.

**Providers**

- Register, set, show, and remove providers. A provider is started
  when it is added, which is the test that it works. The `anthropic`,
  `openai`, and `chatgpt` of rumbo's own plugin are there without being
  registered, and cannot be removed; one registered under the name of one
  of them takes its place. `{self}` stands for rumbo's own executable in a
  command.
- Keep of each provider and front end only what the user chose for it,
  and for a provider the model new sessions start with. What is not chosen
  is the program's own default, so one a new version changes applies to
  everyone who chose nothing else.
- Start a provider and show each of its settings, its model, the fields of
  its configuration, and its options, with the value that applies and where
  it comes from, its own defaults included, and what it offers, which is
  also the test that it works (`rumbo provider show`, `provider-show`).
- Show everything that is set and what applies, each value with where it
  comes from, without starting anything (`rumbo config`, `config`).
- List the models a provider offers (`rumbo provider models`,
  `provider-models`) and the values its settings take, asking it, so a
  front end can offer them as choices: the models after the provider `use`
  names, and the `name=value` changes of `system-set`, `session-set`, and
  `provider-set`, with those in force marked.
- Keep as many processes of a provider as the requests running at once need,
  and replace one that died.
- Change the provider and the model of a session for its next runs, while a
  run in progress keeps the ones it started with.
- Keep the values of a provider's options in its configuration, with the
  rest of it. A run gets those with every request, the ones chosen in its
  session over them, and those its front end chose for it over both.
- Pass a provider's description to the front end, send the chosen options
  with every request, and relay calls to its functions, understanding none of
  them.
- Relay what a provider's function says to the person and asks of them while
  it works, and the answers back, and give the function up when the person
  does.
- Serve the providers of its own plugin itself (`rumbo provide <name>`),
  over the same protocol as any other.

**Front ends**

- Start a registered front end and give it the terminal, or be started by
  one (`rumbo serve`). Without `-f` or the `frontend` setting it starts `tui`
  in a terminal and `plain` elsewhere.
- Register, configure, show, and remove front ends, and serve `tui`, its
  own plugin's, and `plain`, which it has built in, itself (`rumbo attach
  <name>`); both are there without being registered.
- Describe functions of its own, which any front end offers as commands
  without knowing them, each saying what it acts on. For the system:
  `config`, `system-set`, `processes`, and `quit`. For its session: `status`, `skills`,
  `branch`, `rename`, `use`, `session-set`, and `compact`. For the
  providers:
  `providers`, `provider-models`, `provider-show`, `provider-set`,
  `provider-add`, and `provider-rm`. For the extensions: `extensions`,
  `extension-show`, `extension-set`, `extension-add`, and `extension-rm`.
- Mark the functions that are there to list the values a parameter takes,
  so a front end offers their values and not them as commands:
  `system-values`, `branch-points`, `session-values`, `provider-values`,
  and `extension-values`.
- Mark the functions that cannot be undone, so a front end asks the person
  to confirm them.
- Answer its own functions, and the provider's description and functions,
  while a run works, but for those that need the conversation still:
  `compact`, and `branch` from where the session is now.
- Report each run as events, each naming its run and session: the model's
  text, the tools it calls and what they return, the context sent, the
  tokens spent, and diagnostics.
- Say which processes it has running, itself, its front end when it
  started it, its providers', its extensions', what those started, and
  what the model left running, and the memory and the processor time each
  uses, as the system's `ps` reports them. It says the name of each
  program, never its arguments.

**Extensions**

- Register, set, show, and remove extensions (`rumbo extension`, or the
  host's functions of the `extension` group from any front end), which are
  the user's and the same in every project. One is started when it is
  added, when it is shown, and with a change to its configuration, together
  with the others, which is the test that it works and that the model can
  be given its tools with theirs; one that fails it is not kept.
- Give a run the tools of the extensions registered when it starts,
  declared after its own, by extension and then by tool, and pass the
  model's calls to them with the directory the session works in.
- Have an extension's tool take the place of one of its own, such as
  `bash`, where the user said so (`rumbo extension add -replaces bash`, or
  `rumbo extension set <name> replaces=bash`): the run then has the
  extension's and not rumbo's.
- Run one of its own tools for an extension that asks while it answers a
  call, where that call was made and for its session, the replaced ones
  included.
- Say with each call, and each request for a section, the session it is
  for.
- Relay what a call says it is doing, and what it asks the person, to the
  front end that follows the run, and the answer back; and tell the
  extension there is no one to ask when nobody follows it. None of it is
  recorded, and the model sees only what the call returns.
- Pass the extensions' functions to the front end with what the provider
  offers, relay calls to them, and what they say and ask while they work,
  as it does a provider's, understanding none of them.
- Tell an extension the events of the types it asked for, of its session,
  as a front end is told them, and besides what each run was asked and how
  it ended; without waiting for it, so one that reads too slowly misses
  some. What extensions print is not told to them.
- Tell the model the sections of the extensions when a run starts, after
  its own: each the first time, and again only when it changed. A section
  that says nothing, or whose extension was removed, is taken away, and one
  whose extension could not say it stays as it was told last, with the
  person told why. A long one is cut.
- Refuse a tool whose name another extension already has, or one of its
  own tools has and the user did not have it replace, or that a model API
  would not take, and a section called as another; and not start a run
  while an extension does not start or declares one of those, saying which
  and how to remove it.
- Keep one process of each extension while it lasts, start it again
  when it died, when it said that what it declares changed, or when its
  command or configuration changed, and stop it when it is removed and
  when the host exits.
- Tell an extension the name the user registered it under.
- Bound what an extension's result adds to the conversation as it bounds
  its own tools', and prepare its images as it does any other.
- Say the tools and the sections an extension declares, the tools of its
  own it replaces, and what declaring its tools adds to every request
  (`rumbo extension show`).
- Pass an extension's diagnostics to the front end, each line with the
  extension's name.
- Serve its own plugin's extension itself (`rumbo extend mcp`), over the
  same protocol as any other.

**Plugins**

- Add a plugin from a directory, which is read as it is each time, or from
  the address of a Git repository, which is copied into the user's data
  (`rumbo plugin add`). A plugin is a directory with a `plugin.yaml` that
  names the providers, front ends, and extensions it brings, each with the
  command that starts it, where `{plugin}` stands for the plugin's
  directory, and the configuration the plugin gives it; and with skills in
  `skills/`.
- Start what a plugin brings before it is added, its providers and, with
  all the others, its extensions, which is the test that it works; one that
  does not pass is not added, and a copy made for it is not left behind.
- Build a plugin where it is, with the command its manifest gives as
  `build`, in its directory, when it is added and when an update brings
  something new, showing what it prints; a plugin whose build fails is not
  added, and an update whose build fails goes back to what it was and
  builds that again.
- Have what a plugin brings there without being registered, as what is
  built in is: listed, shown, and set like any other program, with what the
  user sets for it over what the plugin gives it, and only that kept.
- Refuse a plugin whose name is taken, that brings a program another
  plugin brings, the user registered, or rumbo has built in, or whose
  manifest has a key it does not know.
- Let an extension of a plugin take the place of one of its own tools only
  when the user agreed to it by name as they added the plugin
  (`-replaces bash`), saying which it asks for when they did not.
- Bring a plugin added from an address up to date (`rumbo plugin update`),
  and leave it as it was when what it is now does not pass, or asks to
  replace a tool the user did not agree to.
- Take a plugin out (`rumbo plugin rm`), with what the user had set for the
  programs it brought and the copy of its repository; a directory it was
  added from is left as it is.
- List the plugins and say what each brings (`rumbo plugin list`, `rumbo
  plugin show`), and stop with a plugin whose manifest can no longer be
  read, or that comes to bring what another plugin brings, saying how to
  take it out, instead of going on without what it brought.
- Have its own plugin there before any the user adds, built into the
  executable with its programs: its providers, its terminal front end, and
  its extension for MCP servers. It is listed, shown, and set like any
  other, has nothing in the configuration file, and is never taken out or
  updated apart from rumbo.
- Keep of a plugin, in the configuration file, only where it comes from
  and the replacements the user agreed to.

**System settings**

- Store the system settings, rumbo's own and the same everywhere: the
  provider a new session starts with, the front end, the turn limit, the
  output limit, the share of the model's context at which to summarize,
  and whether the model has the computer tool. List them with where each
  value in force comes from (`rumbo system`).
- Apply a stored system setting, or a provider's option, to the runs that
  start after it is stored, without starting again, whether a command
  stored it or a person wrote it in the file. The default provider and the
  front end apply from the next start.

### Must not

- Know any kind of work: no project detection, and nothing about
  programming or any other domain.
- Look above the directory it is started in: not for a git repository, and
  not for instructions or skills.
- Work in a session other than its own, list the others, or move to one:
  that is for the command line, and for the front end that starts another
  rumbo.
- Judge the model's work. A run ends when the model stops calling tools,
  and the host runs no checks of its own.
- Send context nobody asked for, beyond the environment, the user's
  instructions, what the user's skills are for, and the sections of the
  extensions the user registered.
- Limit what the agent may do. It acts with the rights of the user who
  started it, inside whatever sandbox the user chose; a limit is the user's
  to bring, in an extension that replaces the tool it limits.
- Track the files the model changes. A session keeps the conversation, not
  the disk.
- Call a model API, render anything, or see a provider's credentials.

## Provider

A provider turns a conversation into the next assistant message. It speaks
the Agent Provider Protocol on its stdin and stdout.

### Must

- Answer `initialize` with protocol version 1, its name, and its
  configuration in force, with a default for every field it was not given,
  for a person to see.
- Answer `complete` with the next assistant message, why it stopped (it
  ended, it calls tools, the output limit cut it, or the model refused), and
  what it cost. Every request carries the whole conversation.
- Answer one request at a time. When the host needs more at once, it starts
  more processes.
- Give each tool call the arguments the model produced, as JSON, or as a
  string when they are not valid JSON.
- Mark a failure that can succeed if the request is sent again as transient,
  and a request that did not fit in the model's context as such. Any other
  error ends the run.
- Answer a request it was asked to cancel, normally with an error, within a
  few seconds.
- Answer `shutdown` and exit.
- Write only protocol messages to stdout. Diagnostics go to stderr, which
  reaches the user.
- Hold its own credentials, and keep them where it chooses. The host keeps
  none: one a person types at the provider's question it relays without
  recording.
- Ignore opaque blocks it does not recognize, which is what lets a session
  change provider.

### Can

- Stream the visible text of a response while it is produced.
- Report how many tokens the model's context holds, so the host summarizes
  before it fills.
- Describe how its model is chosen, the options it accepts, each with its
  default, and the functions it offers. The host and the front ends offer
  them without knowing them.
- Offer functions, such as listing its models. One that cannot be undone
  asks to be confirmed, and one that an option names returns the values the
  option accepts.
- Talk with the person while a function works: say things, and ask
  questions, marking an answer secret when it is one, such as the code a
  sign-in gives. The function takes as long as the person does.
- Read what the user chose for it, its configuration, which holds the
  values they set for its options too; the host sends those with every
  request as well, so a provider that reads its options only there gets
  them.
- Keep what it needs to continue a conversation, such as signed reasoning,
  in opaque blocks the host returns unchanged.
- Show the model the images of a tool's result. One whose model API takes
  none there sends the result's text, which says what each image is.
- Cache the start of requests, which the host keeps stable, and report how
  much of each it read from the cache and wrote to it, which is how the
  host tells a request that missed it.

### Must not

- Rely on anything but the request it is answering: the next request of the
  same session may go to another process.

### Of rumbo's own plugin

- **`anthropic`** reaches the Claude API with `ANTHROPIC_API_KEY`,
  `ANTHROPIC_AUTH_TOKEN`, the key its `login` stored, or the profile from
  `ant auth login`, in that order. It lists the models the account can use,
  offers an `effort` option, keeps the model's thinking in opaque blocks,
  and reports the size of the model's context.
- **`openai`** reaches any server compatible with OpenAI's Chat Completions,
  at the address its configuration gives, with the key in the variable its
  configuration names (`OPENAI_API_KEY` by default), or else the key its
  `login` stored for that address. It lists the server's models, offers an
  `effort` option, sent as `reasoning_effort` only when it or the
  configuration chooses one, and takes the size of the context from its
  configuration, since a server cannot say. With `api` set to `responses`
  in its configuration, as rumbo's own plugin has `openai`, it speaks
  OpenAI's Responses API instead, which its reasoning models need to use
  tools with an effort: nothing is stored on the server, and a model's
  encrypted reasoning goes back to that model alone, in opaque blocks.
- Both offer `login`, which asks for an API key, checks it with a request
  that costs nothing, and keeps it for the server in the user's
  configuration directory (`rumbo/credentials/`), readable only by the user;
  `logout`, which removes it; and `account`, which says which credential is
  in use and where it comes from, without showing it.
- **`chatgpt`** uses the person's ChatGPT plan instead of an API key, as
  OpenAI offers to open-source apps that run where the person does. Its
  `login` signs in with ChatGPT in the browser, which comes back to a
  listener on the loopback, or, from another computer, to an address the
  person pastes; OpenAI registers this installation as its client on the
  way. It keeps what that gives with the other credentials, renews it as it
  expires, one process at a time, and forgets it when it can no longer be
  renewed. Its requests go to OpenAI's Responses API as the plan asks:
  nothing stored, the answer streamed, and no limit on the output. It lists
  the models the plan can use, offers an `effort` option, and says where to
  manage the plan's usage, in `account` and when a usage limit is reached.
  `logout` forgets the sign-in and takes it back on OpenAI's side.
- Anthropic's terms do not let other applications use a Claude
  subscription, so no provider of rumbo's does; `anthropic` takes an API key.

## Extension

An extension gives the model tools of its own, and tells it things without
being asked. It speaks the Agent Extension Protocol on its stdin and stdout.

### Must

- Answer `initialize` with protocol version 1, its name, the tools it
  declares, each with a name no other extension's tool has, a description
  for the model, and a JSON Schema for its arguments, and the names of its
  sections; or with an error when it cannot work with the configuration it
  was given.
- Answer `section` with what one of its sections says for the directory a
  run starts in, within a few seconds, and with the same text while nothing
  changed.
- Answer each `call` with what the model reads, saying how to recover when
  the call failed. The arguments are the model's, and it checks them.
- Answer `shutdown` and exit.
- Write only protocol messages to stdout. Diagnostics go to stderr, which
  reaches the user.
- Hold its own credentials, and keep them where it chooses.

### Can

- Keep what it needs from one call to the next: it is started once and
  lasts as long as the host, which is one session.
- Answer calls made at once in any order, or one at a time.
- Show the model images with a result.
- Stop the work of a call it was told was cancelled.
- Read what the user chose for it, its configuration.
- Declare a tool called as one of rumbo's own, to do it another way: in a
  container, on another machine, or only what it allows. It takes rumbo's
  place once the user says so.
- Call rumbo's own tools while it answers a call, so a tool that replaces
  one of them does something before and after it instead of all its work.
- Keep something for each session from one host to the next, since every
  call names its session.
- Be told what the runs do, in the events it names when it starts: to
  notify when one ends, to count what it cost, or to record what it did.
- Say what a call is doing while it works, and ask the person who follows
  the run something, such as whether a command may run, deciding itself
  what no answer means.
- Offer the person functions, such as a sign-in, which a front end shows
  as commands named after the extension, and which talk with the person as
  a provider's do. They cost no tokens.
- Say that what it declares changed, as when a sign-in gives it tools, to
  be started again and heard anew.
- Tell the model something when a run starts, in a section, and take it
  away by saying nothing.

### Must not

- Declare more than a task is likely to use: every declaration is sent with
  every request, and paid for by tasks that never call it. A section is
  paid for the same way.
- Count on being asked before the model acts some other way: it is told
  what happens, and decides only what its own tools do.
- Hold up its reading of events, which the host does not wait for.

### Of rumbo's own plugin

**`mcp`** gives the model the tools of one server of the
[Model Context Protocol](https://modelcontextprotocol.io). It is registered
once for each server, under a name of the user's:

- It starts the server its command names
  (`rumbo extension add files {self} extend mcp <command>...`) and speaks
  with it on its stdin and stdout, or reaches the one at the `url` of its
  configuration over MCP's Streamable HTTP transport, keeping the session
  the server opens.
- It declares the server's tools as the server describes them. `tools`
  names the ones the model is given, separated by commas, since a server
  may have dozens and each is declared with every request; and `prefix`
  goes before each name, to tell them from the tools of another server.
  Its function `tools` lists every tool the server has, with what each
  does and those `tools` names marked, for a front end to choose among.
- A result's text and images go to the model; a resource with text is its
  text, a result given only as structured data is that JSON, and what a
  model cannot be shown, such as audio, is said in a line.
- A server at a `url` that wants a sign-in gets one as MCP specifies it,
  with OAuth: the adapter starts with no tools and one command, `login`
  (`/login <name>` in the terminal UI), which finds where to sign in from
  what the server and its authorization server publish, registers rumbo
  there as a client, and sends the person to their browser, with PKCE and
  a redirect to the loopback; from another computer, the person pastes the
  address the browser ended at. What that gives is kept with the other
  credentials (`rumbo/credentials/mcp.json`), readable only by the user,
  and renewed as it expires. Once signed in, the adapter is started again
  with the server's tools and a `logout` command, which forgets the
  sign-in. A sign-in that ends and cannot be renewed is forgotten, the
  model is told how the person mends it, and `login` is offered again.
- `client_id` is the client the user registered by hand, for an
  authorization server that registers none by itself. An authorization
  server that is not reached over https, off the loopback, is refused.
- `token_env` names the environment variable holding a token of the user's
  own, for a server that takes one instead of a sign-in, so the token is in
  no file of rumbo's.
- It answers a server's ping, and offers it nothing else: no sampling, no
  roots, and no questions for the person. It does not use a server's
  resources or prompts, and reads its tools once, when it starts.
- When the server it started exits, it exits too, so the host starts both
  again for the next call. It refuses a field its configuration does not
  have, and a name in `tools` the server has no tool of.
- It does not sign in to a server that describes its authorization server
  only by a client metadata document, nor end a sign-in on the server's
  side when the person signs out.

## Front end

A front end is what a person uses to give objectives and follow the work. It
speaks the Agent Frontend Protocol with the host.

### Must

- Answer the host's `initialize` with protocol version 2, its name, and its
  start configuration in force, with a default for every field it was not
  given.
- Refuse a field of its configuration it does not have, or a value it
  cannot use, saying what is wrong, since the host checks neither.
- Run the objective the command line gave, or resume the unfinished run,
  when `initialize` asks for it.
- Ignore event types and fields it does not know.
- Not reuse the id of a run in progress, since it names the run.
- Decide what Ctrl-C means, and cancel through the protocol.
- Show the confirmation a function asks for, and call the function only
  after the person agrees.
- Answer a question a function, or a tool of a run it follows, asks through
  it, with what the person typed or with an error, and neither show nor
  keep a secret answer. One nobody is at answers a tool's with an error.
- Wait for an answer when one request has to take effect before another.

### Can

- Start runs in the host's session, one at a time, and choose the model
  and the options for one run.
- Start another host for another session, a new one or one it continues,
  hold several at once, group them by the directory each works in, and
  close those it does not need.
- Ask each host which processes it has and what they use of the machine,
  to say what the sessions it holds cost and choose which to close.
- Show what a session held before it was continued, from its history.
- Bring back to its input what the person asked in the sessions of the
  host's directory, which the host recalls.
- Give files with a task, or with what it tells a run, by their paths
  where the host runs. What has no path there, such as an image pasted
  from the clipboard, it saves first, where and for as long as it
  chooses.
- Tell a run something while it works, and offer it as a new task when the
  run ended first.
- Cancel one run, or all of them.
- Ask what the provider offers and call its functions, call the host's
  functions, and build commands, pickers, and completion from their
  descriptions without knowing them beforehand. A function that lists a
  parameter's values is given what was typed for the parameters before it
  that it takes.
- Show what a provider's function says while it works, ask the person what
  it asks, and give the function up.
- Offer the extensions' functions as commands, each named after its
  extension, and call them at the extension, as it does the provider's.
- Show what a call in progress says it is doing.
- Take what it is attached to, its directory, session, provider, and model,
  from the host's answers instead of remembering it.
- Be started by rumbo, with the terminal and with the protocol on file
  descriptors 3 and 4 (on Windows, two inherited handles
  `RUMBO_FRONTEND_HANDLES` names), or start `rumbo serve` itself and speak
  on its stdin and stdout.
- Read the configuration registered with it.
- End the host by closing the connection.

### Those that come with rumbo

**`tui`**, the terminal UI, of rumbo's own plugin:

- It takes the whole screen, as rumbo-gui takes a window: a sidebar with
  the sessions of the folder, a row apart, each with how long ago it was
  used, put away when it starts, which Ctrl-B shows or puts away; the conversation across the main area, short of its
  sides, which PgUp,
  PgDn, and the mouse's wheel scroll; and under it a composer with the
  model, the Git branch checked out, the session's folder under the home
  as `~/`, how full the context is, and what the session spent, over a
  line that says what the keys do. The conversation goes with the screen when it quits.
- What the person said shows in a box on the right, and what the model
  writes as the Markdown it is: headings, lists, quotes, code, bold,
  italics, links, and tables with their columns lined up. A code block
  shows its language and its code alone, so it can be selected and copied
  as it is.
- A tool call shows with a spinner and its time while it works, and then
  as a block that says how it went: an edit with the lines it changed, a
  command with the start of what it printed, or, when it failed, with its
  status and the end of what it printed.
- A run ends with a line that says how it ended, how long it took, its
  tokens in and out, what the provider's cache gave of what went in, and
  its turns.
- It holds a rumbo for each session it has open, and shows one at a time:
  the one that started it, and those it starts itself, with `rumbo serve`,
  when the person first shows another session of the folder, from the
  sidebar, with Tab and Enter or a click, or with `/resume`, or starts
  a new one with Ctrl-N or `/new`. A session shows what it holds first, told as its
  runs were shown. It lists the sessions with `rumbo sessions -json`.
- A run goes on in a session that is left: the composer counts the runs
  working elsewhere, the sidebar marks the sessions that work, those whose
  run ended unseen, and those a rumbo is open for, and the end of a run
  elsewhere is said where the person is. What a tool of such a run asks
  opens where the person is too, saying which session asks.
- It closes the rumbos of the sessions left with nothing to do past the
  two shown last, never the one that started it, one that works, or one
  that waits for an answer; `/rumbos close` closes them all. A session closed that way is as it was, and showing it
  starts a rumbo for it again. When it quits it ends every rumbo it
  started, whose runs are cancelled and recorded, and it ends with the
  one that started it. `/quit` ends that one, whichever session shows.
- `/rumbos` says what the rumbo of each session open uses, from what each
  answers to `processes`: how many processes, their memory, and their
  processor time, each process counted once.
- `/delete` deletes the session shown once the person agrees, with
  `rumbo sessions rm`, after ending the rumbo it started for it, and shows
  the first one in its place. It refuses the session of the rumbo that
  started it, which that one holds, and one that works. In the sidebar,
  d does the same with the session chosen; one no rumbo of the terminal
  UI holds is deleted with `rumbo sessions rm` at once, which refuses one
  another rumbo has open. j, k, gg, and G choose there as in the
  conversation.
- `/branch` shows the branch once its rumbo starts. In the conversation,
  Enter on what the person asked offers to change it or to ask it again in
  a branch that starts before it, which shows the same way.
- Enter sends a task. During a run it tells the run what was typed, which
  shows once the model has it, and goes back to the composer if the run
  ended first.
- Everything is done with a command typed in the composer; Ctrl-P starts
  one on an empty line, as a slash does. A command that cannot be undone
  asks to be agreed to in a dialog, which starts on the button that leaves
  things as they are.
- The picker, from `/model` or a click on the model under the composer,
  changes the provider, the model, and the options of the session, with
  `use` and `session-set`, each change shown as the `/model` it is.
- Beside the conversation, Ctrl-Alt-B shows the session's details: its id
  and what it is called, its folder, its model, how full its context is and
  what it spent, and what the host's `skills` and `status` say, laid out by
  the shape of their lines; and Ctrl-G what changed in the project's
  folder, as Git sees it against the last commit, new files included,
  which it only reads, with Git's optional locks off. Where there is no
  room for both, the panel takes the conversation's place.
- Shift-Tab goes up into the conversation, where Up and Down, or k and j,
  choose an entry, K and J the person's messages, and gg and G the first
  and the last, and Enter acts on it, as a click on it does: a tool call
  shows all it was given and returned, or goes back to its excerpt, what
  the person asked offers what can be done with it, and anything else is
  copied. c copies the entry chosen, and b chooses the person's message
  its task started with and branches the session from it, once they
  agree: the branch holds the conversation before that message, and the
  message is in its composer.
- Dragging the mouse over the conversation or the panel selects its text,
  shown in reverse, within the part the drag started in, and letting go
  copies it, to the clipboard and to the primary selection; the
  conversation holds still while text is selected. Text is copied with
  OSC 52, which also works over ssh, in the terminals that take it, and
  through the desktop rumbo runs on, since a terminal or a tmux that does
  not take OSC 52 drops it without a word: with `wl-copy`, `xclip` or
  `xsel`, or `pbcopy`, and else, in tmux, through tmux's buffer, which
  tmux sends on to the terminal's clipboard. With Shift, the terminal
  selects as it does elsewhere.
- Its commands are few, each in the person's words, and made of the
  functions the host, the provider, and the extensions describe, which a
  person need not know (`verbs.go`, `verbs_config.go`): `/new`,
  `/sessions`, `/resume`, `/rename`, `/branch`, `/attach`, `/compact`,
  `/delete`, `/copy`, `/export`, `/status`, `/changes`, `/model`,
  `/login`, `/logout`, `/providers`, `/extensions`, `/settings`,
  `/rumbos`, `/help`, and `/quit`. Those functions are not commands of their own,
  nor are the provider's and the extensions'; a function of the host no
  command wraps, as a newer host has, is one, under its name, but for
  those there to list a parameter's values. `/help` lists the commands by
  what they are about, those others after them, and the keys, and `/help
  <command>` says what one does, with examples. The menu shows an example
  of the command chosen, what a command still takes shows after the
  cursor, dimmed, and a command typed wrong is answered with the one
  meant. An empty conversation shows the few commands a person needs
  first.
- `/sessions` lists the sessions of the folder, marked as the sidebar
  marks them, with when each was last used; `/resume <session>` goes to
  one, named by its id, the start of it, or the start of its title.
- `/model` opens the picker, and `/model <provider>/<model>
  <option>=<value>` changes the model and the options at once; what comes
  before the slash is a provider only when there is one of that name, so a
  model's own name may hold slashes. `/login` and `/logout` sign in to the
  provider, or to the extension named.
- `/providers` lists the providers, `/providers <name>` shows one,
  `/providers <name> <name>=<value>…` sets it, `/providers add <name>
  <command…>` registers one, and `/providers remove <name>` removes one,
  once the person agrees; what the session's provider offers is called as
  `/providers <name> <function>`. `/extensions` is the same for the
  extensions, whose `add` also takes `config=` and `replaces=` by name,
  and whose functions are called as `/extensions <name> <function>`.
  `/settings` shows what applies and where each value comes from, and
  `/settings <name>=<value>…` changes rumbo's own settings. Each word is
  completed: the programs, `add` and `remove`, the changes each takes but
  those given already, what each offers and what its functions take, from
  what that program lists, and for `add` how the programs there are are
  started. A field of an extension's configuration that the extension
  lists the values of, by a function of the same name, is completed as a
  list separated by commas, item by item, from the list as it is chosen
  now: `/extensions linear tools=` offers the MCP server's tools.
- `/copy` puts the model's last answer in the clipboard, `/copy code`
  only its last code block, and `/copy id` the session's id.
- `/export` writes the session as Markdown, from its history: what
  was asked, what the model wrote, each tool call with the change it made
  or the start of what it returned, and how each run ended. It writes to
  the path given, from the session's directory, or to `rumbo-<session>.md`
  there, and never over a file that exists.
- A slash in the composer opens a menu of the commands, with what each
  does, and after a command, of what its next parameter takes, with what
  the host or the provider says of each value: a session's title, the text
  of a message to branch before. It narrows as one types, by value or by
  what describes it; Up and Down choose, Tab or Enter take, and Escape
  closes it. Enter on a command that needs nothing more sends it, and on
  one that still needs something opens the menu of what it takes next, or
  says what that is: `/resume` lists the sessions, `/model ` the providers
  and the options, and `/model openai/` the models of `openai`. After
  `/providers <name> `, `/extensions <name> `, and `/settings `, the menu
  offers the changes each takes as `name=value`, with those in force
  marked.
- The words after a function go to its parameters in turn, and the last
  takes the rest of the line. One that can be left out is also typed by
  name, as `name=value`, and one that comes before one that cannot only
  so: `/extensions add docs config={"url":"https://example.com/mcp"}
  {self} extend mcp`.
- Answers, errors, and diagnostics are marked `[host]`, `[provider
  <name>]`, or `[extension <name>]`, by where they come from; what an
  extension prints is marked `[extension]` and starts with its name.
- What a provider's function says shows marked as the provider's. Its
  question opens in a dialog with a line of its own for the answer, so
  what was typed in the composer stays; a secret answer shows as dots and
  is kept nowhere, not even in the history.
- An extension's functions are called after its name, such as
  `/extensions notes forget`, offered by the menu there, and talk with the
  person as the provider's do.
- What a tool asks during a run opens in the same dialog, naming the tool,
  in the conversation that shows the run. Escape declines to answer, which
  leaves the tool to decide and the run going; the question goes when its
  run ends.
- A call in progress shows what it last said it is doing, on its line.
- The composer takes several lines: Enter sends them, and Alt-Enter,
  Shift-Enter, or Ctrl-J starts a new line. Pasted text keeps its line
  breaks. It grows to ten rows, or a third of a short screen, and then
  scrolls with the cursor.
- Up and Down move through the rows typed, and past them through what was
  entered; Ctrl-R searches it. It starts with what the host recalls of the
  project's sessions, and adds what is entered while it runs, commands
  too, which it keeps nowhere; answers to a question are not in it.
- `/attach` gives a file of any kind with what is sent next, by its path,
  from the session's directory. Ctrl-V does it for what the desktop's
  clipboard holds, read with `wl-paste` on Wayland, `xclip` on X11, or
  AppKit on macOS, since a terminal pastes text only: the files copied in
  a file manager, where they are, on Wayland and X11, or else an image,
  saved as it is in rumbo's directory (`pasted`), where it stays; on
  Windows it says it does not read the clipboard yet, and to use `/attach`. A
  file dropped on the terminal, which pastes its path, is given too: a
  paste that is wholly the whole paths of files there are, quoted as a
  shell takes them or as `file://` addresses, is those files and no text.
  The files wait over the composer for what is sent next, each named after
  an icon for its kind, which the end of its name says, or one for any
  file; Backspace on an empty composer takes the last one away.
- Escape gives up a provider's function in progress, else cancels the run
  of the session shown. Ctrl-C does the same, else clears the composer,
  else quits; Ctrl-D quits on an empty composer.
- The composer edits where the cursor is, with readline's keys: Left,
  Right, Home, and End, or Ctrl-F, Ctrl-A, and Ctrl-E, move in it, and by
  words with Ctrl or Alt and the arrows, or Alt-B and Alt-F; Delete or
  Ctrl-D deletes under the cursor, and Ctrl-W, Ctrl-U, and Ctrl-K delete a
  word back, to the start of the line, and to its end.
- The composer's last row shows the provider and the model, or that there
  is no model yet; how full the context is, as a bar that turns yellow
  past half and red past four fifths; the tokens the session has spent;
  and how the run of the session shown stands. What does not fit leaves,
  the least needed first.
- When a run ends, or a provider's function asks something, while the
  terminal is not in front, it rings the terminal's bell and sends a
  desktop notification, in the terminals that report their focus and take
  OSC 9; in tmux, with `focus-events` on.
- Its start configuration chooses its colors: `theme` is `auto`, which
  shows them unless `NO_COLOR` is set, `color`, or `plain`, which shows
  none; and `color_host`, `color_provider`, `color_code`, `color_success`,
  `color_failure`, `color_warning`, and `color_progress` each take a name,
  such as `cyan` or `bright-blue`, a number of the 256 colors, written as
  text or as a number, `#rrggbb`, or `default`. It says the configuration
  in force, with its defaults, and a field it does not have, or a value it
  cannot use, stops it with what is wrong, before it takes the terminal.
- It gives the terminal back as it found it, when it quits or the host
  ends, and leaves in it the command that continues its session,
  `rumbo -s <session>`.

**`plain`**, for pipes and scripts, which rumbo has built in:

- It takes the objective from the command line or from piped input, or
  reads objectives line by line.
- The model's text goes to stdout; progress, the session, and the tokens
  spent go to stderr.
- Ctrl-C cancels the run. It exits with 0 when the run is done, 1 when it
  failed, and 130 when it was cancelled.
- It has no configuration: a field given it stops it, saying how to take
  it out.
