import datetime as dt
import gzip
import hashlib
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

            bin_dir = root / "bin"
            bin_dir.mkdir()
            write_exec(bin_dir / "flock", "#!/bin/sh\nexit 0\n")
            write_exec(
                bin_dir / "docker",
                f"""#!/usr/bin/env python3
import json, os, pathlib, subprocess, sys
log = pathlib.Path({str(root / 'docker-calls.jsonl')!r})
args = sys.argv[1:]
with log.open("a", encoding="utf-8") as fh:
    fh.write(json.dumps(args) + "\\n")
if args[0] == "compose":
    if "pg_restore" in args:
        data = sys.stdin.buffer.read()
        assert data.startswith(b"PGDMP:"), "expected custom dump bytes"
        sys.exit(0)
    if "exec" in args and "postgres" in args:
        db = args[-1]
        sys.stdout.buffer.write(("PGDMP:" + db).encode("utf-8"))
        sys.exit(0)
    sys.exit(0)
if args[0] == "run":
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
            self.assertTrue(any("up" in c and "sandbox" in c and "agent-runtime" in c for c in calls))

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


if __name__ == "__main__":
    unittest.main()
