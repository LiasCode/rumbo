#!/usr/bin/env python3
"""A complete provider in one file, to show the Agent Provider Protocol.

A provider is a program that reads JSON-RPC requests, one per line, on stdin
and writes responses on stdout. This one has no model behind it: it asks for
one tool call and then repeats the tool's output, which is enough to see every
message of the protocol. Replace `answer` with a call to your model or
subscription to make it real.

It also offers one function, `greet`, which talks with the person while it
works, the way a login asks for a code: it says something, asks a question,
and answers with what it was told.

Register and use it:

    rumbo provider add toy python3 /path/to/examples/provider.py
    rumbo -p toy -m any "list the files"
    rumbo -p toy -m any       # then type /greet in the terminal UI
"""

import json
import sys


def send(frame):
    # One JSON object per line. Flush, or the host waits forever.
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", **frame}) + "\n")
    sys.stdout.flush()


def answer(request_id, params):
    """Turn the conversation so far into the next assistant message."""
    last = params["messages"][-1]
    results = [block for block in last["content"] if block["type"] == "tool_result"]

    if not results:
        # First turn: ask the host to run a tool. The host executes it and
        # calls `complete` again with the result appended to the history.
        call = {"type": "tool_call", "id": "call-1", "name": "bash", "args": {"command": "ls"}}

        return {"message": {"role": "assistant", "content": [call]}, "stop": "tool_calls"}

    text = "The directory contains:\n" + results[0]["text"]

    # Deltas stream text to the user while the answer is being produced.
    send({"method": "delta", "params": {"id": request_id, "text": text}})

    return {"message": {"role": "assistant", "content": [{"type": "text", "text": text}]}, "stop": "end"}


def greet(invoke_id):
    """A function that talks with the person while it works."""
    # A delta with the invoke's id is something the function says.
    send({"method": "delta", "params": {"id": invoke_id, "text": "Hello, I am the toy provider."}})

    # A question is a request of the provider's own, with an id of its own;
    # the host answers it with what the person typed, or with an error.
    send({"id": 1, "method": "ask", "params": {"id": invoke_id, "text": "What is your name?"}})

    # This toy waits for that answer alone. A cancel of the function may
    # come instead, when the person gives up.
    for line in sys.stdin:
        reply = json.loads(line)

        if reply.get("method") == "cancel":
            return {"error": {"code": -32603, "message": "greeting given up"}}

        if reply.get("id") == 1 and "method" not in reply:
            break
    else:
        sys.exit(0)

    if "error" in reply:
        return {"error": {"code": -32603, "message": "no name was given: " + reply["error"]["message"]}}

    return {"result": {"greeting": "Nice to meet you, " + reply["result"]["text"] + "."}}


for line in sys.stdin:
    frame = json.loads(line)
    method = frame.get("method")

    if method == "initialize":
        # `params.config` holds this provider's settings as registered in rumbo.
        # The answer gives them back with a default for every field left out,
        # which rumbo keeps with the registration; this toy has none. The
        # functions it lists become commands in any front end.
        config = frame["params"].get("config") or {}
        functions = [{"name": "greet", "description": "Ask your name, and greet you"}]
        send({"id": frame["id"], "result": {"protocol": 1, "name": "toy", "config": config, "functions": functions}})

    elif method == "complete":
        result = answer(frame["id"], frame["params"])
        result["usage"] = {"input": 0, "output": 0}
        send({"id": frame["id"], "result": result})

    elif method == "invoke":
        send({"id": frame["id"], **greet(frame["id"])})

    elif method == "shutdown":
        send({"id": frame["id"], "result": {}})
        break

    # `cancel` is a notification asking to abort a running request. This
    # provider answers completions at once, so only a function waiting for
    # its answer has anything to abort.
