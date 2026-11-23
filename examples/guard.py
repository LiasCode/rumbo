#!/usr/bin/env python3
"""An extension that takes the place of one of rumbo's own tools.

It declares a tool called `bash`, as rumbo's is. Once you say it replaces
rumbo's, the model's `bash` is this one: before a command with `rm -rf` it
asks you whether to run it, and it passes what it lets through to rumbo's
own bash. It reaches both by sending the host requests of its own while it
answers the call: `ask`, for the person who follows the run, and `call`,
for a tool of rumbo's. An extension can put something before and after any
tool of rumbo's that way, without doing the tool's work again.

Register it in place of rumbo's bash:

    rumbo extension add -replaces bash guard python3 /path/to/examples/guard.py
    rumbo "delete everything in /tmp/scratch with rm -rf"   # it asks you first
"""

import json
import sys

# The model is told this tool instead of rumbo's, so it says what it takes.
# rumbo's bash takes more than `command`; whatever the model gives is passed on.
TOOL = {
    "name": "bash",
    "description": "Run a bash command in the working directory. The user is asked before one with `rm -rf` runs.",
    "schema": {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]},
}


def send(frame):
    # One JSON object per line. Flush, or the host waits forever.
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", **frame}) + "\n")
    sys.stdout.flush()


def request(method, params):
    """Ask the host for something, from inside the call `params["id"]` names."""
    # A request of the extension's own, with an id of its own.
    send({"id": 1, "method": method, "params": params})

    # This extension answers one call at a time, so the next response is the
    # answer. One that answers several at once matches each by its id.
    for line in sys.stdin:
        reply = json.loads(line)

        if reply.get("id") == 1 and "method" not in reply:
            return reply

    sys.exit(0)


def allowed(call_id, command):
    """Ask the person whether a command may run. Nobody to ask means no."""
    reply = request("ask", {"id": call_id, "text": "Run `%s`? Type yes to allow it." % command})

    # An error is the answer when the person declined, or when no one
    # follows the run, as in a script.
    return reply.get("result", {}).get("text", "").strip().lower() == "yes"


for line in sys.stdin:
    frame = json.loads(line)
    method = frame.get("method")

    if method == "initialize":
        send({"id": frame["id"], "result": {"protocol": 1, "name": "guard", "tools": [TOOL]}})

    elif method == "call":
        args = frame["params"]["args"]
        command = args.get("command", "")

        if "rm -rf" in command and not allowed(frame["id"], command):
            result = {"text": "the user did not allow this command; do it another way, or ask them", "is_error": True}
        else:
            # rumbo's own bash runs where this call was made. A tool that
            # failed is a result too, with `is_error` set.
            reply = request("call", {"id": frame["id"], "name": "bash", "args": args})
            result = reply.get("result") or {"text": reply["error"]["message"], "is_error": True}

        send({"id": frame["id"], "result": result})

    elif method == "shutdown":
        send({"id": frame["id"], "result": {}})
        break
