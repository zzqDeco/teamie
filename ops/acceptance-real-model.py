#!/usr/bin/env python3
"""Opt-in live model provider & Tavily web search acceptance harness.

In accordance with AGENTS.md, CI and disposable acceptance never call a live
external LLM provider automatically. This script is strictly opt-in and runs
only when the operator explicitly supplies real provider credentials in the
environment (`TEAMIE_REAL_MODEL_ACCEPTANCE=1`).

Security invariants:
- Never hardcodes, logs, prints, or persists provider API keys, base URLs,
  passwords, or raw prompts/model outputs.
- Verifies full Eino v0.10.0-alpha.35 runtime execution end-to-end through the
  public IM & Control Plane HTTP contracts:
  1. Model Gateway connectivity (`PUT /api/v1/admin/model-gateway` + `/test`)
  2. Foreground `@Agent` channel turn & `im_send_message` reply
  3. Sandbox tool execution (`bash` / `write_file`) & attachment export
  4. Optional Tavily `web_search` / `web_fetch` connectivity & turn verification
  5. `schedule_create` one-shot self-wakeup & follow-up IM message
"""
from __future__ import annotations

import http.cookiejar
import json
import os
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


ORIGIN = os.environ.get(
    "TEAMIE_ACCEPTANCE_URL",
    os.environ.get("BASE_URL", "http://127.0.0.1:8080"),
).rstrip("/")
IDENTITY = os.environ.get("TEAMIE_IDENTITY_URL", ORIGIN).rstrip("/")
CHAT = os.environ.get("TEAMIE_CHAT_URL", ORIGIN).rstrip("/")
CONTROL = os.environ.get("TEAMIE_CONTROL_URL", ORIGIN).rstrip("/")
GATEWAY = os.environ.get("TEAMIE_GATEWAY_URL", ORIGIN).rstrip("/")
TIMEOUT = min(
    180.0,
    max(15.0, float(os.environ.get("TEAMIE_REAL_MODEL_TIMEOUT", "90"))),
)


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def base_for(path: str) -> str:
    if path.startswith("/api/v1/auth/"):
        return IDENTITY
    if path.startswith("/api/v1/admin/model-gateway"):
        return GATEWAY
    if path.startswith("/api/v1/agents") or "/agents" in path:
        return CONTROL
    return CHAT


class Client:
    def __init__(self) -> None:
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.cookies)
        )
        self.csrf = ""
        self.user: dict = {}

    def call(
        self,
        method: str,
        path: str,
        body: dict | None = None,
        status: int | tuple[int, ...] = 200,
    ) -> dict:
        headers = {
            "Origin": ORIGIN,
            "Accept": "application/json",
        }
        if method not in ("GET", "HEAD") and self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        payload = None
        if body is not None:
            payload = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(
            base_for(path) + path,
            data=payload,
            method=method,
            headers=headers,
        )
        try:
            resp = self.opener.open(req, timeout=30)
        except urllib.error.HTTPError as err:
            resp = err
        with resp:
            raw = resp.read()
            result = json.loads(raw) if raw else {}
            expected = (status,) if isinstance(status, int) else status
            code = (
                result.get("error", {}).get("code", "")
                if isinstance(result, dict)
                else ""
            )
            require(
                resp.status in expected,
                f"{method} {path} expected {expected}, got {resp.status} (code={code})",
            )
            return result

    def get_bytes(self, path: str, status: int = 200) -> bytes:
        req = urllib.request.Request(
            base_for(path) + path,
            method="GET",
            headers={"Origin": ORIGIN},
        )
        try:
            resp = self.opener.open(req, timeout=30)
        except urllib.error.HTTPError as err:
            resp = err
        with resp:
            require(
                resp.status == status,
                f"GET {path} expected {status}, got {resp.status}",
            )
            return resp.read()


def wait_until(description: str, check, timeout: float = TIMEOUT):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(1.0)
    raise AssertionError(f"{description} did not complete within {timeout:.0f}s")


def main() -> int:
    require(
        os.environ.get("TEAMIE_REAL_MODEL_ACCEPTANCE") == "1",
        "Set TEAMIE_REAL_MODEL_ACCEPTANCE=1 to run live model acceptance.",
    )
    provider_base_url = os.environ.get("TEAMIE_REAL_PROVIDER_BASE_URL", "").strip()
    provider_api_key = os.environ.get("TEAMIE_REAL_PROVIDER_API_KEY", "").strip()
    provider_model = os.environ.get("TEAMIE_REAL_PROVIDER_MODEL", "").strip()
    web_search_key = os.environ.get("TEAMIE_REAL_WEB_SEARCH_API_KEY", "").strip()
    admin_email = os.environ.get("TEAMIE_ADMIN_EMAIL", "").strip()
    admin_password = os.environ.get("TEAMIE_ADMIN_PASSWORD", "").strip()

    require(
        bool(provider_base_url and provider_api_key and provider_model),
        "TEAMIE_REAL_PROVIDER_BASE_URL, TEAMIE_REAL_PROVIDER_API_KEY, and TEAMIE_REAL_PROVIDER_MODEL are required.",
    )
    require(
        bool(admin_email and admin_password),
        "TEAMIE_ADMIN_EMAIL and TEAMIE_ADMIN_PASSWORD are required.",
    )

    admin = Client()
    login_res = admin.call(
        "POST",
        "/api/v1/auth/login",
        {"email": admin_email, "password": admin_password},
        status=200,
    )
    admin.user = login_res["user"]
    admin.csrf = login_res["csrfToken"]
    require(
        admin.user.get("platformRole") == "admin",
        "Authenticated account must hold platformRole=admin.",
    )

    run_suffix = secrets.token_hex(4)
    print(f"[1/5] Configuring model gateway and running live connectivity test (run={run_suffix})...")
    gw_payload: dict = {
        "enabled": True,
        "baseUrl": provider_base_url,
        "apiKey": provider_api_key,
        "model": provider_model,
        "timeoutSeconds": 60,
        "maxTokens": 1024,
        "webSearchEnabled": bool(web_search_key),
    }
    if web_search_key:
        gw_payload["webSearchApiKey"] = web_search_key

    gw_res = admin.call("PUT", "/api/v1/admin/model-gateway", gw_payload, status=200)
    require(gw_res["settings"]["enabled"] is True, "Model gateway must be enabled.")
    require(gw_res["settings"]["hasApiKey"] is True, "Model gateway API key must be stored.")

    test_res = admin.call("POST", "/api/v1/admin/model-gateway/test", {}, status=200)
    require(test_res.get("ok") is True, "Model gateway live connectivity test failed.")

    if web_search_key:
        ws_test = admin.call(
            "POST",
            "/api/v1/admin/model-gateway/web-search-test",
            {},
            status=200,
        )
        require(ws_test.get("ok") is True, "Tavily web search connectivity test failed.")

    print("[2/5] Creating isolated workspace, channel, and personal Agent...")
    ws_res = admin.call(
        "POST",
        "/api/v1/workspaces",
        {"name": f"Live-Accept-{run_suffix}"},
        status=201,
    )
    workspace_id = ws_res["workspace"]["id"]
    bootstrap = admin.call("GET", "/api/v1/bootstrap", status=200)
    channel = next(
        (
            c
            for c in bootstrap.get("conversations", [])
            if c.get("workspaceId") == workspace_id and c.get("type") == "channel"
        ),
        None,
    )
    require(channel is not None, "Default workspace channel not found.")
    channel_id = channel["id"]

    agent_name = f"LiveBot-{run_suffix}"
    agent_res = admin.call(
        "POST",
        "/api/v1/agents",
        {
            "displayName": agent_name,
            "description": "Live model acceptance assistant",
            "prompt": (
                "You are a concise acceptance test assistant in Teamie. "
                "Always use the im_send_message tool to reply in the current conversation."
            ),
            "workspaceIds": [workspace_id],
            "enabled": True,
        },
        status=201,
    )
    agent_id = agent_res["agent"]["id"]

    def post_mention(text: str) -> dict:
        mention_prefix = f"@{agent_name} "
        full_body = mention_prefix + text
        return admin.call(
            "POST",
            f"/api/v1/conversations/{urllib.parse.quote(channel_id)}/messages",
            {
                "clientMessageId": str(uuid.uuid4()),
                "body": full_body,
                "mentions": [
                    {
                        "userId": agent_id,
                        "start": 0,
                        "end": len(f"@{agent_name}"),
                    }
                ],
            },
            status=(200, 201),
        )["message"]

    def latest_agent_messages(after_seq: int) -> list[dict]:
        page = admin.call(
            "GET",
            f"/api/v1/conversations/{urllib.parse.quote(channel_id)}/messages",
            status=200,
        )
        return [
            m
            for m in page.get("messages", [])
            if m.get("senderId") == agent_id and int(m.get("seq", 0)) > after_seq
        ]

    print("[3/5] Verifying foreground @Agent channel turn and im_send_message reply...")
    msg1 = post_mention(f"Please reply in this channel with the exact token ACK-{run_suffix}.")
    reply1 = wait_until(
        "Agent channel reply",
        lambda: next(
            (
                m
                for m in latest_agent_messages(int(msg1["seq"]))
                if f"ACK-{run_suffix}" in m.get("body", "")
            ),
            None,
        ),
    )
    require(bool(reply1.get("id")), "Agent reply message ID must be present.")

    print("[4/5] Verifying Sandbox file creation and IM attachment export...")
    msg2 = post_mention(
        f"Create a file named report-{run_suffix}.txt in your workspace containing "
        f"'sandbox-ok-{run_suffix}' and send it to this conversation as an attachment using im_send_message."
    )
    reply2 = wait_until(
        "Agent Sandbox attachment export",
        lambda: next(
            (
                m
                for m in latest_agent_messages(int(msg2["seq"]))
                if m.get("attachments")
            ),
            None,
        ),
    )
    att = reply2["attachments"][0]
    downloaded = admin.get_bytes(
        f"/api/v1/conversations/{urllib.parse.quote(channel_id)}/attachments/{urllib.parse.quote(att['id'])}",
        status=200,
    )
    require(
        f"sandbox-ok-{run_suffix}".encode("utf-8") in downloaded,
        "Exported attachment content did not match expected Sandbox file output.",
    )

    if web_search_key:
        print("[4b/5] Verifying live web_search / web_fetch tool execution...")
        msg_ws = post_mention(
            f"Use web_search or web_fetch to look up 'IANA example domain' and reply with WEB-{run_suffix}."
        )
        wait_until(
            "Agent web search reply",
            lambda: next(
                (
                    m
                    for m in latest_agent_messages(int(msg_ws["seq"]))
                    if f"WEB-{run_suffix}" in m.get("body", "")
                ),
                None,
            ),
        )

    print("[5/5] Verifying schedule_create self-wakeup and follow-up IM message...")
    msg3 = post_mention(
        f"Create a one-shot schedule 5 seconds from now using schedule_create that wakes you up "
        f"to send a message containing WAKE-{run_suffix} in this conversation."
    )
    wait_until(
        "Agent scheduled wakeup message",
        lambda: next(
            (
                m
                for m in latest_agent_messages(int(msg3["seq"]))
                if f"WAKE-{run_suffix}" in m.get("body", "")
            ),
            None,
        ),
        timeout=max(TIMEOUT, 90.0),
    )

    print("Live model provider acceptance passed all checks.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
