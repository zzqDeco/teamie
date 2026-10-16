#!/usr/bin/env python3
"""Release metadata validation. Only writes non-secret image references to stdout."""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import sys
from urllib.parse import unquote, urlsplit

LEGACY_SERVICES = ("identity", "chat", "realtime", "web", "caddy", "postgres", "redis")
SERVICES = LEGACY_SERVICES + ("agent-control", "agent-runtime", "llm-gateway", "sandbox", "sandbox-runner")
SCHEMA_V2 = {"identity": 2, "chat": 2, "agent_control": 1, "agent_runtime": 1, "gateway": 1}
VERSION = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
SHA = re.compile(r"[0-9a-f]{40}$")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}$")


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
    prefix = "ghcr.io/" + source["repository"].lower() + "-"
    for service, image in data["images"].items():
        require(image.startswith(prefix + service + "@"), f"unexpected {service} image repository")
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
            require(candidate["source"] == highwater["source"] and candidate["images"] == highwater["images"],
                    "an already deployed release is immutable; create a new version")
    if current:
        validate(current)
        require(all(candidate["schema"].get(s, 0) >= current["schema"][s] for s in current["schema"]), "forward deployment cannot lower database schema")
        require(candidate["storage"] == current["storage"], "database major upgrades need a separate reviewed procedure")


def validate_config(config, host_env):
    require((Path(host_env).stat().st_mode & 0o777) == 0o600, "production.env must have mode 0600")
    services = config["services"]
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
        require(re.fullmatch(r"ghcr\.io/zzqdeco/teamie-sandbox-runner@sha256:[0-9a-f]{64}", sandbox["SANDBOX_RUNNER_IMAGE"]), "sandbox runner must be the immutable platform image")
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
            validate(data)
            print(json.dumps(data, indent=2, sort_keys=True))
        elif args.command == "check":
            check_transition(read(args.manifest), read(args.current) if args.current else None,
                             read(args.highwater) if args.highwater else None, args.rollback)
        elif args.command == "env":
            print(image_env(read(args.manifest)), end="")
        else:
            print(validate_config(read(args.config), args.host_env))
    except (ValueError, KeyError, TypeError, OSError) as error:
        print(f"release validation failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
