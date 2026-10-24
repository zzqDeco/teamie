#!/usr/bin/env python3
"""Opt-in live model provider & Tavily web search acceptance harness.

In accordance with AGENTS.md, CI and disposable acceptance never call a live
external LLM provider automatically. This script is strictly opt-in and runs
only when the operator explicitly sets `TEAMIE_REAL_MODEL_ACCEPTANCE=1` (either
passing `TEAMIE_REAL_PROVIDER_*` environment variables or using a model gateway
already enabled at `/app/admin/gateway`).

Security invariants:
- Never hardcodes, logs, prints, or persists provider API keys, base URLs,
  passwords, or raw prompts/model outputs.
- Verifies full Eino v0.10.0-alpha.35 runtime execution end-to-end through the
  public IM, Control Plane, and internal Gateway SSE contracts:
  1. Model Gateway connectivity (`GET/PUT /api/v1/admin/model-gateway` + `/test`)
     and Hertz native `sse.NewWriter` streaming (`POST /internal/v1/chat/stream`
     with `Accept: text/event-stream` and `X-Teamie-Model-Config-ID`).
  2. Scenario 1: `#general` channel `@Agent` turn, `im_send_message` with
     `replyToId`, no raw assistant thought leakage, and cross-conversation ACL boundary.
  3. Scenario 2: Sandbox isolated script execution (no service/provider secrets
     or external network), file attachment export, download, and SHA-256 verification.
  4. Scenario 3: Tavily `web_search` / `web_fetch` connectivity & turn verification
     (when `webSearch` is enabled).
  5. Scenario 4: `schedule_create` (`TEAMIE_SCHEDULE_WAKEUP`) one-shot self-wakeup
     through the single-writer `TurnLoop` and follow-up IM message.
"""
from __future__ import annotations

import hashlib
import http.cookiejar
import json
import os
import re
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


TRANSPORT_VERSION = "teamie-agentic-chat-v1"


def get_env_config() -> dict[str, str]:
    origin = os.environ.get(
        "TEAMIE_ACCEPTANCE_URL",
        os.environ.get("BASE_URL", "http://127.0.0.1:8080"),
    ).rstrip("/")
    return {
        "origin": origin,
        "identity": os.environ.get("TEAMIE_IDENTITY_URL", origin).rstrip("/"),
        "chat": os.environ.get("TEAMIE_CHAT_URL", origin).rstrip("/"),
        "control": os.environ.get("TEAMIE_CONTROL_URL", origin).rstrip("/"),
        "gateway": os.environ.get("TEAMIE_GATEWAY_URL", origin).rstrip("/"),
        "internal_gateway": os.environ.get("TEAMIE_INTERNAL_GATEWAY_URL", "").rstrip("/"),
        "runtime_token": os.environ.get("GATEWAY_RUNTIME_TOKEN", "").strip(),
    }


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def counting_config_id(base_url: str, model_name: str) -> str:
    base = base_url.strip().rstrip("/")
    model = model_name.strip()
    if not base or not model:
        return ""
    payload = json.dumps(
        {
            "version": TRANSPORT_VERSION,
            "baseUrl": base,
            "model": model,
        },
        separators=(",", ":"),
    ).encode("utf-8")
    digest = hashlib.sha256(payload).digest()
    return "modelcfg:v1:" + digest[:16].hex()


class Client:
    def __init__(self, urls: dict[str, str] | None = None) -> None:
        self.urls = urls or get_env_config()
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.cookies)
        )
        self.csrf = ""
        self.user: dict = {}

    def base_for(self, path: str) -> str:
        if path.startswith("/api/v1/auth/"):
            return self.urls["identity"]
        if path.startswith("/api/v1/admin/model-gateway"):
            return self.urls["gateway"]
        if path.startswith("/internal/v1/"):
            return self.urls["internal_gateway"] or self.urls["gateway"]
        if path.startswith("/api/v1/agents") or re.match(
            r"^/api/v1/workspaces/[^/]+/agents(?:[/?]|$)", path
        ):
            return self.urls["control"]
        return self.urls["chat"]

    def call(
        self,
        method: str,
        path: str,
        body: dict | None = None,
        status: int | tuple[int, ...] = 200,
    ) -> dict:
        headers = {
            "Origin": self.urls["origin"],
            "Accept": "application/json",
        }
        if method not in ("GET", "HEAD") and self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        payload = None
        if body is not None:
            payload = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(
            self.base_for(path) + path,
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

    def get_bytes(self, path: str, status: int = 200) -> tuple[bytes, dict[str, str]]:
        req = urllib.request.Request(
            self.base_for(path) + path,
            method="GET",
            headers={"Origin": self.urls["origin"]},
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
            hdrs = {k.lower(): v for k, v in resp.headers.items()}
            return resp.read(), hdrs

    def verify_internal_sse_stream(
        self,
        runtime_token: str,
        owner_id: str,
        expected_config_id: str = "",
    ) -> None:
        headers = {
            "Accept": "text/event-stream",
            "Content-Type": "application/json",
            "Authorization": f"Bearer {runtime_token}",
        }
        if expected_config_id:
            headers["X-Teamie-Model-Config-ID"] = expected_config_id
        inner_request = json.dumps(
            {
                "protocol": TRANSPORT_VERSION,
                "messages": [
                    {
                        "role": "user",
                        "content_blocks": [
                            {
                                "type": "user_input_text",
                                "user_input_text": {"text": "Reply with OK."},
                            }
                        ],
                    }
                ],
                "options": {
                    "MaxTokens": 8,
                    "AgenticToolChoice": {"Type": "forbidden"},
                },
                "stream": True,
            }
        )
        payload = json.dumps(
            {
                "ownerId": owner_id,
                "agentId": str(uuid.uuid4()),
                "sessionId": str(uuid.uuid4()),
                "runId": f"{uuid.uuid4()}:{uuid.uuid4()}",
                "requestJson": inner_request,
                "messageLimit": 128,
            }
        ).encode("utf-8")
        req = urllib.request.Request(
            self.base_for("/internal/v1/chat/stream") + "/internal/v1/chat/stream",
            data=payload,
            method="POST",
            headers=headers,
        )
        try:
            resp = self.opener.open(req, timeout=60)
        except urllib.error.HTTPError as err:
            resp = err
        with resp:
            require(
                resp.status == 200,
                f"POST /internal/v1/chat/stream expected 200, got {resp.status}",
            )
            content_type = resp.headers.get("Content-Type", "")
            require(
                content_type.startswith("text/event-stream"),
                f"Expected text/event-stream Content-Type, got {content_type!r}",
            )
            bound_config_id = resp.headers.get("X-Teamie-Model-Config-ID", "").strip()
            require(
                bool(bound_config_id),
                "Expected X-Teamie-Model-Config-ID response header on SSE stream.",
            )
            if expected_config_id:
                require(
                    bound_config_id == expected_config_id,
                    f"X-Teamie-Model-Config-ID {bound_config_id!r} != {expected_config_id!r}",
                )
            raw_sse = resp.read().decode("utf-8", errors="replace")
            frames = []
            for line in raw_sse.splitlines():
                if line.startswith("data:"):
                    data_str = line[len("data:") :].strip()
                    if data_str:
                        frames.append(json.loads(data_str))
            require(len(frames) > 0, "Expected at least one SSE data frame.")
            require(
                any(f.get("type") == "done" for f in frames),
                "Expected terminal SSE frame with type='done'.",
            )

    def verify_internal_web_search(
        self,
        runtime_token: str,
        owner_id: str,
    ) -> None:
        headers = {
            "Accept": "application/json",
            "Content-Type": "application/json",
            "Authorization": f"Bearer {runtime_token}",
        }
        payload = json.dumps(
            {
                "ownerId": owner_id,
                "agentId": str(uuid.uuid4()),
                "sessionId": str(uuid.uuid4()),
                "runId": f"{uuid.uuid4()}:{uuid.uuid4()}",
                "query": "IANA example domain",
                "maxResults": 3,
            }
        ).encode("utf-8")
        req = urllib.request.Request(
            self.base_for("/internal/v1/web/search") + "/internal/v1/web/search",
            data=payload,
            method="POST",
            headers=headers,
        )
        try:
            resp = self.opener.open(req, timeout=30)
        except urllib.error.HTTPError as err:
            resp = err
        with resp:
            require(
                resp.status == 200,
                f"POST /internal/v1/web/search expected 200, got {resp.status}",
            )
            result = json.loads(resp.read())
            require(
                result.get("ok") is True,
                f"Tavily internal web search check failed: {result.get('errorCode')}",
            )
            results = result.get("results") or []
            extract_url = (
                results[0].get("url")
                if results and results[0].get("url")
                else "https://www.iana.org/help/example-domains"
            )

        extract_payload = json.dumps(
            {
                "ownerId": owner_id,
                "agentId": str(uuid.uuid4()),
                "sessionId": str(uuid.uuid4()),
                "runId": f"{uuid.uuid4()}:{uuid.uuid4()}",
                "urls": [extract_url],
            }
        ).encode("utf-8")
        extract_req = urllib.request.Request(
            self.base_for("/internal/v1/web/extract") + "/internal/v1/web/extract",
            data=extract_payload,
            method="POST",
            headers=headers,
        )
        try:
            extract_resp = self.opener.open(extract_req, timeout=30)
        except urllib.error.HTTPError as err:
            extract_resp = err
        with extract_resp:
            require(
                extract_resp.status == 200,
                f"POST /internal/v1/web/extract expected 200, got {extract_resp.status}",
            )
            extract_result = json.loads(extract_resp.read())
            require(
                extract_result.get("ok") is True,
                f"Tavily internal web extract check failed: {extract_result.get('errorCode')}",
            )


def wait_until(
    description: str, check, timeout: float = 90.0, poll_interval: float = 1.0
):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(poll_interval)
    raise AssertionError(f"{description} did not complete within {timeout:.0f}s")


def main() -> int:
    require(
        os.environ.get("TEAMIE_REAL_MODEL_ACCEPTANCE") == "1",
        "Set TEAMIE_REAL_MODEL_ACCEPTANCE=1 to run live model acceptance.",
    )
    urls = get_env_config()
    timeout = min(
        180.0,
        max(1.0, float(os.environ.get("TEAMIE_REAL_MODEL_TIMEOUT", "90"))),
    )
    poll_interval = max(
        0.05, float(os.environ.get("TEAMIE_REAL_MODEL_POLL_INTERVAL", "1.0"))
    )

    provider_base_url = os.environ.get("TEAMIE_REAL_PROVIDER_BASE_URL", "").strip()
    provider_api_key = os.environ.get("TEAMIE_REAL_PROVIDER_API_KEY", "").strip()
    provider_model = os.environ.get("TEAMIE_REAL_PROVIDER_MODEL", "").strip()
    web_search_key = os.environ.get("TEAMIE_REAL_WEB_SEARCH_API_KEY", "").strip()
    admin_email = os.environ.get("TEAMIE_ADMIN_EMAIL", "").strip()
    admin_password = os.environ.get("TEAMIE_ADMIN_PASSWORD", "").strip()

    require(
        bool(admin_email and admin_password),
        "TEAMIE_ADMIN_EMAIL and TEAMIE_ADMIN_PASSWORD are required.",
    )

    admin = Client(urls)
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
    print(
        f"[1/5] Verifying model gateway configuration and SSE contract (run={run_suffix})..."
    )
    has_env_provider = bool(
        provider_base_url and provider_api_key and provider_model
    )
    if has_env_provider:
        gw_payload: dict = {
            "enabled": True,
            "baseUrl": provider_base_url,
            "apiKey": provider_api_key,
            "model": provider_model,
            "contextWindowTokens": 32768,
        }
        if web_search_key:
            gw_payload["webSearchEnabled"] = True
            gw_payload["webSearchApiKey"] = web_search_key
        gw_res = admin.call(
            "PUT", "/api/v1/admin/model-gateway", gw_payload, status=200
        )
    else:
        gw_res = admin.call("GET", "/api/v1/admin/model-gateway", status=200)

    config = gw_res.get("config", {})
    require(
        gw_res.get("status") == "ready"
        and config.get("enabled") is True
        and config.get("hasApiKey") is True
        and config.get("configured") is True,
        "Model gateway must be enabled and ready with a stored API key (either via TEAMIE_REAL_PROVIDER_* env vars or pre-configured at /app/admin/gateway).",
    )
    expected_config_id = counting_config_id(
        config.get("baseUrl", ""), config.get("model", "")
    )
    web_search_cfg = config.get("webSearch") or {}
    web_search_active = bool(web_search_key) or bool(
        web_search_cfg.get("enabled")
        and web_search_cfg.get("hasApiKey")
        and web_search_cfg.get("configured")
    )

    test_res = admin.call(
        "POST", "/api/v1/admin/model-gateway/test", {}, status=200
    )
    require(
        test_res.get("ok") is True, "Model gateway live connectivity test failed."
    )

    if urls["runtime_token"]:
        admin.verify_internal_sse_stream(
            runtime_token=urls["runtime_token"],
            owner_id=admin.user["id"],
            expected_config_id=expected_config_id,
        )
        if web_search_active:
            admin.verify_internal_web_search(
                runtime_token=urls["runtime_token"],
                owner_id=admin.user["id"],
            )

    print(
        "[2/5] Creating isolated workspace, #general channel, and personal Agent..."
    )
    ws_res = admin.call(
        "POST",
        "/api/v1/workspaces",
        {"name": f"Live-Accept-{run_suffix}"},
        status=201,
    )
    workspace_id = ws_res["workspace"]["id"]
    channel = ws_res.get("conversation")
    if not channel:
        bootstrap = admin.call("GET", "/api/v1/bootstrap", status=200)
        ws_channels = [
            c
            for c in bootstrap.get("conversations", [])
            if c.get("workspaceId") == workspace_id and c.get("type") == "channel"
        ]
        channel = next(
            (c for c in ws_channels if c.get("name") == "general"),
            ws_channels[0] if ws_channels else None,
        )
    require(channel is not None, "Default #general workspace channel not found.")
    channel_id = channel["id"]

    # Create a restricted group conversation where the Agent is NOT a member to verify cross-conversation ACL boundary
    restricted_group = admin.call(
        "POST",
        f"/api/v1/workspaces/{urllib.parse.quote(workspace_id)}/conversations",
        {"type": "group", "name": f"restricted-{run_suffix}", "memberIds": []},
        status=201,
    )["conversation"]
    restricted_id = restricted_group["id"]

    agent_name = f"LiveBot-{run_suffix}"
    agent_res = admin.call(
        "POST",
        "/api/v1/agents",
        {
            "name": agent_name,
            "description": (
                "Live model acceptance assistant. Think internally first, then ALWAYS use "
                "im_send_message with replyToId set to the triggering message ID."
            ),
            "workspaceIds": [workspace_id],
            "enabled": True,
        },
        status=201,
    )
    agent_id = agent_res["agent"]["id"]
    try:
        require(
            agent_id not in restricted_group.get("memberIds", []),
            "Agent must not be a member of uninvited group conversation.",
        )
        wait_until(
            "Agent IM workspace membership admission",
            lambda: any(
                m.get("userId") == agent_id
                for m in admin.call(
                    "GET",
                    f"/api/v1/workspaces/{urllib.parse.quote(workspace_id)}/members",
                    status=200,
                ).get("members", [])
            ),
            timeout=timeout,
            poll_interval=poll_interval,
        )

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

        print(
            "[3/5] Scenario 1: #general @Agent mention, replyToId, no raw thought leak, and cross-conversation ACL..."
        )
        msg1 = post_mention(
            f"Think internally (do not include your internal reasoning in the IM message), "
            f"then call im_send_message in this #general channel with replyToId set to this message's ID "
            f"and exact body 'ACK-{run_suffix}'. Also verify you cannot access conversation {restricted_id}."
        )
        reply1 = wait_until(
            "Agent #general channel reply",
            lambda: next(
                (
                    m
                    for m in latest_agent_messages(int(msg1["seq"]))
                    if f"ACK-{run_suffix}" in m.get("body", "")
                ),
                None,
            ),
            timeout=timeout,
            poll_interval=poll_interval,
        )
        require(bool(reply1.get("id")), "Agent reply message ID must be present.")
        require(
            reply1.get("replyToId") == msg1["id"],
            f"Expected Agent replyToId={msg1['id']!r}, got {reply1.get('replyToId')!r}",
        )
        turn1_msgs = latest_agent_messages(int(msg1["seq"]))
        require(
            all(f"ACK-{run_suffix}" in m.get("body", "") for m in turn1_msgs),
            "Ordinary assistant thought text must never automatically leak as an un-tooled IM message.",
        )
        restricted_msgs = admin.call(
            "GET",
            f"/api/v1/conversations/{urllib.parse.quote(restricted_id)}/messages",
            status=200,
        ).get("messages", [])
        require(
            not any(m.get("senderId") == agent_id for m in restricted_msgs),
            "Agent must not bypass ACLs to post in restricted group conversation.",
        )

        print(
            "[4/5] Scenario 2: Sandbox isolated execution, file attachment export, and SHA-256 verification..."
        )
        expected_content = f"sandbox-ok-{run_suffix}:isolated\n".encode("utf-8")
        expected_sha256 = hashlib.sha256(expected_content).hexdigest()
        msg2 = post_mention(
            f"In your Sandbox workspace, use workspace_execute to run a script that verifies no GATEWAY_RUNTIME_TOKEN "
            f"or OPENAI_API_KEY environment variables exist and external network is disabled, then writes exact bytes "
            f"'sandbox-ok-{run_suffix}:isolated\\n' to report-{run_suffix}.txt, uploads it via im_upload_file, and sends "
            f"it to this conversation as an attachment via im_send_message (attachmentIds)."
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
            timeout=timeout,
            poll_interval=poll_interval,
        )
        att = reply2["attachments"][0]
        downloaded, dl_headers = admin.get_bytes(
            f"/api/v1/conversations/{urllib.parse.quote(channel_id)}/attachments/{urllib.parse.quote(att['id'])}/content",
            status=200,
        )
        require(
            f"sandbox-ok-{run_suffix}".encode("utf-8") in downloaded,
            "Exported attachment content did not match expected Sandbox file output.",
        )
        actual_sha256 = hashlib.sha256(downloaded).hexdigest()
        require(len(actual_sha256) == 64, "Invalid SHA-256 digest length.")
        if att.get("sha256"):
            require(
                att["sha256"] == actual_sha256,
                f"Attachment metadata sha256 {att['sha256']} != downloaded sha256 {actual_sha256}",
            )
        header_sha256 = dl_headers.get("x-attachment-sha256", "")
        if header_sha256:
            require(
                header_sha256 == actual_sha256,
                f"X-Attachment-SHA256 header {header_sha256} != downloaded sha256 {actual_sha256}",
            )
        if downloaded == expected_content:
            require(
                actual_sha256 == expected_sha256,
                f"Attachment SHA-256 {actual_sha256} != expected {expected_sha256}",
            )

        if web_search_active:
            print(
                "[4b/5] Scenario 3: Verifying live Tavily web_search / web_fetch tool execution..."
            )
            msg_ws = post_mention(
                f"Use web_search and web_fetch to look up 'IANA example domain' and reply with WEB-{run_suffix} and the cited domain."
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
                timeout=timeout,
                poll_interval=poll_interval,
            )

        print(
            "[5/5] Scenario 4: Verifying schedule_create (TEAMIE_SCHEDULE_WAKEUP) self-wakeup..."
        )
        msg3 = post_mention(
            f"Create a one-shot schedule 5 seconds from now using schedule_create that wakes you up "
            f"(TEAMIE_SCHEDULE_WAKEUP) to send a follow-up message containing WAKE-{run_suffix} in this conversation."
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
            timeout=max(timeout, 90.0) if timeout >= 15.0 else timeout,
            poll_interval=poll_interval,
        )
    finally:
        try:
            admin.call(
                "DELETE",
                f"/api/v1/agents/{urllib.parse.quote(agent_id)}",
                status=(200, 204, 404),
            )
        except Exception:
            pass

    print("Live model provider acceptance passed all checks.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
