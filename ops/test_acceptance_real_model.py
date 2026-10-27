import hashlib
import importlib.util
import json
import os
import pathlib
import re
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest import mock


SCRIPT_PATH = pathlib.Path(__file__).resolve().parent / "acceptance-real-model.py"
SPEC = importlib.util.spec_from_file_location("acceptance_real_model", SCRIPT_PATH)
assert SPEC and SPEC.loader
MOD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MOD)


class _MockTeamieState:
    def __init__(self) -> None:
        self.base_url = "https://1.1.1.1/v1"
        self.model = "gpt-live-test"
        self.enabled = True
        self.has_api_key = True
        self.context_window = 32768
        self.web_search_enabled = True
        self.has_web_search_key = True
        self.workspaces: list[dict] = []
        self.conversations: dict[str, dict] = {}
        self.messages: dict[str, list[dict]] = {}
        self.attachments: dict[str, bytes] = {}
        self.agent_id = ""
        self.agent_name = ""
        self.agent_deleted = False
        self.config_write_called = False
        self.sse_stream_called = False
        self.web_search_called = False
        self.web_extract_called = False
        self.peer_joined = False
        self.tamper_file = False
        self.evidence_flaw = ""
        self.config_id = "modelcfg:v1:" + "c" * 32
        self.rotate_stream_config = False

    def gateway_resp(self) -> dict:
        configured = bool(self.base_url and self.model and self.has_api_key)
        ws_configured = bool(self.has_web_search_key)
        return {
            "config": {
                "baseUrl": self.base_url,
                "model": self.model,
                "contextWindowTokens": self.context_window,
                "hasApiKey": self.has_api_key,
                "enabled": self.enabled,
                "configured": configured,
                "webSearch": {
                    "provider": "tavily",
                    "enabled": self.web_search_enabled,
                    "hasApiKey": self.has_web_search_key,
                    "configured": ws_configured,
                },
                "updatedAt": "2026-10-24T06:00:00Z",
            },
            "status": "ready" if (configured and self.enabled) else ("disabled" if configured else "unconfigured"),
        }


class _MockHandler(BaseHTTPRequestHandler):
    state: _MockTeamieState

    def log_message(self, fmt: str, *args) -> None:
        return

    def _send_json(self, status: int, data: dict) -> None:
        raw = json.dumps(data).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def _read_json(self, allowed_keys: set[str] | None = None) -> dict:
        length = int(self.headers.get("Content-Length", "0"))
        if length <= 0:
            return {}
        data = json.loads(self.rfile.read(length).decode("utf-8"))
        if allowed_keys is not None:
            unknown = set(data.keys()) - allowed_keys
            if unknown:
                raise ValueError(f"DisallowUnknownFields rejected keys: {sorted(unknown)}")
        return data

    def do_GET(self) -> None:
        st = self.state
        if self.path == "/api/v1/admin/model-gateway":
            self._send_json(200, st.gateway_resp())
            return
        if self.path == "/api/v1/bootstrap":
            self._send_json(
                200,
                {
                    "workspaces": st.workspaces,
                    "conversations": list(st.conversations.values()),
                },
            )
            return
        m_members = re.match(r"^/api/v1/workspaces/([^/]+)/members$", self.path)
        if m_members:
            members = [{"userId": "u-admin", "displayName": "Admin", "role": "owner", "kind": "human"}]
            if st.agent_id:
                members.append({"userId": st.agent_id, "displayName": st.agent_name, "role": "member", "kind": "agent"})
            self._send_json(200, {"members": members})
            return
        m_msg = re.match(r"^/api/v1/conversations/([^/]+)/messages$", self.path)
        if m_msg:
            cid = m_msg.group(1)
            self._send_json(200, {"messages": st.messages.get(cid, []), "hasMore": False})
            return
        m_att = re.match(r"^/api/v1/conversations/([^/]+)/attachments/([^/]+)/content$", self.path)
        if m_att:
            att_id = m_att.group(2)
            raw = st.attachments.get(att_id, b"")
            sha = hashlib.sha256(raw).hexdigest()
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("X-Attachment-SHA256", sha)
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return
        self._send_json(404, {"error": {"code": "not_found"}})

    def do_PUT(self) -> None:
        if self.path == "/api/v1/admin/model-gateway/routing":
            self._read_json({"defaultModelId"})
            self._send_json(200, self.state.gateway_resp())
            return
        self._send_json(409, {"error":{"code":"config_api_changed"}})

    def do_PATCH(self) -> None:
        st = self.state
        if self.path == "/api/v1/admin/model-gateway/web-search":
            body = self._read_json({"enabled","apiKey","clearApiKey"})
            st.config_write_called = True
            if "enabled" in body:
                st.web_search_enabled = bool(body["enabled"])
            st.has_web_search_key = bool(body.get("apiKey")) or st.has_web_search_key
            self._send_json(200, st.gateway_resp())
            return
        self._send_json(404, {"error":{"code":"not_found"}})

    def do_DELETE(self) -> None:
        st = self.state
        m_del_agent = re.match(r"^/api/v1/agents/([^/]+)$", self.path)
        if m_del_agent:
            st.agent_deleted = True
            self.send_response(204)
            self.end_headers()
            return
        self._send_json(404, {"error": {"code": "not_found"}})

    def do_POST(self) -> None:
        st = self.state
        if self.path == "/api/v1/admin/model-gateway/models":
            body = self._read_json({"id","baseUrl","model","apiKey","enabled","contextWindowTokens"})
            st.config_write_called = True
            st.base_url = body["baseUrl"]
            st.model = body["model"]
            st.enabled = bool(body["enabled"])
            st.has_api_key = bool(body.get("apiKey")) or st.has_api_key
            st.context_window = int(body.get("contextWindowTokens",st.context_window))
            self._send_json(201, st.gateway_resp())
            return
        if self.path == "/api/v1/auth/login":
            body = self._read_json({"email", "password"})
            self._send_json(
                200,
                {
                    "user": {
                        "id": "u-admin",
                        "email": body.get("email", "admin@example.com"),
                        "displayName": "Admin",
                        "platformRole": "admin",
                    },
                    "csrfToken": "csrf-mock-123",
                },
            )
            return
        if self.path == "/api/v1/auth/admin/invites":
            self._read_json(set())
            self._send_json(201, {"invite": {"code": "single-use-fixture"}})
            return
        if self.path == "/api/v1/auth/register":
            body = self._read_json({"email", "password", "displayName", "inviteCode"})
            assert body["inviteCode"] == "single-use-fixture"
            self._send_json(201, {"user": {"id": "u-peer", "platformRole": "user"}, "csrfToken": "peer-csrf"})
            return
        if re.match(r"^/api/v1/workspaces/[^/]+/invites$", self.path):
            self._read_json(set())
            self._send_json(201, {"code": "workspace-fixture"})
            return
        if self.path == "/api/v1/invites/workspace-fixture/accept":
            self._read_json(set())
            st.peer_joined = True
            self._send_json(200, {})
            return
        if self.path == "/internal/v1/chat":
            self._read_json({"ownerId", "agentId", "sessionId", "runId", "requestJson", "messageLimit"})
            raw = b'{"ok":true}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("X-Teamie-Model-Config-ID", st.config_id)
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return
        if self.path == "/api/v1/admin/model-gateway/test":
            self._read_json(set())
            self._send_json(200, {"ok": True, "model": st.model, "latencyMs": 12})
            return
        if self.path == "/internal/v1/web/search":
            body = self._read_json(
                {
                    "ownerId",
                    "agentId",
                    "sessionId",
                    "runId",
                    "query",
                    "maxResults",
                    "timeRange",
                    "includeDomains",
                    "excludeDomains",
                }
            )
            st.web_search_called = True
            self._send_json(
                200,
                {
                    "ok": True,
                    "results": [
                        {
                            "title": "Example Domain",
                            "url": "https://www.iana.org/help/example-domains",
                            "snippet": "IANA example domains",
                            "score": 0.99,
                        }
                    ],
                    "statusCode": 200,
                },
            )
            return
        if self.path == "/internal/v1/web/extract":
            body = self._read_json(
                {
                    "ownerId",
                    "agentId",
                    "sessionId",
                    "runId",
                    "urls",
                }
            )
            assert body.get("urls"), "Expected non-empty urls in /internal/v1/web/extract"
            st.web_extract_called = True
            self._send_json(
                200,
                {
                    "ok": True,
                    "results": [
                        {
                            "url": body["urls"][0],
                            "content": "IANA-managed Reserved Domains: example.com, example.net, example.org",
                        }
                    ],
                    "failedResults": [],
                    "statusCode": 200,
                },
            )
            return
        if self.path == "/internal/v1/chat/stream":
            body = self._read_json(
                {
                    "ownerId",
                    "agentId",
                    "sessionId",
                    "runId",
                    "requestJson",
                    "messageLimit",
                    "expectedModelConfigId",
                }
            )
            inner = json.loads(body["requestJson"])
            assert inner.get("protocol") == "teamie-agentic-chat-v1"
            st.sse_stream_called = True
            cfg_id = ("modelcfg:v1:" + "d" * 32) if st.rotate_stream_config else st.config_id
            sse_payload = (
                'event: chunk\ndata: {"type":"chunk","responseJson":"{\\"role\\":\\"assistant\\"}"}\n\n'
                f'event: done\ndata: {{"type":"done","modelConfigId":"{cfg_id}"}}\n\n'
            ).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream; charset=utf-8")
            self.send_header("X-Teamie-Model-Config-ID", cfg_id)
            self.send_header("Content-Length", str(len(sse_payload)))
            self.end_headers()
            self.wfile.write(sse_payload)
            return
        if self.path == "/api/v1/workspaces":
            body = self._read_json({"name"})
            ws_id = "ws-accept-1"
            ws = {"id": ws_id, "name": body["name"]}
            st.workspaces.append(ws)
            ch_id = "ch-general-1"
            conv = {
                "id": ch_id,
                "workspaceId": ws_id,
                "type": "channel",
                "name": "general",
                "memberIds": ["u-admin"],
            }
            st.conversations[ch_id] = conv
            st.messages[ch_id] = []
            self._send_json(201, {"workspace": ws, "conversation": conv})
            return
        m_ws_conv = re.match(r"^/api/v1/workspaces/([^/]+)/conversations$", self.path)
        if m_ws_conv:
            ws_id = m_ws_conv.group(1)
            body = self._read_json({"type", "name", "memberIds"})
            members = set(["u-admin", *body.get("memberIds", [])])
            if len(members) < 2 or not st.peer_joined or members != {"u-admin", "u-peer"}:
                self._send_json(400, {"error": {"code": "invalid_members_count"}})
                return
            cid = f"grp-{len(st.conversations) + 1}"
            conv = {
                "id": cid,
                "workspaceId": ws_id,
                "type": body.get("type", "group"),
                "name": body.get("name", "restricted"),
                "memberIds": ["u-admin", *(body.get("memberIds") or [])],
            }
            st.conversations[cid] = conv
            st.messages[cid] = []
            self._send_json(201, {"conversation": conv})
            return
        if self.path == "/api/v1/agents":
            body = self._read_json({"name", "description", "workspaceIds", "enabled"})
            st.agent_id = "ag-live-1"
            st.agent_name = body["name"]
            self._send_json(
                201,
                {
                    "agent": {
                        "id": st.agent_id,
                        "principalId": st.agent_id,
                        "ownerId": "u-admin",
                        "ownerName": "Admin",
                        "name": st.agent_name,
                        "description": body.get("description", ""),
                        "workspaceIds": body.get("workspaceIds", []),
                        "enabled": bool(body.get("enabled", True)),
                        "createdAt": "2026-10-24T06:00:00Z",
                        "updatedAt": "2026-10-24T06:00:00Z",
                    }
                },
            )
            return
        m_post_msg = re.match(r"^/api/v1/conversations/([^/]+)/messages$", self.path)
        if m_post_msg:
            cid = m_post_msg.group(1)
            body = self._read_json({"clientMessageId", "body", "replyToId", "attachmentIds", "mentions"})
            msgs = st.messages.setdefault(cid, [])
            user_seq = len(msgs) + 1
            user_msg = {
                "id": f"m-{user_seq}",
                "conversationId": cid,
                "senderId": "u-admin",
                "seq": user_seq,
                "body": body.get("body", ""),
            }
            msgs.append(user_msg)
            text = user_msg["body"]

            m_ack = re.search(r"ACK-([0-9a-f]+)", text)
            m_sandbox = re.search(r"sandbox-ok-([0-9a-f]+):isolated", text)
            m_web = re.search(r"WEB-([0-9a-f]+)", text)
            m_wake = re.search(r"WAKE-([0-9a-f]+)", text)

            if m_ack:
                suffix = m_ack.group(1)
                msgs.append(
                    {
                        "id": f"m-{len(msgs) + 1}",
                        "conversationId": cid,
                        "senderId": st.agent_id,
                        "seq": len(msgs) + 1,
                        "replyToId": user_msg["id"],
                        "body": f"ACK-{suffix}",
                    }
                )
            elif m_sandbox:
                suffix = m_sandbox.group(1)
                att_id = f"att-{suffix}"
                content = f"sandbox-ok-{suffix}:isolated\n".encode("utf-8")
                if st.tamper_file:
                    content += b"incorrect extra bytes"
                st.attachments[att_id] = content
                msgs.append(
                    {
                        "id": f"m-{len(msgs) + 1}",
                        "conversationId": cid,
                        "senderId": st.agent_id,
                        "seq": len(msgs) + 1,
                        "replyToId": user_msg["id"],
                        "body": f"Exported report-{suffix}.txt",
                        "attachments": [
                            {
                                "id": att_id,
                                "filename": f"report-{suffix}.txt",
                                "mediaType": "text/plain",
                                "sizeBytes": len(content),
                                "sha256": hashlib.sha256(content).hexdigest(),
                            }
                        ],
                    }
                )
            elif m_web:
                suffix = m_web.group(1)
                msgs.append(
                    {
                        "id": f"m-{len(msgs) + 1}",
                        "conversationId": cid,
                        "senderId": st.agent_id,
                        "seq": len(msgs) + 1,
                        "replyToId": user_msg["id"],
                        "body": f"WEB-{suffix} example.com (IANA)",
                    }
                )
            elif m_wake:
                suffix = m_wake.group(1)
                msgs.append(
                    {
                        "id": f"m-{len(msgs) + 1}",
                        "conversationId": cid,
                        "senderId": st.agent_id,
                        "seq": len(msgs) + 1,
                        "body": f"WAKE-{suffix}",
                    }
                )

            self._send_json(201, {"message": user_msg})
            return
        self._send_json(404, {"error": {"code": "not_found"}})


class AcceptanceRealModelTest(unittest.TestCase):
    def setUp(self) -> None:
        self.state = _MockTeamieState()

        class BoundHandler(_MockHandler):
            state = self.state

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), BoundHandler)
        self.port = self.server.server_address[1]
        self.base_url = f"http://127.0.0.1:{self.port}"
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.evidence_patch = mock.patch.object(MOD.Evidence, "call", autospec=True, side_effect=self.evidence_call)
        self.evidence_patch.start()
        self.save_patch = mock.patch.object(MOD.Evidence, "save", return_value="private-mock-receipt")
        self.save_patch.start()

    def tearDown(self) -> None:
        self.evidence_patch.stop()
        self.save_patch.stop()
        self.server.shutdown()
        self.server.server_close()

    def evidence_call(self, evidence, kind, **scope):
        if kind == "isolation":
            return {"version": 1, "nonce": scope["nonce"], "verified": True}
        if kind == "task":
            return {"version": 1, "agentId": scope["agent"], "runId": scope["run"], "taskId": scope["task"], "state": "running" if self.state.evidence_flaw == "task-running" else "completed", "success": self.state.evidence_flaw != "task-running"}
        source = scope["message"]
        messages = self.state.messages["ch-general-1"]
        trigger = next(m for m in messages if m["id"] == source)
        reply = messages[messages.index(trigger) + 1]
        rid = "run-" + source
        base = {"version": 1, "agentId": scope["agent"], "messageId": source, "runs": [{"id": rid, "state": "completed", "inputs": [{"messageId": source, "state": "done"}]}], "tools": [], "schedules": []}
        def call(name, **extra):
            return {"runId": rid, "callId": name + "-call", "name": name, "completed": True, "ok": True, **extra}
        base["tools"].append(call("im_send_message", messageId=reply["id"], conversationId="ch-general-1"))
        text = trigger["body"]
        if "ACK-" in text:
            restricted = next(c["id"] for c in self.state.conversations.values() if c["type"] == "group")
            base["tools"].append(call("im_list_messages", ok=False, denied=True, conversationId=restricted))
        elif "sandbox-ok-" in text:
            base["tools"].extend([call("im_upload_file", conversationId="ch-general-1", attachmentId=reply["attachments"][0]["id"]), call("native:workspace_execute", ok=False, taskId="shell-fixture")])
        elif "WEB-" in text:
            base["tools"].extend([call("web_search"), call("web_fetch")])
        elif "WAKE-" in text:
            base["tools"].append(call("schedule_create", scheduleId="sched-fixture"))
            base["schedules"].append({"id": "sched-fixture", "runId": rid, "runCount": 1, "lastRunAt": "2026-10-26T00:00:05Z"})
            if self.state.evidence_flaw != "foreground-wake":
                base["runs"].append({"id": "wake-" + rid, "state": "completed", "inputs": [{"kind": "schedule.wakeup", "scheduleId": "sched-fixture", "parentRunId": rid, "state": "done"}]})
                base["tools"][0]["runId"] = "wake-" + rid
        if self.state.evidence_flaw == "no-execution":
            base["tools"] = []
        if self.state.evidence_flaw == "wrong-run":
            for c in base["tools"]:
                c["runId"] = "unrelated-old-run"
        if self.state.evidence_flaw == "no-web":
            base["tools"] = [c for c in base["tools"] if c["name"] not in ("web_search", "web_fetch")]
        if self.state.evidence_flaw == "no-acl":
            base["tools"] = [c for c in base["tools"] if c["name"] != "im_list_messages"]
        return base

    def environment(self):
        return {"TEAMIE_REAL_MODEL_ACCEPTANCE": "1", "TEAMIE_ACCEPTANCE_URL": self.base_url, "TEAMIE_ADMIN_EMAIL": "admin@example.test", "TEAMIE_ADMIN_PASSWORD": "fixture-only-password", "GATEWAY_RUNTIME_TOKEN": "rt-token-123456789012345678901234", "TEAMIE_REAL_MODEL_TIMEOUT": "1", "TEAMIE_REAL_MODEL_POLL_INTERVAL": "0.05"}

    def test_singleton_group_is_rejected(self):
        with mock.patch.dict(os.environ, self.environment(), clear=True):
            client = MOD.Client()
            with self.assertRaisesRegex(AssertionError, "invalid_members_count"):
                client.call("POST", "/api/v1/workspaces/fixture/conversations", {"type": "group", "memberIds": []}, status=201)

    def test_web_key_only_applies_without_changing_model_window(self):
        self.state.web_search_enabled = False
        self.state.has_web_search_key = False
        self.state.context_window = 1000000
        env = {**self.environment(), "TEAMIE_REAL_WEB_SEARCH_API_KEY": "fixture-key"}
        with mock.patch.dict(os.environ, env, clear=True):
            self.assertEqual(MOD.main(), 0)
        self.assertTrue(self.state.config_write_called)
        self.assertTrue(self.state.web_search_enabled)
        self.assertEqual(self.state.context_window, 1000000)

    def test_exact_file_bytes_are_required_even_with_consistent_metadata(self):
        self.state.tamper_file = True
        with mock.patch.dict(os.environ, self.environment(), clear=True), self.assertRaisesRegex(AssertionError, "bytes/digest"):
            MOD.main()

    def test_markers_without_correct_durable_execution_fail(self):
        for flaw in ("no-execution", "wrong-run", "no-acl", "no-web", "task-running", "foreground-wake"):
            with self.subTest(flaw=flaw):
                self.state.evidence_flaw = flaw
                with mock.patch.dict(os.environ, self.environment(), clear=True), self.assertRaises(AssertionError):
                    MOD.main()
                self.assertTrue(self.state.agent_deleted)
                self.state.agent_deleted = False

    def test_non_ascii_configuration_uses_gateway_id(self):
        self.state.model = "模型<&>\u2028\u2029"
        with mock.patch.dict(os.environ, self.environment(), clear=True):
            self.assertEqual(MOD.main(), 0)

    def test_configuration_change_between_unary_and_stream_is_rejected(self):
        self.state.rotate_stream_config = True
        with mock.patch.dict(os.environ, self.environment(), clear=True), self.assertRaisesRegex(AssertionError, "Config-ID"):
            MOD.main()

    def test_partial_provider_configuration_is_rejected(self):
        env = {**self.environment(), "TEAMIE_REAL_PROVIDER_MODEL": "partial"}
        with mock.patch.dict(os.environ, env, clear=True), self.assertRaisesRegex(AssertionError, "all three"):
            MOD.main()

    def test_disabled_web_is_reported_as_unverified(self):
        self.state.web_search_enabled = False
        with mock.patch.dict(os.environ, self.environment(), clear=True):
            self.assertEqual(MOD.main(), 2)
        self.assertFalse(self.state.web_search_called)

    def test_missing_private_probe_is_not_full_acceptance(self):
        env = self.environment()
        del env["GATEWAY_RUNTIME_TOKEN"]
        with mock.patch.dict(os.environ, env, clear=True):
            self.assertEqual(MOD.main(), 2)

    def test_requires_explicit_opt_in_flag(self) -> None:
        with mock.patch.dict(os.environ, {}, clear=True):
            with self.assertRaises(AssertionError):
                MOD.main()

    def test_full_acceptance_with_env_provider_and_sse_stream(self) -> None:
        env = {
            "TEAMIE_REAL_MODEL_ACCEPTANCE": "1",
            "TEAMIE_ACCEPTANCE_URL": self.base_url,
            "TEAMIE_ADMIN_EMAIL": "admin@example.com",
            "TEAMIE_ADMIN_PASSWORD": "secret-password",
            "TEAMIE_REAL_PROVIDER_BASE_URL": "https://1.1.1.1/v1",
            "TEAMIE_REAL_PROVIDER_API_KEY": "sk-test-123",
            "TEAMIE_REAL_PROVIDER_MODEL": "gpt-4o-mini",
            "TEAMIE_REAL_WEB_SEARCH_API_KEY": "tvly-test-123",
            "GATEWAY_RUNTIME_TOKEN": "rt-token-123456789012345678901234",
            "TEAMIE_REAL_MODEL_TIMEOUT": "5",
            "TEAMIE_REAL_MODEL_POLL_INTERVAL": "0.05",
        }
        with mock.patch.dict(os.environ, env, clear=True):
            rc = MOD.main()
        self.assertEqual(rc, 0)
        self.assertTrue(self.state.config_write_called)
        self.assertTrue(self.state.sse_stream_called)
        self.assertTrue(self.state.web_search_called)
        self.assertTrue(self.state.web_extract_called)
        self.assertTrue(self.state.agent_deleted)

    def test_acceptance_with_preconfigured_admin_gateway(self) -> None:
        env = {
            "TEAMIE_REAL_MODEL_ACCEPTANCE": "1",
            "TEAMIE_ACCEPTANCE_URL": self.base_url,
            "TEAMIE_ADMIN_EMAIL": "admin@example.com",
            "TEAMIE_ADMIN_PASSWORD": "secret-password",
            "GATEWAY_RUNTIME_TOKEN": "rt-token-123456789012345678901234",
            "TEAMIE_REAL_MODEL_TIMEOUT": "5",
            "TEAMIE_REAL_MODEL_POLL_INTERVAL": "0.05",
        }
        with mock.patch.dict(os.environ, env, clear=True):
            rc = MOD.main()
        self.assertEqual(rc, 0)
        self.assertFalse(self.state.config_write_called)
        self.assertTrue(self.state.sse_stream_called)
        self.assertTrue(self.state.web_search_called)
        self.assertTrue(self.state.web_extract_called)
        self.assertTrue(self.state.agent_deleted)


if __name__ == "__main__":
    unittest.main()
