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

SERVICES = ("identity", "chat", "realtime", "web", "caddy", "postgres", "redis")
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
    require(data.get("manifestVersion") == 1, "unsupported manifest version")
    version(data["version"])
    source = data["source"]
    require(source["repository"].lower() == "zzqdeco/teamie", "unexpected source repository")
    require(SHA.fullmatch(source["sha"]), "invalid source SHA")
    require(source["sha"] == source["mainSha"], "release source must match checked main")
    require(source["ref"] == "refs/heads/release/" + data["version"], "release ref/version mismatch")
    require(set(data["images"]) == set(SERVICES), "exactly seven images are required")
    prefix = "ghcr.io/" + source["repository"].lower() + "-"
    for service, image in data["images"].items():
        require(image.startswith(prefix + service + "@"), f"unexpected {service} image repository")
        require(DIGEST.fullmatch(image.split("@", 1)[1]), f"{service} must use an immutable digest")
    require(set(data["schema"]) == {"identity", "chat"}, "unknown database schema")
    require(all(type(x) is int and x >= 1 for x in data["schema"].values()), "invalid schema version")
    require(data["storage"]["postgresMajor"] == 17, "unreviewed PostgreSQL storage major")
    require(re.fullmatch(r"[0-9a-f]{64}", data["featureFlags"]["sha256"]), "invalid default flags hash")
    require(bool(data["featureFlags"]["revision"]), "missing default flag revision")
    created = dt.datetime.fromisoformat(data["createdAt"].replace("Z", "+00:00"))
    require(created.tzinfo is not None, "manifest date needs a timezone")
    return data


def image_env(data):
    validate(data)
    return "".join(f"{key.upper()}_IMAGE={data['images'][key]}\n" for key in SERVICES)


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
        require(all(candidate["schema"][s] >= current["schema"][s] for s in current["schema"]), "forward deployment cannot lower database schema")
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
    pg_env = services["postgres"]["environment"]
    for name in ("identity", "chat", "realtime"):
        env = services[name]["environment"]
        require(str(env["COOKIE_SECURE"]).lower() == "true", "production cookies must be Secure")
        require(env["APP_ORIGIN"] == origin, "all service origins must match")
    for name in ("identity", "chat"):
        db = urlsplit(services[name]["environment"]["DATABASE_URL"])
        require(db.scheme in ("postgres", "postgresql") and db.hostname == "postgres" and db.port == 5432,
                f"{name} must use private PostgreSQL")
        require(db.username == "teamie_" + name and db.path == "/teamie_" + name, f"{name} must own a separate role/database")
        require(unquote(db.password or "") == pg_env["POSTGRES_" + name.upper() + "_PASSWORD"], f"{name} initialization/runtime passwords disagree")
    credentials = [pg_env[k] for k in ("POSTGRES_PASSWORD", "POSTGRES_IDENTITY_PASSWORD", "POSTGRES_CHAT_PASSWORD")]
    credentials.append(services["identity"]["environment"]["REGISTRATION_CODE"])
    require(all(len(value) >= 24 and "REPLACE" not in value for value in credentials), "replace every credential with a random value of at least 24 characters")
    require(len(set(credentials)) == len(credentials), "database and registration credentials must be independent")
    for name, service in services.items():
        require(name == "caddy" or not service.get("ports"), "only Caddy may publish host ports")
    return origin


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    create = sub.add_parser("create")
    for arg in ("repository", "version", "sha", "main-sha", "digests", "flags"):
        create.add_argument("--" + arg, required=True)
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
            for service in SERVICES:
                artifact = read(Path(args.digests) / (service + ".json"))
                require(artifact["service"] == service, "digest artifact service mismatch")
                images[service] = artifact["image"]
            data = dict(manifestVersion=1, version=args.version,
                        source=dict(repository=args.repository, sha=args.sha, mainSha=args.main_sha, ref="refs/heads/release/" + args.version),
                        createdAt=dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z"),
                        schema=dict(identity=1, chat=1), storage=dict(postgresMajor=17),
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
