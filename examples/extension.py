#!/usr/bin/env python3
"""A complete extension in one file, to show the Agent Extension Protocol.

An extension is a program that gives the model tools of its own, tells it
things without being asked, and offers the person commands. It reads
JSON-RPC requests, one per line, on stdin and writes responses on stdout.
This one keeps notes in the process: its tool, `remember`, keeps one; its
section, `notes`, tells the model all of them when a run starts; and its
function, `forget`, is a command for you, which asks before it forgets them.
rumbo starts an extension once and keeps it while it runs, so what one call
leaves is there for the next, in any session.

Register it, and the runs that start from then on have it:

    rumbo extension add notes python3 /path/to/examples/extension.py
    rumbo extension show notes
    rumbo                 # ask it to remember something, then ask what it knows;
                          # /notes-forget in the terminal UI forgets it all
"""

import json
import sys

# What the model is told of each tool, with every request: keep it short.
# `schema` is a JSON Schema object for the arguments.
TOOLS = [
    {
        "name": "remember",
        "description": "Keep a note for later in this rumbo.",
        "schema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]},
    },
]

# A section is ambient context: text the model is told when a run starts,
# and again only when it changed. It is sent whether the task needs it or
# not, so it is for the little that most tasks do.
SECTIONS = ["notes"]

# A function is for the person, not the model: a front end offers it as a
# command named after the extension, here /notes-forget. It costs no tokens.
FUNCTIONS = [{"name": "forget", "description": "Forget every note"}]

notes = []


def send(frame):
    # One JSON object per line. Flush, or the host waits forever.
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", **frame}) + "\n")
    sys.stdout.flush()


def call(params):
    """Run one tool. `args` is what the model gave; `dir` is where the session works."""
    if params["name"] == "remember":
        text = params["args"].get("text", "").strip()

        if not text:
            # A failed call says how to recover: the model reads it and tries again.
            return {"text": "text is empty; give the note to keep", "is_error": True}

        notes.append(text)

        return {"text": "kept"}

    return {"text": "there is no tool " + params["name"], "is_error": True}


def section(params):
    """Say one section, for a run that starts in `dir`. Empty text takes it away."""
    return {"text": "\n".join("- " + note for note in notes)}


def forget(invoke_id):
    """A function that talks with the person while it works."""
    # A delta with the invoke's id is something the function says.
    send({"method": "delta", "params": {"id": invoke_id, "text": "There are %d notes." % len(notes)}})

    # A question is a request of the extension's own, with an id of its own;
    # the host answers it with what the person typed, or with an error when
    # they gave up.
    send({"id": 1, "method": "ask", "params": {"id": invoke_id, "text": "Forget them all? Type yes to do it."}})

    # This extension does one thing at a time, so the next response is the
    # answer. One that does several matches each by its id.
    for line in sys.stdin:
        reply = json.loads(line)

        if reply.get("id") == 1 and "method" not in reply:
            break
    else:
        sys.exit(0)

    if reply.get("result", {}).get("text", "").strip().lower() != "yes":
        return "nothing was forgotten"

    notes.clear()

    return "every note is forgotten"


for line in sys.stdin:
    frame = json.loads(line)
    method = frame.get("method")

    if method == "initialize":
        # `params.config` holds what the user chose for this extension in
        # rumbo (`rumbo extension set notes field=value`). An error here
        # refuses it, and rumbo does not keep the change.
        result = {"protocol": 1, "name": "notes", "tools": TOOLS, "sections": SECTIONS, "functions": FUNCTIONS}
        send({"id": frame["id"], "result": result})

    elif method == "call":
        send({"id": frame["id"], "result": call(frame["params"])})

    elif method == "section":
        send({"id": frame["id"], "result": section(frame["params"])})

    elif method == "invoke":
        send({"id": frame["id"], "result": forget(frame["id"])})

    elif method == "shutdown":
        send({"id": frame["id"], "result": {}})
        break

    # `cancel` is a notification asking to abort a call in progress. This
    # extension answers at once, so there is never one to abort.
