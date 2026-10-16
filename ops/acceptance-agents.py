#!/usr/bin/env python3
"""Public Agent/control-plane acceptance against a disposable loopback stack.

This harness creates fixture accounts, workspaces and Agents. It refuses remote
URLs and any initialized Identity database unless explicitly resuming this
harness's private disposable-account receipt. It never calls a real model or
runs the administrator model-test endpoint. Optional browser QA credentials
are written only to a private local receipt. The shared gateway is cleared
after disabled-only save/redaction checks.

Required: TEAMIE_AGENT_ACCEPTANCE_DISPOSABLE=1. Service URLs use the existing IM
acceptance environment plus TEAMIE_CONTROL_URL and TEAMIE_GATEWAY_URL. Browser
acceptance verifies public profile minimization and retired runtime routes.
Continuous Session and task durability are covered separately by private Go
runtime integration tests; this harness makes no execution-health claim.
"""
from __future__ import annotations

import http.cookiejar
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import stat
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


ORIGIN = os.environ.get("TEAMIE_ACCEPTANCE_URL", os.environ.get("BASE_URL", "http://127.0.0.1:8080")).rstrip("/")
IDENTITY = os.environ.get("TEAMIE_IDENTITY_URL", ORIGIN).rstrip("/")
CHAT = os.environ.get("TEAMIE_CHAT_URL", ORIGIN).rstrip("/")
CONTROL = os.environ.get("TEAMIE_CONTROL_URL", ORIGIN).rstrip("/")
GATEWAY = os.environ.get("TEAMIE_GATEWAY_URL", ORIGIN).rstrip("/")
BOOTSTRAP_INVITE = os.environ.get("BOOTSTRAP_INVITE_CODE", "teamie-bootstrap")
TIMEOUT = min(120.0, max(5.0, float(os.environ.get("TEAMIE_AGENT_ACCEPTANCE_TIMEOUT", "45"))))


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def loopback_url(value: str) -> None:
    url = urllib.parse.urlsplit(value)
    require(url.scheme in ("http", "https") and bool(url.hostname), "A loopback HTTP(S) service URL is required")
    require(not url.username and not url.password and not url.query and not url.fragment, "Service URLs cannot contain credentials, query parameters or fragments")
    host = url.hostname or ""
    if host != "localhost":
        try:
            require(ipaddress.ip_address(host).is_loopback, "Remote URLs are forbidden by this disposable-only harness")
        except ValueError as error:
            raise AssertionError("Only literal loopback IPs or localhost are permitted") from error


def base_for(path: str) -> str:
    if path.startswith("/api/v1/auth/"):
        return IDENTITY
    if path.startswith("/api/v1/admin/model-gateway"):
        return GATEWAY
    if path.startswith("/api/v1/agents") or re.match(r"^/api/v1/workspaces/[^/]+/agents(?:[/?]|$)", path):
        return CONTROL
    return CHAT


def safe_path(path: str) -> str:
    return re.sub(r"(/invites/)[^/?]+", r"\1<redacted>", path)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, target):
        raise AssertionError("Disposable service redirects are forbidden")


class Client:
    def __init__(self) -> None:
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPCookieProcessor(self.cookies))
        self.csrf = ""
        self.user: dict = {}

    def call(self, method: str, path: str, body: dict | None = None, status: int | tuple[int, ...] = 200, *, csrf: bool = True, origin: str | None = None) -> dict:
        headers = {"Origin": ORIGIN if origin is None else origin, "Accept": "application/json"}
        if method not in ("GET", "HEAD") and csrf and self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        payload = None
        if body is not None:
            payload = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(base_for(path) + path, data=payload, method=method, headers=headers)
        try:
            response = self.opener.open(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read()
            try:
                result = json.loads(raw) if raw else {}
            except json.JSONDecodeError as error:
                raise AssertionError(f"Non-JSON response for {method} {safe_path(path)}") from error
            expected = (status,) if isinstance(status, int) else status
            code = result.get("error", {}).get("code", "") if isinstance(result, dict) else ""
            require(response.status in expected, f"{method} {safe_path(path)} expected {expected}, got {response.status}, code={code}")
            if response.status < 400 and isinstance(result, dict):
                if "agent" in result:
                    public_profile(result["agent"])
                discovery = re.match(r"^/api/v1/workspaces/([^/]+)/agents(?:[/?]|$)", path)
                for profile in result.get("agents", []):
                    public_profile(profile, urllib.parse.unquote(discovery[1]) if discovery else None)
            return result

    def register(self, name: str, suffix: str, code: str) -> None:
        self.email = f"agent-acceptance-{name.lower()}-{suffix}@example.invalid"
        self.password = os.environ.get("TEAMIE_AGENT_ACCEPTANCE_PASSWORD") or secrets.token_urlsafe(24)
        result = self.call("POST", "/api/v1/auth/register", {"email": self.email, "password": self.password, "displayName": name, "inviteCode": code}, status=201)
        self.user, self.csrf = result["user"], result["csrfToken"]
        require(bool(self.user.get("id")) and bool(self.csrf), "Registration must establish a session")

    def login_fixture(self, account: dict) -> None:
        self.email, self.password = account["email"], account["password"]
        result = self.call("POST", "/api/v1/auth/login", {"email": self.email, "password": self.password})
        self.user, self.csrf = result["user"], result["csrfToken"]
        for field in ("id", "displayName", "platformRole"):
            require(self.user.get(field) == account.get(field), "Disposable fixture identity changed; refusing reuse")
        require(bool(self.csrf), "Fixture login must establish a CSRF session")


def wait_until(description: str, check, timeout: float = TIMEOUT):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(0.25)
    raise AssertionError(description + " did not become true within the bounded deadline")


def visible_agent(client: Client, workspace: str, agent: str) -> dict | None:
    return next((item for item in client.call("GET", f"/api/v1/workspaces/{workspace}/agents")["agents"] if item["id"] == agent), None)


def member_present(client: Client, workspace: str, principal: str) -> bool:
    return any(member["userId"] == principal for member in client.call("GET", f"/api/v1/workspaces/{workspace}/members")["members"])


def secret_redacted(value: dict, forbidden: str) -> None:
    require(forbidden not in json.dumps(value), "Gateway response must not expose the submitted key")
    def inspect(item):
        if isinstance(item, dict):
            for key, child in item.items():
                require(key.lower() not in {"apikey", "api_key", "encryptedapikey", "encrypted_api_key", "ciphertext", "password"}, "Gateway response must contain public metadata only")
                inspect(child)
        elif isinstance(item, list):
            for child in item:
                inspect(child)
    inspect(value)


def public_profile(profile: dict, workspace: str | None = None) -> None:
    require(set(profile) == {"id", "principalId", "ownerId", "ownerName", "name", "description", "workspaceIds", "enabled", "createdAt", "updatedAt"}, "Agent responses must contain exactly the public profile fields")
    require(profile["principalId"] == profile["id"] and bool(profile["ownerId"]) and bool(profile["ownerName"]), "Public Agent profile must retain participant and owner provenance")
    require(isinstance(profile["workspaceIds"], list) and isinstance(profile["enabled"], bool), "Agent visibility and enabled state must retain public types")
    if workspace is not None:
        require(profile["workspaceIds"] == [workspace], "Discovery must reveal only the requested workspace")


def retired_runtime_routes(client: Client, agent: str) -> None:
    base = f"/api/v1/agents/{agent}"
    routes = [("GET", suffix) for suffix in (
        "/runtime", "/runs", "/runs/private-run", "/tasks", "/tasks/private-task",
        "/events?cursor=0", "/events?cursor=-1&limit=101", "/events/stream?cursor=0",
    )]
    routes += [("POST", suffix) for suffix in (
        "/runs/private-run/cancel", "/runs/private-run/retry", "/tasks/private-task/cancel",
    )]
    for method, suffix in routes:
        result = client.call(method, base + suffix, {} if method == "POST" else None, status=404)
        require(result.get("error", {}).get("code") == "not_found", "Retired runtime routes must return only a generic 404")


def create_agent(client: Client, name: str, workspaces: list[str], enabled: bool = True) -> dict:
    return client.call("POST", "/api/v1/agents", {"name": name, "description": "Disposable acceptance participant", "workspaceIds": workspaces, "enabled": enabled}, status=201)["agent"]


def send(client: Client, conversation: str, body: str) -> dict:
    return client.call("POST", f"/api/v1/conversations/{conversation}/messages", {"clientMessageId": str(uuid.uuid4()), "body": body}, status=201)["message"]


def main() -> None:
    require(os.environ.get("TEAMIE_AGENT_ACCEPTANCE_DISPOSABLE") == "1", "Set TEAMIE_AGENT_ACCEPTANCE_DISPOSABLE=1 only for a fresh disposable local stack")
    for base in (ORIGIN, IDENTITY, CHAT, CONTROL, GATEWAY):
        loopback_url(base)
    require(bool(BOOTSTRAP_INVITE), "A disposable bootstrap invitation is required")
    suffix = uuid.uuid4().hex[:10]
    admin, owner, peer, outsider = Client(), Client(), Client(), Client()
    registered: list[Client] = []

    def record_local_qa() -> None:
        destination = os.environ.get("TEAMIE_AGENT_ACCEPTANCE_AUTH_RECEIPT")
        if not destination:
            return
        path = Path(destination)
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        descriptor = os.open(path, os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600)
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "w") as output:
            json.dump({"origin": ORIGIN, "disposableOnly": True, "accounts": [{"id": client.user["id"], "email": client.email, "password": client.password, "displayName": client.user["displayName"], "platformRole": client.user["platformRole"]} for client in registered]}, output, indent=2)

    policy = Client().call("GET", "/api/v1/auth/registration")
    reuse = os.environ.get("TEAMIE_AGENT_ACCEPTANCE_REUSE_AUTH_RECEIPT")
    if reuse:
        path = Path(reuse)
        info = path.lstat()
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and not info.st_mode & 0o077, "Fixture receipt must be a private regular file owned by the current user")
        receipt = json.loads(path.read_text())
        require(receipt.get("disposableOnly") is True and receipt.get("origin") == ORIGIN, "Only this origin's explicit disposable fixture receipt can be reused")
        require(policy == {"mode": "invite", "bootstrapRequired": False}, "Fixture reuse requires initialized invitation-only Identity")
        accounts = receipt.get("accounts", [])
        require(len(accounts) == 4 and {account.get("displayName") for account in accounts} == {"Admin", "Owner", "Peer", "Outsider"}, "Fixture receipt must contain exactly the four acceptance accounts")
        for client, name in ((admin, "Admin"), (owner, "Owner"), (peer, "Peer"), (outsider, "Outsider")):
            account = next(account for account in accounts if account["displayName"] == name)
            require(account.get("platformRole") == ("admin" if name == "Admin" else "user"), "Fixture role must match its original acceptance role")
            client.login_fixture(account)
            registered.append(client)
        print("PASS explicitly resumed private disposable account fixtures (bootstrap checks previously completed)", flush=True)
    else:
        require(policy == {"mode": "invite", "bootstrapRequired": True}, "This harness requires a fresh Identity database; refusing initialized data without an explicit private disposable receipt")
        admin.register("Admin", suffix, BOOTSTRAP_INVITE)
        registered.append(admin)
        record_local_qa()
        require(admin.user.get("platformRole") == "admin", "Bootstrap must establish a platform admin")
        for client, name in ((owner, "Owner"), (peer, "Peer"), (outsider, "Outsider")):
            invitation = admin.call("POST", "/api/v1/auth/admin/invites", {}, status=201)["invite"]
            client.register(name, suffix, invitation["code"])
            registered.append(client)
            record_local_qa()
            require(client.user.get("platformRole") == "user", "Ordinary invitations must not grant platform administration")
        print("PASS disposable invitation-only account fixtures", flush=True)

    # Saved fields are not upstream proof. The clearly fake key is never enabled
    # and this harness intentionally does not invoke the administrator test route.
    gateway_path = "/api/v1/admin/model-gateway"
    Client().call("GET", gateway_path, status=401)
    owner.call("GET", gateway_path, status=403)
    owner.call("PUT", gateway_path, {"baseUrl": "https://1.1.1.1/v1", "model": "acceptance-never-call", "enabled": False}, status=403)
    owner.call("POST", gateway_path + "/test", {}, status=403)
    initial = admin.call("GET", gateway_path)
    require(initial["status"] == "unconfigured" and not initial["config"]["hasApiKey"] and not initial["config"]["configured"], "Fresh gateway must be explicitly unconfigured")
    fake_key = "acceptance-not-a-provider-key-" + secrets.token_hex(16)
    # A public literal avoids local proxy fake-IP DNS answers. This address is
    # never requested: configuration remains disabled and /test is not called.
    settings = {"baseUrl": "https://1.1.1.1/v1", "model": "acceptance-never-call", "apiKey": fake_key, "enabled": False, "contextWindowTokens": 32768}
    admin.call("PUT", gateway_path, settings, status=403, csrf=False)
    admin.call("PUT", gateway_path, settings, status=403, origin="https://untrusted.example.invalid")
    saved = admin.call("PUT", gateway_path, settings)
    secret_redacted(saved, fake_key)
    require(saved["status"] == "disabled" and saved["config"]["configured"] and saved["config"]["hasApiKey"] and not saved["config"]["enabled"], "Disabled-only fixture save must retain credentials without enabling a provider")
    read = admin.call("GET", gateway_path)
    secret_redacted(read, fake_key)
    preserved = admin.call("PUT", gateway_path, {"baseUrl": settings["baseUrl"], "model": settings["model"], "enabled": False})
    secret_redacted(preserved, fake_key)
    require(preserved["config"]["hasApiKey"], "Omitting a key must preserve it")
    require(preserved["config"].get("contextWindowTokens") == 32768, "Omitting model capacity must preserve administrator configuration")
    cleared = admin.call("PUT", gateway_path, {"baseUrl": settings["baseUrl"], "model": settings["model"], "enabled": False, "clearApiKey": True})
    secret_redacted(cleared, fake_key)
    require(cleared["status"] == "unconfigured" and not cleared["config"]["hasApiKey"] and not cleared["config"]["configured"], "Explicit removal returns to unconfigured state")
    print("PASS gateway administrator authorization, Origin/CSRF, write-only redaction and unconfigured state (no provider call)", flush=True)

    shared = owner.call("POST", "/api/v1/workspaces", {"name": "Agent shared " + suffix}, status=201)
    wid, channel = shared["workspace"]["id"], shared["conversation"]["id"]
    invitation = owner.call("POST", f"/api/v1/workspaces/{wid}/invites", status=(200, 201))
    peer.call("POST", "/api/v1/invites/" + invitation["code"] + "/accept", status=(200, 201))
    private = outsider.call("POST", "/api/v1/workspaces", {"name": "Private outsider " + suffix}, status=201)
    private_wid = private["workspace"]["id"]
    owner_private = owner.call("POST", "/api/v1/workspaces", {"name": "Private owner " + suffix}, status=201)
    owner_private_wid = owner_private["workspace"]["id"]
    capabilities = owner.call("GET", "/api/v1/bootstrap")["capabilities"]
    for capability in ("agents.enabled", "agents.create", "agents.update", "agents.delete", "agents.discover"):
        require(capabilities.get(capability) is True, "Disposable acceptance requires enabled capability " + capability)
    created = create_agent(owner, "Private profile " + suffix, [], enabled=False)
    aid = created["id"]
    require(created["principalId"] == aid and created["ownerId"] == owner.user["id"], "Agent participant and owner identity remain public")
    require(created["workspaceIds"] == [] and not created["enabled"], "An owner-private disabled profile is allowed")
    for client in (peer, outsider, admin):
        client.call("GET", f"/api/v1/agents/{aid}", status=(403, 404))
        client.call("PATCH", f"/api/v1/agents/{aid}", {"name": "Unauthorized takeover"}, status=(403, 404))
        client.call("DELETE", f"/api/v1/agents/{aid}", status=(403, 404))
        require(not any(item["id"] == aid for item in client.call("GET", "/api/v1/agents")["agents"]), "Owned Agent listing cannot include another person's Agent")
    owner.call("POST", "/api/v1/agents", {"name": "Forbidden model setting", "workspaceIds": [], "model": "caller-selected-model"}, status=400)
    owner.call("PATCH", f"/api/v1/agents/{aid}", {"workspaceIds": [private_wid]}, status=(400, 403, 404))
    require(owner.call("GET", f"/api/v1/agents/{aid}")["agent"]["workspaceIds"] == [], "Rejected visibility must not change the profile")
    updated = owner.call("PATCH", f"/api/v1/agents/{aid}", {"name": "Team partner " + suffix, "description": "Public fixture profile", "workspaceIds": [wid, owner_private_wid], "enabled": True})["agent"]
    require(updated["name"] == "Team partner " + suffix and set(updated["workspaceIds"]) == {wid, owner_private_wid}, "Owner can update basic profile and selected visibility")
    discovered = wait_until("Workspace Agent discovery", lambda: visible_agent(peer, wid, aid))
    require(discovered["ownerId"] == owner.user["id"] and bool(discovered["ownerName"]), "Discovery must preserve owner provenance")
    require(owner_private_wid not in json.dumps(discovered), "Shared discovery must not disclose the owner's private team ID")
    wait_until("Agent IM membership", lambda: member_present(owner, wid, aid))
    for client in (outsider, admin):
        client.call("GET", f"/api/v1/workspaces/{wid}/agents", status=(403, 404))
    owner.call("GET", f"/api/v1/workspaces/{private_wid}/agents", status=(403, 404))
    print("PASS owner-only CRUD, private visibility, admin privacy and shared IM principal discovery", flush=True)

    dm = owner.call("POST", f"/api/v1/workspaces/{wid}/conversations", {"type": "direct", "memberIds": [aid]}, status=(200, 201))["conversation"]
    require(set(dm["memberIds"]) == {owner.user["id"], aid}, "Human-Agent DM uses ordinary participant IDs")
    repeat_dm = owner.call("POST", f"/api/v1/workspaces/{wid}/conversations", {"type": "direct", "memberIds": [aid]}, status=(200, 201))["conversation"]
    require(repeat_dm["id"] == dm["id"], "Human-Agent direct chat remains idempotently unique")
    peer.call("GET", f"/api/v1/conversations/{dm['id']}/messages", status=(403, 404))
    admin.call("GET", f"/api/v1/conversations/{dm['id']}/messages", status=(403, 404))
    group = owner.call("POST", f"/api/v1/workspaces/{wid}/conversations", {"type": "group", "name": "Human Agent group", "memberIds": [peer.user["id"]]}, status=201)["conversation"]
    require(group.get("canManageMembers") is True, "Group membership management is computed for its current creator")
    peer.call("POST", f"/api/v1/conversations/{group['id']}/members", {"userId": aid}, status=403)
    joined = owner.call("POST", f"/api/v1/conversations/{group['id']}/members", {"userId": aid})["conversation"]
    require(aid in joined["memberIds"], "Agent joins a private group through the shared member endpoint")
    peer.call("DELETE", f"/api/v1/conversations/{group['id']}/members/{aid}", status=403)
    removed = owner.call("DELETE", f"/api/v1/conversations/{group['id']}/members/{aid}")["conversation"]
    require(aid not in removed["memberIds"], "Removing an Agent changes the explicit group member list")
    owner.call("POST", f"/api/v1/conversations/{group['id']}/members", {"userId": aid})
    owner.call("POST", f"/api/v1/conversations/{dm['id']}/messages", {"clientMessageId": str(uuid.uuid4()), "body": "Human cannot forge an Agent sender", "senderId": aid}, status=400)
    inbound = send(owner, dm["id"], "Disposable unconfigured-model input " + suffix)
    grouped_input = send(peer, group["id"], "A second authorized conversation shares the same Agent Session " + suffix)
    for client in (Client(), owner, admin, peer, outsider):
        retired_runtime_routes(client, aid)
    public_responses = [owner.call("GET", f"/api/v1/agents/{aid}"), owner.call("GET", "/api/v1/agents"), visible_agent(peer, wid, aid)]
    serialized = json.dumps(public_responses)
    require(inbound["body"] not in serialized and grouped_input["body"] not in serialized and "agent:" + aid not in serialized, "Public profiles must not expose cross-conversation inputs or private Session identity")
    print("PASS exact public profile fields, scoped discovery and retired runtime routes for anonymous/owner/admin/peer/outsider (no provider call)", flush=True)
    dm_messages = owner.call("GET", f"/api/v1/conversations/{dm['id']}/messages")["messages"]
    group_messages = peer.call("GET", f"/api/v1/conversations/{group['id']}/messages")["messages"]
    require(any(message["id"] == inbound["id"] for message in dm_messages) and any(message["id"] == grouped_input["id"] for message in group_messages), "Human inputs remain durable while unconfigured")
    require(not any(message["senderId"] == aid for message in dm_messages + group_messages), "Unconfigured Agent must not fabricate or auto-publish operational output")
    searched = owner.call("GET", f"/api/v1/conversations/{dm['id']}/messages?q=unconfigured-model")["messages"]
    require(any(message["id"] == inbound["id"] for message in searched), "History query searches authorized content on the server")
    nearby = owner.call("GET", f"/api/v1/conversations/{dm['id']}/messages?aroundSeq={inbound['seq']}&limit=5")["messages"]
    require(any(message["id"] == inbound["id"] for message in nearby), "Referenced-message context is available through ordinary history")
    outsider.call("GET", f"/api/v1/conversations/{dm['id']}/messages?q=unconfigured-model", status=(403, 404))
    print("PASS Agent DM/private-group membership and no fabricated unconfigured responses", flush=True)

    disabled = owner.call("PATCH", f"/api/v1/agents/{aid}", {"enabled": False})["agent"]
    require(not disabled["enabled"] and owner.call("GET", f"/api/v1/agents/{aid}")["agent"]["id"] == aid, "Disable preserves the owned profile")
    retired_runtime_routes(owner, aid)
    require(any(message["id"] == inbound["id"] for message in owner.call("GET", f"/api/v1/conversations/{dm['id']}/messages")["messages"]), "Disabled Agent does not hide persisted conversation history")
    owner.call("DELETE", f"/api/v1/agents/{aid}", status=204)
    owner.call("GET", f"/api/v1/agents/{aid}", status=404)
    require(not visible_agent(peer, wid, aid), "Deleted Agent is absent from workspace discovery")
    require(any(message["id"] == inbound["id"] for message in owner.call("GET", f"/api/v1/conversations/{dm['id']}/messages")["messages"]), "Delete preserves existing IM history")
    print("PASS disable/delete preserve existing profiles where applicable and IM history", flush=True)

    delegated = create_agent(peer, "Delegated visibility " + suffix, [wid])
    did = delegated["id"]
    wait_until("Delegated Agent admission", lambda: visible_agent(owner, wid, did) and member_present(owner, wid, did))
    owner.call("DELETE", f"/api/v1/workspaces/{wid}/members/{peer.user['id']}", status=204)
    wait_until("Owner departure revokes delegated Agent membership", lambda: not visible_agent(owner, wid, did) and not member_present(owner, wid, did))
    peer.call("PATCH", f"/api/v1/agents/{did}", {"workspaceIds": [wid]}, status=(400, 403, 404))
    peer.call("DELETE", f"/api/v1/agents/{did}", status=204)
    moderated = create_agent(owner, "Moderator removal " + suffix, [wid])
    mid = moderated["id"]
    wait_until("Moderator fixture admission", lambda: visible_agent(owner, wid, mid) and member_present(owner, wid, mid))
    owner.call("DELETE", f"/api/v1/workspaces/{wid}/members/{mid}", status=204)
    for _ in range(8):
        owner.call("GET", f"/api/v1/agents/{mid}")
        require(not visible_agent(owner, wid, mid) and not member_present(owner, wid, mid), "Background reconciliation must not undo moderator removal")
        time.sleep(0.25)
    owner.call("DELETE", f"/api/v1/agents/{mid}", status=204)
    print("PASS granting-owner departure and moderator removal revoke Agent workspace access", flush=True)
    require(admin.call("GET", gateway_path)["status"] == "unconfigured", "Harness must leave the gateway unconfigured")
    receipt = {"origin": ORIGIN, "publicProfilePrivacyChecked": True, "retiredRuntimeRoutesChecked": True, "privateRuntimeExecutionChecked": False, "realProviderRequests": 0, "accountIds": [client.user["id"] for client in (admin, owner, peer, outsider)], "workspaceIds": [wid, private_wid, owner_private_wid], "agentIds": [aid, did, mid]}
    destination = os.environ.get("TEAMIE_AGENT_ACCEPTANCE_RECEIPT")
    if destination:
        path = Path(destination)
        with os.fdopen(os.open(path, os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600), "w") as output:
            json.dump(receipt, output, indent=2)
        path.chmod(0o600)
    print("PASS all public Agent control-plane acceptance checks; private execution is verified separately", flush=True)


if __name__ == "__main__":
    main()
