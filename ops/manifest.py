#!/usr/bin/env python3
"""Release metadata validation and ephemeral registry authentication."""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import subprocess
import sys
import tempfile
from urllib.parse import unquote, urlsplit

LEGACY_SERVICES = ("identity", "chat", "realtime", "web", "caddy", "postgres", "redis")
SERVICES = LEGACY_SERVICES + ("agent-control", "agent-runtime", "llm-gateway", "sandbox", "sandbox-runner")
SERVICES_V3 = SERVICES + ("marketplace",)
ONLINE_AB_SERVICES = ("identity", "chat", "marketplace", "realtime", "web", "agent-control", "llm-gateway")
SINGLE_INSTANCE_SERVICES = ("postgres", "redis", "sandbox", "agent-runtime")
MAX_CONTAINER_BUDGET_BYTES = 1600 * 1024 * 1024
SANDBOX_CHILD_RESERVE_BYTES = 128 * 1024 * 1024
SCHEMA_V2 = {"identity": 3, "chat": 9, "agent_control": 3, "agent_runtime": 15, "gateway": 6}
SCHEMA_V3 = {"identity": 3, "chat": 12, "marketplace": 1, "agent_control": 3, "agent_runtime": 16, "gateway": 7}
VERSION = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
SHA = re.compile(r"[0-9a-f]{40}$")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}$")
NAMESPACE = re.compile(r"[a-z0-9]+(?:[._-][a-z0-9]+)*$")
REGION = r"[a-z]{2}(?:-[a-z0-9]+)+"
INSTANCE = r"[a-z0-9](?:[a-z0-9-]*[a-z0-9])?"
ACR_PUBLIC_HOSTS = (
    (re.compile(r"registry\.(" + REGION + r")\.aliyuncs\.com$"),
     lambda m: "registry-vpc." + m[1] + ".aliyuncs.com"),
    (re.compile(r"(crpi-[a-z0-9]+)\.(" + REGION + r")\.personal\.cr\.aliyuncs\.com$"),
     lambda m: m[1] + "-vpc." + m[2] + ".personal.cr.aliyuncs.com"),
    (re.compile(r"(" + INSTANCE + r")-registry\.(" + REGION + r")\.cr\.aliyuncs\.com$"),
     lambda m: m[1] + "-registry-vpc." + m[2] + ".cr.aliyuncs.com"),
)


def validate_registry(registry):
    require(type(registry) is dict and set(registry) == {"provider", "pushHost", "pullHost", "namespace"},
            "registry must contain exactly provider, pushHost, pullHost and namespace")
    require(registry["provider"] == "acr", "unsupported registry provider")
    public, pull, namespace = registry["pushHost"], registry["pullHost"], registry["namespace"]
    for host in (public, pull):
        require(isinstance(host, str) and len(host) <= 253 and all(0 < len(label) <= 63 for label in host.split(".")),
                "invalid ACR registry host")
    require(isinstance(namespace, str) and len(namespace) <= 63 and NAMESPACE.fullmatch(namespace),
            "invalid ACR registry namespace")
    for pattern, vpc_host in ACR_PUBLIC_HOSTS:
        match = pattern.fullmatch(public)
        if match:
            require(pull in (public, vpc_host(match)), "ACR push/pull hosts must name the same instance and region")
            return registry
    raise ValueError("pushHost must be a supported ACR public registry host")


def release_registry(data):
    if "registry" not in data:
        return {"provider": "ghcr", "pullHost": "ghcr.io"}
    require(data["manifestVersion"] in (2, 3), "registry metadata requires manifest version 2 or 3")
    return validate_registry(data["registry"])


def image_prefix(data):
    registry = release_registry(data)
    if registry["provider"] == "ghcr":
        return "ghcr.io/" + data["source"]["repository"].lower() + "-"
    return registry["pullHost"] + "/" + registry["namespace"] + "/teamie-"


def registry_credentials(frame, provider):
    require(type(frame) is dict and set(frame) <= {"acr", "ghcr"}, "invalid registry credential frame")
    require(provider in frame, "credentials for the selected release registry are required")
    for value in frame.values():
        require(type(value) is dict and set(value) == {"username", "password"}, "invalid registry credential frame")
    selected = frame[provider]
    user, password = selected["username"], selected["password"]
    # Docker receives the alias as one --username=value argument. ACR RAM aliases
    # may contain @ and non-ASCII account letters; colon/control/space are invalid.
    require(isinstance(user, str) and 0 < len(user) <= 256 and user[0].isalnum() and
            all(c.isalnum() or c in "_.@+-" for c in user), "invalid registry username")
    require(isinstance(password, str) and 0 < len(password.encode("utf-8")) <= 16384 and
            not any(c in password for c in "\0\r\n"), "invalid registry password")
    return user, password


def registry_login(data, frame):
    validate(data)
    registry = release_registry(data)
    username, password = registry_credentials(frame, registry["provider"])
    config = os.environ.get("DOCKER_CONFIG", "")
    require(config and Path(config).is_dir() and (Path(config).stat().st_mode & 0o777) == 0o700 and
            Path(config).stat().st_uid == os.getuid(), "registry login requires a private temporary DOCKER_CONFIG")
    try:
        subprocess.run(["docker", "login", registry["pullHost"], "--username=" + username, "--password-stdin"],
                       input=password.encode("utf-8"), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                       check=True, timeout=60)
    except (OSError, subprocess.SubprocessError) as error:
        raise ValueError("registry authentication failed") from error


def require(condition, message):
    if not condition:
        raise ValueError(message)


def read(path):
    return json.loads(Path(path).read_text())


def version(value):
    match = VERSION.fullmatch(value)
    require(match, "version must be vMAJOR.MINOR.PATCH without leading zeroes")
    return tuple(map(int, match.groups()))


def services_for_manifest_version(manifest_version):
    if manifest_version == 1:
        return LEGACY_SERVICES
    if manifest_version == 2:
        return SERVICES
    return SERVICES_V3


def schema_for_manifest_version(manifest_version):
    if manifest_version == 1:
        return {"identity": 1, "chat": 1}
    if manifest_version == 2:
        return dict(SCHEMA_V2)
    return dict(SCHEMA_V3)


def validate(data):
    require(type(data.get("manifestVersion")) is int and data["manifestVersion"] in (1, 2, 3), "unsupported manifest version")
    services = services_for_manifest_version(data["manifestVersion"])
    version(data["version"])
    source = data["source"]
    require(source["repository"].lower() == "zzqdeco/teamie", "unexpected source repository")
    require(SHA.fullmatch(source["sha"]), "invalid source SHA")
    require(source["sha"] == source["mainSha"], "release source must match checked main")
    require(source["ref"] == "refs/heads/release/" + data["version"], "release ref/version mismatch")
    require(set(data["images"]) == set(services), "image vector does not match the manifest version")
    prefix = image_prefix(data)
    for service, image in data["images"].items():
        require(isinstance(image, str) and image.startswith(prefix + service + "@"), f"unexpected {service} image repository")
        require(DIGEST.fullmatch(image.split("@", 1)[1]), f"{service} must use an immutable digest")
    schema_keys = set(schema_for_manifest_version(data["manifestVersion"]))
    require(set(data["schema"]) == schema_keys, "unknown database schema")
    require(all(type(x) is int and x >= 1 for x in data["schema"].values()), "invalid schema version")
    require(data["storage"]["postgresMajor"] == 17, "unreviewed PostgreSQL storage major")
    require(re.fullmatch(r"[0-9a-f]{64}", data["featureFlags"]["sha256"]), "invalid default flags hash")
    require(bool(data["featureFlags"]["revision"]), "missing default flag revision")
    created = dt.datetime.fromisoformat(data["createdAt"].replace("Z", "+00:00"))
    require(created.tzinfo is not None, "manifest date needs a timezone")
    if "minCompatibleVersion" in data:
        require(isinstance(data["minCompatibleVersion"], str), "minCompatibleVersion must be a semantic version string")
        require(version(data["minCompatibleVersion"]) <= version(data["version"]),
                "minCompatibleVersion cannot exceed manifest version")
    if "onlineRollout" in data:
        require(
            data["manifestVersion"] == 3 and data["onlineRollout"] is True,
            "onlineRollout capability requires manifestVersion 3 and boolean true",
        )
    return data


def image_env(data):
    validate(data)
    return "".join(f"{key.upper().replace(chr(45), chr(95))}_IMAGE={value}\n" for key, value in data["images"].items())


def compatible(current, target):
    require(current["schema"] == target["schema"], "image rollback requires identical schema versions; migrations are never reversed")
    require(current["storage"] == target["storage"], "image rollback cannot change database storage major")
    require(current["featureFlags"] == target["featureFlags"], "image rollback requires matching default flag contract")
    if "minCompatibleVersion" in current:
        require(version(target["version"]) >= version(current["minCompatibleVersion"]),
                "target version is older than current minCompatibleVersion")
    if "minCompatibleVersion" in target:
        require(version(current["version"]) >= version(target["minCompatibleVersion"]),
                "current version is older than target minCompatibleVersion")


def online_compatible(target, current=None):
    """Return True only when Slot A (current) and Slot B (target) can safely run concurrently."""
    if current is None:
        return False
    try:
        validate(target)
        validate(current)
        require(target["manifestVersion"] == current["manifestVersion"],
                "online A/B rollout requires identical manifestVersion")
        require(
            target.get("onlineRollout") is True and current.get("onlineRollout") is True,
            "online A/B rollout requires dual-slot capability in both current and target releases",
        )
        compatible(target, current)
        compatible(current, target)
    except (ValueError, KeyError, TypeError):
        return False
    return True


def check_transition(candidate, current=None, highwater=None, rollback=False):
    validate(candidate)
    if rollback:
        require(current is not None, "nothing has been deployed")
        validate(current)
        compatible(current, candidate)
        require(version(candidate["version"]) <= version(current["version"]), "rollback target must not be newer")
        return
    created = dt.datetime.fromisoformat(candidate["createdAt"].replace("Z", "+00:00"))
    age = (dt.datetime.now(dt.timezone.utc) - created).total_seconds()
    require(-300 <= age <= 86400, "release manifest is stale or its clock is in the future")
    if highwater:
        validate(highwater)
        new, previous = version(candidate["version"]), version(highwater["version"])
        require(new >= previous, "release version is lower than the highest deployed version")
        if new == previous:
            require(candidate["source"] == highwater["source"] and candidate["images"] == highwater["images"] and
                    candidate.get("registry") == highwater.get("registry") and
                    candidate.get("onlineRollout") == highwater.get("onlineRollout"),
                    "an already deployed release is immutable; create a new version")
    if current:
        validate(current)
        require(all(candidate["schema"].get(s, 0) >= current["schema"][s] for s in current["schema"]), "forward deployment cannot lower database schema")
        require(candidate["storage"] == current["storage"], "database major upgrades need a separate reviewed procedure")


def validate_config(config, host_env, release_manifest=None):
    require((Path(host_env).stat().st_mode & 0o777) == 0o600, "production.env must have mode 0600")
    services = config["services"]
    if release_manifest is not None:
        validate(release_manifest)
        expected_services = set(release_manifest["images"]) - {"sandbox-runner"}
        require(set(services) == expected_services, "compose services differ from the selected release")
        require(all(services[name].get("image") == release_manifest["images"][name] for name in expected_services),
                "compose images differ from the selected release")
    origin = services["identity"]["environment"]["APP_ORIGIN"]
    parsed = urlsplit(origin)
    require(parsed.scheme == "https" and parsed.hostname and not parsed.username and not parsed.password,
            "production APP_ORIGIN must be HTTPS")
    require(parsed.path in ("", "/") and not parsed.query and not parsed.fragment, "APP_ORIGIN must be an origin")
    require(origin == origin.rstrip("/"), "APP_ORIGIN must not have a trailing slash")
    require(services["caddy"]["environment"]["SITE_ADDRESS"] == origin, "SITE_ADDRESS and APP_ORIGIN must match")
    require(services["caddy"]["environment"].get("TLS_SERVER_NAME") == parsed.hostname,
            "TLS_SERVER_NAME must equal the APP_ORIGIN hostname without scheme or port")
    pg_env = services["postgres"]["environment"]
    for name in ("identity", "chat", "realtime", "marketplace", "agent-control", "llm-gateway"):
        if name not in services:
            continue
        env = services[name]["environment"]
        require(str(env["COOKIE_SECURE"]).lower() == "true", "production cookies must be Secure")
        require(env["APP_ORIGIN"] == origin, "all service origins must match")
    db_services = {"identity": "identity", "chat": "chat"}
    if "marketplace" in services:
        db_services["marketplace"] = "marketplace"
    if "agent-control" in services:
        db_services.update({"agent-control": "agent_control", "agent-runtime": "agent_runtime", "llm-gateway": "gateway"})
    for name, suffix in db_services.items():
        db = urlsplit(services[name]["environment"]["DATABASE_URL"])
        require(db.scheme in ("postgres", "postgresql") and db.hostname == "postgres" and db.port == 5432,
                f"{name} must use private PostgreSQL")
        require(db.username == "teamie_" + suffix and db.path == "/teamie_" + suffix, f"{name} must own a separate role/database")
        require(unquote(db.password or "") == pg_env["POSTGRES_" + suffix.upper() + "_PASSWORD"], f"{name} initialization/runtime passwords disagree")
    credentials = [pg_env["POSTGRES_PASSWORD"]] + [pg_env["POSTGRES_" + suffix.upper() + "_PASSWORD"] for suffix in db_services.values()]
    require(all(len(value) >= 24 and "REPLACE" not in value for value in credentials), "replace every database credential with a random value of at least 24 characters")
    require(len(set(credentials)) == len(credentials), "database credentials must be independent")
    if "marketplace" in services:
        require("egress" in services["marketplace"].get("networks", ()), "marketplace requires endpoint egress")
        chat_nets = services["chat"].get("networks")
        require(not chat_nets or set(chat_nets) == {"private"}, "chat must stay on private network without egress")
    requires_realtime_rpc = (
        (release_manifest is not None and release_manifest["schema"].get("chat", 0) >= 12)
        or (release_manifest is None and "marketplace" in services)
        or "REALTIME_RPC_TOKEN" in services["chat"]["environment"]
        or "REALTIME_RPC_TOKEN" in services["realtime"]["environment"]
        or "REALTIME_RPC_ADDR" in services["chat"]["environment"]
    )
    realtime_tokens = []
    if requires_realtime_rpc:
        chat_env = services["chat"]["environment"]
        rt_env = services["realtime"]["environment"]
        require(chat_env.get("REALTIME_RPC_ADDR") == "realtime:8883", "chat must use private realtime RPC")
        require(rt_env.get("RPC_ADDR") == ":8883", "realtime must listen on private RPC :8883")
        rt_nets = services["realtime"].get("networks")
        require(not rt_nets or set(rt_nets) == {"private"}, "realtime must stay on private network without egress")
        rt_token = chat_env.get("REALTIME_RPC_TOKEN", "")
        require(isinstance(rt_token, str) and len(rt_token) >= 24 and "REPLACE" not in rt_token, "replace realtime rpc credential")
        require(rt_env.get("REALTIME_RPC_TOKEN") == rt_token, "realtime rpc credential mismatch")
        for name, svc in services.items():
            if name not in ("chat", "realtime"):
                svc_env = svc.get("environment") or {}
                require("REALTIME_RPC_TOKEN" not in svc_env and "REALTIME_RPC_ADDR" not in svc_env,
                        "realtime rpc credential and address belong to chat and realtime only")
        realtime_tokens.append(rt_token)
        require(len(set(credentials + realtime_tokens)) == len(credentials + realtime_tokens), "database and service credentials must be independent")
    if "llm-gateway" in services:
        gateway = services["llm-gateway"]["environment"]
        runtime = services["agent-runtime"]["environment"]
        sandbox = services["sandbox"]["environment"]
        control = services["agent-control"]["environment"]
        identity = services["identity"]["environment"]
        master = gateway["GATEWAY_MASTER_KEY"]
        require(re.fullmatch(r"[0-9a-fA-F]{64}", master), "gateway encryption key must be 64 hex characters")
        tokens = [control["AGENT_RUNTIME_TOKEN"], identity["IM_PRINCIPAL_CONTROL_TOKEN"], identity["IM_PRINCIPAL_RUNTIME_TOKEN"], gateway["GATEWAY_RUNTIME_TOKEN"], sandbox["SANDBOX_RUNTIME_TOKEN"]]
        require(all(len(v) >= 24 and "REPLACE" not in v for v in tokens), "replace every internal service credential")
        all_secrets = credentials + tokens + realtime_tokens + [master]
        require(len(set(all_secrets)) == len(all_secrets), "database, encryption and service credentials must be independent")
        for key, source in (("AGENT_RUNTIME_TOKEN",control),("GATEWAY_RUNTIME_TOKEN",gateway),("SANDBOX_RUNTIME_TOKEN",sandbox),("IM_PRINCIPAL_RUNTIME_TOKEN",identity)):
            require(runtime[key] == source[key], "runtime scoped credential mismatch")
        require(control["IM_PRINCIPAL_CONTROL_TOKEN"] == identity["IM_PRINCIPAL_CONTROL_TOKEN"] == services["chat"]["environment"]["IM_PRINCIPAL_CONTROL_TOKEN"], "principal control credential mismatch")
        require("egress" in services["llm-gateway"]["networks"], "gateway requires provider egress")
        require(not any(k in sandbox for k in ("GATEWAY_MASTER_KEY","GATEWAY_RUNTIME_TOKEN","IM_PRINCIPAL_RUNTIME_TOKEN","DATABASE_URL")), "sandbox must not receive provider/IM credentials")
        runner = sandbox["SANDBOX_RUNNER_IMAGE"]
        if release_manifest is None:
            # Unbound legacy callers retain the exact historical allowlist.
            require(re.fullmatch(r"ghcr\.io/zzqdeco/teamie-sandbox-runner@sha256:[0-9a-f]{64}", runner), "sandbox runner must be the immutable platform image")
        else:
            require(runner == release_manifest["images"].get("sandbox-runner"), "sandbox runner differs from the selected release")
        for name, service in services.items():
            if name != "sandbox":
                require(not any(v.get("target") == "/var/run/docker.sock" for v in service.get("volumes", [])), "only sandbox may access Docker")
    bootstrap = services["identity"]["environment"].get("BOOTSTRAP_INVITE_CODE", "teamie-bootstrap")
    require(isinstance(bootstrap, str) and bool(bootstrap.strip()), "bootstrap invitation must resolve to a nonempty code or its default")
    for name, service in services.items():
        require(name == "caddy" or not service.get("ports"), "only Caddy may publish host ports")
    return origin


def ensure_host_env(host_env, release_manifest):
    validate(release_manifest)
    env_path = Path(host_env)
    require((env_path.stat().st_mode & 0o777) == 0o600, "production.env must have mode 0600")
    needs_marketplace = "marketplace" in release_manifest["images"]
    needs_realtime_rpc = release_manifest["schema"].get("chat", 0) >= 12
    if not needs_marketplace and not needs_realtime_rpc:
        return False
    raw_text = env_path.read_text(encoding="utf-8")
    entries = {}
    for line in raw_text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if stripped.startswith("export ") and "=" in stripped[7:]:
            stripped = stripped[7:].lstrip()
        if "=" in stripped:
            key, val = stripped.split("=", 1)
            entries[key.strip()] = val.strip().strip("'\"")
    has_mkt_pw = bool(entries.get("POSTGRES_MARKETPLACE_PASSWORD"))
    has_mkt_url = bool(entries.get("MARKETPLACE_DATABASE_URL"))
    if needs_marketplace and has_mkt_pw != has_mkt_url:
        return False
    existing_values = {v for v in entries.values() if v}
    addition_lines = []
    if needs_marketplace and not has_mkt_pw and not has_mkt_url:
        password = secrets.token_hex(32)
        while password in existing_values:
            password = secrets.token_hex(32)
        existing_values.add(password)
        addition_lines.append(
            f"POSTGRES_MARKETPLACE_PASSWORD={password}\n"
            f"MARKETPLACE_DATABASE_URL=postgres://teamie_marketplace:{password}@postgres:5432/teamie_marketplace?sslmode=disable\n"
        )
    if needs_realtime_rpc and not entries.get("REALTIME_RPC_TOKEN"):
        rt_token = secrets.token_hex(32)
        while rt_token in existing_values:
            rt_token = secrets.token_hex(32)
        existing_values.add(rt_token)
        addition_lines.append(f"REALTIME_RPC_TOKEN={rt_token}\n")
    if not addition_lines:
        return False
    addition = "".join(addition_lines)
    updated = raw_text + ("" if not raw_text or raw_text.endswith("\n") else "\n") + addition
    fd, tmp_name = tempfile.mkstemp(prefix=".production.env.", dir=str(env_path.parent))
    try:
        os.chmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(updated)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp_name, str(env_path))
    except Exception:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise
    return True


def _compose_network_name(config, net_key):
    project = config.get("name") or "teamie"
    net_defs = config.get("networks") or {}
    net_cfg = net_defs.get(net_key) or {}
    if isinstance(net_cfg, dict) and net_cfg.get("name"):
        return str(net_cfg["name"])
    return f"{project}_{net_key}"


def _compose_volume_name(config, vol_key):
    project = config.get("name") or "teamie"
    vol_defs = config.get("volumes") or {}
    vol_cfg = vol_defs.get(vol_key) or {}
    if isinstance(vol_cfg, dict) and vol_cfg.get("name"):
        return str(vol_cfg["name"])
    return f"{project}_{vol_key}"


_RECIPE_CONTENT_MOUNT_TARGETS = frozenset(
    {
        "/etc/caddy/Caddyfile",
        "/docker-entrypoint-initdb.d/01-databases.sh",
    }
)


def _same_mount_source(expected_source, mounted_source):
    if not expected_source or not mounted_source:
        return False
    expected_abs = os.path.abspath(str(expected_source))
    mounted_abs = os.path.abspath(str(mounted_source))
    if expected_abs != mounted_abs:
        return False
    return os.path.realpath(expected_abs) == os.path.realpath(mounted_abs)


_DURATION_TOKEN_RE = re.compile(r"(\d+(?:\.\d+)?)(ns|us|µs|μs|ms|s|m|h)")
_DURATION_UNITS_NS = {
    "ns": 1,
    "us": 1_000,
    "µs": 1_000,
    "μs": 1_000,
    "ms": 1_000_000,
    "s": 1_000_000_000,
    "m": 60 * 1_000_000_000,
    "h": 3600 * 1_000_000_000,
}


def _parse_mem_bytes(value):
    if value is None or value == "":
        return 0
    if isinstance(value, (int, float)):
        return max(0, int(value))
    raw = str(value).strip().lower()
    if not raw:
        return 0
    if raw.isdigit():
        return int(raw)
    match = re.fullmatch(r"([0-9]+)\s*(b|k|kb|kib|m|mb|mib|g|gb|gib)", raw)
    require(match is not None, f"invalid memory limit value {value!r}")
    amount = int(match.group(1))
    unit = match.group(2)
    if unit == "b":
        return amount
    if unit in ("k", "kb", "kib"):
        return amount * 1024
    if unit in ("m", "mb", "mib"):
        return amount * 1024 * 1024
    return amount * 1024 * 1024 * 1024


def _parse_signed_mem_bytes(value):
    if value is None or value == "":
        return 0
    if isinstance(value, int):
        return value
    text = str(value).strip()
    if text == "-1":
        return -1
    return _parse_mem_bytes(text)


def _parse_duration_ns(value):
    if value is None or value == "":
        return 0
    if isinstance(value, (int, float)):
        return int(value)
    text = str(value).strip()
    if not text:
        return 0
    if text.isdigit():
        return int(text)
    pos = 0
    total = 0
    for match in _DURATION_TOKEN_RE.finditer(text):
        if match.start() != pos:
            raise ValueError(f"invalid duration {value!r}")
        total += int(round(float(match.group(1)) * _DURATION_UNITS_NS[match.group(2)]))
        pos = match.end()
    if pos != len(text):
        raise ValueError(f"invalid duration {value!r}")
    return total


def _normalize_argv(value):
    if value is None:
        return None
    if isinstance(value, str):
        return shlex.split(value) if value else []
    if isinstance(value, (list, tuple)):
        return [str(x) for x in value]
    raise ValueError(f"invalid argv specification {value!r}")


def _normalize_pids_limit(value):
    if value in (None, "", 0, -1, "0", "-1"):
        return 0
    return int(value)


def _normalize_caps(items):
    out = []
    for raw in items or []:
        cap = str(raw).strip().upper()
        if cap.startswith("CAP_") and cap != "ALL":
            cap = cap[4:]
        if cap:
            out.append(cap)
    return sorted(out)


def _normalize_security_opts(items):
    out = []
    for raw in items or []:
        opt = str(raw).strip()
        if opt.startswith("no-new-privileges="):
            opt = "no-new-privileges:" + opt.split("=", 1)[1]
        if opt:
            out.append(opt)
    return sorted(out)


def _normalize_spec_tmpfs(spec):
    result = {}
    for entry in spec.get("tmpfs") or []:
        if isinstance(entry, str) and entry:
            path, opts = entry.split(":", 1) if ":" in entry else (entry, "")
            result[path] = opts
    for vol in spec.get("volumes") or []:
        if isinstance(vol, dict) and vol.get("type") == "tmpfs" and vol.get("target"):
            tmpfs_opts = vol.get("tmpfs") or {}
            parts = []
            if tmpfs_opts.get("size") is not None:
                parts.append(f"size={_parse_mem_bytes(tmpfs_opts['size'])}")
            if tmpfs_opts.get("mode") is not None:
                parts.append(f"mode={oct(int(tmpfs_opts['mode']))[2:]}")
            result[str(vol["target"])] = ",".join(parts)
    return result


def _normalize_host_tmpfs(raw):
    if isinstance(raw, dict):
        return {str(k): str(v or "") for k, v in raw.items()}
    if isinstance(raw, list):
        result = {}
        for entry in raw:
            if isinstance(entry, str) and entry:
                path, opts = entry.split(":", 1) if ":" in entry else (entry, "")
                result[path] = opts
        return result
    return {}


def _normalize_spec_ports(spec):
    bindings = set()
    for p in spec.get("ports") or []:
        if isinstance(p, dict):
            proto = str(p.get("protocol") or "tcp").lower()
            target = str(p.get("target") or "")
            published = str(p.get("published") or "")
            host_ip = str(p.get("host_ip") or "")
            if host_ip == "0.0.0.0":
                host_ip = ""
            bindings.add((f"{target}/{proto}", host_ip, published))
        elif isinstance(p, str) and p:
            raw = p
            proto = "tcp"
            if "/" in raw:
                raw, proto = raw.rsplit("/", 1)
                proto = proto.lower()
            parts = raw.split(":")
            if len(parts) == 3:
                host_ip, published, target = parts
            elif len(parts) == 2:
                host_ip, published, target = "", parts[0], parts[1]
            else:
                host_ip, published, target = "", "", parts[0]
            if host_ip == "0.0.0.0":
                host_ip = ""
            bindings.add((f"{target}/{proto}", host_ip, published))
    return bindings


def _normalize_host_ports(port_bindings):
    bindings = set()
    if not isinstance(port_bindings, dict):
        return bindings
    for key, entries in port_bindings.items():
        key_norm = str(key).lower()
        if "/" not in key_norm:
            key_norm += "/tcp"
        for item in entries or []:
            if isinstance(item, dict):
                host_ip = str(item.get("HostIp") or "")
                if host_ip == "0.0.0.0":
                    host_ip = ""
                host_port = str(item.get("HostPort") or "")
                bindings.add((key_norm, host_ip, host_port))
    return bindings


def _normalize_spec_restart(value):
    if not value or value == "no":
        return ("no", 0)
    raw = str(value).strip()
    if ":" in raw:
        name, count = raw.split(":", 1)
        return (name or "no", int(count or 0))
    return (raw, 0)


def _normalize_host_restart(restart_policy):
    if not isinstance(restart_policy, dict):
        return ("no", 0)
    name = str(restart_policy.get("Name") or "").strip() or "no"
    count = int(restart_policy.get("MaximumRetryCount") or 0)
    return (name, count)


def _normalize_ulimits(raw):
    out = {}
    if isinstance(raw, dict):
        for name, val in raw.items():
            if isinstance(val, dict):
                out[str(name).lower()] = (int(val.get("soft", 0)), int(val.get("hard", 0)))
            else:
                iv = int(val)
                out[str(name).lower()] = (iv, iv)
    elif isinstance(raw, list):
        for item in raw:
            if isinstance(item, dict) and item.get("Name"):
                out[str(item["Name"]).lower()] = (int(item.get("Soft", 0)), int(item.get("Hard", 0)))
    return out


def _inspect_image_config(image):
    if not image:
        return None
    try:
        inspected = json.loads(
            subprocess.check_output(["docker", "image", "inspect", str(image)], text=True, stderr=subprocess.DEVNULL)
        )
    except (OSError, subprocess.SubprocessError, ValueError):
        return None
    if not isinstance(inspected, list) or not inspected or not isinstance(inspected[0], dict):
        return None
    cfg = inspected[0].get("Config")
    return cfg if isinstance(cfg, dict) else None


def _caddy_tls_state_path():
    root = os.environ.get("TEAMIE_ROOT", "/srv/teamie")
    return Path(root) / "state" / "caddy-tls-loaded.json"


def _caddy_tls_fingerprint(config, container_id=None):
    services = config.get("services") or {}
    spec = services.get("caddy") or {}
    issuer_source = None
    tls_dir_source = None
    for vol in spec.get("volumes") or []:
        if not isinstance(vol, dict) or vol.get("type") != "bind":
            continue
        if vol.get("target") == "/etc/caddy/issuer.caddy":
            issuer_source = vol.get("source")
        elif vol.get("target") == "/run/tls":
            tls_dir_source = vol.get("source")
    if not issuer_source:
        return None
    issuer_bytes = Path(issuer_source).read_bytes()
    issuer_sha = hashlib.sha256(issuer_bytes).hexdigest()
    issuer_text = issuer_bytes.decode("utf-8", errors="replace")
    if "/run/tls/" not in issuer_text:
        return {"issuerSha256": issuer_sha, "tlsFilesSha256": "acme"}
    require(bool(tls_dir_source), "missing /run/tls mount for file-based TLS")
    try:
        fc_sha = hashlib.sha256((Path(tls_dir_source) / "fullchain.pem").read_bytes()).hexdigest()
        pk_sha = hashlib.sha256((Path(tls_dir_source) / "privkey.pem").read_bytes()).hexdigest()
    except PermissionError:
        require(bool(container_id), "running caddy container required to hash root-owned /run/tls files")
        out = subprocess.check_output(
            ["docker", "exec", str(container_id), "sha256sum", "/run/tls/fullchain.pem", "/run/tls/privkey.pem"],
            text=True,
            stderr=subprocess.DEVNULL,
        )
        lines = [ln.strip().split()[0] for ln in out.splitlines() if ln.strip()]
        require(
            len(lines) == 2 and all(re.fullmatch(r"[0-9a-f]{64}", d) for d in lines),
            "unexpected sha256sum output for /run/tls certificate files",
        )
        fc_sha, pk_sha = lines[0], lines[1]
    combined = hashlib.sha256(f"fullchain:{fc_sha}\nprivkey:{pk_sha}\n".encode("utf-8")).hexdigest()
    return {"issuerSha256": issuer_sha, "tlsFilesSha256": combined}


def record_caddy_tls(config):
    """Record the exact TLS snippet and certificate digests loaded by the running Caddy container."""
    services = config.get("services") or {}
    require("caddy" in services, "caddy service missing from compose config")
    project = config.get("name") or "teamie"
    ps = subprocess.run(
        [
            "docker",
            "ps",
            "--filter",
            f"label=com.docker.compose.project={project}",
            "--filter",
            "label=com.docker.compose.service=caddy",
            "--format",
            "{{.ID}}",
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    ids = ps.stdout.split()
    require(len(ids) == 1, "expected exactly one running caddy container")
    inspected = json.loads(subprocess.check_output(["docker", "inspect", ids[0]], text=True, stderr=subprocess.DEVNULL))
    require(bool(inspected), "unable to inspect running caddy container")
    info = inspected[0]
    state = info.get("State") or {}
    require(bool(state.get("Running")), "caddy container is not running")
    health = state.get("Health")
    if isinstance(health, dict):
        require(health.get("Status") == "healthy", "caddy container is not healthy")
    fp = _caddy_tls_fingerprint(config, info.get("Id") or ids[0])
    if fp is None:
        return
    stamp_path = _caddy_tls_state_path()
    stamp_path.parent.mkdir(parents=True, exist_ok=True)
    payload = {
        "containerId": info.get("Id") or ids[0],
        "startedAt": state.get("StartedAt") or "",
        "issuerSha256": fp["issuerSha256"],
        "tlsFilesSha256": fp["tlsFilesSha256"],
    }
    fd, tmp_name = tempfile.mkstemp(prefix=".caddy-tls-", dir=str(stamp_path.parent))
    try:
        os.chmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, sort_keys=True)
            handle.write("\n")
            handle.flush()
        os.replace(tmp_name, stamp_path)
    except Exception:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise


def _runtime_settings_match(config, spec, info):
    container_cfg = info.get("Config") or {}
    host_cfg = info.get("HostConfig") or {}

    image_cfg_loaded = False
    image_cfg = None

    def get_image_cfg():
        nonlocal image_cfg_loaded, image_cfg
        if not image_cfg_loaded:
            image_cfg = _inspect_image_config(spec.get("image"))
            image_cfg_loaded = True
        return image_cfg

    # 1. Environment variables (target keys must match, and any extra container keys must match image defaults).
    actual_env = {}
    for item in container_cfg.get("Env") or []:
        if isinstance(item, str) and "=" in item:
            k, v = item.split("=", 1)
            actual_env[k] = v
    raw_target_env = spec.get("environment")
    expected_env = {}
    if isinstance(raw_target_env, dict):
        for key, value in raw_target_env.items():
            if value is None:
                continue
            expected_env[str(key)] = str(value)
    elif isinstance(raw_target_env, list):
        for item in raw_target_env:
            if isinstance(item, str) and "=" in item:
                k, v = item.split("=", 1)
                expected_env[k] = v
    for key, expected_val in expected_env.items():
        if actual_env.get(key) != expected_val:
            return False
    extra_env_keys = set(actual_env) - set(expected_env)
    if extra_env_keys:
        img_cfg = get_image_cfg()
        if img_cfg is None:
            return False
        image_env = {}
        for item in img_cfg.get("Env") or []:
            if isinstance(item, str) and "=" in item:
                k, v = item.split("=", 1)
                image_env[k] = v
        for key in extra_env_keys:
            if image_env.get(key) != actual_env[key]:
                return False

    # 2. Entrypoint, command, user, working_dir.
    actual_ep = list(container_cfg.get("Entrypoint") or [])
    if spec.get("entrypoint") is not None:
        if actual_ep != _normalize_argv(spec["entrypoint"]):
            return False
    elif actual_ep:
        img_cfg = get_image_cfg()
        if img_cfg is None or actual_ep != list(img_cfg.get("Entrypoint") or []):
            return False

    actual_cmd = list(container_cfg.get("Cmd") or [])
    if spec.get("command") is not None:
        if actual_cmd != _normalize_argv(spec["command"]):
            return False
    elif actual_cmd:
        if spec.get("entrypoint") is not None:
            return False
        img_cfg = get_image_cfg()
        if img_cfg is None or actual_cmd != list(img_cfg.get("Cmd") or []):
            return False

    actual_user = str(container_cfg.get("User") or "")
    if spec.get("user") is not None:
        if actual_user != str(spec["user"]):
            return False
    elif actual_user:
        img_cfg = get_image_cfg()
        if img_cfg is None or actual_user != str(img_cfg.get("User") or ""):
            return False

    actual_wd = str(container_cfg.get("WorkingDir") or "")
    if spec.get("working_dir") is not None:
        if actual_wd != str(spec["working_dir"]):
            return False
    elif actual_wd:
        img_cfg = get_image_cfg()
        if img_cfg is None or actual_wd != str(img_cfg.get("WorkingDir") or ""):
            return False

    # 3. Memory, swap, shm, PIDs, and CPU resource settings.
    expected_mem = _parse_mem_bytes(spec.get("mem_limit") or 0)
    actual_mem = _parse_mem_bytes(host_cfg.get("Memory") or 0)
    if actual_mem != expected_mem:
        return False

    actual_swap = _parse_signed_mem_bytes(host_cfg.get("MemorySwap") or 0)
    if spec.get("memswap_limit") is not None:
        if actual_swap != _parse_signed_mem_bytes(spec["memswap_limit"]):
            return False
    else:
        allowed_swaps = {0, 2 * expected_mem} if expected_mem > 0 else {0}
        if actual_swap not in allowed_swaps:
            return False

    if _parse_mem_bytes(host_cfg.get("MemoryReservation") or 0) != _parse_mem_bytes(spec.get("mem_reservation") or 0):
        return False

    actual_shm = _parse_mem_bytes(host_cfg.get("ShmSize") or 0)
    if spec.get("shm_size") is not None:
        if actual_shm != _parse_mem_bytes(spec["shm_size"]):
            return False
    elif actual_shm not in (0, 64 * 1024 * 1024):
        return False

    if _normalize_pids_limit(host_cfg.get("PidsLimit")) != _normalize_pids_limit(spec.get("pids_limit")):
        return False

    expected_nano = (
        int(round(float(spec["cpus"]) * 1_000_000_000))
        if spec.get("cpus") is not None
        else int(spec.get("nano_cpus") or 0)
    )
    if int(host_cfg.get("NanoCpus") or 0) != expected_nano:
        return False
    if int(host_cfg.get("CpuShares") or 0) != int(spec.get("cpu_shares") or 0):
        return False
    if int(host_cfg.get("CpuQuota") or 0) != int(spec.get("cpu_quota") or 0):
        return False
    if int(host_cfg.get("CpuPeriod") or 0) != int(spec.get("cpu_period") or 0):
        return False
    if str(host_cfg.get("CpusetCpus") or "") != str(spec.get("cpuset") or ""):
        return False

    # 4. Filesystem isolation, init, privileges, capabilities, security options, tmpfs, ports, restart, logging, sysctls, ulimits.
    if bool(host_cfg.get("ReadonlyRootfs") or False) != bool(spec.get("read_only") or False):
        return False
    if bool(host_cfg.get("Init") or False) != bool(spec.get("init") or False):
        return False
    if bool(host_cfg.get("Privileged") or False) != bool(spec.get("privileged") or False):
        return False
    if _normalize_security_opts(host_cfg.get("SecurityOpt")) != _normalize_security_opts(spec.get("security_opt")):
        return False
    if _normalize_caps(host_cfg.get("CapDrop")) != _normalize_caps(spec.get("cap_drop")):
        return False
    if _normalize_caps(host_cfg.get("CapAdd")) != _normalize_caps(spec.get("cap_add")):
        return False
    if _normalize_host_tmpfs(host_cfg.get("Tmpfs")) != _normalize_spec_tmpfs(spec):
        return False
    if _normalize_host_ports(host_cfg.get("PortBindings")) != _normalize_spec_ports(spec):
        return False
    if _normalize_host_restart(host_cfg.get("RestartPolicy")) != _normalize_spec_restart(spec.get("restart")):
        return False

    logging_spec = spec.get("logging")
    if isinstance(logging_spec, dict):
        actual_log = host_cfg.get("LogConfig") or {}
        if logging_spec.get("driver") and str(actual_log.get("Type") or "") != str(logging_spec["driver"]):
            return False
        expected_log_opts = {str(k): str(v) for k, v in (logging_spec.get("options") or {}).items()}
        actual_log_opts = {str(k): str(v) for k, v in (actual_log.get("Config") or {}).items()}
        if actual_log_opts != expected_log_opts:
            return False

    expected_sysctls = {str(k): str(v) for k, v in (spec.get("sysctls") or {}).items()}
    actual_sysctls = {str(k): str(v) for k, v in (host_cfg.get("Sysctls") or {}).items()}
    if actual_sysctls != expected_sysctls:
        return False

    if _normalize_ulimits(host_cfg.get("Ulimits")) != _normalize_ulimits(spec.get("ulimits")):
        return False

    # 5. Healthcheck configuration.
    hc = spec.get("healthcheck")
    if isinstance(hc, dict):
        actual_hc = container_cfg.get("Healthcheck") or {}
        if hc.get("disable"):
            expected_hc_test = ["NONE"]
        elif hc.get("test"):
            raw_test = hc["test"]
            expected_hc_test = ["CMD-SHELL", raw_test] if isinstance(raw_test, str) else [str(x) for x in raw_test]
        else:
            expected_hc_test = None
        if expected_hc_test is not None and list(actual_hc.get("Test") or []) != expected_hc_test:
            return False
        for spec_key, inspect_key in (
            ("interval", "Interval"),
            ("timeout", "Timeout"),
            ("start_period", "StartPeriod"),
            ("start_interval", "StartInterval"),
        ):
            if hc.get(spec_key) is not None:
                if _parse_duration_ns(hc[spec_key]) != _parse_duration_ns(actual_hc.get(inspect_key) or 0):
                    return False
        if hc.get("retries") is not None and int(hc["retries"]) != int(actual_hc.get("Retries") or 0):
            return False

    # 6. Attached networks.
    raw_nets = spec.get("networks")
    if raw_nets:
        net_keys = list(raw_nets.keys()) if isinstance(raw_nets, dict) else [str(n) for n in raw_nets]
        expected_nets = {_compose_network_name(config, n) for n in net_keys}
        net_settings = info.get("NetworkSettings")
        if not isinstance(net_settings, dict):
            return False
        actual_nets = set((net_settings.get("Networks") or {}).keys())
        if actual_nets != expected_nets:
            return False

    return True


def service_up_to_date(config, service_name):
    """Return True when exactly one healthy canonical container matches the target spec."""
    services = config.get("services") or {}
    if service_name not in services:
        return False
    spec = services[service_name]
    project = config.get("name") or "teamie"
    try:
        ps = subprocess.run(
            [
                "docker",
                "ps",
                "--filter",
                f"label=com.docker.compose.project={project}",
                "--filter",
                f"label=com.docker.compose.service={service_name}",
                "--format",
                "{{.ID}}",
            ],
            check=True,
            capture_output=True,
            text=True,
        )
    except (OSError, subprocess.SubprocessError):
        return False
    ids = ps.stdout.split()
    if len(ids) != 1:
        return False
    try:
        inspected = json.loads(
            subprocess.check_output(["docker", "inspect", ids[0]], text=True, stderr=subprocess.DEVNULL)
        )
    except (OSError, subprocess.SubprocessError, ValueError):
        return False
    if not inspected:
        return False
    info = inspected[0]
    state = info.get("State") or {}
    if not state.get("Running"):
        return False
    health = state.get("Health")
    if isinstance(health, dict) and health.get("Status") != "healthy":
        return False
    container_cfg = info.get("Config") or {}
    if container_cfg.get("Image") != spec.get("image"):
        return False
    try:
        if not _runtime_settings_match(config, spec, info):
            return False
    except (ValueError, TypeError, KeyError):
        return False
    actual_mounts = {
        m.get("Destination"): m
        for m in (info.get("Mounts") or [])
        if isinstance(m, dict) and m.get("Destination")
    }
    for vol in spec.get("volumes") or []:
        if not isinstance(vol, dict):
            continue
        vtype = vol.get("type", "bind")
        if vtype not in ("bind", "volume"):
            continue
        target = vol.get("target")
        target_source = vol.get("source")
        mount_info = actual_mounts.get(target)
        if not target or not target_source or not isinstance(mount_info, dict):
            return False
        if "RW" in mount_info:
            expected_rw = not bool(vol.get("read_only", False))
            if bool(mount_info.get("RW")) != expected_rw:
                return False
        if vtype == "volume":
            expected_vol_name = _compose_volume_name(config, str(target_source))
            actual_vol_name = mount_info.get("Name") or mount_info.get("Source")
            if mount_info.get("Type", "volume") != "volume" or actual_vol_name != expected_vol_name:
                return False
        else:
            mounted_source = mount_info.get("Source")
            if not mounted_source or mount_info.get("Type", "bind") != "bind":
                return False
            if target in _RECIPE_CONTENT_MOUNT_TARGETS:
                try:
                    if (
                        hashlib.sha256(Path(target_source).read_bytes()).hexdigest()
                        != hashlib.sha256(Path(mounted_source).read_bytes()).hexdigest()
                    ):
                        return False
                except OSError:
                    return False
            elif not _same_mount_source(target_source, mounted_source):
                return False
    if service_name == "caddy":
        try:
            expected_tls = _caddy_tls_fingerprint(config, info.get("Id") or ids[0])
        except (OSError, subprocess.SubprocessError, ValueError):
            return False
        if expected_tls is not None:
            stamp_path = _caddy_tls_state_path()
            try:
                stamp = json.loads(stamp_path.read_text(encoding="utf-8"))
            except (OSError, ValueError):
                return False
            if (
                not isinstance(stamp, dict)
                or stamp.get("containerId") != (info.get("Id") or ids[0])
                or stamp.get("startedAt") != (state.get("StartedAt") or "")
                or stamp.get("issuerSha256") != expected_tls["issuerSha256"]
                or stamp.get("tlsFilesSha256") != expected_tls["tlsFilesSha256"]
            ):
                return False
    return True


def check_canary_memory_budget(config, canary_bytes, ignore_existing_canaries=False):
    """Ensure active containers + Sandbox execution child reservation + canary stay within 1,600 MiB."""
    services = config.get("services") or {}
    ps = subprocess.run(["docker", "ps", "-q"], check=True, capture_output=True, text=True)
    ids = ps.stdout.split()
    other_running_bytes = 0
    sandbox_child_bytes = 0
    sandbox_supervisor_running = False
    if ids:
        inspected = json.loads(subprocess.check_output(["docker", "inspect", *ids], text=True, stderr=subprocess.DEVNULL))
        for info in inspected:
            state = info.get("State") or {}
            if not state.get("Running"):
                continue
            labels = (info.get("Config") or {}).get("Labels") or {}
            host_mem = _parse_mem_bytes((info.get("HostConfig") or {}).get("Memory") or 0)
            if labels.get("io.teamie.canary") == "true":
                if ignore_existing_canaries:
                    continue
                if host_mem <= 0:
                    canary_svc = labels.get("io.teamie.canary.service", "")
                    host_mem = _parse_mem_bytes((services.get(canary_svc) or {}).get("mem_limit", 0))
                other_running_bytes += host_mem
            elif labels.get("io.teamie.sandbox") == "true":
                sandbox_child_bytes += host_mem if host_mem > 0 else SANDBOX_CHILD_RESERVE_BYTES
            else:
                compose_svc = labels.get("com.docker.compose.service", "")
                if compose_svc == "sandbox":
                    sandbox_supervisor_running = True
                if host_mem <= 0 and compose_svc in services:
                    host_mem = _parse_mem_bytes((services.get(compose_svc) or {}).get("mem_limit", 0))
                other_running_bytes += host_mem
    reserve_sandbox = sandbox_supervisor_running or ("sandbox" in services)
    effective_sandbox_bytes = max(sandbox_child_bytes, SANDBOX_CHILD_RESERVE_BYTES if reserve_sandbox else 0)
    projected = other_running_bytes + effective_sandbox_bytes + max(0, int(canary_bytes))
    require(
        projected <= MAX_CONTAINER_BUDGET_BYTES,
        f"online canary exceeds {MAX_CONTAINER_BUDGET_BYTES // (1024 * 1024)} MiB container memory budget ({projected} bytes projected)",
    )
    return projected


def online_memory_ready(config):
    """Return True when all pending online canary cutovers fit within the 1,600 MiB budget alongside Sandbox child reservation."""
    services = config.get("services") or {}
    peak_canary_bytes = 0
    try:
        for svc in ONLINE_AB_SERVICES:
            if svc in services and not service_up_to_date(config, svc):
                peak_canary_bytes = max(peak_canary_bytes, _parse_mem_bytes((services.get(svc) or {}).get("mem_limit", 0)))
        check_canary_memory_budget(config, peak_canary_bytes, ignore_existing_canaries=True)
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
        return False
    return True


def build_canary_plan(config, service_name):
    """Build a deterministic Docker creation plan for a stateless canary slot."""
    require(service_name in ONLINE_AB_SERVICES, f"{service_name} is not eligible for dual-slot canary rollout")
    services = config.get("services") or {}
    require(service_name in services, f"unknown compose service {service_name}")
    spec = services[service_name]
    require(not spec.get("ports"), "canary slot must never publish host ports")
    require(
        not any(isinstance(v, dict) and v.get("target") == "/var/run/docker.sock" for v in (spec.get("volumes") or [])),
        "canary slot must never mount /var/run/docker.sock",
    )

    raw_nets = spec.get("networks") or ["private"]
    net_keys = list(raw_nets.keys()) if isinstance(raw_nets, dict) else [str(n) for n in raw_nets]
    require("private" in net_keys, f"{service_name} must attach to the private network")
    ordered_nets = ["private"] + [n for n in net_keys if n != "private"]
    primary_network = _compose_network_name(config, ordered_nets[0])
    extra_networks = [_compose_network_name(config, n) for n in ordered_nets[1:]]

    container_name = f"teamie-{service_name}-canary"
    alias = f"{service_name}-canary"
    create_args = [
        "--name",
        container_name,
        "--restart",
        "no",
        "--label",
        "io.teamie.canary=true",
        "--label",
        f"io.teamie.canary.service={service_name}",
        "--network",
        primary_network,
        "--network-alias",
        alias,
    ]
    if spec.get("init"):
        create_args.append("--init")
    if spec.get("read_only"):
        create_args.append("--read-only")
    if spec.get("user"):
        create_args += ["--user", str(spec["user"])]
    if spec.get("working_dir"):
        create_args += ["--workdir", str(spec["working_dir"])]
    if spec.get("mem_limit"):
        create_args += ["--memory", str(spec["mem_limit"])]
    if spec.get("memswap_limit"):
        create_args += ["--memory-swap", str(spec["memswap_limit"])]
    if spec.get("shm_size"):
        create_args += ["--shm-size", str(spec["shm_size"])]
    if spec.get("pids_limit"):
        create_args += ["--pids-limit", str(spec["pids_limit"])]
    if spec.get("cpus") is not None:
        create_args += ["--cpus", str(spec["cpus"])]
    if spec.get("cpuset"):
        create_args += ["--cpuset-cpus", str(spec["cpuset"])]
    for sec_opt in spec.get("security_opt") or []:
        create_args += ["--security-opt", str(sec_opt)]
    for cap in spec.get("cap_drop") or []:
        create_args += ["--cap-drop", str(cap)]
    for cap in spec.get("cap_add") or []:
        create_args += ["--cap-add", str(cap)]
    for tmpfs_entry in spec.get("tmpfs") or []:
        create_args += ["--tmpfs", str(tmpfs_entry)]
    for vol in spec.get("volumes") or []:
        if isinstance(vol, dict):
            vtype = vol.get("type", "bind")
            src, dst = str(vol["source"]), str(vol["target"])
            ro_suffix = ":ro" if vol.get("read_only") else ""
            if vtype == "volume":
                src = _compose_volume_name(config, src)
            create_args += ["--volume", f"{src}:{dst}{ro_suffix}"]
        elif isinstance(vol, str):
            create_args += ["--volume", vol]
    logging_cfg = spec.get("logging") or {}
    if logging_cfg.get("driver"):
        create_args += ["--log-driver", str(logging_cfg["driver"])]
    for lk, lv in sorted((logging_cfg.get("options") or {}).items()):
        create_args += ["--log-opt", f"{lk}={lv}"]

    hc = spec.get("healthcheck") or {}
    test_cmd = hc.get("test") or []
    if isinstance(test_cmd, list) and len(test_cmd) >= 2:
        hc_shell = ""
        if test_cmd[0] == "CMD":
            hc_shell = shlex.join(str(x) for x in test_cmd[1:])
        elif test_cmd[0] == "CMD-SHELL":
            hc_shell = str(test_cmd[1])
        if hc_shell:
            create_args += [
                "--health-cmd",
                hc_shell,
                "--health-interval",
                "1s",
                "--health-timeout",
                "3s",
                "--health-retries",
                "30",
                "--health-start-period",
                "5s",
            ]

    env_lines = []
    for k, v in sorted((spec.get("environment") or {}).items()):
        if v is None:
            continue
        k_str, v_str = str(k), str(v)
        require(not any(ch in k_str or ch in v_str for ch in ("\0", "\r", "\n")), f"invalid environment character in {service_name}")
        env_lines.append(f"{k_str}={v_str}\n")

    entrypoint = spec.get("entrypoint")
    if isinstance(entrypoint, list) and entrypoint:
        create_args += ["--entrypoint", str(entrypoint[0])]
        cmd_tail = [str(x) for x in entrypoint[1:]]
    elif isinstance(entrypoint, str) and entrypoint:
        create_args += ["--entrypoint", entrypoint]
        cmd_tail = []
    else:
        cmd_tail = []

    raw_cmd = spec.get("command")
    if isinstance(raw_cmd, list):
        cmd_tail += [str(x) for x in raw_cmd]
    elif isinstance(raw_cmd, str) and raw_cmd:
        cmd_tail += shlex.split(raw_cmd)

    return {
        "container_name": container_name,
        "alias": alias,
        "image": str(spec["image"]),
        "create_args": create_args,
        "command": cmd_tail,
        "extra_networks": extra_networks,
        "env_content": "".join(env_lines),
    }


def canary_launch(config, service_name):
    plan = build_canary_plan(config, service_name)
    container_name = plan["container_name"]
    subprocess.run(["docker", "rm", "-f", container_name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
    canary_bytes = _parse_mem_bytes(((config.get("services") or {}).get(service_name) or {}).get("mem_limit", 0))
    check_canary_memory_budget(config, canary_bytes, ignore_existing_canaries=False)
    fd, env_file = tempfile.mkstemp(prefix=f".teamie-canary-{service_name}-")
    created = False
    try:
        os.chmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(plan["env_content"])
            handle.flush()
        cmd = ["docker", "create"] + plan["create_args"] + ["--env-file", env_file, plan["image"]] + plan["command"]
        subprocess.run(cmd, check=True, stdout=subprocess.DEVNULL)
        created = True
    finally:
        try:
            os.unlink(env_file)
        except OSError:
            pass
    try:
        for extra_net in plan["extra_networks"]:
            subprocess.run(
                ["docker", "network", "connect", "--alias", plan["alias"], extra_net, container_name],
                check=True,
                stdout=subprocess.DEVNULL,
            )
        subprocess.run(["docker", "start", container_name], check=True, stdout=subprocess.DEVNULL)
    except Exception:
        if created:
            subprocess.run(["docker", "rm", "-f", container_name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    create = sub.add_parser("create")
    for arg in ("repository", "version", "sha", "main-sha", "digests", "flags"):
        create.add_argument("--" + arg, required=True)
    create.add_argument("--manifest-version", type=int, choices=(1, 2, 3), default=3)
    create.add_argument("--min-compatible-version")
    registry_check = sub.add_parser("registry-check")
    for command in (create, registry_check):
        for arg in ("registry-public-host", "registry-pull-host", "registry-namespace"):
            command.add_argument("--" + arg, required=command is registry_check)
    for command in ("registry-host", "registry-login"):
        registry = sub.add_parser(command)
        registry.add_argument("manifest")
    check = sub.add_parser("check")
    check.add_argument("manifest")
    check.add_argument("--current")
    check.add_argument("--highwater")
    check.add_argument("--rollback", action="store_true")
    online = sub.add_parser("online-compatible")
    online.add_argument("manifest")
    online.add_argument("current", nargs="?")
    up_to_date = sub.add_parser("service-up-to-date")
    up_to_date.add_argument("config")
    up_to_date.add_argument("service")
    mem_ready = sub.add_parser("online-memory-ready")
    mem_ready.add_argument("config")
    rec_tls = sub.add_parser("record-caddy-tls")
    rec_tls.add_argument("config")
    canary = sub.add_parser("canary-launch")
    canary.add_argument("config")
    canary.add_argument("service")
    env = sub.add_parser("env")
    env.add_argument("manifest")
    ensure_env = sub.add_parser("ensure-host-env")
    ensure_env.add_argument("host_env")
    ensure_env.add_argument("manifest")
    config = sub.add_parser("config")
    config.add_argument("config")
    config.add_argument("host_env")
    config.add_argument("--manifest")
    args = parser.parse_args()
    try:
        if args.command == "create":
            flags_path = Path(args.flags)
            flags = read(flags_path)
            images = {}
            for service in services_for_manifest_version(args.manifest_version):
                artifact = read(Path(args.digests) / (service + ".json"))
                require(artifact["service"] == service, "digest artifact service mismatch")
                images[service] = artifact["image"]
            data = dict(manifestVersion=args.manifest_version, version=args.version,
                        source=dict(repository=args.repository, sha=args.sha, mainSha=args.main_sha, ref="refs/heads/release/" + args.version),
                        createdAt=dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z"),
                        schema=schema_for_manifest_version(args.manifest_version), storage=dict(postgresMajor=17),
                        featureFlags=dict(revision=flags["revision"], sha256=hashlib.sha256(flags_path.read_bytes()).hexdigest()),
                        images=images)
            if args.manifest_version == 3:
                data["onlineRollout"] = True
            if args.min_compatible_version is not None:
                data["minCompatibleVersion"] = args.min_compatible_version
            supplied = (args.registry_public_host, args.registry_pull_host, args.registry_namespace)
            if any(value is not None for value in supplied):
                require(all(value is not None for value in supplied), "all three registry options must be supplied together")
                data["registry"] = validate_registry(dict(provider="acr", pushHost=args.registry_public_host,
                                                         pullHost=args.registry_pull_host, namespace=args.registry_namespace))
            validate(data)
            print(json.dumps(data, indent=2, sort_keys=True))
        elif args.command == "registry-check":
            print(json.dumps(validate_registry(dict(provider="acr", pushHost=args.registry_public_host,
                                                    pullHost=args.registry_pull_host, namespace=args.registry_namespace)), sort_keys=True))
        elif args.command == "registry-host":
            data = validate(read(args.manifest))
            print(release_registry(data)["pullHost"])
        elif args.command == "registry-login":
            data = validate(read(args.manifest))
            raw = sys.stdin.buffer.read(65537)
            require(len(raw) <= 65536, "registry credential frame exceeds limit")
            registry_login(data, json.loads(raw))
        elif args.command == "check":
            check_transition(read(args.manifest), read(args.current) if args.current else None,
                             read(args.highwater) if args.highwater else None, args.rollback)
        elif args.command == "online-compatible":
            if not args.current or not Path(args.current).is_file():
                return 1
            return 0 if online_compatible(read(args.manifest), read(args.current)) else 1
        elif args.command == "service-up-to-date":
            return 0 if service_up_to_date(read(args.config), args.service) else 1
        elif args.command == "online-memory-ready":
            return 0 if online_memory_ready(read(args.config)) else 1
        elif args.command == "record-caddy-tls":
            record_caddy_tls(read(args.config))
        elif args.command == "canary-launch":
            canary_launch(read(args.config), args.service)
        elif args.command == "env":
            print(image_env(read(args.manifest)), end="")
        elif args.command == "ensure-host-env":
            if ensure_host_env(args.host_env, read(args.manifest)):
                print("Provisioned missing teamie_marketplace role credentials in host environment.", file=sys.stderr)
        else:
            print(validate_config(read(args.config), args.host_env, read(args.manifest) if args.manifest else None))
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError) as error:
        print(f"release validation failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
