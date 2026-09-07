#!/usr/bin/env python3
"""Summarize actual go test -json outcomes; never treat listing/skip as execution.

Use --require Package:TestName for mandatory roots (or native/live scenarios).
Allowed skips are exact Package:TestName identifiers, not regular expressions.
This complements the process exit code, which must independently be zero.
"""
import argparse
import collections
import json
import pathlib
import sys


def check(records, required=(), allowed_skips=()):
    counts = collections.Counter()
    passed, skipped, failed = set(), set(), set()
    for record in records:
        action, test = record.get("Action"), record.get("Test")
        identity = record.get("Package", "") + ":" + (test or "")
        if test and action in ("pass", "fail", "skip"):
            counts[action] += 1
            {"pass": passed, "fail": failed, "skip": skipped}[action].add(identity)
        elif action == "fail":
            failed.add(identity)
    missing = sorted(set(required) - passed)
    unexpected = sorted(skipped - set(allowed_skips))
    return {
        "pass": counts["pass"], "fail": counts["fail"], "skip": counts["skip"],
        "executed_test_count": counts["pass"] + counts["fail"],
        "missing_required_pass": missing, "unexpected_skips": unexpected,
        "failed": sorted(failed), "permitted_skips": sorted(skipped & set(allowed_skips)),
        "accepted": bool(counts["pass"] + counts["fail"]) and not (failed or missing or unexpected),
    }


def self_test():
    def event(action, name="TestRequired"):
        return {"Action": action, "Package": "fixture", "Test": name}
    assert not check([])["accepted"]
    assert not check([event("skip")])["accepted"]
    assert not check([event("pass", "TestUnrelated")], ["fixture:TestRequired"])["accepted"]
    assert not check([event("pass"), event("fail", "TestRequired/child")])["accepted"]
    assert not check([event("pass"), {"Action": "fail", "Package": "fixture"}])["accepted"]
    assert check([event("pass"), event("skip", "TestOptional")], ["fixture:TestRequired"], ["fixture:TestOptional"])["accepted"]
    print("6 evidence oracle cases passed; no Go/platform/provider execution claimed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("log", nargs="?", type=pathlib.Path)
    parser.add_argument("--require", action="append", default=[])
    parser.add_argument("--allow-skip", action="append", default=[])
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        self_test()
        return 0
    if not args.log:
        parser.error("a go test -json log is required")
    try:
        records = [json.loads(line) for line in args.log.read_text().splitlines() if line.strip()]
        if not all(isinstance(record, dict) for record in records):
            raise ValueError("every log record must be an object")
    except (ValueError, OSError) as error:
        parser.error(str(error))
    summary = check(records, args.require, args.allow_skip)
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0 if summary["accepted"] else 1


if __name__ == "__main__":
    sys.exit(main())
