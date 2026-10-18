#!/usr/bin/env python3
"""Release metadata validation and ephemeral registry authentication."""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
from urllib.parse import unquote, urlsplit

LEGACY_SERVICES = ("identity", "chat", "realtime", "web", "caddy", "postgres", "redis")
SERVICES = LEGACY_SERVICES + ("agent-control", "agent-runtime", "llm-gateway", "sandbox", "sandbox-runner")
SCHEMA_V2 = {"identity": 3, "chat": 5, "agent_control": 1, "agent_runtime": 8, "gateway": 2}
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
    require(data["manifestVersion"] == 2, "registry metadata requires manifest version 2")
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


def validate(data):
    require(type(data.get("manifestVersion")) is int and data["manifestVersion"] in (1, 2), "unsupported manifest version")
    services = LEGACY_SERVICES if data["manifestVersion"] == 1 else SERVICES
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
    schema_keys = {"identity", "chat"} if data["manifestVersion"] == 1 else set(SCHEMA_V2)
    require(set(data["schema"]) == schema_keys, "unknown database schema")
    require(all(type(x) is int and x >= 1 for x in data["schema"].values()), "invalid schema version")
    require(data["storage"]["postgresMajor"] == 17, "unreviewed PostgreSQL storage major")
    require(re.fullmatch(r"[0-9a-f]{64}", data["featureFlags"]["sha256"]), "invalid default flags hash")
    require(bool(data["featureFlags"]["revision"]), "missing default flag revision")
    created = dt.datetime.fromisoformat(data["createdAt"].replace("Z", "+00:00"))
    require(created.tzinfo is not None, "manifest date needs a timezone")
    return data


def image_env(data):
    validate(data)
    return "".join(f"{key.upper().replace(chr(45), chr(95))}_IMAGE={value}\n" for key, value in data["images"].items())


def compatible(current, target):
    require(current["schema"] == target["schema"], "image rollback requires identical schema versions; migrations are never reversed")
    require(current["storage"] == target["storage"], "image rollback cannot change database storage major")
    require(current["featureFlags"] == target["featureFlags"], "image rollback requires matching default flag contract")


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
                    candidate.get("registry") == highwater.get("registry"),
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
    for name in ("identity", "chat", "realtime", "agent-control", "llm-gateway"):
        if name not in services:
            continue
        env = services[name]["environment"]
        require(str(env["COOKIE_SECURE"]).lower() == "true", "production cookies must be Secure")
        require(env["APP_ORIGIN"] == origin, "all service origins must match")
    db_services = {"identity": "identity", "chat": "chat"}
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
        require(len(set(credentials + tokens + [master])) == len(credentials + tokens + [master]), "database, encryption and service credentials must be independent")
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    create = sub.add_parser("create")
    for arg in ("repository", "version", "sha", "main-sha", "digests", "flags"):
        create.add_argument("--" + arg, required=True)
    create.add_argument("--manifest-version", type=int, choices=(1, 2), default=2)
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
    env = sub.add_parser("env")
    env.add_argument("manifest")
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
            for service in (LEGACY_SERVICES if args.manifest_version == 1 else SERVICES):
                artifact = read(Path(args.digests) / (service + ".json"))
                require(artifact["service"] == service, "digest artifact service mismatch")
                images[service] = artifact["image"]
            data = dict(manifestVersion=args.manifest_version, version=args.version,
                        source=dict(repository=args.repository, sha=args.sha, mainSha=args.main_sha, ref="refs/heads/release/" + args.version),
                        createdAt=dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z"),
                        schema=({"identity": 1, "chat": 1} if args.manifest_version == 1 else SCHEMA_V2), storage=dict(postgresMajor=17),
                        featureFlags=dict(revision=flags["revision"], sha256=hashlib.sha256(flags_path.read_bytes()).hexdigest()),
                        images=images)
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
        elif args.command == "env":
            print(image_env(read(args.manifest)), end="")
        else:
            print(validate_config(read(args.config), args.host_env, read(args.manifest) if args.manifest else None))
    except (ValueError, KeyError, TypeError, OSError) as error:
        print(f"release validation failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
