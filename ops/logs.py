#!/usr/bin/env python3
"""Read-only exact-ID lookup in the existing production container log ring."""
import argparse
import json
import re
import subprocess
import sys

SERVICES = ("identity", "chat", "realtime", "agent-control", "agent-runtime", "llm-gateway", "sandbox")
FIELDS = {"trace": "trace.id", "run": "custom.runtime.run_id", "agent": "custom.agent.id", "task": "custom.task.id", "attempt": "custom.runtime.attempt_id", "event": "custom.input.event_id", "step": "custom.model.step"}


def field(event, key):
    if key in event:
        return event[key]
    value = event
    for part in key.split("."):
        if not isinstance(value, dict) or part not in value:
            return None
        value = value[part]
    return value


def matches(event, filters):
    for key, value in filters.items():
        if value is None or field(event, FIELDS[key]) == value:
            continue
        if key == "event":
            inputs = field(event, "custom.input.event_ids")
            if isinstance(inputs, list) and value in inputs:
                continue
        return False
    return True


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--service", choices=SERVICES)
    parser.add_argument("--since", default="15m", help="Docker duration or RFC3339 timestamp")
    parser.add_argument("--tail", type=int, default=5000, help="maximum lines read per container")
    for name in FIELDS:
        parser.add_argument("--" + name, type=int if name == "step" else str)
    args = parser.parse_args(argv)
    if not 1 <= args.tail <= 100000:
        parser.error("tail must be from 1 to 100000")
    if not re.fullmatch(r"[0-9TZtz:+.hmsd-]{1,80}", args.since):
        parser.error("use a duration or RFC3339 timestamp")
    if args.trace and not re.fullmatch(r"[0-9a-f]{32}", args.trace):
        parser.error("trace must be 32 lowercase hexadecimal characters")
    filters = {key: getattr(args, key) for key in FIELDS}
    command = ["docker", "ps", "--filter", "label=com.docker.compose.project=teamie"]
    if args.service:
        command += ["--filter", "label=com.docker.compose.service=" + args.service]
    command += ["--format", "{{.ID}} {{.Label \"com.docker.compose.service\"}}"]
    try:
        containers = subprocess.check_output(command, text=True).splitlines()
        if not containers:
            print("No matching Teamie containers", file=sys.stderr)
            return 1
        for container in containers:
            identity, service = container.split(" ", 1)
            if service not in SERVICES:
                continue
            # Merge the container stdout/stderr pipes so either native sink is
            # parsed. Docker diagnostics that are not JSON are not re-emitted.
            process = subprocess.Popen(["docker", "logs", "--since", args.since, "--tail", str(args.tail), identity], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            assert process.stdout is not None
            for line in process.stdout:
                try:
                    event = json.loads(line)
                except (ValueError, TypeError):
                    continue
                if isinstance(event, dict) and matches(event, filters):
                    print(json.dumps(event, ensure_ascii=False), flush=True)
            if process.wait() != 0:
                print("Container logs could not be read: " + service, file=sys.stderr)
                return 1
    except (OSError, subprocess.SubprocessError):
        print("Docker log access failed", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
