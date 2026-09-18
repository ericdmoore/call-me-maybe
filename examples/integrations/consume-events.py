#!/usr/bin/env python3
"""A local, single-consumer example. No SQL access or Python dependencies.

Invoke --once from a doorbell handler, or leave running for periodic catch-up.
Each consumer needs its own checkpoint. stdout is the example processing action;
replace process() with an idempotent operation keyed by event_id in your system.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time


def process(event):
    # A crash after this succeeds but before checkpointing replays the event.
    print(json.dumps(event), flush=True)


def save(path, state):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=path.name + ".")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(state, f)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--doorman", default="doorman")
    parser.add_argument("--path", required=True, help="journal path passed to Doorman")
    parser.add_argument("--checkpoint", type=Path, required=True)
    parser.add_argument("--limit", type=int, default=100)
    parser.add_argument("--eventType")
    parser.add_argument("--poll", type=float, default=30)
    parser.add_argument("--once", action="store_true", help="catch up through a captured watermark, then exit")
    args = parser.parse_args()
    if not 1 <= args.limit <= 10000 or args.poll <= 0:
        parser.error("limit must be 1–10000 and poll must be positive")
    # Do not run overlapping invocations with this checkpoint. Use a service or
    # serialize doorbells in the receiver; separate consumers use separate files.
    state = {"after": "0", "eventType": args.eventType}
    if args.checkpoint.exists():
        state = json.loads(args.checkpoint.read_text())
        if state.get("eventType") != args.eventType:
            parser.error("use a new checkpoint when changing eventType")
    target = None
    while True:
        cmd = [args.doorman, "events", "--json", "--path", args.path,
               "--after", state["after"], "--limit", str(args.limit)]
        if target is not None:
            cmd += ["--through", str(target)]
        if args.eventType:
            cmd += ["--eventType", args.eventType]
        if "journal_id" in state:
            cmd += ["--journal", state["journal_id"], "--generation", state["generation"]]
        # A read failure (including expired cursor) leaves the checkpoint intact.
        page = json.loads(subprocess.check_output(cmd, text=True))
        if target is None:
            target = int(page["through"])
        for event in page["events"]:
            process(event)
        state.update(after=page["next_cursor"], journal_id=page["journal_id"],
                     generation=page["generation"])
        save(args.checkpoint, state)
        if int(state["after"]) >= target:
            if args.once:
                return
            time.sleep(args.poll)
            target = None


if __name__ == "__main__":
    main()
