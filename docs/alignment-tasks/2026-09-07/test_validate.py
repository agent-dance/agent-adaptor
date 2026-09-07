"""Fault-injection checks for the dispatch validator; fixtures are not reports."""

import copy
from pathlib import Path
import tempfile
import unittest

import validate


class DispatchValidationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.original = validate.load_package()

    def setUp(self):
        self.data = copy.deepcopy(self.original)

    def assert_package_rejects(self, text):
        errors = validate.package_errors(self.data, verify_files=False)
        self.assertTrue(any(text in error for error in errors), errors)

    def fixture_result(self, tid="C01"):
        """Synthetic metadata for validation tests only, never written to handoffs."""
        task = self.data["tasks"][tid]
        first, last = "1" * 40, "2" * 40
        state = validate.read_json(validate.PACKAGE / "execution-state.template.json")
        state["seed_head"], state["implementation_head"] = first, last
        for row in state["tasks"].values():
            row.update(status="complete", attempt=1, base_head=first, head_sha=last,
                       result_artifact="unit-fixture-only/result.json")
        evaluate = lambda rid: {"id": rid, "status": "passed",
                                "evidence": ["unit-fixture-only"], "note": ""}
        checks = []
        for row in task["validation"]:
            checks.append({
                "id": row["id"], "command": row["command"], "cwd": "repository_root",
                "environment": dict(row["environment"]), "os": "linux",
                "go_version": "unit-fixture-only", "cli_version": "unit-fixture-only",
                "head_sha": last, "status": "passed", "exit_code": 0,
                "executed_test_count": 1, "skipped_required_tests": 0,
                "permitted_skips": [], "log_path": "unit-fixture-only/log", "reason": "",
            })
        report = {
            "schema_version": "1.0", "task_id": tid, "status": "complete", "attempt": 1,
            "base_head": first if task["base"]["predecessor_gate"] is None else last,
            "head_sha": last, "commits": [], "changed_files": [],
            "acceptance": [evaluate(row["id"]) for row in task["acceptance_criteria"]],
            "requirements": [evaluate(rid) for rid in task["handoff"]["requirement_ids"]],
            "checks": checks, "artifacts": [{
                "kind": "documentation", "path": task["documentation"]["fragment"],
                "sha256": "a" * 64,
            }], "findings": [], "remaining_items": [],
            "documentation": {"fragment": task["documentation"]["fragment"], "ready": True,
                              "targets": [], "centralized_by_gate": task["documentation"]["central_owner"]},
            "live_authorization": None, "gate_details": None, "summary": "unit-fixture-only",
        }
        return report, state

    def test_package_passes_with_real_files(self):
        self.assertEqual(validate.package_errors(self.data), [])

    def test_future_directory_overlap(self):
        self.data["tasks"]["T02"]["ownership"]["allow"].append("CLAUDE/future/new.go")
        self.assert_package_rejects("write overlap")

    def test_directory_prefix_does_not_match_sibling(self):
        self.assertFalse(validate.overlaps("claude/**", "claude-other/new.go"))
        self.assertTrue(validate.overlaps("claude/**", "claude/subdir/**"))
        self.assertTrue(validate.overlaps("CLAUDE/x.go", "claude/x.go"))

    def test_same_batch_dependency_rejected(self):
        self.data["tasks"]["T02"]["depends_on"].append("T01")
        self.assert_package_rejects("same-batch worker dependency")

    def test_dependency_cycle_rejected(self):
        self.data["tasks"]["C01"]["depends_on"].append("G06")
        self.assert_package_rejects("dependency cycle")

    def test_future_artifact_not_accepted(self):
        self.data["tasks"]["T01"]["context"]["dependency_artifacts"].append({
            "producer": "T14", "path": "claude/later.go", "version_source": "unit-fixture",
        })
        self.assert_package_rejects("artifact not from prerequisite")

    def test_missing_coverage_owner(self):
        self.data["coverage"]["requirements"][0]["implementation_owner"] = "T99"
        self.assert_package_rejects("missing implementation owner")

    def test_author_cannot_self_verify(self):
        req = self.data["coverage"]["requirements"][0]
        req["verification_tasks"] = [req["implementation_owner"]]
        self.assert_package_rejects("independent verifier")

    def test_verifier_must_receive_original_scope(self):
        self.data["tasks"]["T21"]["work_items"].remove("W13")
        self.assert_package_rejects("verifier lacks source work item")

    def test_lost_history_commit(self):
        self.data["coverage"]["history"].pop()
        self.assert_package_rejects("history commit missing")

    def test_plan_excerpt_drift(self):
        self.data["tasks"]["T01"]["context"]["embedded_plan_sections"]["W01"] += "\nchanged"
        self.assert_package_rejects("embedded plan drift")

    def test_unknown_fields_and_duplicate_json_keys(self):
        self.data["tasks"]["T01"]["execute_anything"] = True
        self.assert_package_rejects("unknown field")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "duplicate.json"
            path.write_text('{"id":"C01","id":"T01"}', encoding="utf-8")
            with self.assertRaises(ValueError):
                validate.read_json(path)

    def test_task_hash_drift(self):
        self.data["manifest"]["tasks"][0]["sha256"] = "0" * 64
        self.assertTrue(any("hash drift" in e for e in validate.package_errors(self.data)))

    def test_scope_traversal_and_glob_rejected(self):
        for path in ("../outside", "/absolute", "C:/escape", "a//b", "a/./b", "a/*.go"):
            self.assertFalse(validate.valid_scope(path), path)

    def test_complete_metadata_accepted(self):
        report, state = self.fixture_result()
        self.assertEqual(validate.result_errors(self.data, report, state), [])

    def test_complete_requires_state(self):
        report, _ = self.fixture_result()
        self.assertTrue(any("requires --state" in e for e in validate.result_errors(self.data, report)))

    def test_complete_cannot_omit_acceptance(self):
        report, state = self.fixture_result()
        report["acceptance"].pop()
        self.assertTrue(any("coverage" in e for e in validate.result_errors(self.data, report, state)))

    def test_report_cannot_modify_peer(self):
        report, state = self.fixture_result()
        report["changed_files"] = ["claude/run.go"]
        self.assertTrue(any("outside ownership" in e for e in validate.result_errors(self.data, report, state)))

    def test_wrong_base_and_failed_dependency(self):
        report, state = self.fixture_result("T01")
        report["base_head"] = "3" * 40
        state["tasks"]["G00"]["status"] = "blocked"
        errors = validate.result_errors(self.data, report, state)
        self.assertTrue(any("not based" in e for e in errors))
        self.assertTrue(any("dependencies not complete" in e for e in errors))

    def test_zero_tests_and_changed_command(self):
        report, state = self.fixture_result("T01")
        report["checks"][0]["executed_test_count"] = 0
        report["checks"][1]["command"] += " -run DoesNotExist"
        errors = validate.result_errors(self.data, report, state)
        self.assertTrue(any("zero tests" in e for e in errors))
        self.assertTrue(any("command changed" in e for e in errors))

    def test_live_authorization_and_frozen_head_required(self):
        report, state = self.fixture_result("T27")
        report["head_sha"] = "3" * 40
        errors = validate.result_errors(self.data, report, state)
        self.assertTrue(any("authorization evidence absent" in e for e in errors))
        self.assertTrue(any("frozen implementation SHA" in e for e in errors))

    def test_native_platform_not_cross_compile(self):
        report, state = self.fixture_result("T26")
        self.assertTrue(any("wrong native platform" in e for e in validate.result_errors(self.data, report, state)))

    def test_required_skip_and_environment_mismatch(self):
        report, state = self.fixture_result("T01")
        report["checks"][0]["skipped_required_tests"] = 1
        report["checks"][0]["environment"]["AGENT_ADAPTOR_LIVE_CONFORMANCE"] = "1"
        errors = validate.result_errors(self.data, report, state)
        self.assertTrue(any("scenarios were skipped" in e for e in errors))
        self.assertTrue(any("wrong environment" in e for e in errors))

    def test_blocked_requires_actionable_remainder(self):
        report, _ = self.fixture_result()
        report.update(status="blocked", base_head=None, head_sha=None, checks=[])
        self.assertTrue(any("actionable" in e for e in validate.result_errors(self.data, report)))
        report["remaining_items"] = ["Await actual contract review evidence."]
        self.assertEqual(validate.result_errors(self.data, report), [])

    def test_release_readiness_cannot_ignore_requirements(self):
        _, state = self.fixture_result()
        state["release_readiness"] = True
        self.assertTrue(any("unverified requirements" in e for e in validate.state_errors(self.data, state)))


if __name__ == "__main__":
    unittest.main()
