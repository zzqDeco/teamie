import importlib.util
import json
import os
import shutil
from pathlib import Path
import subprocess
import tempfile
import unittest

SPEC=importlib.util.spec_from_file_location("test_manifest",Path(__file__).with_name("test_manifest.py"))
fixtures=importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fixtures)


class OperatorRecoveryTest(unittest.TestCase):
    def invoke(self,identity_schema,arguments):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            for name in ("state","current/recipe/deploy","config","bin"):
                (root/name).mkdir(parents=True,exist_ok=True)
            manifest=fixtures.AgentManifestTest().modern()
            manifest["schema"]["identity"]=identity_schema
            # Recovery must still work months after deployment; release freshness
            # applies to deployment admission, not operator account recovery.
            manifest["createdAt"]="2020-01-01T00:00:00+00:00"
            (root/"current/release-manifest.json").write_text(json.dumps(manifest))
            (root/"current/images.env").touch()
            (root/"config/production.env").touch(mode=0o600)
            (root/"current/recipe/deploy/compose.production.yaml").touch()
            called=root/"docker-arguments.json"
            docker=root/"bin/docker"
            docker.write_text("#!/usr/bin/env python3\nimport json,os,sys\nfrom pathlib import Path\nPath(os.environ['CALLS']).write_text(json.dumps(sys.argv[1:]))\nprint(json.dumps({'url':'https://192.0.2.1/reset-password#token=synthetic-only','expiresAt':'2030-01-01T01:00:00Z'}))\n")
            docker.chmod(0o700)
            if shutil.which("flock") is None:
                # Production and hosted CI are Linux and exercise real flock.
                # macOS uses this fixture only for wrapper argument validation.
                lock=root/"bin/flock"
                lock.write_text("#!/bin/sh\nexit 0\n")
                lock.chmod(0o700)
            result=subprocess.run(["bash",str(Path(__file__).with_name("password-reset-link.sh")),*arguments],env={**os.environ,"TEAMIE_ROOT":str(root),"PATH":str(root/"bin")+os.pathsep+os.environ["PATH"],"CALLS":str(called)},capture_output=True,text=True)
            calls=json.loads(called.read_text()) if called.exists() else None
            return result,calls

    def test_current_container_command_preserves_literal_account_argument(self):
        account="literal+$(should-not-execute)@example.invalid"
        result,calls=self.invoke(3,["--email",account])
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertEqual(calls[-7:],["exec","-T","identity","/app/identityctl","password-reset-link","--email",account])
        self.assertEqual(json.loads(result.stdout)["url"],"https://192.0.2.1/reset-password#token=synthetic-only")

    def test_old_identity_schema_never_invokes_a_container(self):
        result,calls=self.invoke(2,["--email","admin@example.invalid"])
        self.assertNotEqual(result.returncode,0)
        self.assertIsNone(calls)

    def test_missing_or_extra_arguments_never_invoke_a_container(self):
        for arguments in ([],["--email"],["--email",""],["--email","admin@example.invalid","--set-admin"]):
            with self.subTest(arguments=arguments):
                result,calls=self.invoke(3,arguments)
                self.assertEqual(result.returncode,2)
                self.assertIsNone(calls)


if __name__=="__main__":
    unittest.main()
