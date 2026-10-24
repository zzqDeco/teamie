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
        self.gateway_settings = {
            "enabled": True,
            "baseUrl": "https://api.example.invalid/v1",
            "model": "gpt-live-test",
            "timeoutSeconds": 60,
            "maxTokens": 1024,
            "hasApiKey": True,
            "configId": "cfg-live-001",
            "webSearchEnabled": True,
            "hasWebSearchKey": True,
        }
        self.workspaces: list[dict] = []
        self.conversations: dict[str, dict] = {}
        self.messages: dict[str, list[dict]] = {}
        self.attachments: dict[str, bytes] = {}
        self.agent_id = ""
        self.agent_name = ""
        self.put_gateway_called = False
        self.sse_stream_called = False
        self.web_search_test_called = False


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

    def _read_json(self) -> dict:
        length = int(self.headers.get("Content-Length", "0"))
        if length <= 0:
            return {}
        return json.loads(self.rfile.read(length).decode("utf-8"))

    def do_GET(self) -> None:
        st = self.state
        if self.path == "/api/v1/admin/model-gateway":
            self._send_json(200, {"settings": st.gateway_settings})
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
        m_msg = re.match(r"^/api/v1/conversations/([^/]+)/messages$", self.path)
        if m_msg:
            cid = m_msg.group(1)
            self._send_json(200, {"messages": st.messages.get(cid, []), "hasMore": False})
            return
        m_att = re.match(r"^/api/v1/conversations/([^/]+)/attachments/([^/]+)$", self.path)
        if m_att:
            att_id = m_att.group(2)
            raw = st.attachments.get(att_id, b"")
            self.send_response(200)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return
        self._send_json(404, {"error": {"code": "not_found"}})

    def do_PUT(self) -> None:
        st = self.state
        if self.path == "/api/v1/admin/model-gateway":
            body = self._read_json()
            st.put_gateway_called = True
            st.gateway_settings.update(
                {
                    "enabled": bool(body.get("enabled", True)),
                    "baseUrl": body.get("baseUrl", st.gateway_settings["baseUrl"]),
                    "model": body.get("model", st.gateway_settings["model"]),
                    "hasApiKey": True,
                    "webSearchEnabled": bool(body.get("webSearchEnabled", False)),
                    "hasWebSearchKey": bool(body.get("webSearchApiKey")),
                }
            )
            self._send_json(200, {"settings": st.gateway_settings})
            return
        self._send_json(404, {"error": {"code": "not_found"}})

    def do_POST(self) -> None:
        st = self.state
        body = self._read_json()
        if self.path == "/api/v1/auth/login":
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
            self._send_json(200, {"ok": True})
            return
        if self.path == "/api/v1/admin/model-gateway/web-search-test":
            st.web_search_test_called = True
            self._send_json(200, {"ok": True})
            return
        if self.path == "/internal/v1/chat/stream":
            st.sse_stream_called = True
            sse_payload = (
                'data: {"type":"chunk","delta":{"role":"assistant","content":"OK"}}\n\n'
                'data: {"type":"done","usage":{"promptTokens":5,"completionTokens":2,"totalTokens":7}}\n\n'
            ).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream; charset=utf-8")
            self.send_header("X-Teamie-Model-Config-ID", st.gateway_settings["configId"])
            self.send_header("Content-Length", str(len(sse_payload)))
            self.end_headers()
            self.wfile.write(sse_payload)
            return
        if self.path == "/api/v1/workspaces":
            ws_id = "ws-accept-1"
            ws = {"id": ws_id, "name": body["name"]}
            st.workspaces.append(ws)
            ch_id = "ch-general-1"
            st.conversations[ch_id] = {
                "id": ch_id,
                "workspaceId": ws_id,
                "type": "channel",
                "name": "general",
                "memberIds": ["u-admin"],
            }
            st.messages[ch_id] = []
            self._send_json(201, {"workspace": ws})
            return
        m_ws_conv = re.match(r"^/api/v1/workspaces/([^/]+)/conversations$", self.path)
        if m_ws_conv:
            ws_id = m_ws_conv.group(1)
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
            st.agent_id = "ag-live-1"
            st.agent_name = body["displayName"]
            self._send_json(
                201,
                {
                    "agent": {
                        "id": st.agent_id,
                        "displayName": st.agent_name,
                        "enabled": True,
                    }
                },
            )
            return
        m_post_msg = re.match(r"^/api/v1/conversations/([^/]+)/messages$", self.path)
        if m_post_msg:
            cid = m_post_msg.group(1)
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
                                "name": f"report-{suffix}.txt",
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
            "TEAMIE_REAL_PROVIDER_BASE_URL": "https://api.example.invalid/v1",
            "TEAMIE_REAL_PROVIDER_API_KEY": "sk-test-123",
            "TEAMIE_REAL_PROVIDER_MODEL": "gpt-4o-mini",
            "TEAMIE_REAL_WEB_SEARCH_API_KEY": "tvly-test-123",
            "GATEWAY_RUNTIME_TOKEN": "rt-token-123",
            "TEAMIE_REAL_MODEL_TIMEOUT": "5",
            "TEAMIE_REAL_MODEL_POLL_INTERVAL": "0.05",
        }
        with mock.patch.dict(os.environ, env, clear=True):
            rc = MOD.main()
        self.assertEqual(rc, 0)
        self.assertTrue(self.state.put_gateway_called)
        self.assertTrue(self.state.sse_stream_called)
        self.assertTrue(self.state.web_search_test_called)

    def test_acceptance_with_preconfigured_admin_gateway(self) -> None:
        env = {
            "TEAMIE_REAL_MODEL_ACCEPTANCE": "1",
            "TEAMIE_ACCEPTANCE_URL": self.base_url,
            "TEAMIE_ADMIN_EMAIL": "admin@example.com",
            "TEAMIE_ADMIN_PASSWORD": "secret-password",
            "TEAMIE_REAL_MODEL_TIMEOUT": "5",
            "TEAMIE_REAL_MODEL_POLL_INTERVAL": "0.05",
        }
        with mock.patch.dict(os.environ, env, clear=True):
            rc = MOD.main()
        self.assertEqual(rc, 0)
        self.assertFalse(self.state.put_gateway_called)
        self.assertTrue(self.state.web_search_test_called)


if __name__ == "__main__":
    unittest.main()
