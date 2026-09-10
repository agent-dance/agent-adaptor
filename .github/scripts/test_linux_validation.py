"""Negative acceptance checks for actual Linux repetition and fuzz evidence."""
import unittest

from linux_validation import SCENARIOS, CONFORMANCE, audit_test


def events(name="TestOne", count=1):
    return [{"Package": "fixture", "Test": name, "Action": action}
            for _ in range(count) for action in ("run", "pass")]

def identified_events(identities):
    return [{"Package": identity.split(":")[0], "Test": identity.split(":")[1], "Action": action}
            for identity in identities for action in ("run", "pass")]


class LinuxAuditTests(unittest.TestCase):
    def test_repeat_requires_twenty_actual_completions(self):
        self.assertFalse(audit_test("T25-V04", events(count=19), (), ["test", "-count=20"])["accepted"])
        self.assertTrue(audit_test("T25-V04", events(count=20), (), ["test", "-count=20"])["accepted"])

    def test_fuzz_baseline_is_not_active_fuzzing(self):
        records = events("FuzzOne")
        records += [{"Action": "output", "Output": "gathering baseline coverage: 10/10 completed"}]
        self.assertFalse(audit_test("T25-V07", records, (), ["test", "-fuzz", "^FuzzOne$"])["accepted"])
        records += [{"Action": "output", "Output": "fuzz: elapsed: 30s, execs: 4000 (10/sec)"}]
        self.assertTrue(audit_test("T25-V07", records, (), ["test", "-fuzz", "^FuzzOne$"])["accepted"])

    def test_fuzz_sample_count_does_not_hide_failure(self):
        records = events("FuzzOne") + [{"Action": "output", "Output": "execs: 4000"}, {"Action": "fail", "Package": "fixture"}]
        self.assertFalse(audit_test("T25-V07", records, (), ["test", "-fuzz", "^FuzzOne$"])["accepted"])

    def test_scenarios_need_all_nine_roots(self):
        records = identified_events(sorted(SCENARIOS)[:-1])
        self.assertFalse(audit_test("T25-V05", records, (), ["test"])["accepted"])
        self.assertTrue(audit_test("T25-V05", identified_events(SCENARIOS), (), ["test"])["accepted"])
        impostors = sum((events("TestScenarioS" + str(n)) for n in range(1, 10)), [])
        self.assertFalse(audit_test("T25-V05", impostors, (), ["test"])["accepted"])

    def test_conformance_needs_all_four_driver_roots(self):
        records = identified_events(sorted(CONFORMANCE)[:-1])
        self.assertFalse(audit_test("T25-V06", records, (), ["test"])["accepted"])
        self.assertTrue(audit_test("T25-V06", identified_events(CONFORMANCE), (), ["test"])["accepted"])
        impostors = sum((events("TestDriver" + str(n)) for n in range(4)), [])
        self.assertFalse(audit_test("T25-V06", impostors, (), ["test"])["accepted"])


if __name__ == "__main__":
    unittest.main()
