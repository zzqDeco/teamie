#!/usr/bin/env python3
"""Read-only loopback pprof capture from a running Teamie service container."""
import argparse
import datetime as dt
import os
from pathlib import Path
import subprocess
import sys
from urllib.parse import urlencode

SERVICES = (
    "identity",
    "chat",
    "realtime",
    "marketplace",
    "agent-control",
    "agent-runtime",
    "llm-gateway",
    "sandbox",
)
PROFILES = (
    "heap",
    "allocs",
    "goroutine",
    "profile",
    "block",
    "mutex",
    "threadcreate",
    "trace",
)


def resolve_seconds(profile, raw_seconds):
    if raw_seconds is not None:
        return raw_seconds
    if profile == "profile":
        return 30
    if profile == "trace":
        return 5
    if profile in ("block", "mutex"):
        return 10
    return 0


def build_pprof_url(port, profile, seconds, debug, gc):
    params = {}
    if profile in ("profile", "trace") or seconds > 0:
        params["seconds"] = str(seconds)
    if debug:
        params["debug"] = str(debug)
    if gc:
        params["gc"] = "1"
    url = f"http://127.0.0.1:{port}/debug/pprof/{profile}"
    if params:
        url += "?" + urlencode(params)
    return url


def default_output_name(service, profile, debug, now=None):
    stamp = (now or dt.datetime.now(dt.timezone.utc)).strftime("%Y%m%dT%H%M%SZ")
    if profile == "trace":
        ext = "trace"
    elif debug == 1:
        ext = "txt"
    else:
        ext = "pb.gz"
    return f"teamie-{service}-{profile}-{stamp}.{ext}"


def write_private_file(path, payload):
    target = Path(path)
    fd = os.open(str(target), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    try:
        os.chmod(fd, 0o600)
        with os.fdopen(fd, "wb") as handle:
            handle.write(payload)
    except Exception:
        try:
            os.close(fd)
        except OSError:
            pass
        raise


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--service", required=True, choices=SERVICES, help="target Go service container")
    parser.add_argument("--profile", default="heap", choices=PROFILES, help="runtime profile endpoint")
    parser.add_argument("--seconds", type=int, default=None, help="sampling window in seconds")
    parser.add_argument("--debug", type=int, choices=(0, 1), default=0, help="0 for binary pb.gz, 1 for aggregated text")
    parser.add_argument("--gc", action="store_true", help="run GC before heap snapshot (heap only)")
    parser.add_argument("--port", type=int, default=6060, help="container loopback pprof port (default: 6060)")
    parser.add_argument("--output", default=None, help="destination file path, or '-' to stream to stdout")
    args = parser.parse_args(argv)

    if not 1 <= args.port <= 65535:
        parser.error("port must be from 1 to 65535")

    seconds = resolve_seconds(args.profile, args.seconds)
    if args.profile == "profile":
        if not 1 <= seconds <= 60:
            parser.error("CPU profile seconds must be from 1 to 60")
        if args.debug != 0:
            parser.error("debug is not supported for CPU profile")
    elif args.profile == "trace":
        if not 1 <= seconds <= 10:
            parser.error("execution trace seconds must be from 1 to 10")
        if args.debug != 0:
            parser.error("debug is not supported for execution trace")
    else:
        if not 0 <= seconds <= 30:
            parser.error("delta profile seconds must be from 0 to 30")
        if seconds > 0 and args.debug != 0:
            parser.error("seconds and debug cannot be combined")

    if args.gc and args.profile != "heap":
        parser.error("--gc is only valid with --profile heap")

    ps_cmd = [
        "docker",
        "ps",
        "--filter",
        "label=com.docker.compose.project=teamie",
        "--filter",
        "label=com.docker.compose.service=" + args.service,
        "--format",
        '{{.ID}} {{.Label "com.docker.compose.service"}}',
    ]
    try:
        lines = [line for line in subprocess.check_output(ps_cmd, text=True).splitlines() if line.strip()]
    except (OSError, subprocess.SubprocessError):
        print("Docker container lookup failed", file=sys.stderr)
        return 1

    matching = []
    for line in lines:
        parts = line.split(" ", 1)
        if len(parts) == 2 and parts[1] == args.service:
            matching.append(parts[0])
    if not matching:
        print(f"No running Teamie container for service: {args.service}", file=sys.stderr)
        return 1
    if len(matching) > 1:
        print(f"Multiple running containers matched service: {args.service}", file=sys.stderr)
        return 1

    url = build_pprof_url(args.port, args.profile, seconds, args.debug, args.gc)
    wget_timeout = max(15, seconds + 15)
    exec_cmd = [
        "docker",
        "exec",
        matching[0],
        "wget",
        "-q",
        "-T",
        str(wget_timeout),
        "-O",
        "-",
        url,
    ]
    try:
        proc = subprocess.run(
            exec_cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=max(20, seconds + 20),
            check=False,
        )
    except subprocess.TimeoutExpired:
        print("Profile capture timed out", file=sys.stderr)
        return 1
    except (OSError, subprocess.SubprocessError):
        print("Docker pprof capture failed", file=sys.stderr)
        return 1

    if proc.returncode != 0 or not proc.stdout:
        print(
            f"Profile capture failed for {args.service} ({args.profile}); verify PPROF_ADDR is enabled on 127.0.0.1:{args.port}",
            file=sys.stderr,
        )
        return 1

    if args.output == "-":
        sys.stdout.buffer.write(proc.stdout)
        sys.stdout.buffer.flush()
        return 0

    out_path = args.output or default_output_name(args.service, args.profile, args.debug)
    try:
        write_private_file(out_path, proc.stdout)
    except OSError:
        print(f"Failed to write profile output: {out_path}", file=sys.stderr)
        return 1

    if args.profile == "trace":
        hint = f"go tool trace {out_path}"
    elif args.debug == 1:
        hint = f"less {out_path}"
    else:
        hint = f"go tool pprof -http=:9090 -source_path=. {out_path}"
    print(f"Saved {len(proc.stdout)} bytes to {out_path} ({hint})", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
