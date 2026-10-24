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
        self.put_gateway_called = False
        self.sse_stream_called = False
        self.web_search_called = False
        self.web_extract_called = False

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
        st = self.state
        if self.path == "/api/v1/admin/model-gateway":
            body = self._read_json(
                {
                    "baseUrl",
                    "model",
                    "apiKey",
                    "enabled",
                    "clearApiKey",
                    "contextWindowTokens",
                    "webSearchEnabled",
                    "webSearchApiKey",
                    "clearWebSearchApiKey",
                }
            )
            st.put_gateway_called = True
            st.base_url = body["baseUrl"]
            st.model = body["model"]
            st.enabled = bool(body["enabled"])
            st.has_api_key = bool(body.get("apiKey")) or st.has_api_key
            st.context_window = int(body.get("contextWindowTokens", st.context_window))
            if "webSearchEnabled" in body:
                st.web_search_enabled = bool(body["webSearchEnabled"])
            st.has_web_search_key = bool(body.get("webSearchApiKey")) or st.has_web_search_key
            self._send_json(200, st.gateway_resp())
            return
        self._send_json(404, {"error": {"code": "not_found"}})

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
            cfg_id = MOD.counting_config_id(st.base_url, st.model)
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

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()

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
        self.assertTrue(self.state.put_gateway_called)
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
        self.assertFalse(self.state.put_gateway_called)
        self.assertTrue(self.state.sse_stream_called)
        self.assertTrue(self.state.web_search_called)
        self.assertTrue(self.state.web_extract_called)
        self.assertTrue(self.state.agent_deleted)


if __name__ == "__main__":
    unittest.main()
