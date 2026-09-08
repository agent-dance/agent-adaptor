"""Fault injection for CI evidence acceptance, independent of Windows execution."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import subprocess

from windows_validation import CANCELLATION_REQUIRED, audit_cancellation_repetitions, audit_events, audit_example, execute_check


def event(action, test="TestRequired"):
    result = {"Action": action, "Package": "fixture"}
    if test:
        result["Test"] = test
    return result


class EvidenceTests(unittest.TestCase):
    def test_cancellation_repetitions_require_both_exact_roots_twenty_times(self):
        records = [{"Action": action, "Package": name.split(":")[0], "Test": name.split(":")[1]}
                   for name in CANCELLATION_REQUIRED for _ in range(20) for action in ("run", "pass")]
        self.assertTrue(audit_cancellation_repetitions(records)["accepted"])
        self.assertFalse(audit_cancellation_repetitions(records[:-2])["accepted"])
        self.assertFalse(audit_cancellation_repetitions([event("run"), event("pass")])["accepted"])

    def test_no_tests_is_not_success(self):
        self.assertFalse(audit_events([event("pass", None)])["accepted"])

    def test_package_failure_rejects_even_with_passed_test(self):
        self.assertFalse(audit_events([event("run"), event("pass"), event("fail", None)])["accepted"])

    def test_incomplete_test_rejects_even_with_other_pass(self):
        self.assertFalse(audit_events([event("run"), event("run", "TestOther"), event("pass", "TestOther")])["accepted"])

    def test_missing_required_root_rejects(self):
        self.assertFalse(audit_events([event("run"), event("pass")], ["fixture:TestOther"])["accepted"])

    def test_child_failure_cannot_be_hidden_by_parent_pass(self):
        self.assertFalse(audit_events([event("run"), event("run", "TestRequired/child"), event("fail", "TestRequired/child"), event("pass")])["accepted"])

    def test_unknown_skip_rejects(self):
        records = [event("run"), event("pass"), event("run", "TestOptional"), event("skip", "TestOptional")]
        self.assertFalse(audit_events(records)["accepted"])
        self.assertTrue(audit_events(records, ["fixture:TestRequired"], ["fixture:TestOptional"])["accepted"])

    def test_even_allowed_skip_cannot_satisfy_required_root(self):
        records = [event("run"), event("skip"), event("run", "TestOther"), event("pass", "TestOther")]
        self.assertFalse(audit_events(records, ["fixture:TestRequired"], ["fixture:TestRequired"])["accepted"])

    def test_duplicate_terminal_rejects(self):
        self.assertFalse(audit_events([event("run"), event("pass"), event("pass")])["accepted"])

    def test_actual_required_pass(self):
        self.assertTrue(audit_events([event("run"), event("pass")], ["fixture:TestRequired"])["accepted"])

    def test_codec_output_needs_changed_guard_and_roundtrip(self):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "codec.log"
            value = {"example": "threads/codec", "resume_id": "codex-session-42", "display_id": "codex-session-42", "guard": {"fingerprint": "first", "after_cwd_change": "first"}}
            log.write_text(json.dumps(value), encoding="utf-8")
            self.assertFalse(audit_example("T26-X01", log))
            value["guard"]["after_cwd_change"] = "second"
            log.write_text(json.dumps(value), encoding="utf-8")
            self.assertTrue(audit_example("T26-X01", log))

    def test_offline_output_needs_actual_effects(self):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "offline.log"
            log.write_text("built successfully\n", encoding="utf-8")
            self.assertFalse(audit_example("T26-X02", log))

    def test_source_audit_error_keeps_command_and_exit(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory)
            (out / "allowed-skips.json").write_text("{}", encoding="utf-8")
            process = mock.Mock()
            process.wait.return_value = 0
            def start(*args, **kwargs):
                kwargs["stdout"].write(b'{"Action":"run","Package":"fixture","Test":"TestExample"}\n{"Action":"pass","Package":"fixture","Test":"TestExample"}\n')
                return process
            with mock.patch("windows_validation.snapshot", side_effect=[{}, ValueError("source drift")]), mock.patch("windows_validation.subprocess.Popen", side_effect=start):
                result = execute_check("T26-X03", ["test", "-json", "-count=1", "./examples/..."], 1, "go", out, "frozen", {}, out, {})
            saved = json.loads((out / "T26-X03-command.json").read_text())
            self.assertEqual(saved, result)
            self.assertEqual(saved["exit_code"], 0)
            self.assertEqual(saved["status"], "failed")
            self.assertIn("source_audit_error", saved)

    def test_timeout_cleanup_error_keeps_known_exit_and_watchdog(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory)
            (out / "allowed-skips.json").write_text("{}", encoding="utf-8")
            process = mock.Mock(pid=123)
            process.wait.side_effect = [subprocess.TimeoutExpired("go", 1), 17]
            with mock.patch("windows_validation.snapshot", return_value={}), mock.patch("windows_validation.subprocess.Popen", return_value=process), mock.patch("windows_validation.subprocess.run", side_effect=subprocess.TimeoutExpired("taskkill", 30)):
                result = execute_check("T26-X03", ["test", "-json", "./examples/..."], 1, "go", out, "frozen", {}, out, {})
            saved = json.loads((out / "T26-X03-command.json").read_text())
            self.assertEqual(saved, result)
            self.assertTrue(saved["watchdog_triggered"])
            self.assertEqual(saved["exit_code"], 17)
            self.assertEqual(saved["status"], "failed")
            self.assertIn("error", saved["tree_cleanup"])

    def test_malformed_events_keep_successful_process_exit_as_failed_audit(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory)
            (out / "allowed-skips.json").write_text("{}", encoding="utf-8")
            process = mock.Mock()
            process.wait.return_value = 0
            def start(*args, **kwargs):
                kwargs["stdout"].write(b'{not JSON}\n')
                return process
            with mock.patch("windows_validation.snapshot", return_value={}), mock.patch("windows_validation.subprocess.Popen", side_effect=start):
                result = execute_check("T26-X03", ["test", "-json", "./examples/..."], 1, "go", out, "frozen", {}, out, {})
            self.assertEqual(result["exit_code"], 0)
            self.assertEqual(result["status"], "failed")
            self.assertIn("result_audit_error", result)
            self.assertTrue((out / "T26-X03-command.json").is_file())


if __name__ == "__main__":
    unittest.main()
