import copy
import datetime as dt
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("manifest", Path(__file__).with_name("manifest.py"))
manifest = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(manifest)


def release(release_version="v0.1.0", sha="a" * 40):
    return {
        "manifestVersion": 1,
        "version": release_version,
        "source": {"repository": "zzqDeco/teamie", "sha": sha, "mainSha": sha,
                   "ref": "refs/heads/release/" + release_version},
        "createdAt": dt.datetime.now(dt.timezone.utc).isoformat(),
        "schema": {"identity": 1, "chat": 1},
        "storage": {"postgresMajor": 17},
        "featureFlags": {"revision": "im-v1", "sha256": "f" * 64},
        "images": {service: "ghcr.io/zzqdeco/teamie-" + service + "@sha256:" + "0" * 64
                   for service in manifest.LEGACY_SERVICES},
    }


class ManifestTest(unittest.TestCase):
    def test_accepts_exact_release_and_only_digest_environment(self):
        data = release()
        manifest.check_transition(data)
        rendered = manifest.image_env(data)
        self.assertEqual(len(rendered.splitlines()), 7)
        self.assertIn("IDENTITY_IMAGE=ghcr.io/zzqdeco/teamie-identity@sha256:", rendered)

    def test_rejects_mutable_tags_foreign_registry_and_missing_services(self):
        for image in ("ghcr.io/zzqdeco/teamie-chat:latest", "docker.io/evil/chat@sha256:" + "0" * 64,
                      "ghcr.io/zzqdeco/teamie-chat@sha256:" + "0" * 63):
            with self.subTest(image=image), self.assertRaises(ValueError):
                data = release(); data["images"]["chat"] = image
                manifest.validate(data)
        data = release(); del data["images"]["redis"]
        with self.assertRaises(ValueError): manifest.validate(data)

    def test_source_and_version_must_match_checked_main_and_archive(self):
        for field, value in (("mainSha", "b" * 40), ("ref", "refs/heads/main")):
            data = release(); data["source"][field] = value
            with self.assertRaises(ValueError): manifest.validate(data)
        for value in ("v01.0.0", "1.0.0", "v1.0.0\nanything", "v1.0.0-rc1"):
            with self.assertRaises(ValueError): manifest.version(value)

    def test_stale_and_future_manifests_cannot_deploy(self):
        for offset in (-90000, 600):
            data = release()
            data["createdAt"] = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=offset)).isoformat()
            with self.assertRaises(ValueError): manifest.check_transition(data)

    def test_highwater_blocks_older_releases_even_after_a_rollback(self):
        with self.assertRaises(ValueError):
            manifest.check_transition(release("v0.2.0"), release("v0.1.0"), release("v0.3.0"))
        manifest.check_transition(release("v0.4.0"), release("v0.1.0"), release("v0.3.0"))

    def test_same_version_cannot_change_source_or_images(self):
        old = release()
        manifest.check_transition(copy.deepcopy(old), old, old)
        changed = copy.deepcopy(old)
        changed["images"]["chat"] = changed["images"]["chat"].replace("0" * 64, "1" * 64)
        with self.assertRaises(ValueError): manifest.check_transition(changed, old, old)

    def test_image_rollback_requires_schema_storage_and_flags_compatibility(self):
        current = release("v0.2.0")
        target = release("v0.1.0")
        manifest.check_transition(target, current, rollback=True)
        for key, changed in (("schema", {"identity": 1, "chat": 2}),
                             ("storage", {"postgresMajor": 18}),
                             ("featureFlags", {"revision": "im-v2", "sha256": "b" * 64})):
            candidate = copy.deepcopy(target); candidate[key] = changed
            with self.assertRaises(ValueError): manifest.check_transition(candidate, current, rollback=True)

    def test_rollout_stops_at_first_failed_migration_or_health_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            common = str(Path(__file__).with_name("common.sh"))
            counter = str(Path(directory) / "calls")
            result = subprocess.run(["bash", "-c", 'source "$COMMON"; COMPOSE=(bash -c \'printf x >> "$COUNTER"; exit 1\'); rollout'],
                                    env={**os.environ, "COMMON": common, "COUNTER": counter}, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(Path(counter).read_text(), "x")

    def test_agent_writer_quiesces_before_dependency_migrations(self):
        for stop_fails in (False, True):
            with self.subTest(stop_fails=stop_fails), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                sql = root / "deploy/postgres/01-databases.sh"
                sql.parent.mkdir(parents=True)
                sql.touch()
                probe = root / "probe.sh"
                probe.write_text('''#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$CALLS"
if [[ $* == 'config --services' ]]; then echo llm-gateway; fi
if [[ $* == 'stop --timeout 30 agent-runtime' && $STOP_FAILS == 1 ]]; then exit 1; fi
''')
                calls = root / "calls"
                result = subprocess.run(["bash", "-c", 'source "$COMMON"; COMPOSE=(bash "$PROBE"); COMPOSE_RECIPE=$RECIPE; rollout'],
                                        env={**os.environ, "COMMON": str(Path(__file__).with_name("common.sh")),
                                             "PROBE": str(probe), "RECIPE": str(root), "CALLS": str(calls),
                                             "STOP_FAILS": str(int(stop_fails))}, capture_output=True)
                operations = calls.read_text().splitlines()
                stop = operations.index("stop --timeout 30 agent-runtime")
                identity = "up -d --no-deps --wait --wait-timeout 180 identity"
                if stop_fails:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertNotIn(identity, operations)
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertLess(stop, operations.index(identity))


class AgentManifestTest(unittest.TestCase):
    def modern(self):
        data = release("v0.2.0")
        data.update(manifestVersion=2, schema=dict(manifest.SCHEMA_V2))
        data["images"].update({s: "ghcr.io/zzqdeco/teamie-"+s+"@sha256:"+"1"*64 for s in manifest.SERVICES if s not in manifest.LEGACY_SERVICES})
        return data

    def test_v1_archive_and_v2_forward_compatibility(self):
        old, modern = release(), self.modern()
        manifest.validate(old)
        manifest.check_transition(modern, old, old)
        self.assertEqual(len(manifest.image_env(modern).splitlines()), 12)
        self.assertIn("SANDBOX_RUNNER_IMAGE=", manifest.image_env(modern))
        self.assertIn("LLM_GATEWAY_IMAGE=", manifest.image_env(modern))
        with self.assertRaises(ValueError): manifest.check_transition(old, modern, rollback=True)
        with self.assertRaises(ValueError): manifest.check_transition(old, modern)

    def test_vector_cannot_omit_runner_or_add_unknown_database(self):
        data = self.modern(); del data["images"]["sandbox-runner"]
        with self.assertRaises(ValueError): manifest.validate(data)
        data = self.modern(); data["schema"]["shared"] = 1
        with self.assertRaises(ValueError): manifest.validate(data)

    def test_password_recovery_schema_preserves_archives_and_rejects_image_rollback(self):
        previous=self.modern()
        previous["version"]="v0.3.6"
        previous["source"]["ref"]="refs/heads/release/v0.3.6"
        previous["schema"]={"identity":2,"chat":3,"agent_control":1,"agent_runtime":2,"gateway":2}
        current=copy.deepcopy(previous)
        current["version"]="v0.3.7"
        current["source"]["ref"]="refs/heads/release/v0.3.7"
        current["schema"]={"identity":3,"chat":3,"agent_control":1,"agent_runtime":2,"gateway":2}
        self.assertEqual(current["schema"]["identity"],3)
        manifest.validate(previous)
        manifest.check_transition(current,previous,previous)
        with self.assertRaises(ValueError):
            manifest.check_transition(previous,current,rollback=True)

    def test_context_schema_preserves_v037_archive_and_rejects_image_rollback(self):
        previous=self.modern()
        previous["version"]="v0.3.7"
        previous["source"]["ref"]="refs/heads/release/v0.3.7"
        previous["schema"]={"identity":3,"chat":3,"agent_control":1,"agent_runtime":2,"gateway":2}
        current=copy.deepcopy(previous)
        current["version"]="v0.3.8"
        current["source"]["ref"]="refs/heads/release/v0.3.8"
        current["schema"]={"identity":3,"chat":3,"agent_control":1,"agent_runtime":3,"gateway":2}
        self.assertEqual(current["schema"]["agent_runtime"],3)
        manifest.validate(previous)
        manifest.check_transition(current,previous,previous)
        with self.assertRaises(ValueError):
            manifest.check_transition(previous,current,rollback=True)

    def test_upload_receipt_schema_preserves_context_archive_and_rejects_image_rollback(self):
        previous=self.modern()
        previous["schema"]={"identity":3,"chat":3,"agent_control":1,"agent_runtime":3,"gateway":2}
        current=copy.deepcopy(previous)
        current["schema"]={"identity":3,"chat":4,"agent_control":1,"agent_runtime":3,"gateway":2}
        self.assertEqual(current["schema"]["chat"],4)
        manifest.validate(previous)
        manifest.check_transition(current,previous,previous)
        with self.assertRaises(ValueError):
            manifest.check_transition(previous,current,rollback=True)

    def test_mentions_schema_preserves_file_archive_and_rejects_image_rollback(self):
        previous=self.modern()
        previous["schema"]={"identity":3,"chat":4,"agent_control":1,"agent_runtime":3,"gateway":2}
        current=copy.deepcopy(previous)
        current["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":3,"gateway":2}
        self.assertEqual(current["schema"]["chat"],5)
        manifest.validate(previous)
        manifest.check_transition(current,previous,previous)
        with self.assertRaises(ValueError):
            manifest.check_transition(previous,current,rollback=True)

    def test_output_recovery_schema_preserves_v039_and_rejects_old_runtime(self):
        previous=self.modern()
        previous["version"]="v0.3.9"
        previous["source"]["ref"]="refs/heads/release/v0.3.9"
        previous["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":3,"gateway":2}
        current=copy.deepcopy(previous)
        current["version"]="v0.3.10"
        current["source"]["ref"]="refs/heads/release/v0.3.10"
        current["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":4,"gateway":2}
        self.assertEqual(current["schema"]["agent_runtime"],4)
        manifest.validate(previous)
        manifest.check_transition(current,previous,previous)
        with self.assertRaises(ValueError):
            manifest.check_transition(previous,current,rollback=True)

    def test_current_input_schema_preserves_v0310_and_rejects_old_runtime(self):
        previous=self.modern()
        previous["version"]="v0.3.10"
        previous["source"]["ref"]="refs/heads/release/v0.3.10"
        previous["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":4,"gateway":2}
        current=copy.deepcopy(previous)
        current["version"]="v0.3.11"
        current["source"]["ref"]="refs/heads/release/v0.3.11"
        current["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":5,"gateway":2}
        self.assertEqual(current["schema"]["agent_runtime"],5)
        manifest.check_transition(current,previous,previous)
        with self.assertRaises(ValueError):
            manifest.check_transition(previous,current,rollback=True)

    def test_history_coalescing_schema_preserves_v0311_and_rejects_old_runtime(self):
        previous=self.modern()
        previous["version"]="v0.3.11"
        previous["source"]["ref"]="refs/heads/release/v0.3.11"
        previous["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":5,"gateway":2}
        current=copy.deepcopy(previous)
        current["version"]="v0.3.12"
        current["source"]["ref"]="refs/heads/release/v0.3.12"
        current["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":6,"gateway":2}
        self.assertEqual(current["schema"]["agent_runtime"],6)
        manifest.check_transition(current,previous,previous)
        with self.assertRaises(ValueError):
            manifest.check_transition(previous,current,rollback=True)

    def test_notification_batch_schema_preserves_v0313_and_rejects_old_runtime(self):
        previous=self.modern()
        previous["version"]="v0.3.13"
        previous["source"]["ref"]="refs/heads/release/v0.3.13"
        previous["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":6,"gateway":2}
        current=copy.deepcopy(previous)
        current["version"]="v0.3.14"
        current["source"]["ref"]="refs/heads/release/v0.3.14"
        current["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":8,"gateway":2}
        self.assertEqual(current["schema"]["agent_runtime"],8)
        manifest.check_transition(current,previous,previous)
        with self.assertRaises(ValueError):
            manifest.check_transition(previous,current,rollback=True)

    def test_event_driven_schema_rejects_explicit_and_automatic_old_writer_rollback(self):
        previous=self.modern()
        previous["version"]="v0.3.15"
        previous["source"]["ref"]="refs/heads/release/v0.3.15"
        previous["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":8,"gateway":2}
        current=copy.deepcopy(previous)
        current["version"]="v0.3.16"
        current["source"]["ref"]="refs/heads/release/v0.3.16"
        current["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":9,"gateway":2}
        self.assertEqual(current["schema"]["agent_runtime"],9)
        self.assertEqual(current["featureFlags"],previous["featureFlags"])
        self.assertEqual(current["storage"],previous["storage"])
        manifest.check_transition(current,previous,previous)
        with self.assertRaisesRegex(ValueError,"identical schema versions"):
            manifest.check_transition(previous,current,rollback=True)
        with self.assertRaisesRegex(ValueError,"identical schema versions"):
            manifest.compatible(current,previous)

    def test_calibrated_context_schema_rejects_old_writer_rollback(self):
        previous=self.modern()
        previous["version"]="v0.3.20"
        previous["source"]["ref"]="refs/heads/release/v0.3.20"
        previous["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":9,"gateway":2}
        current=copy.deepcopy(previous)
        current["version"]="v0.3.22"
        current["source"]["ref"]="refs/heads/release/v0.3.22"
        current["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":10,"gateway":2}
        self.assertEqual(current["schema"]["agent_runtime"],10)
        manifest.validate(previous)
        manifest.check_transition(current,previous,previous)
        with self.assertRaisesRegex(ValueError,"identical schema versions"):
            manifest.check_transition(previous,current,rollback=True)
        # deploy.sh restore_previous uses this same compatibility entry point.
        with self.assertRaisesRegex(ValueError,"identical schema versions"):
            manifest.compatible(current,previous)

    def test_web_search_schema_rejects_old_writer_and_gateway_rollback(self):
        previous=self.modern()
        previous["version"]="v0.3.22"
        previous["source"]["ref"]="refs/heads/release/v0.3.22"
        previous["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":10,"gateway":2}
        current=copy.deepcopy(previous)
        current["version"]="v0.3.23"
        current["source"]["ref"]="refs/heads/release/v0.3.23"
        current["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":11,"gateway":3}
        self.assertEqual(current["schema"]["agent_runtime"],11)
        self.assertEqual(current["schema"]["gateway"],3)
        manifest.validate(previous)
        manifest.check_transition(current,previous,previous)
        with self.assertRaisesRegex(ValueError,"identical schema versions"):
            manifest.check_transition(previous,current,rollback=True)
        with self.assertRaisesRegex(ValueError,"identical schema versions"):
            manifest.compatible(current,previous)

    def test_schedule_schema_rejects_old_writer_rollback(self):
        previous=self.modern()
        previous["version"]="v0.3.23"
        previous["source"]["ref"]="refs/heads/release/v0.3.23"
        previous["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":11,"gateway":3}
        current=copy.deepcopy(previous)
        current["version"]="v0.3.24"
        current["source"]["ref"]="refs/heads/release/v0.3.24"
        current["schema"]={"identity":3,"chat":5,"agent_control":1,"agent_runtime":13,"gateway":3}
        self.assertEqual(current["schema"]["agent_runtime"],13)
        self.assertEqual(current["schema"]["gateway"],3)
        manifest.validate(previous)
        manifest.check_transition(current,previous,previous)
        with self.assertRaisesRegex(ValueError,"identical schema versions"):
            manifest.check_transition(previous,current,rollback=True)
        with self.assertRaisesRegex(ValueError,"identical schema versions"):
            manifest.compatible(current,previous)

    def test_multi_model_gateway_and_control_schema_rejects_image_only_rollback(self):
        previous = self.modern()
        previous["version"] = "v0.3.30"
        previous["source"]["ref"] = "refs/heads/release/v0.3.30"
        previous["schema"] = {"identity": 3, "chat": 6, "agent_control": 1, "agent_runtime": 13, "gateway": 3}
        current = self.modern()
        current["version"] = "v0.3.31"
        current["source"]["ref"] = "refs/heads/release/v0.3.31"
        current["schema"]["agent_control"] = 2
        current["schema"]["gateway"] = 5
        self.assertEqual(current["schema"]["agent_control"], 2)
        self.assertEqual(current["schema"]["gateway"], 5)
        manifest.validate(previous)
        manifest.check_transition(current, previous, previous)
        with self.assertRaisesRegex(ValueError, "identical schema versions"):
            manifest.check_transition(previous, current, rollback=True)
        with self.assertRaisesRegex(ValueError, "identical schema versions"):
            manifest.compatible(current, previous)

    def test_agent_creation_hash_schema_rejects_image_only_rollback(self):
        previous = self.modern()
        previous["schema"]["agent_control"] = 2
        current = self.modern()
        self.assertEqual(current["schema"]["agent_control"], 3)
        manifest.check_transition(current, previous, previous)
        with self.assertRaisesRegex(ValueError, "identical schema versions"):
            manifest.check_transition(previous, current, rollback=True)

    def test_direct_message_schema_fences_previous_writer_and_allows_same_schema_rollback(self):
        previous = self.modern()
        previous["schema"]["agent_runtime"] = 12
        current = self.modern()
        self.assertEqual(current["schema"]["agent_runtime"], 13)
        with self.assertRaisesRegex(ValueError, "identical schema versions"):
            manifest.compatible(current, previous)
        manifest.compatible(current, copy.deepcopy(current))

    def test_conversation_mutation_schema_rejects_image_only_rollback(self):
        previous = self.modern()
        previous["schema"]["chat"] = 5
        current = self.modern()
        current["schema"]["chat"] = 6
        self.assertEqual(current["schema"]["chat"], 6)
        with self.assertRaisesRegex(ValueError, "identical schema versions"):
            manifest.compatible(current, previous)
        manifest.compatible(current, copy.deepcopy(current))

    def test_reactions_and_notifications_schema_rejects_image_only_rollback(self):
        previous = self.modern()
        previous["schema"]["chat"] = 6
        current = self.modern()
        current["schema"]["chat"] = 7
        self.assertEqual(current["schema"]["chat"], 7)
        with self.assertRaisesRegex(ValueError, "identical schema versions"):
            manifest.compatible(current, previous)
        manifest.compatible(current, copy.deepcopy(current))

    def test_provider_multi_model_gateway_schema_rejects_image_only_rollback(self):
        previous = self.modern()
        previous["schema"]["gateway"] = 5
        current = self.modern()
        self.assertEqual(current["schema"]["gateway"], 6)
        manifest.check_transition(current, previous, previous)
        with self.assertRaisesRegex(ValueError, "identical schema versions"):
            manifest.check_transition(previous, current, rollback=True)
        with self.assertRaisesRegex(ValueError, "identical schema versions"):
            manifest.compatible(current, previous)
        manifest.compatible(current, copy.deepcopy(current))

    def test_marketplace_schema_rejects_image_only_rollback(self):
        previous = self.modern()
        previous["schema"]["chat"] = 7
        current = self.modern()
        current["schema"]["chat"] = 8
        self.assertEqual(current["schema"]["chat"], 8)
        with self.assertRaisesRegex(ValueError, "identical schema versions"):
            manifest.compatible(current, previous)
        manifest.compatible(current, copy.deepcopy(current))

    def test_developer_marketplace_schema_rejects_image_only_rollback(self):
        previous = self.modern()
        previous["schema"]["chat"] = 8
        current = self.modern()
        self.assertEqual(current["schema"]["chat"], 9)
        with self.assertRaisesRegex(ValueError, "identical schema versions"):
            manifest.compatible(current, previous)
        manifest.compatible(current, copy.deepcopy(current))

    def test_v3_marketplace_service_forward_upgrade_and_rollback_guard(self):
        v2 = self.modern()
        v2["version"] = "v0.4.9"
        v2["source"]["ref"] = "refs/heads/release/v0.4.9"
        v3 = copy.deepcopy(v2)
        v3["manifestVersion"] = 3
        v3["version"] = "v0.5.0"
        v3["source"]["ref"] = "refs/heads/release/v0.5.0"
        v3["schema"] = dict(manifest.SCHEMA_V3)
        v3["images"]["marketplace"] = "ghcr.io/zzqdeco/teamie-marketplace@sha256:" + "2" * 64
        manifest.validate(v2)
        manifest.validate(v3)
        self.assertEqual(len(manifest.image_env(v3).splitlines()), 13)
        self.assertIn("MARKETPLACE_IMAGE=", manifest.image_env(v3))
        manifest.check_transition(v3, v2, v2)
        with self.assertRaisesRegex(ValueError, "identical schema versions"):
            manifest.check_transition(v2, v3, rollback=True)
        with self.assertRaisesRegex(ValueError, "lower database schema"):
            manifest.check_transition(v2, v3)


class ProductionConfigTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.env = Path(self.directory.name) / "production.env"
        self.env.touch(mode=0o600)
        self.config = {"services": {
            "identity": {"environment": {"APP_ORIGIN": "https://192.0.2.1", "COOKIE_SECURE": "true",
                "BOOTSTRAP_INVITE_CODE": "teamie-bootstrap", "DATABASE_URL": "postgres://teamie_identity:" + "i" * 64 + "@postgres:5432/teamie_identity?sslmode=disable"}},
            "chat": {"environment": {"APP_ORIGIN": "https://192.0.2.1", "COOKIE_SECURE": "true",
                "DATABASE_URL": "postgres://teamie_chat:" + "c" * 64 + "@postgres:5432/teamie_chat?sslmode=disable"}},
            "realtime": {"environment": {"APP_ORIGIN": "https://192.0.2.1", "COOKIE_SECURE": "true"}},
            "caddy": {"environment": {"SITE_ADDRESS": "https://192.0.2.1", "TLS_SERVER_NAME": "192.0.2.1"}},
            "postgres": {"environment": {"POSTGRES_PASSWORD": "a" * 64, "POSTGRES_IDENTITY_PASSWORD": "i" * 64, "POSTGRES_CHAT_PASSWORD": "c" * 64}}
        }}

    def tearDown(self): self.directory.cleanup()

    def test_valid_secure_private_config(self):
        self.assertEqual(manifest.validate_config(self.config, self.env), "https://192.0.2.1")

    def test_no_sni_certificate_name_must_match_public_origin(self):
        for value in (None, "127.0.0.1", "https://192.0.2.1", "192.0.2.1:443"):
            candidate = copy.deepcopy(self.config)
            if value is None:
                del candidate["services"]["caddy"]["environment"]["TLS_SERVER_NAME"]
            else:
                candidate["services"]["caddy"]["environment"]["TLS_SERVER_NAME"] = value
            with self.assertRaises(ValueError): manifest.validate_config(candidate, self.env)

    def test_bootstrap_invite_accepts_default_and_short_configured_code(self):
        for code in ("teamie-bootstrap", "custom-code"):
            candidate = copy.deepcopy(self.config)
            candidate["services"]["identity"]["environment"]["BOOTSTRAP_INVITE_CODE"] = code
            manifest.validate_config(candidate, self.env)
        candidate = copy.deepcopy(self.config)
        del candidate["services"]["identity"]["environment"]["BOOTSTRAP_INVITE_CODE"]
        manifest.validate_config(candidate, self.env)

    def test_bootstrap_invite_cannot_resolve_to_blank(self):
        for code in ("", "   "):
            candidate = copy.deepcopy(self.config)
            candidate["services"]["identity"]["environment"]["BOOTSTRAP_INVITE_CODE"] = code
            with self.assertRaises(ValueError): manifest.validate_config(candidate, self.env)

    def test_database_credentials_remain_long_and_independent(self):
        candidate = copy.deepcopy(self.config)
        candidate["services"]["postgres"]["environment"]["POSTGRES_PASSWORD"] = "short"
        with self.assertRaises(ValueError): manifest.validate_config(candidate, self.env)
        candidate = copy.deepcopy(self.config)
        pg = candidate["services"]["postgres"]["environment"]
        pg["POSTGRES_CHAT_PASSWORD"] = pg["POSTGRES_IDENTITY_PASSWORD"]
        candidate["services"]["chat"]["environment"]["DATABASE_URL"] = "postgres://teamie_chat:" + "i" * 64 + "@postgres:5432/teamie_chat?sslmode=disable"
        with self.assertRaises(ValueError): manifest.validate_config(candidate, self.env)

    def test_rejects_nonsecure_cookies_cross_service_db_access_and_public_ports(self):
        for mutate in (
            lambda c: c["services"]["identity"]["environment"].update(COOKIE_SECURE="false"),
            lambda c: c["services"]["chat"]["environment"].update(DATABASE_URL=c["services"]["identity"]["environment"]["DATABASE_URL"]),
            lambda c: c["services"]["postgres"].update(ports=[{"target": 5432, "published": "5432"}]),
        ):
            candidate = copy.deepcopy(self.config); mutate(candidate)
            with self.assertRaises(ValueError): manifest.validate_config(candidate, self.env)

    def test_production_secret_file_must_be_private(self):
        self.env.chmod(0o644)
        with self.assertRaises(ValueError): manifest.validate_config(self.config, self.env)


if __name__ == "__main__": unittest.main()
