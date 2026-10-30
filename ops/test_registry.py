import copy
import datetime as dt
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock


OPS = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("registry_manifest", OPS / "manifest.py")
manifest = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(manifest)

PUBLIC = "crpi-example123.cn-shenzhen.personal.cr.aliyuncs.com"
PRIVATE = "crpi-example123-vpc.cn-shenzhen.personal.cr.aliyuncs.com"
NAMESPACE = "teamie-production"
ACR_USER = "teamie-pull@1234567890123456.onaliyun.com"
ACR_PASSWORD = "fake-acr-password-with-$-quotes-'\""
GHCR_USER = "teamie-ci"
GHCR_PASSWORD = "fake-legacy-ghcr-token"


def registry(public=PUBLIC, pull=PRIVATE, namespace=NAMESPACE):
    return {"provider": "acr", "pushHost": public, "pullHost": pull, "namespace": namespace}


def release(acr=True, release_version="v0.5.0", sha="a" * 40):
    prefix = PRIVATE + "/" + NAMESPACE + "/teamie-" if acr else "ghcr.io/zzqdeco/teamie-"
    value = {
        "manifestVersion": 3, "version": release_version,
        "source": {"repository": "zzqDeco/teamie", "sha": sha, "mainSha": sha,
                   "ref": "refs/heads/release/" + release_version},
        "createdAt": dt.datetime.now(dt.timezone.utc).isoformat(),
        "schema": dict(manifest.SCHEMA_V3), "storage": {"postgresMajor": 17},
        "featureFlags": {"revision": "agents-v2", "sha256": "f" * 64},
        "images": {name: prefix + name + "@sha256:" + "1" * 64 for name in manifest.SERVICES_V3},
    }
    if acr:
        value["registry"] = registry()
    return value


def credentials():
    return {"acr": {"username": ACR_USER, "password": ACR_PASSWORD},
            "ghcr": {"username": GHCR_USER, "password": GHCR_PASSWORD}}


def production_config(data):
    services = {name: {"image": image, "environment": {}}
                for name, image in data["images"].items() if name != "sandbox-runner"}
    origin = "https://teamie.example.com"
    for name in ("identity", "chat", "realtime", "marketplace", "agent-control", "llm-gateway"):
        if name in services:
            services[name]["environment"].update(APP_ORIGIN=origin, COOKIE_SECURE="true")
    services["caddy"]["environment"].update(SITE_ADDRESS=origin, TLS_SERVER_NAME="teamie.example.com")
    pg = services["postgres"]["environment"]
    pg["POSTGRES_PASSWORD"] = "a" * 64
    db_specs = [("identity", "identity", "i"), ("chat", "chat", "c"),
                ("agent-control", "agent_control", "p"),
                ("agent-runtime", "agent_runtime", "r"),
                ("llm-gateway", "gateway", "g")]
    if "marketplace" in services:
        db_specs.append(("marketplace", "marketplace", "m"))
        services["marketplace"]["networks"] = ["private", "egress"]
        services["chat"]["networks"] = ["private"]
    for name, suffix, char in db_specs:
        password = char * 64
        pg["POSTGRES_" + suffix.upper() + "_PASSWORD"] = password
        services[name]["environment"]["DATABASE_URL"] = (
            "postgres://teamie_" + suffix + ":" + password + "@postgres:5432/teamie_" + suffix)
    runtime = services["agent-runtime"]["environment"]
    for key, source, char in (("AGENT_RUNTIME_TOKEN", "agent-control", "t"),
                              ("IM_PRINCIPAL_RUNTIME_TOKEN", "identity", "u"),
                              ("GATEWAY_RUNTIME_TOKEN", "llm-gateway", "v"),
                              ("SANDBOX_RUNTIME_TOKEN", "sandbox", "w")):
        runtime[key] = services[source]["environment"][key] = char * 64
    for name in ("agent-control", "identity", "chat"):
        services[name]["environment"]["IM_PRINCIPAL_CONTROL_TOKEN"] = "x" * 64
    services["llm-gateway"]["environment"]["GATEWAY_MASTER_KEY"] = "b" * 64
    services["llm-gateway"]["networks"] = ["private", "egress"]
    services["sandbox"]["environment"]["SANDBOX_RUNNER_IMAGE"] = data["images"]["sandbox-runner"]
    return {"services": services}


def executable(path, content):
    path.write_text(content)
    path.chmod(0o700)


class RegistryManifestTest(unittest.TestCase):
    def test_official_public_and_matching_vpc_hosts_across_regions(self):
        for public, private in (
            ("registry.cn-shenzhen.aliyuncs.com", "registry-vpc.cn-shenzhen.aliyuncs.com"),
            (PUBLIC, PRIVATE),
            ("teamie-registry.cn-shenzhen.cr.aliyuncs.com", "teamie-registry-vpc.cn-shenzhen.cr.aliyuncs.com"),
            ("teamie-2-registry.eu-central-1.cr.aliyuncs.com", "teamie-2-registry-vpc.eu-central-1.cr.aliyuncs.com"),
            ("crpi-a1.us-west-1.personal.cr.aliyuncs.com", "crpi-a1-vpc.us-west-1.personal.cr.aliyuncs.com"),
        ):
            for pull in (public, private):
                with self.subTest(public=public, pull=pull):
                    self.assertEqual(manifest.validate_registry(registry(public, pull)), registry(public, pull))

    def test_mismatched_or_untrusted_registry_metadata_is_rejected(self):
        mutations = [
            {"pullHost": PRIVATE.replace("shenzhen", "hangzhou")},
            {"pullHost": PRIVATE.replace("example123", "other")},
            {"pullHost": "registry-vpc.cn-shenzhen.aliyuncs.com"},
            {"pushHost": PRIVATE}, {"pushHost": "https://" + PUBLIC},
            {"pushHost": PUBLIC + ":443"}, {"pushHost": PUBLIC + ".evil.example"},
            {"pushHost": PUBLIC.upper()}, {"pushHost": "a" * 55 + "-registry.cn-shenzhen.cr.aliyuncs.com"},
            {"namespace": "../teamie"}, {"namespace": "teamie/other"}, {"namespace": "Teamie"},
            {"namespace": "teamie;echo"}, {"namespace": "a" * 64}, {"namespace": ""},
            {"provider": "ghcr"}, {"extra": "ignored"},
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                manifest.validate_registry({**registry(), **mutation})
        for missing in registry():
            with self.subTest(missing=missing), self.assertRaises(ValueError):
                value = registry(); del value[missing]
                manifest.validate_registry(value)

    def test_acr_uses_only_selected_pull_host_namespace_and_fixed_repositories(self):
        data = release()
        manifest.validate(data)
        self.assertTrue(all(line.split("=", 1)[1].startswith(PRIVATE + "/" + NAMESPACE + "/teamie-")
                            for line in manifest.image_env(data).splitlines()))
        original = data["images"]["sandbox-runner"]
        for bad in (original.replace(PRIVATE, PUBLIC), original.replace(NAMESPACE, "other"),
                    original.replace("teamie-sandbox-runner", "arbitrary-runner"),
                    original.replace("1" * 64, "2" * 63), original.replace("@sha256:", ":latest@sha256:")):
            with self.subTest(image=bad), self.assertRaises(ValueError):
                changed = copy.deepcopy(data); changed["images"]["sandbox-runner"] = bad
                manifest.validate(changed)

    def test_legacy_manifests_keep_exact_ghcr_allowlist_and_can_be_rollback_targets(self):
        old = release(False, "v0.3.0", "b" * 40)
        current = release()
        manifest.check_transition(old, current, rollback=True)
        for host in (PRIVATE, "docker.io", "ghcr.io.evil.example"):
            with self.subTest(host=host), self.assertRaises(ValueError):
                changed = copy.deepcopy(old)
                changed["images"]["chat"] = changed["images"]["chat"].replace("ghcr.io", host)
                manifest.validate(changed)
        v2 = copy.deepcopy(old)
        v2["manifestVersion"] = 2
        v2["schema"] = dict(manifest.SCHEMA_V2)
        v2["images"] = {key: value for key, value in v2["images"].items() if key in manifest.SERVICES}
        manifest.validate(v2)
        legacy = copy.deepcopy(old)
        legacy["manifestVersion"] = 1
        legacy["schema"] = {"identity": 1, "chat": 1}
        legacy["images"] = {key: value for key, value in legacy["images"].items() if key in manifest.LEGACY_SERVICES}
        manifest.validate(legacy)
        legacy["registry"] = registry()
        with self.assertRaises(ValueError): manifest.validate(legacy)

    def test_existing_version_registry_metadata_is_immutable(self):
        old = release()
        changed = copy.deepcopy(old)
        changed["registry"]["pushHost"] = "registry.cn-shenzhen.aliyuncs.com"
        changed["registry"]["pullHost"] = "registry-vpc.cn-shenzhen.aliyuncs.com"
        changed["images"] = {name: image.replace(PRIVATE, changed["registry"]["pullHost"])
                             for name, image in changed["images"].items()}
        with self.assertRaises(ValueError): manifest.check_transition(changed, old, old)

    def test_cli_checks_config_before_push_and_create_requires_complete_options(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            flags = root / "flags.json"; flags.write_text('{"revision":"agents-v2","flags":{}}')
            data = release()
            for name, image in data["images"].items():
                (root / (name + ".json")).write_text(json.dumps({"service": name, "image": image}))
            args = ["--registry-public-host", PUBLIC, "--registry-pull-host", PRIVATE,
                    "--registry-namespace", NAMESPACE]
            checked = subprocess.run([sys.executable, str(OPS / "manifest.py"), "registry-check", *args], capture_output=True, text=True)
            self.assertEqual(checked.returncode, 0, checked.stderr)
            self.assertEqual(json.loads(checked.stdout), registry())
            command = [sys.executable, str(OPS / "manifest.py"), "create", "--repository", "zzqDeco/teamie",
                       "--version", "v0.4.0", "--sha", "a" * 40, "--main-sha", "a" * 40,
                       "--digests", str(root), "--flags", str(flags)]
            created = subprocess.run([*command, *args], capture_output=True, text=True)
            self.assertEqual(created.returncode, 0, created.stderr)
            self.assertEqual(json.loads(created.stdout)["registry"], registry())
            partial = subprocess.run([*command, *args[:2]], capture_output=True, text=True)
            self.assertNotEqual(partial.returncode, 0)
            self.assertIn("all three registry options", partial.stderr)
            for name, image in release(False)["images"].items():
                (root / (name + ".json")).write_text(json.dumps({"service": name, "image": image}))
            legacy = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(legacy.returncode, 0, legacy.stderr)
            self.assertNotIn("registry", json.loads(legacy.stdout))


class RegistryAuthTest(unittest.TestCase):
    def test_manifest_selects_host_and_credentials_without_provider_fallback(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"DOCKER_CONFIG": directory}), \
                mock.patch.object(manifest.subprocess, "run") as run:
            Path(directory).chmod(0o700)
            for acr, host, username, password in ((True, PRIVATE, ACR_USER, ACR_PASSWORD),
                                                   (False, "ghcr.io", GHCR_USER, GHCR_PASSWORD)):
                manifest.registry_login(release(acr), credentials())
                self.assertEqual(run.call_args.args[0], ["docker", "login", host, "--username=" + username, "--password-stdin"])
                self.assertEqual(run.call_args.kwargs["input"], password.encode())
                self.assertEqual(run.call_args.kwargs["stdout"], subprocess.DEVNULL)
                self.assertEqual(run.call_args.kwargs["stderr"], subprocess.DEVNULL)
            run.reset_mock()
            frame = credentials(); frame["ghcr"] = {"username": "", "password": ""}
            with self.assertRaises(ValueError): manifest.registry_login(release(False), frame)
            run.assert_not_called()
            manifest.registry_login(release(), frame)  # The unused provider may be unconfigured.

    def test_unsafe_aliases_frames_and_nonprivate_config_are_rejected(self):
        for username in ("--password-stdin", "a b", "a:b", "a\nother", "a;echo", "a$(echo)", ""):
            with self.subTest(username=username), self.assertRaises(ValueError):
                frame = credentials(); frame["acr"]["username"] = username
                manifest.registry_credentials(frame, "acr")
        for frame in ({"host": PRIVATE}, {"acr": {"username": "a", "password": "b", "host": PRIVATE}},
                      {"ghcr": {"username": "a", "password": "b"}}, {"acr": None}):
            with self.subTest(frame=frame), self.assertRaises(ValueError): manifest.registry_credentials(frame, "acr")
        for password in ("", "a\nb", "a\rb", "a\0b", "x" * 16385):
            with self.subTest(password_length=len(password)), self.assertRaises(ValueError):
                frame = credentials(); frame["acr"]["password"] = password
                manifest.registry_credentials(frame, "acr")
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"DOCKER_CONFIG": directory}), \
                mock.patch.object(manifest.subprocess, "run") as run:
            Path(directory).chmod(0o755)
            with self.assertRaises(ValueError): manifest.registry_login(release(), credentials())
            run.assert_not_called()

    def test_invalid_manifest_is_rejected_before_authentication(self):
        with mock.patch.object(manifest.subprocess, "run") as run:
            data = release(); data["registry"]["pullHost"] = "registry.evil.example"
            with self.assertRaises(ValueError): manifest.registry_login(data, credentials())
            data = release(); data["images"]["chat"] = data["images"]["chat"].replace(PRIVATE, PUBLIC)
            with self.assertRaises(ValueError): manifest.registry_login(data, credentials())
            run.assert_not_called()

    def test_authentication_failure_does_not_echo_docker_output_or_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = root / "release.json"; data.write_text(json.dumps(release()))
            docker = root / "docker"
            executable(docker, "#!/usr/bin/env python3\nimport sys\np=sys.stdin.read()\nprint(p)\nprint(p,file=sys.stderr)\nsys.exit(1)\n")
            result = subprocess.run([sys.executable, str(OPS / "manifest.py"), "registry-login", str(data)],
                                    input=json.dumps(credentials()), capture_output=True, text=True,
                                    env={**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"], "DOCKER_CONFIG": directory})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, "")
            self.assertIn("registry authentication failed", result.stderr)
            for secret in (ACR_PASSWORD, GHCR_PASSWORD, ACR_USER): self.assertNotIn(secret, result.stderr)


class RegistryConfigTest(unittest.TestCase):
    def test_selected_manifest_binds_every_service_and_exact_runner_digest(self):
        with tempfile.TemporaryDirectory() as directory:
            host_env = Path(directory) / "production.env"; host_env.touch(mode=0o600)
            for acr in (False, True):
                data = release(acr)
                config = production_config(data)
                manifest.validate_config(config, host_env, data)
                runner = config["services"]["sandbox"]["environment"]["SANDBOX_RUNNER_IMAGE"]
                for bad in (runner.replace("1" * 64, "2" * 64), runner.replace("teamie-sandbox-runner", "other"),
                            runner.replace(PRIVATE, PUBLIC) if acr else runner.replace("ghcr.io", PRIVATE)):
                    with self.subTest(acr=acr, runner=bad), self.assertRaises(ValueError):
                        changed = copy.deepcopy(config)
                        changed["services"]["sandbox"]["environment"]["SANDBOX_RUNNER_IMAGE"] = bad
                        manifest.validate_config(changed, host_env, data)
                changed = copy.deepcopy(config); changed["services"]["chat"]["image"] = data["images"]["identity"]
                with self.assertRaises(ValueError): manifest.validate_config(changed, host_env, data)
                changed = copy.deepcopy(config); del changed["services"]["redis"]
                with self.assertRaises(ValueError): manifest.validate_config(changed, host_env, data)
                if acr:
                    with self.assertRaises(ValueError): manifest.validate_config(config, host_env)
                else:
                    manifest.validate_config(config, host_env)


class RegistryTransportTest(unittest.TestCase):
    def test_ci_apply_and_rollback_use_json_stdin_and_unique_helper_bundle(self):
        for mode in ("apply", "rollback"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                bin_dir = root / "bin"; bin_dir.mkdir()
                shutil.copytree(OPS, root / "ops", ignore=shutil.ignore_patterns("__pycache__"))
                (root / "deploy").mkdir()
                (root / "release-manifest.json").write_text(json.dumps(release()))
                executable(bin_dir / "scp", '''#!/usr/bin/env python3
import json,os,pathlib,shutil,sys
p=pathlib.Path(os.environ["CAPTURE"])
(p/"scp.json").write_text(json.dumps(sys.argv[1:]))
shutil.copyfile(sys.argv[-2],p/"uploaded.tgz")
''')
                executable(bin_dir / "ssh", '''#!/usr/bin/env python3
import json,os,pathlib,sys
p=pathlib.Path(os.environ["CAPTURE"])
(p/"ssh.json").write_text(json.dumps(sys.argv[1:]))
(p/"stdin.json").write_bytes(sys.stdin.buffer.read())
''')
                result = subprocess.run(["bash", str(root / "ops/ci-deploy.sh"), mode], cwd=root, capture_output=True, text=True,
                                        env={**os.environ, "PATH": str(bin_dir) + os.pathsep + os.environ["PATH"],
                                             "CAPTURE": str(root), "PRODUCTION_SSH_HOST": "192.0.2.1", "PRODUCTION_SSH_USER": "teamie",
                                             "COPYFILE_DISABLE": "1",
                                             "PRODUCTION_SSH_KEY": "fake-private-key", "PRODUCTION_SSH_KNOWN_HOSTS": "fake-host-key",
                                             "GITHUB_RUN_ID": "12345", "GITHUB_RUN_ATTEMPT": "2", "RELEASE_VERSION": "v0.4.0",
                                             "RELEASE_SHA": "a" * 40, "ROLLBACK_VERSION": "v0.3.0", "EXPECTED_CURRENT_SHA": "a" * 40,
                                             "REGISTRY_USERNAME": ACR_USER, "REGISTRY_PASSWORD": ACR_PASSWORD,
                                             "LEGACY_GHCR_USER": GHCR_USER, "LEGACY_GHCR_TOKEN": GHCR_PASSWORD})
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads((root / "stdin.json").read_text()), credentials())
                ssh_args = json.loads((root / "ssh.json").read_text())
                command = ssh_args[-1]
                for secret in (ACR_USER, ACR_PASSWORD, GHCR_USER, GHCR_PASSWORD, "fake-private-key"):
                    self.assertNotIn(secret, command + result.stdout + result.stderr)
                private_key = Path(ssh_args[ssh_args.index("-i") + 1])
                self.assertFalse(private_key.parent.exists(), "ephemeral SSH key directory must be removed")
                with tarfile.open(root / "uploaded.tgz") as bundle:
                    members = bundle.getnames()
                self.assertTrue(any(name.endswith("ops/manifest.py") for name in members))
                if mode == "rollback":
                    helper = "rollback-v0.3.0-" + "a" * 40 + "-12345-2"
                    self.assertIn("/releases/" + helper + "/ops/deploy.sh' rollback 'v0.3.0' '" + "a" * 40, command)
                    self.assertNotIn("current/recipe/ops/deploy.sh", command)
                    self.assertTrue(all(name == "ops" or name.startswith("ops/") for name in members))
                else:
                    self.assertIn("/ops/deploy.sh' apply '/srv/teamie/releases/v0.4.0-", command)
                    self.assertIn("release-manifest.json", members)


class DeployRegistryTest(unittest.TestCase):
    def harness(self, root, target, current=None):
        host = root.resolve() / "teamie"
        (host / "state/history").mkdir(parents=True)
        (host / "config").mkdir()
        (host / "config/production.env").touch(mode=0o600)
        recipe = host / "releases/recipe"; recipe.mkdir(parents=True)
        (recipe / "release-manifest.json").write_text(json.dumps(target))
        flags = root / "flags.json"; flags.write_text('{"revision":"agents-v2","flags":{}}')
        tls = root / "issuer.caddy"; tls.write_text("tls { issuer acme }\n")
        config = production_config(target)
        config["services"]["identity"]["volumes"] = [{"source": str(flags), "target": "/etc/teamie/feature-flags.json"}]
        config["services"]["caddy"]["volumes"] = [{"source": str(tls), "target": "/etc/caddy/issuer.caddy"}]
        compose = root / "compose.json"; compose.write_text(json.dumps(config))
        if current:
            record = host / "state/current-record"; record.mkdir()
            (record / "release-manifest.json").write_text(json.dumps(current))
            (record / "recipe").symlink_to(recipe)
            (host / "current").symlink_to(record)
            (host / "state/history" / (target["version"] + ".json")).write_text(json.dumps(target))
        bin_dir = root / "bin"; bin_dir.mkdir()
        executable(bin_dir / "flock", "#!/bin/sh\nexit 0\n")
        executable(bin_dir / "stat", "#!/usr/bin/env python3\nimport os,sys\nprint(os.stat(sys.argv[-1]).st_uid)\n")
        executable(bin_dir / "docker", '''#!/usr/bin/env python3
import json,os,pathlib,sys
root=pathlib.Path(os.environ["CAPTURE"])
args=sys.argv[1:]
with (root/"docker.jsonl").open("a") as f: f.write(json.dumps(args)+"\\n")
if args[0]=="login":
    password=sys.stdin.buffer.read().decode()
    config=pathlib.Path(os.environ["DOCKER_CONFIG"])
    (config/"config.json").write_text("fake-docker-auth")
    (root/"login.json").write_text(json.dumps({"host":args[1],"username":args[2],"password":password,"config":str(config)}))
    if os.environ.get("FAIL_LOGIN")=="1":
        print(password,file=sys.stderr)
        sys.exit(1)
elif args[0]=="compose" and args[-3:]==["config","--format","json"]:
    sys.stdout.write(pathlib.Path(os.environ["FAKE_COMPOSE"]).read_text())
elif "pull" in args:
    sys.exit(42)  # Stop before rollout: these tests never run a real container.
else:
    sys.exit(99)
''')
        env = {**os.environ, "PATH": str(bin_dir) + os.pathsep + os.environ["PATH"],
               "TEAMIE_ROOT": str(host), "FAKE_COMPOSE": str(compose), "CAPTURE": str(root)}
        return host, recipe, env

    def test_apply_authenticates_only_validated_acr_and_cleans_auth_after_pull_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            current = release(False, "v0.3.0", "b" * 40)
            host, recipe, env = self.harness(root, release(), current)
            result = subprocess.run(["bash", str(OPS / "deploy.sh"), "apply", str(recipe)], input=json.dumps(credentials()),
                                    capture_output=True, text=True, env=env)
            self.assertEqual(result.returncode, 42, result.stderr)
            login = json.loads((root / "login.json").read_text())
            self.assertEqual((login["host"], login["username"], login["password"]), (PRIVATE, "--username=" + ACR_USER, ACR_PASSWORD))
            self.assertFalse(Path(login["config"]).exists())
            self.assertEqual(list((host / "state").glob("deploy-*")), [])
            self.assertEqual(json.loads((host / "current/release-manifest.json").read_text()), current)
            self.assertNotIn(ACR_PASSWORD, result.stdout + result.stderr)

    def test_legacy_rollback_uses_history_ghcr_credentials_and_current_recipe(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target, current = release(False, "v0.3.0", "b" * 40), release()
            host, recipe, env = self.harness(root, target, current)
            # The uploaded recipe manifest does not choose rollback images/provider.
            (recipe / "release-manifest.json").write_text(json.dumps(current))
            result = subprocess.run(["bash", str(OPS / "deploy.sh"), "rollback", "v0.3.0", "a" * 40],
                                    input=json.dumps(credentials()), capture_output=True, text=True, env=env)
            self.assertEqual(result.returncode, 42, result.stderr)
            login = json.loads((root / "login.json").read_text())
            self.assertEqual((login["host"], login["username"], login["password"]), ("ghcr.io", "--username=" + GHCR_USER, GHCR_PASSWORD))
            self.assertFalse(Path(login["config"]).exists())
            commands = [json.loads(line) for line in (root / "docker.jsonl").read_text().splitlines()]
            self.assertIn(str(recipe / "deploy/compose.production.yaml"), commands[0])
            self.assertEqual(json.loads((host / "current/release-manifest.json").read_text()), current)
            self.assertEqual(json.loads((host / "state/history/v0.3.0.json").read_text()), target)

    def test_rollback_sha_guard_and_missing_target_credentials_prevent_pulls(self):
        for wrong_sha in (False, True):
            with self.subTest(wrong_sha=wrong_sha), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                host, _, env = self.harness(root, release(False, "v0.3.0", "b" * 40), release())
                frame = credentials(); frame["ghcr"] = {"username": "", "password": ""}
                result = subprocess.run(["bash", str(OPS / "deploy.sh"), "rollback", "v0.3.0", ("c" if wrong_sha else "a") * 40],
                                        input=json.dumps(frame), capture_output=True, text=True, env=env)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((root / "login.json").exists())
                self.assertEqual(list((host / "state").glob("deploy-*")), [])
                if wrong_sha: self.assertIn("Current production SHA changed", result.stderr)
                else: self.assertIn("invalid registry username", result.stderr)

    def test_login_failure_redacts_output_and_removes_private_docker_config(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            host, recipe, env = self.harness(root, release(), release(False, "v0.3.0", "b" * 40))
            result = subprocess.run(["bash", str(OPS / "deploy.sh"), "apply", str(recipe)], input=json.dumps(credentials()),
                                    capture_output=True, text=True, env={**env, "FAIL_LOGIN": "1"})
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("registry authentication failed", result.stderr)
            self.assertNotIn(ACR_PASSWORD, result.stdout + result.stderr)
            self.assertEqual(list((host / "state").glob("deploy-*")), [])
            login = json.loads((root / "login.json").read_text())
            self.assertFalse(Path(login["config"]).exists())


if __name__ == "__main__": unittest.main()
