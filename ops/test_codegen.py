import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


OPS = Path(__file__).resolve().parent
BASH = shutil.which("bash")


class CodegenPrerequisitesTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "ops").mkdir()
        shutil.copy2(OPS / "codegen.sh", self.root / "ops/codegen.sh")
        self.bin = self.root / "bin"
        self.bin.mkdir()
        (self.bin / "dirname").symlink_to(shutil.which("dirname"))
        self.executable("go", 'printf "%s\\n" "$CODEGEN_TEST_GOPATH"')
        self.executable("node", 'printf "%s\\n" "$*" >> "$CODEGEN_TEST_CALLS"')
        self.calls = self.root / "calls"
        self.env = {
            **os.environ,
            "PATH": str(self.bin),
            "CODEGEN_TEST_GOPATH": str(self.root / "gopath"),
            "CODEGEN_TEST_CALLS": str(self.calls),
        }
        self.generated = self.root / "contracts/gen/im/kitex_gen/im/rpc.go"
        self.generated.parent.mkdir(parents=True)
        self.generated.write_text("existing generated bindings\n")

    def executable(self, name, body):
        path = self.bin / name
        path.write_text("#!/bin/sh\n" + body + "\n")
        path.chmod(0o700)

    def run_codegen(self, *args):
        return subprocess.run(
            [BASH, str(self.root / "ops/codegen.sh"), *args],
            cwd=self.root, env=self.env, text=True, capture_output=True,
        )

    def test_full_generation_requires_each_rpc_generator_before_writing(self):
        for missing, present in (("kitex", "thriftgo"), ("thriftgo", "kitex")):
            with self.subTest(missing=missing):
                self.executable(present, "exit 0")
                try:
                    result = self.run_codegen()
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(f"missing required codegen binary: {missing}", result.stderr)
                    self.assertFalse(self.calls.exists(), "must not run partial generation")
                    self.assertEqual(self.generated.read_text(), "existing generated bindings\n")
                finally:
                    (self.bin / present).unlink()

    def test_route_and_typescript_check_does_not_require_rpc_generators(self):
        result = self.run_codegen("--check")
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls.read_text().splitlines()
        self.assertEqual(len(calls), 2)
        self.assertTrue(all(call.endswith("index.mjs --check") for call in calls))


if __name__ == "__main__":
    unittest.main()
