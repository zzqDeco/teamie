import datetime as dt
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("teamie_pprof", Path(__file__).with_name("pprof.py"))
pprof = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pprof)


class PprofOperatorTests(unittest.TestCase):
    def test_url_and_default_window_resolution(self):
        self.assertEqual(pprof.resolve_seconds("profile", None), 30)
        self.assertEqual(pprof.resolve_seconds("trace", None), 5)
        self.assertEqual(pprof.resolve_seconds("mutex", None), 10)
        self.assertEqual(pprof.resolve_seconds("block", None), 10)
        self.assertEqual(pprof.resolve_seconds("heap", None), 0)
        self.assertEqual(
            pprof.build_pprof_url(6060, "heap", 0, 0, True),
            "http://127.0.0.1:6060/debug/pprof/heap?gc=1",
        )
        self.assertEqual(
            pprof.build_pprof_url(6060, "mutex", 10, 0, False),
            "http://127.0.0.1:6060/debug/pprof/mutex?seconds=10",
        )
        fixed = dt.datetime(2026, 10, 30, 8, 15, 0, tzinfo=dt.timezone.utc)
        self.assertEqual(
            pprof.default_output_name("agent-runtime", "profile", 0, now=fixed),
            "teamie-agent-runtime-profile-20261030T081500Z.pb.gz",
        )
        self.assertEqual(
            pprof.default_output_name("chat", "trace", 0, now=fixed),
            "teamie-chat-trace-20261030T081500Z.trace",
        )
        self.assertEqual(
            pprof.default_output_name("realtime", "goroutine", 1, now=fixed),
            "teamie-realtime-goroutine-20261030T081500Z.txt",
        )
        self.assertEqual(
            pprof.default_output_name("marketplace", "heap", 0, now=fixed),
            "teamie-marketplace-heap-20261030T081500Z.pb.gz",
        )

    def test_production_compose_wires_pprof_addr_to_all_supported_go_services(self):
        compose_path = Path(__file__).resolve().parents[1] / "deploy" / "compose.production.yaml"
        in_services = False
        current_service = None
        in_env = False
        service_envs = {}
        for raw_line in compose_path.read_text().splitlines():
            stripped = raw_line.strip()
            if not stripped or stripped.startswith("#"):
                continue
            indent = len(raw_line) - len(raw_line.lstrip(" "))
            if indent == 0:
                in_services = stripped == "services:"
                current_service = None
                in_env = False
                continue
            if not in_services:
                continue
            if indent == 2 and stripped.endswith(":"):
                current_service = stripped[:-1]
                service_envs.setdefault(current_service, {})
                in_env = False
            elif indent == 4 and current_service is not None:
                in_env = stripped == "environment:"
            elif indent == 6 and current_service is not None and in_env and ":" in stripped:
                key, value = stripped.split(":", 1)
                service_envs[current_service][key.strip()] = value.strip()

        for svc in pprof.SERVICES:
            self.assertIn(svc, service_envs, f"missing compose service {svc}")
            self.assertEqual(
                service_envs[svc].get("PPROF_ADDR"),
                "${PPROF_ADDR:-127.0.0.1:6060}",
                f"{svc} must wire loopback PPROF_ADDR in deploy/compose.production.yaml",
            )
        for non_go in ("caddy", "web", "postgres", "redis"):
            self.assertNotIn("PPROF_ADDR", service_envs.get(non_go, {}))

    def test_cli_streams_stdout_and_writes_0600_file_without_shell(self):
        with tempfile.TemporaryDirectory() as root:
            folder = Path(root)
            args_log = folder / "docker-args.txt"
            docker = folder / "docker"
            docker.write_text(f"""#!/usr/bin/env python3
import sys
from pathlib import Path
Path({str(args_log)!r}).write_text("\\n".join(sys.argv[1:]))
if sys.argv[1] == "ps":
    print("cid-runtime agent-runtime")
elif sys.argv[1] == "exec":
    sys.stdout.buffer.write(b"\\x1f\\x8b\\x08fixture-pb-gz")
""")
            docker.chmod(0o700)
            env = {**os.environ, "PATH": str(folder) + os.pathsep + os.environ["PATH"]}

            # 1. Stream directly to stdout (--output -) for SSH pipe workflows.
            stream_res = subprocess.run(
                [
                    sys.executable,
                    str(Path(__file__).with_name("pprof.py")),
                    "--service",
                    "agent-runtime",
                    "--profile",
                    "profile",
                    "--seconds",
                    "12",
                    "--output",
                    "-",
                ],
                env=env,
                capture_output=True,
            )
            self.assertEqual(stream_res.returncode, 0, stream_res.stderr.decode())
            self.assertEqual(stream_res.stdout, b"\x1f\x8b\x08fixture-pb-gz")
            recorded = args_log.read_text().splitlines()
            self.assertEqual(
                recorded,
                [
                    "exec",
                    "cid-runtime",
                    "wget",
                    "-q",
                    "-T",
                    "27",
                    "-O",
                    "-",
                    "http://127.0.0.1:6060/debug/pprof/profile?seconds=12",
                ],
            )

            # 2. Save to private 0600 file on host.
            out_file = folder / "captured-heap.pb.gz"
            file_res = subprocess.run(
                [
                    sys.executable,
                    str(Path(__file__).with_name("pprof.py")),
                    "--service",
                    "agent-runtime",
                    "--profile",
                    "heap",
                    "--gc",
                    "--output",
                    str(out_file),
                ],
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertEqual(file_res.returncode, 0, file_res.stderr)
            self.assertEqual(out_file.read_bytes(), b"\x1f\x8b\x08fixture-pb-gz")
            self.assertEqual(out_file.stat().st_mode & 0o777, 0o600)
            self.assertIn("go tool pprof", file_res.stderr)

    def test_rejects_invalid_or_unsafe_cli_combinations(self):
        invalid_cases = (
            ("--service", "caddy"),
            ("--service", "chat", "--profile", "profile", "--seconds", "61"),
            ("--service", "chat", "--profile", "profile", "--seconds", "0"),
            ("--service", "chat", "--profile", "trace", "--seconds", "11"),
            ("--service", "chat", "--profile", "goroutine", "--debug", "2"),
            ("--service", "chat", "--profile", "goroutine", "--seconds", "5", "--debug", "1"),
            ("--service", "chat", "--profile", "goroutine", "--gc"),
            ("--service", "chat", "--port", "70000"),
        )
        for case in invalid_cases:
            with self.assertRaises(SystemExit, msg=f"accepted invalid args: {case}"):
                pprof.main(case)


if __name__ == "__main__":
    unittest.main()
