import base64
import copy
import datetime as dt
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request


REPO_ROOT = Path(__file__).resolve().parents[1]
MANIFEST_SPEC = importlib.util.spec_from_file_location("manifest", REPO_ROOT / "ops" / "manifest.py")
manifest_mod = importlib.util.module_from_spec(MANIFEST_SPEC)
MANIFEST_SPEC.loader.exec_module(manifest_mod)

SERVICE_MEM_MIB = {
    "caddy": 64,
    "web": 32,
    "identity": 96,
    "chat": 96,
    "marketplace": 64,
    "realtime": 96,
    "postgres": 320,
    "redis": 64,
    "agent-control": 64,
    "agent-runtime": 256,
    "llm-gateway": 128,
    "sandbox": 64,
}


def make_v3_manifest(
    ver: str,
    sha_char: str,
    unchanged_infra_from: dict | None = None,
    online_rollout: bool = True,
) -> dict:
    sha = sha_char * 40
    images = {
        svc: f"ghcr.io/zzqdeco/teamie-{svc}@sha256:" + sha_char * 64
        for svc in manifest_mod.SERVICES_V3
    }
    if unchanged_infra_from is not None:
        for keep in ("postgres", "redis", "caddy", "web"):
            images[keep] = unchanged_infra_from["images"][keep]
    data = {
        "manifestVersion": 3,
        "version": ver,
        "source": {
            "repository": "zzqDeco/teamie",
            "sha": sha,
            "mainSha": sha,
            "ref": "refs/heads/release/" + ver,
        },
        "createdAt": dt.datetime.now(dt.timezone.utc).isoformat(),
        "schema": dict(manifest_mod.SCHEMA_V3),
        "storage": {"postgresMajor": 17},
        "featureFlags": {"revision": "context-engineering-v1", "sha256": "f" * 64},
        "images": images,
    }
    if online_rollout:
        data["onlineRollout"] = True
    return data


def make_compose_json(
    release_manifest: dict,
    caddyfile_path: Path,
    flags_path: Path,
    tls_config_path: Path | None = None,
    tls_cert_dir: Path | None = None,
) -> dict:
    services = {}
    for svc in manifest_mod.SERVICES_V3:
        if svc == "sandbox-runner":
            continue
        mem_bytes = SERVICE_MEM_MIB[svc] * 1024 * 1024
        nets = {"private": None}
        if svc in ("marketplace", "llm-gateway", "agent-runtime"):
            nets["egress"] = None
        if svc == "caddy":
            nets["edge"] = None
        env = {"LOG_LEVEL": "info"}
        vols = []
        if svc == "caddy":
            env.update({"SITE_ADDRESS": "https://192.0.2.1", "TLS_SERVER_NAME": "192.0.2.1"})
            vols.append(
                {
                    "type": "bind",
                    "source": str(caddyfile_path),
                    "target": "/etc/caddy/Caddyfile",
                    "read_only": True,
                }
            )
            if tls_config_path is not None:
                vols.append(
                    {
                        "type": "bind",
                        "source": str(tls_config_path),
                        "target": "/etc/caddy/issuer.caddy",
                        "read_only": True,
                    }
                )
            if tls_cert_dir is not None:
                vols.append(
                    {
                        "type": "bind",
                        "source": str(tls_cert_dir),
                        "target": "/run/tls",
                        "read_only": True,
                    }
                )
        elif svc == "sandbox":
            env.update({"SANDBOX_RUNNER_IMAGE": release_manifest["images"]["sandbox-runner"]})
        elif svc not in ("postgres", "redis", "web"):
            env.update(
                {
                    "APP_ORIGIN": "https://192.0.2.1",
                    "COOKIE_SECURE": "true",
                    "DATABASE_URL": f"postgres://teamie_{svc}:secret_{svc}_0123456789abcdef@postgres:5432/teamie_{svc}",
                }
            )
            if svc != "llm-gateway":
                vols.append(
                    {
                        "type": "bind",
                        "source": str(flags_path),
                        "target": "/etc/teamie/feature-flags.json",
                        "read_only": True,
                    }
                )
        services[svc] = {
            "image": release_manifest["images"][svc],
            "restart": "unless-stopped",
            "init": svc not in ("caddy", "web", "postgres", "redis"),
            "read_only": svc != "postgres",
            "mem_limit": mem_bytes,
            "memswap_limit": mem_bytes,
            "pids_limit": 32 if svc == "redis" else (64 if svc in ("caddy", "web") else 128),
            "security_opt": ["no-new-privileges:true"],
            "cap_drop": ["ALL"] if svc != "postgres" else [],
            "cap_add": ["NET_BIND_SERVICE"] if svc in ("caddy", "web") else [],
            "tmpfs": (
                ["/data:size=1m,mode=1777"]
                if svc == "redis"
                else (["/tmp:size=16m,mode=1777"] if svc != "postgres" else [])
            ),
            "networks": nets,
            "environment": env,
            "volumes": vols,
            "logging": {"driver": "json-file", "options": {"max-size": "10m", "max-file": "3"}},
            "healthcheck": {
                "test": ["CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1/readyz"],
                "interval": "10s",
                "timeout": "3s",
                "retries": 6,
            },
        }
        if svc == "postgres":
            services[svc]["shm_size"] = 64 * 1024 * 1024
            services[svc]["command"] = [
                "postgres",
                "-c",
                "shared_buffers=64MB",
                "-c",
                "work_mem=2MB",
                "-c",
                "maintenance_work_mem=32MB",
                "-c",
                "max_connections=40",
            ]
        elif svc == "redis":
            services[svc]["user"] = "redis"
            services[svc]["command"] = [
                "redis-server",
                "--save",
                "",
                "--appendonly",
                "no",
                "--maxmemory",
                "40mb",
                "--maxmemory-policy",
                "allkeys-lru",
            ]
    return {
        "name": "teamie",
        "networks": {"private": {"internal": True}, "egress": {}, "edge": {}},
        "services": services,
    }


def _container_from_service_spec(cid: str, name: str, labels: dict, spec: dict, started_at: str) -> dict:
    mem_bytes = int(spec.get("mem_limit") or 0)
    memswap_bytes = int(spec["memswap_limit"]) if spec.get("memswap_limit") is not None else mem_bytes
    tmpfs_map = {}
    for entry in spec.get("tmpfs") or []:
        path, opts = entry.split(":", 1) if ":" in entry else (entry, "")
        tmpfs_map[path] = opts
    raw_nets = spec.get("networks") or {"private": None}
    net_keys = list(raw_nets.keys()) if isinstance(raw_nets, dict) else [str(n) for n in raw_nets]
    hc = spec.get("healthcheck") or {}
    hc_inspect = None
    if hc:
        hc_inspect = {"Test": list(hc.get("test") or [])}
        if hc.get("interval") == "10s":
            hc_inspect["Interval"] = 10_000_000_000
        if hc.get("timeout") == "3s":
            hc_inspect["Timeout"] = 3_000_000_000
        if hc.get("retries") is not None:
            hc_inspect["Retries"] = int(hc["retries"])
    log_spec = spec.get("logging") or {}
    return {
        "id": cid,
        "name": name,
        "labels": labels,
        "image": spec["image"],
        "running": True,
        "started_at": started_at,
        "health": "healthy",
        "mem_limit": mem_bytes,
        "memswap_limit": memswap_bytes,
        "shm_size": int(spec.get("shm_size") or 0),
        "pids_limit": int(spec.get("pids_limit") or 0),
        "nano_cpus": int(round(float(spec["cpus"]) * 1_000_000_000)) if spec.get("cpus") is not None else int(spec.get("nano_cpus") or 0),
        "cpuset": str(spec.get("cpuset") or ""),
        "read_only": bool(spec.get("read_only") or False),
        "init": bool(spec.get("init") or False),
        "security_opt": list(spec.get("security_opt") or []),
        "cap_drop": list(spec.get("cap_drop") or []),
        "cap_add": list(spec.get("cap_add") or []),
        "tmpfs": tmpfs_map,
        "restart": str(spec.get("restart") or ""),
        "log_config": {
            "Type": str(log_spec.get("driver") or "json-file"),
            "Config": {str(k): str(v) for k, v in (log_spec.get("options") or {}).items()},
        },
        "cmd": list(spec["command"]) if spec.get("command") is not None else None,
        "entrypoint": list(spec["entrypoint"]) if spec.get("entrypoint") is not None else None,
        "user": str(spec.get("user") or ""),
        "working_dir": str(spec.get("working_dir") or ""),
        "healthcheck": hc_inspect,
        "env": [f"{k}={v}" for k, v in (spec.get("environment") or {}).items() if v is not None],
        "mounts": [
            {
                "Destination": v["target"],
                "Source": v["source"],
                "Type": v.get("type", "bind"),
                "RW": not bool(v.get("read_only", False)),
            }
            for v in (spec.get("volumes") or [])
            if isinstance(v, dict)
        ],
        "networks": [f"teamie_{n}" for n in net_keys],
    }


MOCK_DOCKER_SCRIPT = r"""#!/usr/bin/env python3
import fcntl
import json
import os
from pathlib import Path
import sys

STATE_PATH = Path(os.environ["MOCK_DOCKER_STATE"])
LOCK_PATH = STATE_PATH.with_suffix(".lock")


def with_state(mutator):
    LOCK_PATH.touch(exist_ok=True)
    with LOCK_PATH.open("r+") as lf:
        fcntl.flock(lf.fileno(), fcntl.LOCK_EX)
        state = json.loads(STATE_PATH.read_text())
        res = mutator(state)
        # Recompute peak metrics on every state transition.
        running = [c for c in state["containers"].values() if c["running"]]
        canaries = [c for c in running if c["labels"].get("io.teamie.canary") == "true"]
        total_mem = sum(int(c.get("mem_limit", 0)) for c in running)
        if len(canaries) > state["max_concurrent_canaries"]:
            state["max_concurrent_canaries"] = len(canaries)
        if total_mem > state["max_total_mem_bytes"]:
            state["max_total_mem_bytes"] = total_mem
        STATE_PATH.write_text(json.dumps(state, indent=2))
        return res


def parse_compose_invocation(argv):
    # argv starts after 'compose'
    i = 0
    env_files = []
    compose_file = ""
    while i < len(argv):
        if argv[i] in ("--project-name", "-p"):
            i += 2
        elif argv[i] in ("--file", "-f"):
            compose_file = argv[i + 1]
            i += 2
        elif argv[i] == "--env-file":
            env_files.append(argv[i + 1])
            i += 2
        else:
            break
    return compose_file, env_files, argv[i:]


def load_images_from_env(env_files):
    images = {}
    for ef in env_files:
        p = Path(ef)
        if not p.is_file():
            continue
        for line in p.read_text().splitlines():
            if "=" in line and not line.lstrip().startswith("#"):
                k, v = line.split("=", 1)
                if k.endswith("_IMAGE"):
                    svc = k[:-6].lower().replace("_", "-")
                    images[svc] = v.strip()
    return images


def main():
    args = sys.argv[1:]
    if not args:
        return 1

    if args[0] == "compose":
        compose_file, env_files, rest = parse_compose_invocation(args[1:])
        images = load_images_from_env(env_files)
        sub = rest[0]
        if sub == "config" and "--services" in rest:
            for s in ("caddy", "web", "identity", "chat", "marketplace", "realtime",
                      "agent-control", "agent-runtime", "llm-gateway", "sandbox", "postgres", "redis"):
                print(s)
            return 0
        if sub == "exec":
            # exec -T postgres ...
            joined = " ".join(rest)
            _ = sys.stdin.read() if not sys.stdin.isatty() else ""
            if "to_regclass" in joined:
                def rec(st):
                    st["events"].append("check_legacy_marketplace")
                with_state(rec)
                print(os.environ.get("MOCK_LEGACY_MARKETPLACE", "f"))
                return 0
            def rec_exec(st):
                st["events"].append("compose_exec:" + " ".join(rest[1:4]))
            with_state(rec_exec)
            return 0
        if sub == "stop":
            # stop [--timeout N] svc...
            svcs = []
            i = 1
            while i < len(rest):
                if rest[i] in ("--timeout", "-t"):
                    i += 2
                elif rest[i].startswith("-"):
                    i += 1
                else:
                    svcs.append(rest[i])
                    i += 1
            def do_stop(st):
                for svc in svcs:
                    st["events"].append(f"canonical_stop:{svc}")
                    cid = f"canonical-{svc}"
                    if cid in st["containers"]:
                        st["containers"][cid]["running"] = False
            with_state(do_stop)
            return 0
        if sub == "up":
            force_recreate = "--force-recreate" in rest
            svcs = [tok for tok in rest[1:] if not tok.startswith("-") and not tok.isdigit()]
            caddyfile = str(Path(compose_file).parent / "Caddyfile") if compose_file else ""
            compose_json_env = os.environ.get("TEAMIE_COMPOSE_JSON", "")
            compose_services = {}
            if compose_json_env and Path(compose_json_env).is_file():
                compose_services = json.loads(Path(compose_json_env).read_text()).get("services") or {}
            def do_up(st):
                flags_source = st.get("flags_file", "")
                tls_cfg_source = os.environ.get("MOCK_TLS_CONFIG_FILE") or st.get("tls_config_file", "")
                tls_dir_source = os.environ.get("MOCK_TLS_CERT_DIR") or st.get("tls_cert_dir", "")
                for svc in svcs:
                    st["events"].append(f"canonical_up:{svc}")
                    if force_recreate:
                        st["events"].append(f"canonical_force_recreate:{svc}")
                    seq = int(st.get("start_seq", 0)) + 1
                    st["start_seq"] = seq
                    cid = f"canonical-{svc}"
                    spec = json.loads(json.dumps(compose_services.get(svc) or st["service_defaults"][svc]))
                    img = images.get(svc, spec["image"])
                    spec["image"] = img
                    if svc == "sandbox" and "sandbox-runner" in images:
                        spec.setdefault("environment", {})["SANDBOX_RUNNER_IMAGE"] = images["sandbox-runner"]
                    if svc == "caddy":
                        vols = []
                        if caddyfile:
                            vols.append({"type": "bind", "source": caddyfile, "target": "/etc/caddy/Caddyfile", "read_only": True})
                        if tls_cfg_source:
                            vols.append({"type": "bind", "source": tls_cfg_source, "target": "/etc/caddy/issuer.caddy", "read_only": True})
                        if tls_dir_source:
                            vols.append({"type": "bind", "source": tls_dir_source, "target": "/run/tls", "read_only": True})
                        if vols:
                            spec["volumes"] = vols
                    elif svc not in ("postgres", "redis", "web", "sandbox", "llm-gateway") and flags_source and not spec.get("volumes"):
                        spec["volumes"] = [{"type": "bind", "source": flags_source, "target": "/etc/teamie/feature-flags.json", "read_only": True}]
                    mem_bytes = int(spec.get("mem_limit") or 0)
                    memswap_bytes = int(spec["memswap_limit"]) if spec.get("memswap_limit") is not None else mem_bytes
                    tmpfs_map = {}
                    for entry in spec.get("tmpfs") or []:
                        path, opts = entry.split(":", 1) if ":" in entry else (entry, "")
                        tmpfs_map[path] = opts
                    raw_nets = spec.get("networks") or {"private": None}
                    net_keys = list(raw_nets.keys()) if isinstance(raw_nets, dict) else [str(n) for n in raw_nets]
                    hc = spec.get("healthcheck") or {}
                    hc_inspect = None
                    if hc:
                        hc_inspect = {"Test": list(hc.get("test") or [])}
                        if hc.get("interval") == "10s":
                            hc_inspect["Interval"] = 10_000_000_000
                        if hc.get("timeout") == "3s":
                            hc_inspect["Timeout"] = 3_000_000_000
                        if hc.get("retries") is not None:
                            hc_inspect["Retries"] = int(hc["retries"])
                    log_spec = spec.get("logging") or {}
                    mounts = [
                        {
                            "Destination": v["target"],
                            "Source": v["source"],
                            "Type": v.get("type", "bind"),
                            "RW": not bool(v.get("read_only", False)),
                        }
                        for v in (spec.get("volumes") or [])
                        if isinstance(v, dict)
                    ]
                    env_list = [f"{k}={v}" for k, v in (spec.get("environment") or {}).items() if v is not None]
                    new_rec = {
                        "id": cid,
                        "name": f"teamie-{svc}-1",
                        "labels": {
                            "com.docker.compose.project": "teamie",
                            "com.docker.compose.service": svc,
                        },
                        "image": img,
                        "running": True,
                        "started_at": "2026-11-11T00:00:00Z",
                        "health": "healthy",
                        "mem_limit": mem_bytes,
                        "memswap_limit": memswap_bytes,
                        "shm_size": int(spec.get("shm_size") or 0),
                        "pids_limit": int(spec.get("pids_limit") or 0),
                        "nano_cpus": int(round(float(spec["cpus"]) * 1_000_000_000)) if spec.get("cpus") is not None else int(spec.get("nano_cpus") or 0),
                        "cpuset": str(spec.get("cpuset") or ""),
                        "read_only": bool(spec.get("read_only") or False),
                        "init": bool(spec.get("init") or False),
                        "security_opt": list(spec.get("security_opt") or []),
                        "cap_drop": list(spec.get("cap_drop") or []),
                        "cap_add": list(spec.get("cap_add") or []),
                        "tmpfs": tmpfs_map,
                        "restart": str(spec.get("restart") or ""),
                        "log_config": {
                            "Type": str(log_spec.get("driver") or "json-file"),
                            "Config": {str(k): str(v) for k, v in (log_spec.get("options") or {}).items()},
                        },
                        "cmd": list(spec["command"]) if spec.get("command") is not None else None,
                        "entrypoint": list(spec["entrypoint"]) if spec.get("entrypoint") is not None else None,
                        "user": str(spec.get("user") or ""),
                        "working_dir": str(spec.get("working_dir") or ""),
                        "healthcheck": hc_inspect,
                        "env": env_list,
                        "mounts": mounts,
                        "networks": [f"teamie_{n}" for n in net_keys],
                    }
                    prev = st["containers"].get(cid)
                    spec_keys = (
                        "image", "mem_limit", "memswap_limit", "shm_size", "pids_limit",
                        "nano_cpus", "cpuset", "read_only", "init", "security_opt",
                        "cap_drop", "cap_add", "tmpfs", "restart", "log_config",
                        "cmd", "entrypoint", "user", "working_dir", "healthcheck",
                        "env", "mounts", "networks",
                    )
                    spec_changed = prev is None or any(prev.get(k) != new_rec.get(k) for k in spec_keys)
                    recreated = force_recreate or spec_changed or not prev["running"]
                    new_rec["started_at"] = (
                        f"2026-11-11T00:00:{seq:02d}Z"
                        if recreated
                        else prev.get("started_at", "2026-11-11T00:00:00Z")
                    )
                    if recreated:
                        st["events"].append(f"canonical_recreated:{svc}")
                    st["containers"][cid] = new_rec
            with_state(do_up)
            return 0
        return 1

    if args[0] == "ps":
        all_containers = "-a" in args or "-aq" in args or "-qa" in args
        quiet = "-q" in args or "-aq" in args or "-qa" in args
        fmt = ""
        filters = []
        i = 1
        while i < len(args):
            if args[i] == "--filter":
                filters.append(args[i + 1])
                i += 2
            elif args[i] == "--format":
                fmt = args[i + 1]
                i += 2
            else:
                i += 1
        def do_ps(st):
            matched = []
            for c in st["containers"].values():
                if not all_containers and not c["running"]:
                    continue
                ok = True
                for flt in filters:
                    if flt.startswith("label="):
                        lspec = flt[len("label="):]
                        if "=" in lspec:
                            lk, lv = lspec.split("=", 1)
                            if c["labels"].get(lk) != lv:
                                ok = False
                        elif lspec not in c["labels"]:
                            ok = False
                if ok:
                    matched.append(c["id"])
            return matched
        for cid in with_state(do_ps):
            print(cid)
        return 0

    if args[0] == "inspect":
        fmt = ""
        targets = []
        i = 1
        while i < len(args):
            if args[i] == "--format":
                fmt = args[i + 1]
                i += 2
            else:
                targets.append(args[i])
                i += 1
        def do_inspect(st):
            found = []
            for t in targets:
                c = st["containers"].get(t)
                if c is None:
                    for cand in st["containers"].values():
                        if cand["name"] == t:
                            c = cand
                            break
                if c is None:
                    return None
                found.append(c)
            return found
        found = with_state(do_inspect)
        if not found:
            return 1
        if fmt:
            c = found[0]
            if ".State.Health" in fmt:
                if not c["running"]:
                    print("stopped")
                else:
                    print(c.get("health", "healthy"))
            elif fmt == "{{.Config.Image}}":
                print(c["image"])
            else:
                print(c["id"])
            return 0
        payload = []
        for c in found:
            payload.append({
                "Id": c["id"],
                "Name": "/" + c["name"],
                "State": {
                    "Running": c["running"],
                    "StartedAt": c.get("started_at", "2026-11-11T00:00:00Z"),
                    "Health": {"Status": c.get("health", "healthy")},
                },
                "Config": {
                    "Image": c["image"],
                    "Labels": c["labels"],
                    "Env": c.get("env", []),
                    "Cmd": c.get("cmd"),
                    "Entrypoint": c.get("entrypoint"),
                    "User": c.get("user", ""),
                    "WorkingDir": c.get("working_dir", ""),
                    "Healthcheck": c.get("healthcheck"),
                },
                "HostConfig": {
                    "Memory": int(c.get("mem_limit", 0)),
                    "MemorySwap": int(c.get("memswap_limit", c.get("mem_limit", 0))),
                    "ShmSize": int(c.get("shm_size", 0)),
                    "PidsLimit": int(c.get("pids_limit", 0)),
                    "NanoCpus": int(c.get("nano_cpus", 0)),
                    "CpuShares": int(c.get("cpu_shares", 0)),
                    "CpuQuota": int(c.get("cpu_quota", 0)),
                    "CpuPeriod": int(c.get("cpu_period", 0)),
                    "CpusetCpus": c.get("cpuset", ""),
                    "ReadonlyRootfs": bool(c.get("read_only", False)),
                    "Init": bool(c.get("init", False)),
                    "Privileged": bool(c.get("privileged", False)),
                    "SecurityOpt": c.get("security_opt", []),
                    "CapDrop": c.get("cap_drop", []),
                    "CapAdd": c.get("cap_add", []),
                    "Tmpfs": c.get("tmpfs", {}),
                    "PortBindings": c.get("port_bindings", {}),
                    "RestartPolicy": {"Name": c.get("restart", ""), "MaximumRetryCount": 0},
                    "LogConfig": c.get("log_config", {"Type": "json-file", "Config": {}}),
                },
                "Mounts": c.get("mounts", []),
                "NetworkSettings": {
                    "Networks": {net: {} for net in c.get("networks", [])},
                },
            })
        print(json.dumps(payload))
        return 0

    if args[0] == "exec":
        import hashlib as _hl
        cid = args[1]
        cmd_rest = args[2:]
        if cmd_rest and cmd_rest[0] == "sha256sum":
            def get_tls_mount(st):
                c = st["containers"].get(cid)
                if not c or not c.get("running"):
                    return None
                for m in c.get("mounts", []):
                    if m.get("Destination") == "/run/tls":
                        return m.get("Source")
                return None
            src_dir = os.environ.get("MOCK_ROOT_TLS_SHADOW_DIR") or with_state(get_tls_mount)
            if not src_dir:
                return 1
            for target_file in cmd_rest[1:]:
                rel = Path(target_file).name
                digest = _hl.sha256((Path(src_dir) / rel).read_bytes()).hexdigest()
                print(f"{digest}  {target_file}")
            return 0
        return 1

    if args[0] == "create":
        raw_joined = " ".join(args)
        name = ""
        labels = {}
        mem_limit = 0
        nets = []
        env_list = []
        i = 1
        image = ""
        while i < len(args):
            a = args[i]
            if a == "--name":
                name = args[i + 1]; i += 2
            elif a == "--label":
                lk, lv = args[i + 1].split("=", 1)
                labels[lk] = lv; i += 2
            elif a == "--memory":
                mem_limit = int(args[i + 1]); i += 2
            elif a == "--network":
                nets.append(args[i + 1]); i += 2
            elif a == "--env-file":
                ef = Path(args[i + 1])
                mode = ef.stat().st_mode & 0o777
                env_list = ef.read_text().splitlines()
                if mode != 0o600:
                    sys.stderr.write(f"insecure canary env file mode: {oct(mode)}\n")
                    return 1
                i += 2
            elif a in ("--restart", "--network-alias", "--user", "--memory-swap", "--pids-limit",
                       "--security-opt", "--cap-drop", "--cap-add", "--tmpfs", "--volume",
                       "--log-driver", "--log-opt", "--health-cmd", "--health-interval",
                       "--health-timeout", "--health-retries", "--health-start-period", "--entrypoint"):
                i += 2
            elif a in ("--init", "--read-only"):
                i += 1
            else:
                image = a
                break
        def do_create(st):
            if "DATABASE_URL" in raw_joined or "secret_" in raw_joined:
                st["secret_leak_detected"] = True
            svc = labels.get("io.teamie.canary.service", name)
            st["events"].append(f"canary_create:{svc}")
            st["containers"][name] = {
                "id": name,
                "name": name,
                "labels": labels,
                "image": image,
                "running": False,
                "health": "starting",
                "mem_limit": mem_limit,
                "env": env_list,
                "mounts": [],
                "networks": nets,
            }
        with_state(do_create)
        print(name)
        return 0

    if args[:2] == ["network", "connect"]:
        # network connect [--alias X] net container
        net = args[-2]
        cname = args[-1]
        def do_net(st):
            if cname in st["containers"]:
                st["containers"][cname]["networks"].append(net)
                st["events"].append(f"canary_net:{cname}:{net}")
        with_state(do_net)
        return 0

    if args[0] == "start":
        cname = args[-1]
        fail_canary = os.environ.get("MOCK_FAIL_CANARY_SERVICE", "")
        def do_start(st):
            c = st["containers"][cname]
            svc = c["labels"].get("io.teamie.canary.service", cname)
            c["running"] = True
            c["health"] = "unhealthy" if svc == fail_canary else "healthy"
            st["events"].append(f"canary_start:{svc}")
        with_state(do_start)
        return 0

    if args[0] == "stop":
        cname = args[-1]
        def do_cstop(st):
            if cname in st["containers"]:
                svc = st["containers"][cname]["labels"].get("io.teamie.canary.service", cname)
                st["containers"][cname]["running"] = False
                st["events"].append(f"canary_stop:{svc}")
        with_state(do_cstop)
        return 0

    if args[0] == "rm":
        targets = [a for a in args[1:] if not a.startswith("-")]
        def do_rm(st):
            for t in targets:
                c = st["containers"].pop(t, None)
                if c is not None:
                    svc = c["labels"].get("io.teamie.canary.service", t)
                    st["events"].append(f"canary_rm:{svc}")
        with_state(do_rm)
        return 0

    return 1


if __name__ == "__main__":
    sys.exit(main())
"""


class ABRolloutOrchestrationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="teamie-ab-test-")
        self.root = Path(self.tmp.name)
        (self.root / "bin").mkdir()
        (self.root / "config").mkdir()
        self.host_env = self.root / "config" / "production.env"
        self.host_env.write_text("LOG_LEVEL=info\n")
        self.host_env.chmod(0o600)
        self.flags_file = self.root / "config" / "feature-flags.json"
        self.flags_file.write_text('{"revision":"context-engineering-v1","flags":{}}\n')
        (self.root / "tls" / "certs-v1").mkdir(parents=True)
        self.tls_config_file = self.root / "tls" / "issuer.caddy"
        self.tls_config_file.write_text("tls internal\n")
        self.tls_cert_dir = self.root / "tls" / "certs-v1"

        # Create recipe v0.5.0 and v0.5.1 directories with deploy/Caddyfile and postgres scripts.
        self.recipe_v1 = self._create_recipe("v0.5.0")
        self.recipe_v2 = self._create_recipe("v0.5.1")

        self.manifest_v1 = make_v3_manifest("v0.5.0", "a")
        self.manifest_v2 = make_v3_manifest("v0.5.1", "b", unchanged_infra_from=self.manifest_v1)

        self.docker_bin = self.root / "bin" / "docker"
        self.docker_bin.write_text(MOCK_DOCKER_SCRIPT)
        self.docker_bin.chmod(0o755)
        self.state_path = self.root / "docker-state.json"
        self._seed_running_stack(self.manifest_v1, self.recipe_v1)

    def tearDown(self):
        self.tmp.cleanup()

    def _create_recipe(self, name: str) -> Path:
        recipe = self.root / "releases" / name
        (recipe / "deploy" / "postgres").mkdir(parents=True)
        shutil.copy2(REPO_ROOT / "deploy" / "Caddyfile", recipe / "deploy" / "Caddyfile")
        shutil.copy2(REPO_ROOT / "deploy" / "compose.production.yaml", recipe / "deploy" / "compose.production.yaml")
        (recipe / "deploy" / "postgres" / "01-databases.sh").write_text("#!/usr/bin/env bash\nexit 0\n")
        (recipe / "deploy" / "postgres" / "02-migrate-chat-marketplace.sh").write_text("#!/usr/bin/env bash\nexit 0\n")
        return recipe

    def _seed_running_stack(self, manifest_data: dict, recipe: Path):
        compose_spec = make_compose_json(
            manifest_data,
            recipe / "deploy" / "Caddyfile",
            self.flags_file,
            tls_config_path=self.tls_config_file,
            tls_cert_dir=self.tls_cert_dir,
        )
        containers = {}
        defaults = {}
        for svc, spec in compose_spec["services"].items():
            defaults[svc] = copy.deepcopy(spec)
            containers[f"canonical-{svc}"] = _container_from_service_spec(
                cid=f"canonical-{svc}",
                name=f"teamie-{svc}-1",
                labels={
                    "com.docker.compose.project": "teamie",
                    "com.docker.compose.service": svc,
                },
                spec=spec,
                started_at="2026-11-11T00:00:00Z",
            )
        state = {
            "containers": containers,
            "service_defaults": defaults,
            "flags_file": str(self.flags_file),
            "tls_config_file": str(self.tls_config_file),
            "tls_cert_dir": str(self.tls_cert_dir),
            "start_seq": 0,
            "max_concurrent_canaries": 0,
            "max_total_mem_bytes": sum(c["mem_limit"] for c in containers.values()),
            "secret_leak_detected": False,
            "events": [],
        }
        self.state_path.write_text(json.dumps(state, indent=2))
        state_dir = self.root / "state"
        state_dir.mkdir(parents=True, exist_ok=True)
        issuer_sha = hashlib.sha256(self.tls_config_file.read_bytes()).hexdigest()
        (state_dir / "caddy-tls-loaded.json").write_text(
            json.dumps(
                {
                    "containerId": "canonical-caddy",
                    "startedAt": "2026-11-11T00:00:00Z",
                    "issuerSha256": issuer_sha,
                    "tlsFilesSha256": "acme",
                },
                sort_keys=True,
            )
            + "\n"
        )

    def _run_rollout(
        self,
        target_manifest: dict,
        current_manifest: dict | None,
        recipe: Path,
        extra_env: dict | None = None,
        tls_config_path: Path | None = None,
        tls_cert_dir: Path | None = None,
        compose_mutator=None,
    ):
        target_path = self.root / "target-manifest.json"
        target_path.write_text(json.dumps(target_manifest))
        images_env = self.root / "images.env"
        images_env.write_text(manifest_mod.image_env(target_manifest))
        eff_tls_cfg = tls_config_path if tls_config_path is not None else self.tls_config_file
        eff_tls_dir = tls_cert_dir if tls_cert_dir is not None else self.tls_cert_dir
        compose_data = make_compose_json(
            target_manifest,
            recipe / "deploy" / "Caddyfile",
            self.flags_file,
            tls_config_path=eff_tls_cfg,
            tls_cert_dir=eff_tls_dir,
        )
        if compose_mutator is not None:
            compose_mutator(compose_data)
        compose_json = self.root / "compose.json"
        compose_json.write_text(json.dumps(compose_data))

        env = {
            **os.environ,
            "PATH": f"{self.root / 'bin'}:{os.environ.get('PATH', '')}",
            "MOCK_DOCKER_STATE": str(self.state_path),
            "TEAMIE_ROOT": str(self.root),
            "TEAMIE_TARGET_MANIFEST": str(target_path),
            "TEAMIE_COMPOSE_JSON": str(compose_json),
            "TEAMIE_CANARY_POLL_SECONDS": "0.01",
            "TEAMIE_CANARY_SETTLE_SECONDS": "0.01",
            "COMMON": str(REPO_ROOT / "ops" / "common.sh"),
            "RECIPE": str(recipe),
            "IMAGES_ENV": str(images_env),
        }
        if current_manifest is not None:
            cur_path = self.root / "current-manifest.json"
            cur_path.write_text(json.dumps(current_manifest))
            env["TEAMIE_CURRENT_MANIFEST"] = str(cur_path)
        else:
            env.pop("TEAMIE_CURRENT_MANIFEST", None)
        if extra_env:
            env.update(extra_env)

        return subprocess.run(
            ["bash", "-ec", 'source "$COMMON"; compose_for "$RECIPE" "$IMAGES_ENV"; rollout'],
            env=env,
            capture_output=True,
            text=True,
        )

    def test_online_ab_rollout_cuts_over_serially_within_1600mib_and_preserves_single_writers(self):
        res = self._run_rollout(self.manifest_v2, self.manifest_v1, self.recipe_v2)
        self.assertEqual(res.returncode, 0, res.stderr)
        state = json.loads(self.state_path.read_text())

        # Strictly 1 canary at a time and peak container memory <= 1,600 MiB!
        self.assertEqual(state["max_concurrent_canaries"], 1)
        self.assertLessEqual(state["max_total_mem_bytes"], 1600 * 1024 * 1024)
        self.assertFalse(state["secret_leak_detected"])

        events = state["events"]
        # Unchanged web and caddy digests must be skipped completely.
        self.assertNotIn("canary_create:web", events)
        self.assertNotIn("canonical_up:web", events)
        self.assertNotIn("canonical_up:caddy", events)

        # Single-instance writers never spawn canaries and stop before starting.
        for single in ("sandbox", "agent-runtime", "postgres", "redis"):
            self.assertNotIn(f"canary_create:{single}", events)
        self.assertLess(events.index("canonical_stop:sandbox"), events.index("canonical_up:sandbox"))
        self.assertLess(events.index("canonical_stop:agent-runtime"), events.index("canonical_up:agent-runtime"))

        # Changed stateless services cut over in exact serial A/B lifecycle order.
        expected_ab = ["identity", "chat", "marketplace", "realtime", "agent-control", "llm-gateway"]
        prev_rm_idx = -1
        for svc in expected_ab:
            c_create = events.index(f"canary_create:{svc}")
            c_start = events.index(f"canary_start:{svc}")
            old_stop = events.index(f"canonical_stop:{svc}")
            new_up = events.index(f"canonical_up:{svc}")
            c_stop = events.index(f"canary_stop:{svc}")
            c_rm = events.index(f"canary_rm:{svc}")
            self.assertGreater(c_create, prev_rm_idx, f"{svc} canary started before previous canary was removed")
            self.assertTrue(c_create < c_start < old_stop < new_up < c_stop < c_rm)
            prev_rm_idx = c_rm

        # Marketplace and Gateway canaries attach teamie_egress before starting.
        self.assertLess(
            events.index("canary_net:teamie-marketplace-canary:teamie_egress"),
            events.index("canary_start:marketplace"),
        )
        self.assertLess(
            events.index("canary_net:teamie-llm-gateway-canary:teamie_egress"),
            events.index("canary_start:llm-gateway"),
        )

        # Zero canary containers remain; all 12 canonical containers run target digests.
        canaries_left = [
            c for c in state["containers"].values() if c["labels"].get("io.teamie.canary") == "true"
        ]
        self.assertEqual(canaries_left, [])
        for svc in SERVICE_MEM_MIB:
            c = state["containers"][f"canonical-{svc}"]
            self.assertTrue(c["running"])
            self.assertEqual(c["image"], self.manifest_v2["images"][svc])

    def test_schema_epoch_change_automatically_falls_back_to_fenced_rollout(self):
        fenced_target = copy.deepcopy(self.manifest_v2)
        fenced_target["version"] = "v0.6.0"
        fenced_target["source"]["ref"] = "refs/heads/release/v0.6.0"
        fenced_target["schema"]["chat"] += 1

        res = self._run_rollout(fenced_target, self.manifest_v1, self.recipe_v2)
        self.assertEqual(res.returncode, 0, res.stderr)
        state = json.loads(self.state_path.read_text())
        self.assertEqual(state["max_concurrent_canaries"], 0)
        events = state["events"]
        self.assertLess(events.index("canonical_stop:agent-runtime"), events.index("canonical_up:identity"))

    def test_failed_canary_healthcheck_preserves_live_canonical_slot_and_restores_cleanly(self):
        res = self._run_rollout(
            self.manifest_v2,
            self.manifest_v1,
            self.recipe_v2,
            extra_env={"MOCK_FAIL_CANARY_SERVICE": "chat"},
        )
        self.assertNotEqual(res.returncode, 0)
        mid_state = json.loads(self.state_path.read_text())
        # Old canonical chat was never stopped when chat-canary failed readiness!
        self.assertNotIn("canonical_stop:chat", mid_state["events"])
        self.assertTrue(mid_state["containers"]["canonical-chat"]["running"])
        self.assertEqual(mid_state["containers"]["canonical-chat"]["image"], self.manifest_v1["images"]["chat"])
        # Failed canary container was cleaned up immediately.
        self.assertEqual(
            [c for c in mid_state["containers"].values() if c["labels"].get("io.teamie.canary") == "true"],
            [],
        )

        # Running restore_previous back to manifest_v1 cuts identity back and keeps chat untouched.
        restore_res = self._run_rollout(self.manifest_v1, self.manifest_v2, self.recipe_v1)
        self.assertEqual(restore_res.returncode, 0, restore_res.stderr)
        final_state = json.loads(self.state_path.read_text())
        self.assertNotIn("canonical_stop:chat", final_state["events"])
        for svc in SERVICE_MEM_MIB:
            c = final_state["containers"][f"canonical-{svc}"]
            self.assertTrue(c["running"])
            self.assertEqual(c["image"], self.manifest_v1["images"][svc])

    def test_caddy_tls_mount_source_changes_trigger_online_caddy_recreate(self):
        # 1. Changing TLS_CONFIG_FILE (/etc/caddy/issuer.caddy source) with identical Caddy image/env/Caddyfile
        # must mark caddy out-of-date and recreate canonical caddy during online rollout.
        alt_issuer = self.root / "tls" / "issuer-acme.caddy"
        alt_issuer.write_text("tls internal\n")
        res_issuer = self._run_rollout(
            self.manifest_v1,
            self.manifest_v1,
            self.recipe_v1,
            extra_env={"MOCK_TLS_CONFIG_FILE": str(alt_issuer)},
            tls_config_path=alt_issuer,
        )
        self.assertEqual(res_issuer.returncode, 0, res_issuer.stderr)
        state_issuer = json.loads(self.state_path.read_text())
        self.assertIn("canonical_up:caddy", state_issuer["events"])
        caddy_mounts = {
            m["Destination"]: m["Source"] for m in state_issuer["containers"]["canonical-caddy"]["mounts"]
        }
        self.assertEqual(caddy_mounts["/etc/caddy/issuer.caddy"], str(alt_issuer))

        # 2. Re-running with the same alt_issuer and tls_cert_dir skips caddy cleanly.
        state_issuer["events"] = []
        self.state_path.write_text(json.dumps(state_issuer, indent=2))
        res_noop = self._run_rollout(
            self.manifest_v1,
            self.manifest_v1,
            self.recipe_v1,
            extra_env={"MOCK_TLS_CONFIG_FILE": str(alt_issuer)},
            tls_config_path=alt_issuer,
        )
        self.assertEqual(res_noop.returncode, 0, res_noop.stderr)
        state_noop = json.loads(self.state_path.read_text())
        self.assertNotIn("canonical_up:caddy", state_noop["events"])

        # 3. Changing TLS_CERT_DIR (/run/tls source) with identical Caddy image/env/Caddyfile
        # must also mark caddy out-of-date and recreate canonical caddy.
        alt_certs = self.root / "tls" / "certs-v2"
        alt_certs.mkdir(parents=True)
        res_certs = self._run_rollout(
            self.manifest_v1,
            self.manifest_v1,
            self.recipe_v1,
            extra_env={
                "MOCK_TLS_CONFIG_FILE": str(alt_issuer),
                "MOCK_TLS_CERT_DIR": str(alt_certs),
            },
            tls_config_path=alt_issuer,
            tls_cert_dir=alt_certs,
        )
        self.assertEqual(res_certs.returncode, 0, res_certs.stderr)
        state_certs = json.loads(self.state_path.read_text())
        self.assertIn("canonical_up:caddy", state_certs["events"])
        caddy_mounts_v2 = {
            m["Destination"]: m["Source"] for m in state_certs["containers"]["canonical-caddy"]["mounts"]
        }
        self.assertEqual(caddy_mounts_v2["/run/tls"], str(alt_certs))

    def test_parent_release_without_online_rollout_capability_uses_fenced_upgrade(self):
        parent_v048 = make_v3_manifest("v0.4.8", "0", unchanged_infra_from=self.manifest_v1, online_rollout=False)
        self.assertNotIn("onlineRollout", parent_v048)
        self.assertFalse(manifest_mod.online_compatible(self.manifest_v1, parent_v048))
        self.assertFalse(manifest_mod.online_compatible(parent_v048, self.manifest_v1))
        # Rollback compatibility itself remains intact across identical schema/storage/flags.
        manifest_mod.compatible(self.manifest_v1, parent_v048)

        self._seed_running_stack(parent_v048, self.recipe_v1)
        res = self._run_rollout(self.manifest_v1, parent_v048, self.recipe_v1)
        self.assertEqual(res.returncode, 0, res.stderr)
        state = json.loads(self.state_path.read_text())
        self.assertEqual(state["max_concurrent_canaries"], 0)
        events = state["events"]
        # Fenced rollout stops agent-runtime BEFORE replacing llm-gateway so pre-dual-slot clients never hit a dead canonical gateway!
        self.assertLess(events.index("canonical_stop:agent-runtime"), events.index("canonical_up:llm-gateway"))

    def test_caddy_in_place_tls_config_and_cert_edits_force_recreate_caddy(self):
        # 1. Edit issuer.caddy IN PLACE at the exact same path to switch from ACME to file-based TLS.
        (self.tls_cert_dir / "fullchain.pem").write_text("CERT-V1\n")
        (self.tls_cert_dir / "privkey.pem").write_text("KEY-V1\n")
        self.tls_config_file.write_text("tls /run/tls/fullchain.pem /run/tls/privkey.pem\n")

        res_edit = self._run_rollout(self.manifest_v1, self.manifest_v1, self.recipe_v1)
        self.assertEqual(res_edit.returncode, 0, res_edit.stderr)
        state_edit = json.loads(self.state_path.read_text())
        self.assertIn("canonical_force_recreate:caddy", state_edit["events"])
        first_started = state_edit["containers"]["canonical-caddy"]["started_at"]
        self.assertNotEqual(first_started, "2026-11-11T00:00:00Z")

        # 2. Re-running with unchanged issuer.caddy and cert files skips caddy completely.
        state_edit["events"] = []
        self.state_path.write_text(json.dumps(state_edit, indent=2))
        res_noop = self._run_rollout(self.manifest_v1, self.manifest_v1, self.recipe_v1)
        self.assertEqual(res_noop.returncode, 0, res_noop.stderr)
        state_noop = json.loads(self.state_path.read_text())
        self.assertNotIn("canonical_up:caddy", state_noop["events"])

        # 3. Rotate fullchain.pem IN PLACE at the unchanged /run/tls directory -> must force-recreate caddy!
        (self.tls_cert_dir / "fullchain.pem").write_text("CERT-V2-ROTATED\n")
        res_rot = self._run_rollout(self.manifest_v1, self.manifest_v1, self.recipe_v1)
        self.assertEqual(res_rot.returncode, 0, res_rot.stderr)
        state_rot = json.loads(self.state_path.read_text())
        self.assertIn("canonical_force_recreate:caddy", state_rot["events"])
        self.assertNotEqual(state_rot["containers"]["canonical-caddy"]["started_at"], first_started)

        # 4. Missing TLS state stamp conservatively force-recreates caddy and re-records the stamp.
        stamp_file = self.root / "state" / "caddy-tls-loaded.json"
        stamp_file.unlink()
        state_rot["events"] = []
        self.state_path.write_text(json.dumps(state_rot, indent=2))
        res_missing = self._run_rollout(self.manifest_v1, self.manifest_v1, self.recipe_v1)
        self.assertEqual(res_missing.returncode, 0, res_missing.stderr)
        state_missing = json.loads(self.state_path.read_text())
        self.assertIn("canonical_force_recreate:caddy", state_missing["events"])
        self.assertTrue(stamp_file.is_file())

    def test_online_rollout_with_running_sandbox_child_stays_within_1600mib_and_fences_overbudget(self):
        # 1. Seed one active 128 MiB Sandbox execution child (io.teamie.sandbox=true) running throughout rollout_online.
        state = json.loads(self.state_path.read_text())
        state["containers"]["sandbox-exec-1"] = {
            "id": "sandbox-exec-1",
            "name": "teamie-sbx-1",
            "labels": {"io.teamie.sandbox": "true", "io.teamie.agent": "a1", "io.teamie.task": "t1"},
            "image": self.manifest_v1["images"]["sandbox-runner"],
            "running": True,
            "started_at": "2026-11-11T00:00:00Z",
            "health": "healthy",
            "mem_limit": 128 * 1024 * 1024,
            "env": [],
            "mounts": [],
            "networks": [],
        }
        state["max_total_mem_bytes"] = sum(c["mem_limit"] for c in state["containers"].values())
        self.state_path.write_text(json.dumps(state, indent=2))

        res = self._run_rollout(self.manifest_v2, self.manifest_v1, self.recipe_v2)
        self.assertEqual(res.returncode, 0, res.stderr)
        after = json.loads(self.state_path.read_text())
        # Online rollout ran with 1 canary AND peak memory across 12 services + 1 Sandbox child + 128 MiB canary == 1,600 MiB!
        self.assertEqual(after["max_concurrent_canaries"], 1)
        self.assertEqual(after["max_total_mem_bytes"], 1600 * 1024 * 1024)
        self.assertTrue(after["containers"]["sandbox-exec-1"]["running"])

        # 2. If anomalous extra Sandbox/host containers push projected memory above 1,600 MiB,
        # online-memory-ready rejects online cutover and rollout safely falls back to fenced rollout!
        self._seed_running_stack(self.manifest_v1, self.recipe_v1)
        over_state = json.loads(self.state_path.read_text())
        for idx in (1, 2):
            over_state["containers"][f"sandbox-exec-{idx}"] = {
                "id": f"sandbox-exec-{idx}",
                "name": f"teamie-sbx-{idx}",
                "labels": {"io.teamie.sandbox": "true"},
                "image": self.manifest_v1["images"]["sandbox-runner"],
                "running": True,
                "started_at": "2026-11-11T00:00:00Z",
                "health": "healthy",
                "mem_limit": 128 * 1024 * 1024,
                "env": [],
                "mounts": [],
                "networks": [],
            }
        self.state_path.write_text(json.dumps(over_state, indent=2))

        res_over = self._run_rollout(self.manifest_v2, self.manifest_v1, self.recipe_v2)
        self.assertEqual(res_over.returncode, 0, res_over.stderr)
        after_over = json.loads(self.state_path.read_text())
        self.assertEqual(after_over["max_concurrent_canaries"], 0)
        self.assertLess(
            after_over["events"].index("canonical_stop:agent-runtime"),
            after_over["events"].index("canonical_up:llm-gateway"),
        )

    def test_runtime_settings_changes_without_image_bump_trigger_rollout(self):
        # 1. Redis command + mem_limit change with identical image digests must gate out online rollout
        # and run rollout_fenced so canonical-redis is recreated with the new command and 32 MiB cap!
        def mutate_redis(cfg: dict):
            redis_spec = cfg["services"]["redis"]
            redis_spec["mem_limit"] = 32 * 1024 * 1024
            redis_spec["memswap_limit"] = 32 * 1024 * 1024
            redis_spec["command"] = [
                "redis-server",
                "--save",
                "",
                "--appendonly",
                "no",
                "--maxmemory",
                "20mb",
                "--maxmemory-policy",
                "allkeys-lru",
            ]

        res_redis = self._run_rollout(
            self.manifest_v1,
            self.manifest_v1,
            self.recipe_v1,
            compose_mutator=mutate_redis,
        )
        self.assertEqual(res_redis.returncode, 0, res_redis.stderr)
        state_redis = json.loads(self.state_path.read_text())
        self.assertEqual(state_redis["max_concurrent_canaries"], 0)
        self.assertIn("canonical_recreated:redis", state_redis["events"])
        self.assertNotIn("canonical_recreated:postgres", state_redis["events"])
        self.assertEqual(state_redis["containers"]["canonical-redis"]["mem_limit"], 32 * 1024 * 1024)
        self.assertIn("20mb", state_redis["containers"]["canonical-redis"]["cmd"])

        # 2. Stateless web resource change + single-writer sandbox pids_limit change with identical image digests
        # must run online canary cutover for web and stop-then-recreate for sandbox (never skipping them)!
        self._seed_running_stack(self.manifest_v1, self.recipe_v1)

        def mutate_web_and_sandbox(cfg: dict):
            cfg["services"]["web"]["pids_limit"] = 96
            cfg["services"]["sandbox"]["pids_limit"] = 192

        res_online = self._run_rollout(
            self.manifest_v1,
            self.manifest_v1,
            self.recipe_v1,
            compose_mutator=mutate_web_and_sandbox,
        )
        self.assertEqual(res_online.returncode, 0, res_online.stderr)
        state_online = json.loads(self.state_path.read_text())
        self.assertEqual(state_online["max_concurrent_canaries"], 1)
        events = state_online["events"]
        self.assertIn("canary_create:web", events)
        self.assertIn("canonical_force_recreate:web", events)
        self.assertIn("canonical_stop:sandbox", events)
        self.assertIn("canonical_force_recreate:sandbox", events)
        self.assertNotIn("canary_create:chat", events)
        self.assertEqual(state_online["containers"]["canonical-web"]["pids_limit"], 96)
        self.assertEqual(state_online["containers"]["canonical-sandbox"]["pids_limit"], 192)


# --- Live Caddy v2.11.7 HTTP + WebSocket Dual-Slot Cutover Harness ---

WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"


class _BufferedSocket:
    def __init__(self, sock: socket.socket, initial: bytes = b""):
        self._sock = sock
        self._buf = bytearray(initial)

    def recv(self, n: int) -> bytes:
        if self._buf:
            out = bytes(self._buf[:n])
            del self._buf[:n]
            return out
        return self._sock.recv(n)


def _ws_write_server_frame(sock: socket.socket, opcode: int, payload: bytes) -> None:
    length = len(payload)
    if length < 126:
        header = struct.pack("!BB", 0x80 | opcode, length)
    elif length < 65536:
        header = struct.pack("!BBH", 0x80 | opcode, 126, length)
    else:
        header = struct.pack("!BBQ", 0x80 | opcode, 127, length)
    sock.sendall(header + payload)


def _ws_read_frame(sock: socket.socket) -> tuple[int, bytes]:
    def recv_exact(n: int) -> bytes:
        buf = bytearray()
        while len(buf) < n:
            chunk = sock.recv(n - len(buf))
            if not chunk:
                raise EOFError("unexpected EOF reading websocket frame")
            buf.extend(chunk)
        return bytes(buf)

    b0, b1 = recv_exact(2)
    opcode = b0 & 0x0F
    masked = bool(b1 & 0x80)
    length = b1 & 0x7F
    if length == 126:
        (length,) = struct.unpack("!H", recv_exact(2))
    elif length == 127:
        (length,) = struct.unpack("!Q", recv_exact(8))
    mask_key = recv_exact(4) if masked else b""
    payload = bytearray(recv_exact(length))
    if masked:
        for idx in range(length):
            payload[idx] ^= mask_key[idx % 4]
    return opcode, bytes(payload)


def _ws_write_client_close(sock: socket.socket, code: int = 1000) -> None:
    mask = os.urandom(4)
    raw = struct.pack("!H", code)
    masked = bytes(raw[i] ^ mask[i % 4] for i in range(len(raw)))
    sock.sendall(struct.pack("!BB", 0x88, 0x80 | len(raw)) + mask + masked)


class _SlotServer:
    """Real HTTP + RFC 6455 WebSocket slot implementing /readyz 503 + drain grace + WS 1001."""

    def __init__(self, slot_name: str, event_broker: "EventBroker", post_recorder: "PostRecorder"):
        self.slot_name = slot_name
        self.broker = event_broker
        self.post_recorder = post_recorder
        self.draining = threading.Event()
        self._ws_lock = threading.Lock()
        self._ws_Stops: list[threading.Event] = []

        parent = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, format, *args):
                return

            def _send_json(self, status: int, body: dict):
                raw = (json.dumps(body) + "\n").encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                if parent.draining.is_set():
                    self.send_header("Connection", "close")
                    self.close_connection = True
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self):
                if self.path == "/readyz":
                    if parent.draining.is_set():
                        self._send_json(503, {"status": "draining"})
                    else:
                        self._send_json(200, {"status": "ready"})
                    return
                if self.path == "/healthz":
                    self._send_json(200, {"status": "ok"})
                    return
                if self.path.startswith("/api/v1/ws"):
                    self._handle_ws()
                    return
                self._send_json(200, {"slot": parent.slot_name, "path": self.path})

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                raw = self.rfile.read(length) if length > 0 else b"{}"
                data = json.loads(raw.decode("utf-8"))
                parent.post_recorder.record(data["reqId"], parent.slot_name)
                self._send_json(200, {"slot": parent.slot_name, "reqId": data["reqId"]})

            def _handle_ws(self):
                if parent.draining.is_set():
                    self._send_json(503, {"status": "draining"})
                    return
                cursor = 0
                if "cursor=" in self.path:
                    cursor = int(self.path.split("cursor=", 1)[1].split("&", 1)[0])
                key = self.headers.get("Sec-WebSocket-Key", "")
                accept = base64.b64encode(hashlib.sha1((key + WS_GUID).encode("ascii")).digest()).decode("ascii")
                self.send_response(101, "Switching Protocols")
                self.send_header("Upgrade", "websocket")
                self.send_header("Connection", "Upgrade")
                self.send_header("Sec-WebSocket-Accept", accept)
                self.end_headers()
                self.close_connection = True

                ws_stop = threading.Event()
                with parent._ws_lock:
                    parent._ws_Stops.append(ws_stop)
                try:
                    last_sent = cursor
                    while not ws_stop.is_set():
                        events = parent.broker.since(last_sent)
                        for seq in events:
                            payload = json.dumps({"seq": seq, "slot": parent.slot_name}).encode("utf-8")
                            _ws_write_server_frame(self.connection, 0x1, payload)
                            last_sent = seq
                        time.sleep(0.02)
                    # Emit RFC 6455 1001 Going Away close frame on graceful drain!
                    close_payload = struct.pack("!H", 1001) + b"server draining"
                    _ws_write_server_frame(self.connection, 0x8, close_payload)
                except OSError:
                    pass

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.httpd.daemon_threads = True
        self.port = self.httpd.server_address[1]
        self._thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self._thread.start()

    def drain_and_stop(self, grace_secs: float = 1.1):
        self.draining.set()
        time.sleep(grace_secs)
        with self._ws_lock:
            stops = list(self._ws_Stops)
        for ev in stops:
            ev.set()
        time.sleep(0.08)
        self.httpd.shutdown()
        self.httpd.server_close()
        self._thread.join(timeout=3)


class EventBroker:
    def __init__(self):
        self._lock = threading.Lock()
        self._seq = 0

    def publish_next(self) -> int:
        with self._lock:
            self._seq += 1
            return self._seq

    def current(self) -> int:
        with self._lock:
            return self._seq

    def since(self, cursor: int) -> list[int]:
        with self._lock:
            if cursor >= self._seq:
                return []
            return list(range(cursor + 1, self._seq + 1))


class PostRecorder:
    def __init__(self):
        self.lock = threading.Lock()
        self.calls: list[tuple[str, str]] = []

    def record(self, req_id: str, slot: str):
        with self.lock:
            self.calls.append((req_id, slot))


def _reserve_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _find_caddy_binary() -> str | None:
    env_bin = os.environ.get("TEAMIE_TEST_CADDY_BIN", "").strip()
    if env_bin and Path(env_bin).is_file():
        return str(Path(env_bin).resolve())
    return shutil.which("caddy")


class LiveCaddyDualSlotCutoverTests(unittest.TestCase):
    def test_live_caddy_zero_502_http_post_and_websocket_cursor_cutover(self):
        caddy_bin = _find_caddy_binary()
        if not caddy_bin:
            self.skipTest("caddy binary not found in PATH or TEAMIE_TEST_CADDY_BIN")

        broker = EventBroker()
        recorder = PostRecorder()
        slot_a1 = _SlotServer("slot-a-v1", broker, recorder)
        canary_port = _reserve_free_port()
        caddy_port = _reserve_free_port()

        with tempfile.TemporaryDirectory(prefix="teamie-live-caddy-") as tmp:
            root = Path(tmp)
            caddyfile = root / "Caddyfile"
            caddyfile.write_text(
                f"""{{
\tadmin off
\tauto_https off
\tlog {{
\t\texclude http.handlers.reverse_proxy.health_checker.active
\t}}
\tlog healthcheck {{
\t\tinclude http.handlers.reverse_proxy.health_checker.active
\t\tlevel ERROR
\t}}
}}

http://127.0.0.1:{caddy_port} {{
\t@steady_nx path /nxdomain-canary-check
\thandle @steady_nx {{
\t\treverse_proxy 127.0.0.1:{slot_a1.port} nonexistent-canary.invalid:8082 {{
\t\t\tlb_policy first
\t\t\tlb_try_duration 5s
\t\t\tlb_try_interval 80ms
\t\t\thealth_uri /readyz
\t\t\thealth_interval 250ms
\t\t\thealth_timeout 500ms
\t\t\tfail_duration 3s
\t\t\ttransport http {{
\t\t\t\tdial_timeout 500ms
\t\t\t\tkeepalive 30s
\t\t\t}}
\t\t}}
\t}}
\thandle {{
\t\treverse_proxy 127.0.0.1:{slot_a1.port} 127.0.0.1:{canary_port} {{
\t\t\tlb_policy first
\t\t\tlb_try_duration 5s
\t\t\tlb_try_interval 80ms
\t\t\thealth_uri /readyz
\t\t\thealth_interval 250ms
\t\t\thealth_timeout 500ms
\t\t\tfail_duration 3s
\t\t\ttransport http {{
\t\t\t\tdial_timeout 500ms
\t\t\t\tkeepalive 30s
\t\t\t}}
\t\t}}
\t}}
}}
"""
            )
            env = {
                **os.environ,
                "XDG_DATA_HOME": str(root / "data"),
                "XDG_CONFIG_HOME": str(root / "config"),
            }
            log_file = root / "caddy.log"
            with log_file.open("w") as log_handle:
                proc = subprocess.Popen(
                    [caddy_bin, "run", "--config", str(caddyfile), "--adapter", "caddyfile"],
                    env=env,
                    stdout=log_handle,
                    stderr=log_handle,
                )
                slot_canary = None
                slot_a2 = None
                try:
                    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                    # Wait for Caddy listener and initial active health pass on slot_a1.
                    ready = False
                    for _ in range(80):
                        try:
                            with opener.open(f"http://127.0.0.1:{caddy_port}/nxdomain-canary-check", timeout=1) as resp:
                                if resp.status == 200:
                                    ready = True
                                    break
                        except Exception:
                            time.sleep(0.05)
                    self.assertTrue(ready, f"Caddy failed to start: {log_file.read_text()}")

                    # 1. Verify steady-state NXDOMAIN on nonexistent-canary.invalid causes 0 errors and 0 active health log spam.
                    for _ in range(15):
                        with opener.open(f"http://127.0.0.1:{caddy_port}/nxdomain-canary-check", timeout=2) as resp:
                            self.assertEqual(resp.status, 200)
                    self.assertNotIn("nonexistent-canary.invalid", log_file.read_text())

                    # 2. Launch continuous concurrent HTTP GET + non-idempotent POST load + WS cursor subscriber.
                    stop_load = threading.Event()
                    http_errors: list[str] = []
                    get_slots_seen: set[str] = set()
                    get_count = 0
                    post_sent_ids: list[str] = []
                    load_lock = threading.Lock()

                    def get_worker():
                        nonlocal get_count
                        local_opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                        while not stop_load.is_set():
                            try:
                                with local_opener.open(f"http://127.0.0.1:{caddy_port}/api/v1/workspaces", timeout=4) as r:
                                    body = json.loads(r.read().decode("utf-8"))
                                    if r.status != 200:
                                        with load_lock:
                                            http_errors.append(f"GET status {r.status}")
                                    else:
                                        with load_lock:
                                            get_count += 1
                                            get_slots_seen.add(body["slot"])
                            except Exception as exc:
                                with load_lock:
                                    http_errors.append(f"GET error: {exc}")
                            time.sleep(0.015)

                    def post_worker():
                        idx = 0
                        local_opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                        while not stop_load.is_set():
                            idx += 1
                            req_id = f"post-{threading.get_ident()}-{idx}"
                            with load_lock:
                                post_sent_ids.append(req_id)
                            payload = json.dumps({"reqId": req_id}).encode("utf-8")
                            req = urllib.request.Request(
                                f"http://127.0.0.1:{caddy_port}/api/v1/messages",
                                data=payload,
                                headers={"Content-Type": "application/json"},
                                method="POST",
                            )
                            try:
                                with local_opener.open(req, timeout=4) as r:
                                    if r.status != 200:
                                        with load_lock:
                                            http_errors.append(f"POST status {r.status}")
                            except Exception as exc:
                                with load_lock:
                                    http_errors.append(f"POST error: {exc}")
                            time.sleep(0.02)

                    ws_received_seqs: list[int] = []
                    ws_close_codes: list[int] = []
                    ws_errors: list[str] = []

                    def ws_subscriber():
                        cursor = 0
                        while not stop_load.is_set():
                            sock = None
                            try:
                                sock = socket.create_connection(("127.0.0.1", caddy_port), timeout=3)
                                sock.settimeout(0.5)
                                ws_key = base64.b64encode(os.urandom(16)).decode("ascii")
                                handshake = (
                                    f"GET /api/v1/ws?cursor={cursor} HTTP/1.1\r\n"
                                    f"Host: 127.0.0.1:{caddy_port}\r\n"
                                    "Upgrade: websocket\r\n"
                                    "Connection: Upgrade\r\n"
                                    f"Sec-WebSocket-Key: {ws_key}\r\n"
                                    "Sec-WebSocket-Version: 13\r\n\r\n"
                                )
                                sock.sendall(handshake.encode("ascii"))
                                resp_buf = b""
                                while b"\r\n\r\n" not in resp_buf:
                                    chunk = sock.recv(1024)
                                    if not chunk:
                                        raise EOFError("EOF during WS handshake")
                                    resp_buf += chunk
                                status_line, remainder = resp_buf.split(b"\r\n\r\n", 1)
                                if b"101" not in status_line.splitlines()[0]:
                                    raise RuntimeError(f"WS upgrade failed: {status_line!r}")
                                reader = _BufferedSocket(sock, remainder)
                                while not stop_load.is_set():
                                    try:
                                        opcode, payload = _ws_read_frame(reader)
                                    except TimeoutError:
                                        continue
                                    if opcode == 0x1:
                                        msg = json.loads(payload.decode("utf-8"))
                                        seq = int(msg["seq"])
                                        ws_received_seqs.append(seq)
                                        cursor = seq
                                    elif opcode == 0x8:
                                        code = struct.unpack("!H", payload[:2])[0] if len(payload) >= 2 else 1000
                                        ws_close_codes.append(code)
                                        try:
                                            _ws_write_client_close(sock, 1000)
                                        except OSError:
                                            pass
                                        break
                            except Exception as exc:
                                if not stop_load.is_set():
                                    ws_errors.append(str(exc))
                                time.sleep(0.05)
                            finally:
                                if sock is not None:
                                    try:
                                        sock.close()
                                    except OSError:
                                        pass

                    def event_publisher():
                        while not stop_load.is_set():
                            broker.publish_next()
                            time.sleep(0.04)

                    threads = [
                        threading.Thread(target=get_worker, daemon=True),
                        threading.Thread(target=get_worker, daemon=True),
                        threading.Thread(target=post_worker, daemon=True),
                        threading.Thread(target=ws_subscriber, daemon=True),
                        threading.Thread(target=event_publisher, daemon=True),
                    ]
                    for t in threads:
                        t.start()

                    time.sleep(0.25)

                    # Phase 1: Start canary slot on canary_port and wait for Caddy active health check to mark it up.
                    slot_canary = _SlotServerOnPort("slot-b-canary", canary_port, broker, recorder)
                    time.sleep(0.65)

                    # Phase 2: Drain and stop primary slot_a1 -> 100% shifts to slot_canary with 0 errors!
                    primary_port = slot_a1.port
                    slot_a1.drain_and_stop(grace_secs=0.75)
                    slot_a1 = None
                    time.sleep(0.25)

                    # Phase 3: Start new canonical slot_a2 on primary_port -> lb_policy first shifts back to primary!
                    slot_a2 = _SlotServerOnPort("slot-a-v2", primary_port, broker, recorder)
                    time.sleep(0.65)

                    # Phase 4: Drain and stop slot_canary -> 100% stays on slot_a2 with 0 errors!
                    slot_canary.drain_and_stop(grace_secs=0.75)
                    slot_canary = None
                    time.sleep(0.35)

                    stop_load.set()
                    for t in threads:
                        t.join(timeout=3)

                    self.assertEqual(http_errors, [], f"HTTP errors during live Caddy cutover: {http_errors}")
                    self.assertEqual(ws_errors, [], f"WS errors during live Caddy cutover: {ws_errors}")
                    self.assertGreaterEqual(get_count, 80)
                    self.assertEqual(get_slots_seen, {"slot-a-v1", "slot-b-canary", "slot-a-v2"})

                    # Verify every single non-idempotent POST executed AT MOST ONCE and AT LEAST ONCE (exact 1:1)!
                    with recorder.lock:
                        recorded_ids = [rid for rid, _ in recorder.calls]
                    self.assertEqual(len(recorded_ids), len(post_sent_ids))
                    self.assertEqual(set(recorded_ids), set(post_sent_ids))

                    # Verify WS received 1001 Going Away on both slot drains and strictly contiguous cursor catch-up!
                    self.assertIn(1001, ws_close_codes)
                    self.assertGreaterEqual(len(ws_close_codes), 2)
                    total_published = broker.current()
                    self.assertGreaterEqual(len(ws_received_seqs), 15)
                    self.assertEqual(ws_received_seqs, list(range(1, ws_received_seqs[-1] + 1)))
                    self.assertGreaterEqual(ws_received_seqs[-1], total_published - 2)
                finally:
                    proc.terminate()
                    proc.wait(timeout=5)
                    for srv in (slot_a1, slot_canary, slot_a2):
                        if srv is not None:
                            srv.drain_and_stop(grace_secs=0)


class _ReusableThreadingHTTPServer(ThreadingHTTPServer):
    allow_reuse_address = True


class _SlotServerOnPort(_SlotServer):
    def __init__(self, slot_name: str, port: int, event_broker: EventBroker, post_recorder: PostRecorder):
        self.slot_name = slot_name
        self.broker = event_broker
        self.post_recorder = post_recorder
        self.draining = threading.Event()
        self._ws_lock = threading.Lock()
        self._ws_Stops: list[threading.Event] = []

        parent = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, format, *args):
                return

            def _send_json(self, status: int, body: dict):
                raw = (json.dumps(body) + "\n").encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                if parent.draining.is_set():
                    self.send_header("Connection", "close")
                    self.close_connection = True
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self):
                if self.path == "/readyz":
                    if parent.draining.is_set():
                        self._send_json(503, {"status": "draining"})
                    else:
                        self._send_json(200, {"status": "ready"})
                    return
                if self.path == "/healthz":
                    self._send_json(200, {"status": "ok"})
                    return
                if self.path.startswith("/api/v1/ws"):
                    if parent.draining.is_set():
                        self._send_json(503, {"status": "draining"})
                        return
                    cursor = 0
                    if "cursor=" in self.path:
                        cursor = int(self.path.split("cursor=", 1)[1].split("&", 1)[0])
                    key = self.headers.get("Sec-WebSocket-Key", "")
                    accept = base64.b64encode(hashlib.sha1((key + WS_GUID).encode("ascii")).digest()).decode("ascii")
                    self.send_response(101, "Switching Protocols")
                    self.send_header("Upgrade", "websocket")
                    self.send_header("Connection", "Upgrade")
                    self.send_header("Sec-WebSocket-Accept", accept)
                    self.end_headers()
                    self.close_connection = True

                    ws_stop = threading.Event()
                    with parent._ws_lock:
                        parent._ws_Stops.append(ws_stop)
                    try:
                        last_sent = cursor
                        while not ws_stop.is_set():
                            for seq in parent.broker.since(last_sent):
                                payload = json.dumps({"seq": seq, "slot": parent.slot_name}).encode("utf-8")
                                _ws_write_server_frame(self.connection, 0x1, payload)
                                last_sent = seq
                            time.sleep(0.02)
                        close_payload = struct.pack("!H", 1001) + b"server draining"
                        _ws_write_server_frame(self.connection, 0x8, close_payload)
                    except OSError:
                        pass
                    return
                self._send_json(200, {"slot": parent.slot_name, "path": self.path})

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                raw = self.rfile.read(length) if length > 0 else b"{}"
                data = json.loads(raw.decode("utf-8"))
                parent.post_recorder.record(data["reqId"], parent.slot_name)
                self._send_json(200, {"slot": parent.slot_name, "reqId": data["reqId"]})

        self.httpd = _ReusableThreadingHTTPServer(("127.0.0.1", port), Handler)
        self.httpd.daemon_threads = True
        self.port = self.httpd.server_address[1]
        self._thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self._thread.start()


if __name__ == "__main__":
    unittest.main()
