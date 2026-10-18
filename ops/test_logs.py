import importlib.util
from pathlib import Path
import unittest
import json
import os
import subprocess
import sys
import tempfile

spec = importlib.util.spec_from_file_location("teamie_logs", Path(__file__).with_name("logs.py"))
logs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(logs)


class LogLookupTests(unittest.TestCase):
    def test_cli_filters_real_subprocess_output_and_skips_legacy_lines(self):
        with tempfile.TemporaryDirectory() as root:
            folder = Path(root)
            docker = folder / "docker"
            docker.write_text("""#!/usr/bin/env python3
import json,sys
if sys.argv[1]=='ps':
 print('fixture-container agent-runtime')
else:
 print('legacy dependency warning',file=sys.stderr)
 print(json.dumps({'trace.id':'1234567890abcdef1234567890abcdef','custom.runtime.run_id':'run-a'}))
 print(json.dumps({'trace.id':'other-trace','custom.runtime.run_id':'run-ab'}))
""")
            docker.chmod(0o700)
            result = subprocess.run([sys.executable, str(Path(__file__).with_name("logs.py")), "--run", "run-a"], env={**os.environ, "PATH": str(folder) + os.pathsep + os.environ["PATH"]}, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            events = [json.loads(line) for line in result.stdout.splitlines()]
            self.assertEqual(len(events), 1)
            self.assertEqual(events[0]["custom.runtime.run_id"], "run-a")
            self.assertNotIn("legacy", result.stdout)

    def test_exact_matching_and_ecs_serialization_variants(self):
        flat = {"trace.id": "trace-a", "custom.runtime.run_id": "run-a"}
        nested = {"trace": {"id": "trace-a"}, "custom": {"runtime": {"run_id": "run-a"}}}
        for event in (flat, nested):
            self.assertTrue(logs.matches(event, {"trace": "trace-a", "run": "run-a", "agent": None}))
            self.assertFalse(logs.matches(event, {"run": "run"}))
            self.assertFalse(logs.matches(event, {"task": "run-a"}))

    def test_missing_or_nonobject_fields_do_not_match(self):
        self.assertFalse(logs.matches({}, {"trace": "id"}))
        self.assertFalse(logs.matches({"custom": "body"}, {"run": "id"}))

    def test_event_lookup_joins_scalar_ingestion_and_run_start_arrays(self):
        ingestion = {"custom.input.event_id": "event-a"}
        run_start = {"custom.input.event_ids": ["event-a", "event-b"], "custom.runtime.run_id": "run-a"}
        nested = {"custom": {"input": {"event_ids": ["event-a", "event-b"]}}}
        for event in (ingestion, run_start, nested):
            self.assertTrue(logs.matches(event, {"event": "event-a"}))
            self.assertFalse(logs.matches(event, {"event": "event"}))
        self.assertTrue(logs.matches(run_start, {"event": "event-b", "run": "run-a"}))
        self.assertFalse(logs.matches(run_start, {"event": "event-b", "run": "other-run"}))
        self.assertFalse(logs.matches({"custom.input.event_ids": "event-a"}, {"event": "event"}))

    def test_rejects_unbounded_or_commandlike_cli_arguments(self):
        for args in (("--tail", "100001"), ("--since", "$(private-command)"), ("--trace", "not-a-trace"), ("--service", "agent-runtime;command")):
            with self.assertRaises(SystemExit):
                logs.main(args)


if __name__ == "__main__":
    unittest.main()
