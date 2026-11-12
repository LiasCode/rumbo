#!/usr/bin/env python3
"""A complete front end in one file, to show the Agent Frontend Protocol.

A front end is the program a person uses: it sends objectives to the host and
shows what happens. rumbo starts it with the terminal on stdin and stdout, and
with the protocol on two extra file descriptors: 3 to read from the host and
4 to write to it. Messages are JSON-RPC, one per line.

This one asks for objectives with input() and prints events as they arrive.

Register and use it:

    rumbo frontend add simple python3 /path/to/examples/frontend.py
    rumbo -f simple
"""

import json
import os

from_host = os.fdopen(3, "r")
to_host = os.fdopen(4, "w")


def send(frame):
    to_host.write(json.dumps({"jsonrpc": "2.0", **frame}) + "\n")
    to_host.flush()


def show(event):
    """Events say what the agent is doing. Ignore the types you do not need."""
    kind = event["type"]

    if kind == "text":
        print(event["text"], end="", flush=True)
    elif kind == "tool_started":
        print(f"\n[tool] {event['name']} {json.dumps(event.get('args'))}")
    elif kind == "tool_finished" and event.get("failed"):
        print(f"[tool failed] {event['result']}")
    elif kind == "log":
        print(f"[log] {event['text']}")


def run(objective, request_id):
    """Send one objective and show events until the host answers the request."""
    send({"id": request_id, "method": "run", "params": {"objective": objective}})

    for line in from_host:
        frame = json.loads(line)

        if frame.get("method") == "event":
            show(frame["params"])
        elif frame.get("id") == request_id:
            # The answer to `run` arrives when the run is over: its status is
            # done, failed, or cancelled.
            result = frame["result"]
            print(f"\n[{result['status']}] {result.get('error', '')}")
            return


# The host speaks first: it says which directory, session, and model this
# front end is attached to, and passes an objective given on the command line.
hello = json.loads(from_host.readline())
send({"id": hello["id"], "result": {"protocol": 1, "name": "simple"}})

info = hello["params"]
print(f"rumbo in {info['dir']} with {info['provider']}/{info['model']}")

objective = info.get("objective", "")
request_id = 0

while True:
    if not objective:
        try:
            objective = input("\nobjective> ").strip()
        except EOFError:
            break

    if objective:
        request_id += 1
        run(objective, request_id)

    objective = ""

# Closing the connection is how a front end says it is done; the host then
# stops the provider and exits.
to_host.close()
