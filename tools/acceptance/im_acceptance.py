#!/usr/bin/env python3
"""Exercise real HTTP/RPC/database/WS paths against a disposable CI instance.

Never point this account-creating check at an existing production dataset without
explicitly choosing to keep its acceptance accounts and workspaces.
"""
from __future__ import annotations

import base64
import concurrent.futures
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import socket
import ssl
import struct
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


ORIGIN = os.environ.get("TEAMIE_ACCEPTANCE_URL", "http://127.0.0.1:8080").rstrip("/")
IDENTITY = os.environ.get("TEAMIE_IDENTITY_URL", ORIGIN).rstrip("/")
CHAT = os.environ.get("TEAMIE_CHAT_URL", ORIGIN).rstrip("/")
WS_URL = os.environ.get("TEAMIE_WS_URL", ORIGIN.replace("https://", "wss://").replace("http://", "ws://") + "/api/v1/ws")
INVITE = os.environ.get("REGISTRATION_CODE", "")


def require(condition: bool, description: str) -> None:
    if not condition:
        raise AssertionError(description)


class Client:
    def __init__(self) -> None:
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.cookies))
        self.csrf = ""
        self.user: dict = {}

    def call(self, method: str, path: str, body: dict | None = None, status: int | tuple[int, ...] = 200, csrf: bool = True) -> dict:
        base = IDENTITY if path.startswith("/api/v1/auth/") else CHAT
        headers = {"Origin": ORIGIN, "Accept": "application/json"}
        if method not in ("GET", "HEAD") and csrf and self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(base + path, data=data, headers=headers, method=method)
        try:
            response = self.opener.open(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read()
            result = json.loads(raw) if raw else {}
            allowed = (status,) if isinstance(status, int) else status
            require(response.status in allowed, f"{method} {path}: expected {allowed}, got {response.status}, code={result.get('error', {}).get('code', '')}")
            return result

    def register(self, name: str, suffix: str) -> None:
        self.email = f"acceptance-{name}-{suffix}@example.invalid"
        self.password = secrets.token_urlsafe(20)
        result = self.call("POST", "/api/v1/auth/register", {"email": self.email, "password": self.password, "displayName": name, "inviteCode": INVITE}, status=201)
        self.user, self.csrf = result["user"], result["csrfToken"]
        require(bool(self.user["id"]) and bool(self.csrf), "registration establishes authenticated identity and CSRF")


class WebSocket:
    """Minimal RFC6455 JSON client; no dependency or compressed extension needed."""
    def __init__(self, client: Client) -> None:
        url = urllib.parse.urlsplit(WS_URL)
        require(url.scheme in ("ws", "wss"), "valid WebSocket scheme")
        port = url.port or (443 if url.scheme == "wss" else 80)
        connection = socket.create_connection((url.hostname, port), timeout=10)
        self.sock = ssl.create_default_context().wrap_socket(connection, server_hostname=url.hostname) if url.scheme == "wss" else connection
        self.buffer = bytearray()
        key = base64.b64encode(secrets.token_bytes(16)).decode()
        cookie_request = urllib.request.Request(ORIGIN + "/api/v1/ws")
        client.cookies.add_cookie_header(cookie_request)
        cookie = cookie_request.get_header("Cookie", "")
        host = url.netloc
        path = url.path + ("?" + url.query if url.query else "")
        handshake = f"GET {path} HTTP/1.1\r\nHost: {host}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: {key}\r\nOrigin: {ORIGIN}\r\nCookie: {cookie}\r\n\r\n"
        self.sock.sendall(handshake.encode())
        while b"\r\n\r\n" not in self.buffer:
            self.buffer.extend(self.sock.recv(4096))
            require(len(self.buffer) < 32768, "bounded WS handshake")
        head, remaining = bytes(self.buffer).split(b"\r\n\r\n", 1)
        self.buffer = bytearray(remaining)
        require(head.split(b"\r\n", 1)[0].startswith(b"HTTP/1.1 101"), "authenticated WS upgrade")
        expected = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
        response_headers = {line.split(b":", 1)[0].decode().lower(): line.split(b":", 1)[1].strip().decode() for line in head.split(b"\r\n")[1:] if b":" in line}
        require(response_headers.get("sec-websocket-accept") == expected, "WS server handshake proof")

    def read(self, count: int) -> bytes:
        while len(self.buffer) < count:
            chunk = self.sock.recv(max(4096, count - len(self.buffer)))
            if not chunk:
                raise EOFError("WS closed")
            self.buffer.extend(chunk)
        result = bytes(self.buffer[:count])
        del self.buffer[:count]
        return result

    def send_frame(self, opcode: int, payload: bytes) -> None:
        require(len(payload) < 126, "small client control frame")
        mask = secrets.token_bytes(4)
        self.sock.sendall(bytes([0x80 | opcode, 0x80 | len(payload)]) + mask + bytes(value ^ mask[i % 4] for i, value in enumerate(payload)))

    def receive(self, timeout: float = 10) -> dict:
        self.sock.settimeout(timeout)
        while True:
            first, second = self.read(2)
            opcode, length = first & 15, second & 127
            if length == 126:
                length = struct.unpack("!H", self.read(2))[0]
            elif length == 127:
                length = struct.unpack("!Q", self.read(8))[0]
            require(length <= 65536, "bounded WS event")
            mask = self.read(4) if second & 128 else b""
            payload = self.read(length)
            if mask:
                payload = bytes(value ^ mask[i % 4] for i, value in enumerate(payload))
            if opcode == 9:
                self.send_frame(10, payload)
                continue
            if opcode == 8:
                raise EOFError("WS close frame")
            if opcode == 1:
                return json.loads(payload)

    def close(self) -> None:
        try:
            self.send_frame(8, b"")
        except OSError:
            pass
        self.sock.close()


def drain(client: Client, cursor: str = "0") -> tuple[list[dict], str]:
    events: list[dict] = []
    for _ in range(100):
        page = client.call("GET", f"/api/v1/sync?cursor={urllib.parse.quote(str(cursor))}&limit=2")
        events.extend(page["events"])
        new_cursor = str(page["cursor"])
        if not page["hasMore"]:
            return events, new_cursor
        require(new_cursor != str(cursor), "sync pagination advances")
        cursor = new_cursor
    raise AssertionError("sync did not drain within bounded pages")


def main() -> None:
    require(bool(INVITE), "REGISTRATION_CODE must be provided")
    for base in (ORIGIN, IDENTITY, CHAT):
        parsed = urllib.parse.urlsplit(base)
        require(parsed.scheme == "https" or parsed.hostname in ("127.0.0.1", "localhost", "::1"), "plain HTTP is allowed only for disposable loopback CI")
    suffix = uuid.uuid4().hex[:10]
    owner, member, outsider = Client(), Client(), Client()
    receipt: dict = {"origin": ORIGIN, "accounts": [], "workspaces": []}

    def record() -> None:
        destination = os.environ.get("TEAMIE_ACCEPTANCE_RECEIPT")
        if destination:
            path = Path(destination)
            with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "w") as output:
                json.dump(receipt, output, indent=2)
            path.chmod(0o600)

    for client, name in ((owner, "Owner"), (member, "Member"), (outsider, "Outsider")):
        client.register(name, suffix)
        receipt["accounts"].append({"id": client.user["id"], "email": client.email, "password": client.password, "displayName": name})
        record()
    owner.call("POST", "/api/v1/auth/logout", status=403, csrf=False)
    require(owner.call("GET", "/api/v1/auth/session")["user"]["id"] == owner.user["id"], "CSRF rejection preserves session")
    print("PASS invitation registration, session and CSRF")

    created = owner.call("POST", "/api/v1/workspaces", {"name": "Acceptance " + suffix}, status=201)
    workspace, channel = created["workspace"], created["conversation"]
    wid, cid = workspace["id"], channel["id"]
    receipt["workspaces"].append({"id": wid, "ownerId": owner.user["id"], "name": workspace["name"]})
    record()
    require(channel["type"] == "channel" and channel["name"] == "general", "atomic default channel")
    invitation = owner.call("POST", f"/api/v1/workspaces/{wid}/invites", status=(200, 201))
    require(Client().call("GET", "/api/v1/invites/" + invitation["code"])["workspace"]["id"] == wid, "public invite metadata")
    for _ in range(2):
        member.call("POST", "/api/v1/invites/" + invitation["code"] + "/accept", status=(200, 201))
    outsider.call("GET", f"/api/v1/conversations/{cid}/messages", status=(403, 404))
    member.call("POST", f"/api/v1/workspaces/{wid}/conversations", {"type": "channel", "name": "forbidden"}, status=403)
    bootstrap = owner.call("GET", "/api/v1/bootstrap")
    for key in ("im.attachments.upload", "im.reactions.create", "im.threads.create"):
        require(not bootstrap["capabilities"].get(key, False), "unsupported feature remains unavailable")
    print("PASS workspace invite admission and authorization")

    direct = owner.call("POST", f"/api/v1/workspaces/{wid}/conversations", {"type": "direct", "memberIds": [member.user["id"]]}, status=(200, 201))["conversation"]
    reverse = member.call("POST", f"/api/v1/workspaces/{wid}/conversations", {"type": "direct", "memberIds": [owner.user["id"]]}, status=(200, 201))["conversation"]
    require(direct["id"] == reverse["id"], "unordered pair reuses a direct conversation")
    group = owner.call("POST", f"/api/v1/workspaces/{wid}/conversations", {"type": "group", "name": "Small group", "memberIds": [member.user["id"]]}, status=201)["conversation"]
    require(group["type"] == "group" and set(group["memberIds"]) == {owner.user["id"], member.user["id"]}, "explicit group membership")
    outsider.call("GET", f"/api/v1/conversations/{direct['id']}/messages", status=(403, 404))
    print("PASS direct conversation uniqueness and explicit group scope")

    ws = WebSocket(member)
    require(ws.receive()["type"] == "ready", "WS ready event")
    try:
        while True:
            ws.receive(timeout=0.3)
    except socket.timeout:
        pass
    payload = {"clientMessageId": str(uuid.uuid4()), "body": "Durable message"}
    owner.call("POST", f"/api/v1/conversations/{cid}/messages", dict(payload, senderId=outsider.user["id"]), status=400)
    message = owner.call("POST", f"/api/v1/conversations/{cid}/messages", payload, status=201)["message"]
    require(message["senderId"] == owner.user["id"], "server derives sender identity")
    retry = owner.call("POST", f"/api/v1/conversations/{cid}/messages", payload, status=200)["message"]
    require(retry["id"] == message["id"] and retry["seq"] == message["seq"], "message retry is idempotent")
    owner.call("POST", f"/api/v1/conversations/{cid}/messages", dict(payload, body="conflicting retry"), status=409)
    end = time.monotonic() + 12
    while time.monotonic() < end:
        if ws.receive(timeout=max(0.1, end - time.monotonic())).get("type") == "sync.required":
            break
    else:
        raise AssertionError("committed message must produce a WS sync hint")
    ws.close()
    initial, cursor = drain(member)
    require(any(event.get("message", {}).get("id") == message["id"] for event in initial), "WS hint corresponds to durable sync")
    print("PASS authenticated WebSocket and committed durable sync")

    reply = owner.call("POST", f"/api/v1/conversations/{cid}/messages", {"clientMessageId": str(uuid.uuid4()), "body": "Reply", "replyToId": message["id"]}, status=201)["message"]
    require(reply["replyToId"] == message["id"], "reply target persisted")
    foreign = owner.call("POST", f"/api/v1/conversations/{direct['id']}/messages", {"clientMessageId": str(uuid.uuid4()), "body": "Private"}, status=201)["message"]
    owner.call("POST", f"/api/v1/conversations/{cid}/messages", {"clientMessageId": str(uuid.uuid4()), "body": "Bad reply", "replyToId": foreign["id"]}, status=(400, 404, 422))
    member.call("PATCH", f"/api/v1/conversations/{cid}/messages/{message['id']}", {"body": "Not mine"}, status=403)
    edited = owner.call("PATCH", f"/api/v1/conversations/{cid}/messages/{message['id']}", {"body": "Edited durable message"})["message"]
    require(edited["body"] == "Edited durable message" and bool(edited.get("editedAt")), "message edit persisted")
    retracted = owner.call("DELETE", f"/api/v1/conversations/{cid}/messages/{message['id']}")["message"]
    require(bool(retracted.get("retractedAt")) and not retracted.get("body"), "retraction is an empty-body tombstone")

    def send(index: int) -> dict:
        return owner.call("POST", f"/api/v1/conversations/{cid}/messages", {"clientMessageId": str(uuid.uuid4()), "body": f"Concurrent {index}"}, status=201)["message"]
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        concurrent_messages = list(pool.map(send, range(8)))
    seqs = sorted(item["seq"] for item in concurrent_messages)
    require(len(set(seqs)) == 8 and seqs == list(range(seqs[0], seqs[0] + 8)), "concurrent sends assign unique consecutive sequence numbers")
    latest = member.call("GET", f"/api/v1/conversations/{cid}/messages?limit=2")
    require(len(latest["messages"]) == 2 and latest["hasMore"], "history pagination has more")
    require([item["seq"] for item in latest["messages"]] == sorted(item["seq"] for item in latest["messages"]), "history is ascending")
    older = member.call("GET", f"/api/v1/conversations/{cid}/messages?beforeSeq={latest['messages'][0]['seq']}&limit=50")
    tombstones = [item for item in older["messages"] if item["id"] == message["id"]]
    require(len(tombstones) == 1 and not tombstones[0].get("body"), "history cannot resurrect retracted content")
    read = member.call("POST", f"/api/v1/conversations/{cid}/read", {"seq": seqs[-1]})["readSeq"]
    require(member.call("POST", f"/api/v1/conversations/{cid}/read", {"seq": 1})["readSeq"] == read, "read cursor is monotonic")
    recovered, next_cursor = drain(member, cursor)
    require(any(event.get("message", {}).get("id") == reply["id"] for event in recovered), "offline/reconnect sync recovers messages")
    require(any(event["type"] == "message.retracted" for event in recovered), "offline/reconnect sync recovers mutations")
    require(not drain(member, next_cursor)[0], "repeated sync after applied cursor has no duplicate events")
    member.call("GET", "/api/v1/sync?cursor=99999999999999999", status=(400, 409, 422))
    print("PASS replies, author-only mutations, concurrent ordering, history, read state and reconnect")

    pending = owner.call("POST", f"/api/v1/conversations/{cid}/messages", {"clientMessageId": str(uuid.uuid4()), "body": "Protected pending event"}, status=201)["message"]
    owner.call("DELETE", f"/api/v1/workspaces/{wid}/members/{member.user['id']}", status=204)
    member.call("GET", f"/api/v1/conversations/{cid}/messages", status=(403, 404))
    member.call("POST", f"/api/v1/conversations/{cid}/messages", {"clientMessageId": str(uuid.uuid4()), "body": "Forbidden"}, status=(403, 404))
    after_removal, _ = drain(member, next_cursor)
    require(not any(event.get("message", {}).get("id") == pending["id"] for event in after_removal), "removal filters already queued protected payloads")
    owner.call("POST", "/api/v1/auth/logout", status=204)
    owner.call("GET", "/api/v1/bootstrap", status=401)
    owner.call("GET", "/api/v1/auth/session", status=401)
    print("PASS membership revocation, queued-event filtering and session logout")
    print("PASS all IM acceptance checks")


if __name__ == "__main__":
    main()
