import datetime as dt
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest

OPS = Path(__file__).resolve().parent
REPO_ROOT = OPS.parent


def _load_sandbox_archive_module():
    spec = importlib.util.spec_from_file_location("sandbox_archive", OPS / "sandbox_archive.py")
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


SANDBOX_ARCHIVE = _load_sandbox_archive_module()


def write_exec(path: Path, body: str) -> None:
    path.write_text(body, encoding="utf-8")
    path.chmod(0o700)


class SandboxArchiveAndBackupTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls._bin_tmp = tempfile.TemporaryDirectory()
        cls.sandboxctl = Path(cls._bin_tmp.name) / "sandboxctl"
        go_bin = shutil.which("go")
        if go_bin:
            subprocess.run(
                [go_bin, "build", "-o", str(cls.sandboxctl), "./cmd/sandboxctl"],
                cwd=str(REPO_ROOT / "services/sandbox"),
                check=True,
            )
        else:
            cls.sandboxctl = None

    @classmethod
    def tearDownClass(cls) -> None:
        cls._bin_tmp.cleanup()

    def test_archive_checksums_restore_and_real_sandboxctl_verification(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            ws_src = root / "src-workspaces"
            st_src = root / "src-state"
            bundle = root / "bundle"
            ws_src.mkdir(mode=0o700)
            st_src.mkdir(mode=0o700)
            bundle.mkdir(mode=0o700)

            agent_id = "10000000-0000-4000-8000-000000000099"
            run_id = "20000000-0000-4000-8000-000000000099"
            agent_hash = hashlib.sha256(agent_id.encode("utf-8")).hexdigest()
            nested = ws_src / agent_hash / "artifacts"
            nested.mkdir(parents=True, mode=0o755)
            (nested / "notes.txt").write_text("hello teamie\n", encoding="utf-8")
            raw_binary = bytes.fromhex("00fffe80890a0ddeadbeef")
            (nested / "blob.bin").write_bytes(raw_binary)

            def task_key(aid: str, tid: str) -> str:
                return hashlib.sha256((aid + "\x00" + tid).encode("utf-8")).hexdigest()

            (st_src / f"{task_key(agent_id, 'task-ok')}.task.json").write_text(
                json.dumps(
                    {
                        "version": 2,
                        "agentId": agent_id,
                        "runId": run_id,
                        "taskId": "task-ok",
                        "command": "echo ok",
                        "fingerprint": "1" * 64,
                        "containerName": "teamie-sbx-ok",
                        "cleanupDone": True,
                        "state": "completed",
                        "result": {"exitCode": 0, "stdout": "ok\n", "stderr": "", "truncated": False},
                        "updatedAt": "2026-11-02T00:00:00Z",
                    }
                ),
                encoding="utf-8",
            )
            (st_src / f"{task_key(agent_id, 'task-unknown')}.task.json").write_text(
                json.dumps(
                    {
                        "version": 2,
                        "agentId": agent_id,
                        "runId": run_id,
                        "taskId": "task-unknown",
                        "command": "echo interrupted",
                        "fingerprint": "2" * 64,
                        "containerName": "teamie-sbx-unk",
                        "cleanupDone": False,
                        "state": "unknown",
                        "result": {
                            "exitCode": -1,
                            "stdout": "",
                            "stderr": "",
                            "truncated": False,
                            "errorCode": "execution_unknown",
                        },
                        "updatedAt": "2026-11-02T00:00:00Z",
                    }
                ),
                encoding="utf-8",
            )
            (bundle / "teamie_marketplace.dump").write_bytes(b"PGDMP-disposable-fixture")

            subprocess.run(
                [
                    sys.executable,
                    str(OPS / "sandbox_archive.py"),
                    "archive",
                    "--workspaces",
                    str(ws_src),
                    "--state",
                    str(st_src),
                    "--out-dir",
                    str(bundle),
                ],
                check=True,
                capture_output=True,
                text=True,
            )
            subprocess.run(
                [sys.executable, str(OPS / "sandbox_archive.py"), "checksums-write", "--dir", str(bundle)],
                check=True,
                capture_output=True,
                text=True,
            )
            subprocess.run(
                [sys.executable, str(OPS / "sandbox_archive.py"), "checksums-verify", "--dir", str(bundle)],
                check=True,
                capture_output=True,
                text=True,
            )

            ws_dst = root / "dst-workspaces"
            st_dst = root / "dst-state"
            subprocess.run(
                [
                    sys.executable,
                    str(OPS / "sandbox_archive.py"),
                    "restore",
                    "--archive",
                    str(bundle / "sandbox-workspaces.tar.gz"),
                    "--target",
                    str(ws_dst),
                ],
                check=True,
                capture_output=True,
                text=True,
            )
            subprocess.run(
                [
                    sys.executable,
                    str(OPS / "sandbox_archive.py"),
                    "restore",
                    "--archive",
                    str(bundle / "sandbox-state.tar.gz"),
                    "--target",
                    str(st_dst),
                ],
                check=True,
                capture_output=True,
                text=True,
            )

            self.assertEqual((ws_dst / agent_hash / "artifacts/blob.bin").read_bytes(), raw_binary)
            self.assertEqual(ws_dst.stat().st_mode & 0o777, 0o700)
            self.assertEqual(st_dst.stat().st_mode & 0o777, 0o700)

            # Refuses restoring into a non-empty target directory.
            non_empty = subprocess.run(
                [
                    sys.executable,
                    str(OPS / "sandbox_archive.py"),
                    "restore",
                    "--archive",
                    str(bundle / "sandbox-workspaces.tar.gz"),
                    "--target",
                    str(ws_dst),
                ],
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(non_empty.returncode, 0)
            self.assertIn("non-empty", non_empty.stderr)

            if self.sandboxctl is not None:
                ok_out = json.loads(
                    subprocess.check_output(
                        [
                            str(self.sandboxctl),
                            "task",
                            "--agent",
                            agent_id,
                            "--run",
                            run_id,
                            "--task",
                            "task-ok",
                        ],
                        env={**os.environ, "SANDBOX_STATE_ROOT": str(st_dst)},
                        text=True,
                    )
                )
                self.assertEqual(ok_out["state"], "completed")
                self.assertTrue(ok_out["success"])

                unk_out = json.loads(
                    subprocess.check_output(
                        [
                            str(self.sandboxctl),
                            "task",
                            "--agent",
                            agent_id,
                            "--run",
                            run_id,
                            "--task",
                            "task-unknown",
                        ],
                        env={**os.environ, "SANDBOX_STATE_ROOT": str(st_dst)},
                        text=True,
                    )
                )
                self.assertEqual(unk_out["state"], "unknown")
                self.assertFalse(unk_out["success"])

    def test_rejects_symlinks_traversal_links_tampered_checksums_and_low_space(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            ws_bad = root / "ws-symlink"
            st_ok = root / "st-ok"
            out_dir = root / "out"
            ws_bad.mkdir()
            st_ok.mkdir()
            out_dir.mkdir()
            (ws_bad / "escape").symlink_to("/etc/hosts")

            res = subprocess.run(
                [
                    sys.executable,
                    str(OPS / "sandbox_archive.py"),
                    "archive",
                    "--workspaces",
                    str(ws_bad),
                    "--state",
                    str(st_ok),
                    "--out-dir",
                    str(out_dir),
                ],
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(res.returncode, 0)
            self.assertIn("symlink", res.stderr)

            # Crafted malicious tar.gz streams (tar-slip, absolute path, symlink, hardlink).
            for label, build_member in (
                ("parent_escape", lambda: self._tar_reg("../escape.txt", b"bad")),
                ("abs_path", lambda: self._tar_reg("/etc/escape.txt", b"bad")),
                ("symlink_member", lambda: self._tar_link("link.txt", "/etc/passwd", tarfile.SYMTYPE)),
                ("hardlink_member", lambda: self._tar_link("hard.txt", "other.txt", tarfile.LNKTYPE)),
            ):
                with self.subTest(case=label):
                    bad_tar = root / f"{label}.tar.gz"
                    with gzip.open(bad_tar, "wb") as gz:
                        with tarfile.open(mode="w", fileobj=gz) as tf:
                            info, payload = build_member()
                            tf.addfile(info, io.BytesIO(payload) if payload is not None else None)
                    target = root / f"restore-{label}"
                    restore_res = subprocess.run(
                        [
                            sys.executable,
                            str(OPS / "sandbox_archive.py"),
                            "restore",
                            "--archive",
                            str(bad_tar),
                            "--target",
                            str(target),
                        ],
                        capture_output=True,
                        text=True,
                    )
                    self.assertNotEqual(restore_res.returncode, 0)
                    self.assertFalse(target.exists())

            # Checksum tamper + unlisted dump detection.
            sums_dir = root / "sums"
            sums_dir.mkdir()
            dump_file = sums_dir / "teamie_chat.dump"
            dump_file.write_bytes(b"original-bytes")
            subprocess.run(
                [sys.executable, str(OPS / "sandbox_archive.py"), "checksums-write", "--dir", str(sums_dir)],
                check=True,
                capture_output=True,
            )
            # Unlisted primary artifact must fail closed.
            (sums_dir / "teamie_gateway.dump").write_bytes(b"unlisted")
            unlisted = subprocess.run(
                [sys.executable, str(OPS / "sandbox_archive.py"), "checksums-verify", "--dir", str(sums_dir)],
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(unlisted.returncode, 0)
            (sums_dir / "teamie_gateway.dump").unlink()

            # Bit-flip must fail closed.
            dump_file.write_bytes(b"tampered-bytes")
            tampered = subprocess.run(
                [sys.executable, str(OPS / "sandbox_archive.py"), "checksums-verify", "--dir", str(sums_dir)],
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(tampered.returncode, 0)
            self.assertIn("mismatch", tampered.stderr)

            # Low disk space watermark must fail closed.
            low_space = subprocess.run(
                [
                    sys.executable,
                    str(OPS / "sandbox_archive.py"),
                    "check-space",
                    "--path",
                    str(root),
                    "--min-bytes",
                    str(1 << 60),
                ],
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(low_space.returncode, 0)
            self.assertIn("insufficient free disk space", low_space.stderr)

    @staticmethod
    def _tar_reg(name: str, data: bytes):
        info = tarfile.TarInfo(name=name)
        info.type = tarfile.REGTYPE
        info.size = len(data)
        info.mode = 0o600
        return info, data

    @staticmethod
    def _tar_link(name: str, linkname: str, link_type: bytes):
        info = tarfile.TarInfo(name=name)
        info.type = link_type
        info.linkname = linkname
        return info, None

    def test_backup_sh_quiesce_six_dbs_sandbox_and_health_sync(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            host = root / "teamie"
            for sub in ("state", "backups", "config", "sandbox-workspaces/agent1", "sandbox-state"):
                (host / sub).mkdir(parents=True)
            (host / "config/production.env").write_text("POSTGRES_PASSWORD=secret\n", encoding="utf-8")
            (host / "config/production.env").chmod(0o600)
            (host / "sandbox-workspaces/agent1/code.py").write_text("print(1)\n", encoding="utf-8")
            (host / "sandbox-state/t1.task.json").write_text('{"state":"completed"}\n', encoding="utf-8")

            recipe = host / "releases/v0.4.8"
            (recipe / "deploy").mkdir(parents=True)
            (recipe / "deploy/compose.production.yaml").write_text("name: teamie\n", encoding="utf-8")
            record = host / "state/record"
            record.mkdir()
            (record / "recipe").symlink_to(recipe)
            (record / "images.env").write_text("", encoding="utf-8")
            (record / "deployment.json").write_text('{"operation":"apply"}\n', encoding="utf-8")
            manifest_data = {
                "manifestVersion": 3,
                "version": "v0.4.8",
                "schema": {
                    "identity": 3,
                    "chat": 11,
                    "marketplace": 1,
                    "agent_control": 3,
                    "agent_runtime": 15,
                    "gateway": 6,
                },
                "images": {"sandbox-runner": "registry-vpc.cn-shenzhen.aliyuncs.com/ns/teamie-sandbox-runner@sha256:" + "1" * 64},
            }
            (record / "release-manifest.json").write_text(json.dumps(manifest_data), encoding="utf-8")
            (host / "current").symlink_to(record)

            child_state_file = root / "sandbox-children-state.json"
            child_state_file.write_text(
                json.dumps({"sbx-child-1": "running", "sbx-child-2": "running"}),
                encoding="utf-8",
            )
            fail_dump_marker = root / "fail-pg-dump"

            bin_dir = root / "bin"
            bin_dir.mkdir()
            write_exec(bin_dir / "flock", "#!/bin/sh\nexit 0\n")
            write_exec(
                bin_dir / "docker",
                f"""#!/usr/bin/env python3
import json, os, pathlib, subprocess, sys
log = pathlib.Path({str(root / 'docker-calls.jsonl')!r})
state_path = pathlib.Path({str(child_state_file)!r})
fail_marker = pathlib.Path({str(fail_dump_marker)!r})
args = sys.argv[1:]
with log.open("a", encoding="utf-8") as fh:
    fh.write(json.dumps(args) + "\\n")
state = json.loads(state_path.read_text(encoding="utf-8"))

if args[0] == "ps":
    if "status=running" in " ".join(args):
        running = [cid for cid, st in sorted(state.items()) if st == "running"]
        if running:
            print("\\n".join(running))
    sys.exit(0)
if args[0] == "pause":
    cid = args[1]
    assert cid in state, f"unknown child {{cid}}"
    state[cid] = "paused"
    state_path.write_text(json.dumps(state), encoding="utf-8")
    sys.exit(0)
if args[0] == "unpause":
    cid = args[1]
    assert cid in state, f"unknown child {{cid}}"
    state[cid] = "running"
    state_path.write_text(json.dumps(state), encoding="utf-8")
    sys.exit(0)
if args[0] == "inspect":
    cid = args[-1]
    print(state.get(cid, "missing"))
    sys.exit(0)
if args[0] == "compose":
    if "pg_restore" in args:
        data = sys.stdin.buffer.read()
        assert data.startswith(b"PGDMP:"), "expected custom dump bytes"
        sys.exit(0)
    if "exec" in args and "postgres" in args:
        assert all(st == "paused" for st in state.values()), f"expected paused children during pg_dump, got {{state}}"
        if fail_marker.exists():
            sys.exit(42)
        db = args[-1]
        sys.stdout.buffer.write(("PGDMP:" + db).encode("utf-8"))
        sys.exit(0)
    if "up" in args and "sandbox" in args and "agent-runtime" in args:
        assert all(st == "running" for st in state.values()), f"expected children unpaused before compose up, got {{state}}"
    sys.exit(0)
if args[0] == "run":
    assert all(st == "paused" for st in state.values()), f"expected paused children during archive, got {{state}}"
    # Simulate the confined read-only runner container streaming sandbox_archive.py
    src_mount = next(a for a in args if a.endswith(":/mnt/source:ro")).split(":", 1)[0]
    script_mount = next(a for a in args if a.endswith(":/mnt/sandbox_archive.py:ro")).split(":", 1)[0]
    res = subprocess.run([sys.executable, script_mount, "stream-archive", "--source", src_mount], check=True, stdout=subprocess.PIPE)
    sys.stdout.buffer.write(res.stdout)
    sys.exit(0)
sys.exit(99)
""",
            )
            write_exec(
                bin_dir / "rsync",
                """#!/usr/bin/env python3
import pathlib, shutil, sys
src = pathlib.Path(sys.argv[-2])
dst = pathlib.Path(sys.argv[-1])
dst.mkdir(parents=True, exist_ok=True)
for item in src.iterdir():
    shutil.copy2(item, dst / item.name)
""",
            )

            env = {
                **os.environ,
                "PATH": str(bin_dir) + os.pathsep + os.environ["PATH"],
                "TEAMIE_ROOT": str(host),
                "TEAMIE_BACKUP_MIN_FREE_BYTES": "1024",
                "TEAMIE_BACKUP_SANDBOX_DRAIN_ATTEMPTS": "0",
            }
            res = subprocess.run(
                ["bash", str(OPS / "backup.sh"), "--quiesce-agents"],
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertEqual(res.returncode, 0, res.stderr)

            backups = list((host / "backups").iterdir())
            self.assertEqual(len(backups), 1)
            bdir = backups[0]
            for expected_db in (
                "teamie_identity.dump",
                "teamie_chat.dump",
                "teamie_marketplace.dump",
                "teamie_agent_control.dump",
                "teamie_agent_runtime.dump",
                "teamie_gateway.dump",
                "sandbox-workspaces.tar.gz",
                "sandbox-state.tar.gz",
                "backup-manifest.json",
                "SHA256SUMS",
            ):
                self.assertTrue((bdir / expected_db).is_file(), f"missing {expected_db}")

            bmeta = json.loads((bdir / "backup-manifest.json").read_text(encoding="utf-8"))
            self.assertTrue(bmeta["sandboxArchived"])
            self.assertTrue(bmeta["sandboxQuiesced"])
            self.assertEqual(len(bmeta["databases"]), 6)

            calls = [json.loads(line) for line in (root / "docker-calls.jsonl").read_text().splitlines()]
            self.assertTrue(any("stop" in c and "agent-runtime" in c and "sandbox" in c for c in calls))
            self.assertTrue(any(c == ["pause", "sbx-child-1"] for c in calls))
            self.assertTrue(any(c == ["pause", "sbx-child-2"] for c in calls))
            self.assertTrue(any(c == ["unpause", "sbx-child-1"] for c in calls))
            self.assertTrue(any(c == ["unpause", "sbx-child-2"] for c in calls))
            self.assertTrue(any("up" in c and "sandbox" in c and "agent-runtime" in c for c in calls))
            self.assertEqual(
                json.loads(child_state_file.read_text(encoding="utf-8")),
                {"sbx-child-1": "running", "sbx-child-2": "running"},
            )

            # Error path during dump must still unpause Sandbox children, restart services, and prune incomplete output!
            fail_dump_marker.write_text("1", encoding="utf-8")
            failed_res = subprocess.run(
                ["bash", str(OPS / "backup.sh"), "--quiesce-agents"],
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(failed_res.returncode, 0)
            fail_dump_marker.unlink()
            self.assertEqual(
                json.loads(child_state_file.read_text(encoding="utf-8")),
                {"sbx-child-1": "running", "sbx-child-2": "running"},
            )
            self.assertEqual(list((host / "backups").iterdir()), [bdir])

            # Health probe succeeds on fresh backup, fails when max-age is exceeded, and syncs off-host.
            health_ok = subprocess.run(
                ["bash", str(OPS / "check-backup-health.sh")],
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertEqual(health_ok.returncode, 0, health_ok.stderr)

            offhost = root / "offhost-target"
            sync_ok = subprocess.run(
                ["bash", str(OPS / "sync-backup-offhost.sh"), "--rsync-dest", str(offhost)],
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertEqual(sync_ok.returncode, 0, sync_ok.stderr)
            self.assertTrue((offhost / bdir.name / "SHA256SUMS").is_file())

            old_stamp = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(hours=48)).strftime("%Y%m%dT%H%M%SZ")
            stale_dir = host / "backups" / f"{old_stamp}-stale0"
            bdir.rename(stale_dir)
            stale_res = subprocess.run(
                ["bash", str(OPS / "check-backup-health.sh"), "--max-age-hours", "24"],
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(stale_res.returncode, 0)
            self.assertIn("stale", stale_res.stderr)

    def test_encrypted_config_backup_roundtrip_and_boundary_guard(self) -> None:
        if shutil.which("openssl") is None:
            self.skipTest("openssl binary unavailable")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            host = root / "teamie"
            (host / "config").mkdir(parents=True)
            (host / "backups").mkdir(parents=True)
            master_key = "d" * 64
            env_body = f"GATEWAY_MASTER_KEY={master_key}\nSANDBOX_RUNTIME_TOKEN={'s' * 32}\n"
            (host / "config/production.env").write_text(env_body, encoding="utf-8")
            (host / "config/production.env").chmod(0o600)
            flags_body = '{"revision":"presence-v1","flags":{"agents.presence.v1":true}}\n'
            (host / "config/feature-flags.json").write_text(flags_body, encoding="utf-8")
            (host / "config/feature-flags.json").chmod(0o644)

            passphrase = root / "operator.pass"
            passphrase.write_text("correct-horse-battery-staple-2026\n", encoding="utf-8")
            passphrase.chmod(0o600)

            env = {**os.environ, "TEAMIE_ROOT": str(host)}

            # Must refuse writing inside $TEAMIE_ROOT/backups!
            bad_out = subprocess.run(
                [
                    "bash",
                    str(OPS / "backup-config-encrypted.sh"),
                    "--out",
                    str(host / "backups/config.enc"),
                    "--passphrase-file",
                    str(passphrase),
                ],
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(bad_out.returncode, 0)
            self.assertIn("separate operator path", bad_out.stderr)

            enc_out = root / "vault/config.enc"
            good = subprocess.run(
                [
                    "bash",
                    str(OPS / "backup-config-encrypted.sh"),
                    "--out",
                    str(enc_out),
                    "--passphrase-file",
                    str(passphrase),
                ],
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertEqual(good.returncode, 0, good.stderr)
            self.assertNotIn(master_key, good.stdout + good.stderr)
            self.assertNotIn(master_key.encode("utf-8"), enc_out.read_bytes())
            self.assertEqual(enc_out.stat().st_mode & 0o777, 0o600)

            restored = root / "restored-cfg"
            dec = subprocess.run(
                [
                    "bash",
                    str(OPS / "backup-config-encrypted.sh"),
                    "--decrypt",
                    "--in",
                    str(enc_out),
                    "--out-dir",
                    str(restored),
                    "--passphrase-file",
                    str(passphrase),
                ],
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertEqual(dec.returncode, 0, dec.stderr)
            self.assertEqual((restored / "production.env").read_text(encoding="utf-8"), env_body)
            self.assertEqual((restored / "production.env").stat().st_mode & 0o777, 0o600)
            self.assertEqual((restored / "feature-flags.json").read_text(encoding="utf-8"), flags_body)
            self.assertEqual((restored / "feature-flags.json").stat().st_mode & 0o777, 0o644)

    def test_mid_walk_directory_to_symlink_toctou_rejected_and_confined(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            outside = root / "outside-secrets"
            outside.mkdir(mode=0o700)
            (outside / "secret.txt").write_text("TOP_SECRET_DO_NOT_ARCHIVE", encoding="utf-8")

            # Case 1: Child directory swapped to symlink AFTER parent os.listdir() and BEFORE stat/open.
            src1 = root / "ws-race-after-listdir"
            (src1 / "victim").mkdir(parents=True)
            (src1 / "victim/legit.txt").write_text("safe", encoding="utf-8")

            def swap_after_root_list(rel_prefix: str) -> None:
                if rel_prefix == "":
                    shutil.rmtree(src1 / "victim")
                    (src1 / "victim").symlink_to(outside)

            buf1 = io.BytesIO()
            with self.assertRaises(ValueError) as ctx1:
                SANDBOX_ARCHIVE.write_tar_gz_stream(src1, buf1, _hooks={"on_dir_listed": swap_after_root_list})
            self.assertIn("symlink", str(ctx1.exception))

            # Case 2: Child directory swapped to symlink BETWEEN os.stat(..., dir_fd) and os.open(..., dir_fd).
            src2 = root / "ws-race-between-stat-and-open"
            (src2 / "victim").mkdir(parents=True)
            (src2 / "victim/legit.txt").write_text("safe", encoding="utf-8")

            def swap_after_stat(rel: str) -> None:
                if rel == "victim":
                    shutil.rmtree(src2 / "victim")
                    (src2 / "victim").symlink_to(outside)

            buf2 = io.BytesIO()
            with self.assertRaises(ValueError) as ctx2:
                SANDBOX_ARCHIVE.write_tar_gz_stream(src2, buf2, _hooks={"on_entry_stat": swap_after_stat})
            self.assertIn("victim", str(ctx2.exception))

            # Case 3: Directory renamed/swapped to symlink AFTER child_fd was opened; open child_fd stays
            # pinned to the original confined directory inode and NEVER reads outside/secret.txt!
            src3 = root / "ws-race-after-open"
            (src3 / "victim").mkdir(parents=True)
            (src3 / "victim/legit.txt").write_text("confined-payload", encoding="utf-8")
            retired = root / "retired-victim"

            def swap_after_child_open(rel_prefix: str) -> None:
                if rel_prefix == "victim":
                    (src3 / "victim").rename(retired)
                    (src3 / "victim").symlink_to(outside)

            buf3 = io.BytesIO()
            summary = SANDBOX_ARCHIVE.write_tar_gz_stream(src3, buf3, _hooks={"on_dir_listed": swap_after_child_open})
            self.assertEqual(summary["files"], 1)
            buf3.seek(0)
            with tarfile.open(fileobj=buf3, mode="r:gz") as tf:
                names = [m.name for m in tf.getmembers()]
                self.assertEqual(names, ["victim", "victim/legit.txt"])
                extracted = tf.extractfile("victim/legit.txt")
                assert extracted is not None
                self.assertEqual(extracted.read(), b"confined-payload")

    def test_workspace_runner_ownership_preserved_and_restored(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            src = root / "src"
            (src / "dir").mkdir(parents=True, mode=0o755)
            (src / "dir/file.txt").write_text("uid-check", encoding="utf-8")

            raw_tar = io.BytesIO()
            SANDBOX_ARCHIVE.write_tar_gz_stream(src, raw_tar)
            raw_tar.seek(0)
            with tarfile.open(fileobj=raw_tar, mode="r:gz") as tf:
                members = {m.name: m for m in tf.getmembers()}
            self.assertEqual(members["dir/file.txt"].uid, os.getuid())
            self.assertEqual(members["dir/file.txt"].gid, os.getgid())

            agent_hash = "a" * 64
            ws_tar = root / "sandbox-workspaces.tar.gz"
            with gzip.open(ws_tar, "wb") as gz:
                with tarfile.open(mode="w", fileobj=gz, format=tarfile.PAX_FORMAT) as tf:
                    def add_dir(name: str, mode: int, uid: int, gid: int) -> None:
                        ti = tarfile.TarInfo(name=name)
                        ti.type = tarfile.DIRTYPE
                        ti.mode = mode
                        ti.uid = uid
                        ti.gid = gid
                        ti.mtime = 1700000000
                        tf.addfile(ti)

                    def add_file(name: str, data: bytes, mode: int, uid: int, gid: int) -> None:
                        ti = tarfile.TarInfo(name=name)
                        ti.type = tarfile.REGTYPE
                        ti.size = len(data)
                        ti.mode = mode
                        ti.uid = uid
                        ti.gid = gid
                        ti.mtime = 1700000000
                        tf.addfile(ti, io.BytesIO(data))

                    add_dir(agent_hash, 0o777, 0, 0)
                    add_dir(f"{agent_hash}/sup-shared", 0o777, 0, 0)
                    add_file(f"{agent_hash}/sup-shared/from-supervisor.txt", b"sup", 0o666, 0, 0)
                    add_dir(f"{agent_hash}/runner-priv", 0o700, 10001, 10001)
                    add_file(f"{agent_hash}/runner-priv/secret.json", b"{}", 0o600, 10001, 10001)
                    add_file(f"{agent_hash}/runner-pub.txt", b"pub", 0o644, 10001, 10001)
                    add_file(f"{agent_hash}/unexpected-uid.txt", b"host", 0o644, 501, 20)
                    add_file(f"{agent_hash}/legacy-zero-uid.txt", b"legacy", 0o644, 0, 0)

            chown_by_inode: dict[tuple[int, int], tuple[int, int]] = {}

            def record_chown(fd: int, uid: int, gid: int) -> None:
                st = os.fstat(fd)
                chown_by_inode[(st.st_dev, st.st_ino)] = (uid, gid)

            ws_dst = root / "restored-workspaces"
            SANDBOX_ARCHIVE.restore_archive(ws_tar, ws_dst, scope="workspaces", chown_fn=record_chown)

            def owner_of(p: Path) -> tuple[int, int]:
                st = os.lstat(p)
                return chown_by_inode[(st.st_dev, st.st_ino)]

            self.assertEqual(owner_of(ws_dst), (0, 0))
            self.assertEqual(ws_dst.stat().st_mode & 0o777, 0o700)
            self.assertEqual(owner_of(ws_dst / agent_hash), (0, 0))
            self.assertEqual(owner_of(ws_dst / agent_hash / "sup-shared"), (0, 0))
            self.assertEqual(owner_of(ws_dst / agent_hash / "sup-shared/from-supervisor.txt"), (0, 0))
            self.assertEqual(owner_of(ws_dst / agent_hash / "runner-priv"), (10001, 10001))
            self.assertEqual((ws_dst / agent_hash / "runner-priv").stat().st_mode & 0o777, 0o700)
            self.assertEqual(owner_of(ws_dst / agent_hash / "runner-priv/secret.json"), (10001, 10001))
            self.assertEqual((ws_dst / agent_hash / "runner-priv/secret.json").stat().st_mode & 0o777, 0o600)
            self.assertEqual(owner_of(ws_dst / agent_hash / "runner-pub.txt"), (10001, 10001))
            self.assertEqual(owner_of(ws_dst / agent_hash / "unexpected-uid.txt"), (10001, 10001))
            self.assertEqual(owner_of(ws_dst / agent_hash / "legacy-zero-uid.txt"), (10001, 10001))

            # State scope MUST force 0:0 on every receipt regardless of tar header UID.
            state_chown: dict[tuple[int, int], tuple[int, int]] = {}

            def record_state_chown(fd: int, uid: int, gid: int) -> None:
                st = os.fstat(fd)
                state_chown[(st.st_dev, st.st_ino)] = (uid, gid)

            st_dst = root / "restored-state"
            SANDBOX_ARCHIVE.restore_archive(ws_tar, st_dst, scope="state", chown_fn=record_state_chown)
            for p in (
                st_dst,
                st_dst / agent_hash,
                st_dst / agent_hash / "runner-priv",
                st_dst / agent_hash / "runner-priv/secret.json",
                st_dst / agent_hash / "runner-pub.txt",
            ):
                pst = os.lstat(p)
                self.assertEqual(state_chown[(pst.st_dev, pst.st_ino)], (0, 0), f"expected 0:0 on {p}")


if __name__ == "__main__":
    unittest.main()
