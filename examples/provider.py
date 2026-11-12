#!/usr/bin/env python3
"""A complete provider in one file, to show the Agent Provider Protocol.

A provider is a program that reads JSON-RPC requests, one per line, on stdin
and writes responses on stdout. This one has no model behind it: it asks for
one tool call and then repeats the tool's output, which is enough to see every
message of the protocol. Replace `answer` with a call to your model or
subscription to make it real.

Register and use it:

    rumbo provider add toy python3 /path/to/examples/provider.py
    rumbo -m toy/any "list the files"
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


for line in sys.stdin:
    frame = json.loads(line)
    method = frame.get("method")

    if method == "initialize":
        # `params.config` holds this provider's settings as registered in rumbo.
        send({"id": frame["id"], "result": {"protocol": 1, "name": "toy"}})

    elif method == "complete":
        result = answer(frame["id"], frame["params"])
        result["usage"] = {"input": 0, "output": 0}
        send({"id": frame["id"], "result": result})

    elif method == "shutdown":
        send({"id": frame["id"], "result": {}})
        break

    # `cancel` is a notification asking to abort a running completion. This
    # provider answers instantly, so there is never anything to abort.
