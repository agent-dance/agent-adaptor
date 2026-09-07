#!/usr/bin/env python3
"""Validate dispatch plans and handoff metadata without running task commands.

Python 3.9+, standard library only. The schema checker implements exactly the
keywords used by the shipped schemas, not arbitrary JSON Schema documents.
Passing metadata validation does not prove that evidence is truthful.
"""

import argparse
import hashlib
import itertools
import json
from pathlib import Path
import re
import shlex
import subprocess
import sys

PACKAGE = Path(__file__).resolve().parent
ROOT = PACKAGE.parents[2]


def read_json(path):
    def unique_object(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("duplicate JSON key: " + key)
            result[key] = value
        return result
    return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def schema_errors(value, schema, where="$"):
    errors = []
    if "anyOf" in schema:
        if not any(not schema_errors(value, variant, where) for variant in schema["anyOf"]):
            return [where + ": no anyOf variant matches"]
        return []
    types = {
        "object": lambda x: isinstance(x, dict),
        "array": lambda x: isinstance(x, list),
        "string": lambda x: isinstance(x, str),
        "integer": lambda x: isinstance(x, int) and not isinstance(x, bool),
        "boolean": lambda x: isinstance(x, bool),
        "null": lambda x: x is None,
    }
    if "type" in schema and not types[schema["type"]](value):
        return [where + ": wrong type (expected " + schema["type"] + ")"]
    if "const" in schema and (type(value) is not type(schema["const"]) or value != schema["const"]):
        errors.append(where + ": wrong const")
    if "enum" in schema and value not in schema["enum"]:
        errors.append(where + ": outside enum")
    if isinstance(value, dict):
        props = schema.get("properties", {})
        for key in schema.get("required", []):
            if key not in value:
                errors.append(where + ": missing " + key)
        for key, item in value.items():
            extra = schema.get("additionalProperties", True)
            if key in props:
                errors.extend(schema_errors(item, props[key], where + "." + key))
            elif extra is False:
                errors.append(where + ": unknown field " + key)
            elif isinstance(extra, dict):
                errors.extend(schema_errors(item, extra, where + "." + key))
    if isinstance(value, list):
        if len(value) < schema.get("minItems", 0):
            errors.append(where + ": too few items")
        if schema.get("uniqueItems"):
            encoded = [json.dumps(x, sort_keys=True) for x in value]
            if len(encoded) != len(set(encoded)):
                errors.append(where + ": duplicate items")
        for index, item in enumerate(value):
            errors.extend(schema_errors(item, schema.get("items", {}), f"{where}[{index}]"))
    if isinstance(value, str):
        if len(value) < schema.get("minLength", 0):
            errors.append(where + ": empty string")
        if "pattern" in schema and not re.search(schema["pattern"], value):
            errors.append(where + ": pattern mismatch")
    if isinstance(value, (int, float)) and "minimum" in schema and value < schema["minimum"]:
        errors.append(where + ": below minimum")
    return errors


def safe_path(path):
    return (isinstance(path, str) and bool(path) and "\\" not in path
            and not path.startswith("/") and not re.match(r"^[A-Za-z]:", path)
            and all(part not in ("", ".", "..") for part in path.split("/")))


def valid_scope(scope):
    base = scope[:-3] if scope.endswith("/**") else scope
    return safe_path(base) and not any(char in base for char in "*?[]")


def allows(scope, path):
    # Fold case to include Windows and default macOS volumes.
    scope, path = scope.casefold(), path.casefold()
    return path.startswith(scope[:-3] + "/") if scope.endswith("/**") else path == scope


def overlaps(left, right):
    a = left[:-3] if left.endswith("/**") else left
    b = right[:-3] if right.endswith("/**") else right
    return (left.casefold() == right.casefold() or allows(left, b)
            or allows(right, a) or a.casefold() == b.casefold())


def load_package(package=PACKAGE):
    root = package.parents[2]
    manifest = read_json(package / "manifest.json")
    for entry in manifest["tasks"]:
        if not safe_path(entry["path"]):
            raise ValueError("unsafe task path: " + entry["path"])
    tasks = {entry["id"]: read_json(root / entry["path"]) for entry in manifest["tasks"]}
    return {"root": root, "package": package, "manifest": manifest, "tasks": tasks,
            "coverage": read_json(package / "coverage.json"),
            "task_schema": read_json(package / "task.schema.json"),
            "result_schema": read_json(package / "result.schema.json")}


def package_errors(data, verify_files=True):
    manifest, tasks, coverage = data["manifest"], data["tasks"], data["coverage"]
    errors = []

    def check(condition, message):
        if not condition:
            errors.append(message)

    entries = manifest["tasks"]
    check(len({e["id"] for e in entries}) == len(entries), "manifest: duplicate task ID")
    check(len({e["path"] for e in entries}) == len(entries), "manifest: duplicate task path")
    for tid, task in tasks.items():
        errors.extend(schema_errors(task, data["task_schema"], tid))
    if errors:
        return errors
    plan_path = data["root"] / manifest["plan"]["path"]
    plan_text = plan_path.read_text(encoding="utf-8")
    sections = {m.group(1): m.group(0).strip() for m in re.finditer(
        r"^### (W\d{2})：.*?(?=^### W\d{2}：|^## 4\.|\Z)", plan_text, re.M | re.S)}
    graph = {tid: set(task["depends_on"]) for tid, task in tasks.items()}
    ancestors, visiting = {}, set()

    def walk(tid):
        if tid in ancestors:
            return ancestors[tid]
        if tid in visiting:
            errors.append("dependency cycle at " + tid)
            return set()
        visiting.add(tid)
        found = set()
        for dep in graph[tid]:
            if dep not in tasks:
                errors.append(tid + ": unknown dependency " + dep)
            else:
                found.add(dep)
                found.update(walk(dep))
        visiting.remove(tid)
        ancestors[tid] = found
        return found

    for tid in tasks:
        walk(tid)
    seen = []
    for index, batch in enumerate(manifest["batches"]):
        workers, gate = batch["parallel_tasks"], batch["gate_task"]
        previous = manifest["batches"][index - 1]["gate_task"] if index else None
        check(batch["id"] == f"B{index:02}", "batch order mismatch")
        check(batch["depends_on_gate"] == previous, batch["id"] + ": broken barrier")
        check(len(set(workers)) == len(workers), batch["id"] + ": duplicate worker")
        check(set(batch["integration_order"]) == set(workers)
              and len(batch["integration_order"]) == len(workers), "integration order lost tasks")
        check(1 <= batch["max_parallelism"] <= min(len(workers), 6), "parallelism cap must be between 1 and min(worker count, 6)")
        check(all(tid in tasks for tid in workers + [gate]), "batch references missing task")
        if not all(tid in tasks for tid in workers + [gate]):
            continue
        seen.extend(workers + [gate])
        check(tasks[gate]["phase"] == "gate" and graph[gate] == set(workers), gate + ": wrong worker barrier")
        for tid in workers + [gate]:
            task = tasks[tid]
            check(task["batch_id"] == batch["id"], tid + ": batch mismatch")
            check(task["base"]["predecessor_gate"] == previous, tid + ": wrong base gate")
            if tid in workers:
                check(task["phase"] == "parallel", tid + ": wrong phase")
                check(not (ancestors[tid] & set(workers)), tid + ": same-batch worker dependency")
                check(previous is None or previous in graph[tid], tid + ": missing previous gate dependency")
                check(task["execution"]["isolation"] == "separate_git_worktree", tid + ": missing isolation")
            for dep in graph[tid]:
                if dep in tasks:
                    check(tasks[dep]["batch_id"] <= batch["id"], tid + ": future dependency")
        for left, right in itertools.combinations(workers, 2):
            for a, b in itertools.product(tasks[left]["ownership"]["allow"], tasks[right]["ownership"]["allow"]):
                check(not overlaps(a, b), f"write overlap: {left} {a} <> {right} {b}")
            shared = set(tasks[left]["ownership"]["exclusive_resources"]) & set(tasks[right]["ownership"]["exclusive_resources"])
            check(not shared, f"exclusive resource overlap: {left}/{right}: {sorted(shared)}")
    check(len(seen) == len(set(seen)) and set(seen) == set(tasks), "batch membership incomplete or duplicate")
    for tid, task in tasks.items():
        check(task["id"] == tid, tid + ": mismatched ID")
        check(task["context"]["plan"]["sha256"] == manifest["plan"]["sha256"], tid + ": plan hash differs")
        check(task["context"]["authority"] == manifest["authority"], tid + ": authority differs")
        check(task["context"]["source_head"] == manifest["repository"]["source_head"], tid + ": source HEAD differs")
        check(task["base"]["required_ancestor"] == manifest["repository"]["code_baseline"], tid + ": baseline differs")
        check(set(task["context"]["plan"]["sections"]) == set(task["work_items"]), tid + ": section mismatch")
        if task["phase"] == "parallel":
            check(task["context"]["embedded_plan_sections"] == {w: sections.get(w) for w in task["work_items"]}, tid + ": embedded plan drift")
        for scope in task["ownership"]["allow"]:
            check(valid_scope(scope), tid + ": unsafe/unsupported scope " + scope)
        for artifact in task["context"]["dependency_artifacts"]:
            producer = artifact["producer"]
            check(producer in ancestors[tid], tid + ": artifact not from prerequisite " + producer)
            check(producer in tasks and any(allows(s, artifact["path"]) for s in tasks[producer]["ownership"]["allow"]), tid + ": producer does not own artifact")
        for collection in (task["validation"], task["acceptance_criteria"]):
            check(len({v["id"] for v in collection}) == len(collection), tid + ": duplicate criterion/check ID")
        live = task["kind"] == "live_verification"
        check(task["execution"]["paid_live_authorization_required"] == live, tid + ": live authorization mismatch")
        for validation in task["validation"]:
            check(validation["environment"].get("AGENT_ADAPTOR_LIVE_CONFORMANCE", "0") == ("1" if live else "0"), tid + ": wrong paid gate")
            if live:
                check("-tags=" + str(task["execution"]["live_build_tag"]) in validation["command"], tid + ": missing live build tag")
        if task["batch_id"] == "B06":
            check(not task["execution"]["source_write_allowed"], tid + ": verification may write production")
            check(task["base"]["verification_head_gate"] == "G05", tid + ": wrong frozen head")
    requirements = coverage["requirements"]
    check(len({r["id"] for r in requirements}) == len(requirements), "duplicate requirement")
    for req in requirements:
        owner, verifiers, rid = req["implementation_owner"], req["verification_tasks"], req["id"]
        check(owner in tasks, rid + ": missing implementation owner")
        check(bool(verifiers) and owner not in verifiers, rid + ": missing independent verifier")
        check(req["status"] == "planned", rid + ": plan fabricated completion")
        if owner not in tasks:
            continue
        check(req["work_item"] in tasks[owner]["work_items"], rid + ": owner lacks work item")
        for verifier in verifiers:
            check(verifier in tasks, rid + ": unknown verifier")
            if verifier in tasks:
                check(tasks[verifier]["batch_id"] > tasks[owner]["batch_id"], rid + ": verifier not after implementation")
                check(req["work_item"] in tasks[verifier]["work_items"], rid + ": verifier lacks source work item")
    for tid, task in tasks.items():
        owned = {r["id"] for r in requirements if r["implementation_owner"] == tid}
        verified = {r["id"] for r in requirements if tid in r["verification_tasks"]}
        check(set(task["requirements_owned"]) == owned, tid + ": owned requirements differ")
        check(set(task["requirements_verified"]) == verified, tid + ": verified requirements differ")
        check(set(task["handoff"]["requirement_ids"]) == owned | verified, tid + ": handoff coverage differs")
    check({w["id"] for w in coverage["work_items"]} == set(sections), "work item lost")
    for item in coverage["work_items"]:
        matched = [r for r in requirements if r["work_item"] == item["id"]]
        check(bool(matched) and set(item["requirement_ids"]) == {r["id"] for r in matched}, item["id"] + ": incomplete requirements")
        check(set(item["implementation_tasks"]) == {r["implementation_owner"] for r in matched}, item["id"] + ": owner index differs")
        check(item["documentation_task"] == "T24" and item["final_closure_gate"] == "G06", item["id"] + ": missing closure")
    ledger = dict(re.findall(r"^\| ([AB]\d{2}) \| \x60([0-9a-f]{12})\x60 \|", plan_text, re.M))
    history = coverage["history"]
    check(len(history) == len(ledger) and len({h["ledger_id"] for h in history}) == len(history), "history coverage count/uniqueness mismatch")
    check({h["ledger_id"]: h["commit"][:12] for h in history} == ledger, "history commit missing or changed")
    for entry in history:
        check(bool(entry["decision"]) and entry["verification_owner"] in tasks, "history missing disposition/owner")
    summary = {
        "work_item_count": len(sections), "history_commit_count": len(history),
        "requirement_count": len(requirements), "batch_count": len(manifest["batches"]),
        "parallel_task_count": sum(t["phase"] == "parallel" for t in tasks.values()),
        "gate_task_count": sum(t["phase"] == "gate" for t in tasks.values()),
        "total_task_count": len(tasks),
        "peak_parallelism": max(b["max_parallelism"] for b in manifest["batches"]),
    }
    check(summary == manifest["summary"], "manifest summary does not match contents")
    check(coverage["plan_sha256"] == manifest["plan"]["sha256"], "coverage plan hash differs")
    if verify_files:
        check(digest(plan_path) == manifest["plan"]["sha256"], "source plan content drift")
        indexed = set()
        for entry in entries:
            path = data["root"] / entry["path"]
            indexed.add(path.resolve())
            check(digest(path) == entry["sha256"], entry["id"] + ": task content hash drift")
            task = tasks[entry["id"]]
            check(entry["phase"] == task["phase"] and entry["batch_id"] == task["batch_id"], "manifest index differs")
            for required in task["context"]["required_reads"]:
                check(safe_path(required) and (data["root"] / required).is_file(), entry["id"] + ": missing required read " + required)
        discovered = {p.resolve() for p in (data["package"] / "batches").rglob("task.json")}
        check(discovered == indexed, "unindexed or missing task.json")
    return errors


def normalized_command(command):
    # Only additional logging flags are allowed, never a narrower -run selector.
    return [p for p in shlex.split(command) if p not in ("-json", "-v")]


def state_errors(data, state):
    errors = []
    tasks = data["tasks"]
    if state.get("manifest_id") != data["manifest"]["id"]:
        errors.append("state: manifest mismatch")
    if set(state.get("tasks", {})) != set(tasks):
        errors.append("state: missing or unknown tasks")
        return errors
    if set(state.get("requirements", {})) != {r["id"] for r in data["coverage"]["requirements"]}:
        errors.append("state: requirement IDs differ")
    for tid, row in state["tasks"].items():
        if row.get("status") not in ("planned", "running", "complete", "blocked", "needs_rework"):
            errors.append(tid + ": unknown execution status")
        if row.get("status") == "complete":
            if not re.fullmatch(r"[0-9a-f]{40}", row.get("head_sha") or "") or not row.get("result_artifact"):
                errors.append(tid + ": completed state lacks SHA/report")
            if not all(state["tasks"][d]["status"] == "complete" for d in tasks[tid]["depends_on"]):
                errors.append(tid + ": completed before dependencies")
    if state.get("release_readiness") and not all(r["status"] == "complete" for r in state["tasks"].values()):
        errors.append("state: release readiness with unfinished tasks")
    if state.get("release_readiness") and not all(
        row.get("implementation") == "passed" and row.get("independent_verification") == "passed"
        and row.get("evidence") for row in state.get("requirements", {}).values()
    ):
        errors.append("state: release readiness with unverified requirements")
    return errors


def result_errors(data, report, state=None, verify_git=False):
    errors = schema_errors(report, data["result_schema"], "result")
    if errors:
        return errors
    tid = report["task_id"]
    if tid not in data["tasks"]:
        return ["result: unknown task " + tid]
    task = data["tasks"][tid]

    def check(condition, message):
        if not condition:
            errors.append(tid + ": " + message)

    complete = report["status"] == "complete"
    allowed = list(task["ownership"]["allow"])
    if task["phase"] == "gate":
        for dep in task["depends_on"]:
            allowed.extend(data["tasks"][dep]["ownership"]["allow"])
    for path in report["changed_files"]:
        check(safe_path(path) and any(allows(scope, path) for scope in allowed), "changed file outside ownership: " + path)
    for collection in ("acceptance", "requirements", "checks"):
        check(len({r["id"] for r in report[collection]}) == len(report[collection]), "duplicate report " + collection)
    if complete:
        check(bool(report["base_head"] and report["head_sha"]), "complete report lacks base/head")
        check(not report["remaining_items"] and all(f["status"] == "resolved" for f in report["findings"]), "complete with remaining work/findings")
        for key, expected in [
            ("acceptance", {r["id"] for r in task["acceptance_criteria"]}),
            ("requirements", set(task["handoff"]["requirement_ids"])),
        ]:
            check({r["id"] for r in report[key]} == expected, "incomplete " + key + " coverage")
            check(all(r["status"] == "passed" for r in report[key]), "unpassed " + key)
        actual_checks = {row["id"]: row for row in report["checks"]}
        for required in task["validation"]:
            row = actual_checks.get(required["id"])
            check(row is not None, "missing check " + required["id"])
            if row is None:
                continue
            check(normalized_command(row["command"]) == normalized_command(required["command"]), "validation command changed: " + required["id"])
            check(row["status"] == "passed" and row["exit_code"] == 0, "failed or unexecuted check")
            check(row["head_sha"] == report["head_sha"], "check ran on different SHA")
            check(row["skipped_required_tests"] == 0, "required scenarios were skipped")
            if required["must_execute_nonzero_tests"]:
                check(row["executed_test_count"] > 0, "zero tests/fuzz executions")
            for key, value in required["environment"].items():
                check(row["environment"].get(key) == value, "missing/wrong environment " + key)
            platform = task["execution"]["environment"]
            if platform in ("linux", "windows"):
                check(platform in row["os"].lower(), "wrong native platform")
            if required["command"].startswith("go "):
                check(bool(row["go_version"]), "Go version absent")
        check(report["documentation"]["ready"], "documentation not ready")
        check(report["documentation"]["fragment"] == task["documentation"]["fragment"], "wrong documentation fragment")
        check(report["documentation"]["centralized_by_gate"] == task["documentation"]["central_owner"], "wrong documentation owner")
        check(any(a["kind"] == "documentation" and a["path"] == task["documentation"]["fragment"] for a in report["artifacts"]), "documentation artifact absent")
        if state is None:
            errors.append(tid + ": complete result requires --state to verify dependencies and base")
        else:
            errors.extend(state_errors(data, state))
            rows = state.get("tasks", {})
            check(all(rows.get(d, {}).get("status") == "complete" for d in task["depends_on"]), "dependencies not complete")
            predecessor = task["base"]["predecessor_gate"]
            expected_base = rows.get(predecessor, {}).get("head_sha") if predecessor else state.get("seed_head")
            check(bool(expected_base) and report["base_head"] == expected_base, "not based on preceding gate/seed SHA")
            if task["batch_id"] == "B06":
                frozen = rows.get("G05", {}).get("head_sha")
                check(report["head_sha"] == frozen == state.get("implementation_head"), "not the G05 frozen implementation SHA")
                check(not report["changed_files"] and not report["commits"], "frozen verification changed the tested checkout")
        live = task["kind"] == "live_verification"
        if live:
            auth = report["live_authorization"]
            check(auth is not None, "live authorization evidence absent")
            if auth:
                check(auth["build_tag"] == task["execution"]["live_build_tag"], "wrong live build tag")
                check(auth["provider"] + "_live" == auth["build_tag"], "wrong provider authorization")
            check(all(c["cli_version"] for c in report["checks"]), "live CLI version absent")
        if task["phase"] == "gate":
            gate = report["gate_details"]
            check(gate is not None, "gate details absent")
            if gate:
                check(set(gate["accepted_tasks"]) == set(task["depends_on"]), "gate omitted worker")
                check(gate["tested_head"] == report["head_sha"], "gate tested wrong SHA")
                check(tid == "G06" or gate["next_batch_base"] == report["head_sha"], "wrong next-batch base")
                check(tid == "G06" or not gate["release_readiness"], "premature release readiness")
                if tid == "G00":
                    check(bool(gate["contracts_manifest"]), "frozen contract manifest absent")
                if tid == "G06":
                    check(gate["release_readiness"], "final gate complete with unfinished readiness")
                    if state is not None:
                        check(all(row.get("implementation") == "passed"
                                  and row.get("independent_verification") == "passed"
                                  and row.get("evidence")
                                  for row in state.get("requirements", {}).values()),
                              "final gate lacks per-requirement closure evidence")
        else:
            check(report["gate_details"] is None, "worker cannot report gate acceptance")
    else:
        check(bool(report["remaining_items"] or report["findings"]), "blocked/rework report has no actionable remaining item")
    if verify_git and report["base_head"] and report["head_sha"]:
        try:
            git = ["git", "-C", str(data["root"])]
            for older, newer in [(task["base"]["required_ancestor"], report["base_head"]),
                                 (report["base_head"], report["head_sha"])]:
                proc = subprocess.run(git + ["merge-base", "--is-ancestor", older, newer], capture_output=True)
                check(proc.returncode == 0, "invalid Git ancestor/base/head")
            changed = subprocess.check_output(
                git + ["diff", "--name-only", "--no-renames", "-z", report["base_head"], report["head_sha"]])
            actual = {p.decode("utf-8") for p in changed.split(b"\0") if p}
            check(actual == set(report["changed_files"]), "Git diff differs from reported changed_files")
            for sha in report["commits"]:
                proc = subprocess.run(git + ["merge-base", "--is-ancestor", sha, report["head_sha"]], capture_output=True)
                check(proc.returncode == 0, "reported commit not contained in delivered head")
        except (OSError, subprocess.CalledProcessError) as exc:
            errors.append(tid + ": Git verification failed: " + str(exc))
    return errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--json", action="store_true", help="print a machine-readable validation report")
    parser.add_argument("--write-report", type=Path, help="also write the package validation result")
    parser.add_argument("--result", type=Path, help="validate one external result.json")
    parser.add_argument("--state", type=Path, help="dispatcher execution state (required for complete results)")
    parser.add_argument("--verify-git", action="store_true", help="also check baseline/actual diff/commit ancestry")
    args = parser.parse_args()
    try:
        data = load_package()
        errors = package_errors(data)
        state = read_json(args.state) if args.state else None
        if state is not None:
            errors.extend(state_errors(data, state))
        if args.verify_git:
            baseline = data["manifest"]["repository"]["code_baseline"]
            authority = data["manifest"]["authority"]
            content = subprocess.check_output(["git", "-C", str(ROOT), "show", baseline + ":" + authority["path"]])
            if hashlib.sha256(content).hexdigest() != authority["baseline_sha256"]:
                errors.append("AGENTS baseline hash differs from pinned Git object")
        if args.result:
            errors.extend(result_errors(data, read_json(args.result), state, args.verify_git))
        report = {"status": "passed" if not errors else "failed",
                  "scope": "dispatch_package_and_supplied_handoff_metadata_only",
                  "implementation_executed": False, "summary": data["manifest"]["summary"],
                  "manifest_sha256": digest(PACKAGE / "manifest.json"), "errors": errors}
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as exc:
        report = {"status": "failed", "errors": [str(exc)], "implementation_executed": False}
    output = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    if args.write_report:
        args.write_report.write_text(output, encoding="utf-8")
    if args.json:
        print(output, end="")
    elif report["status"] == "passed":
        print("PASS: dispatch DAG, ownership, coverage, schemas, hashes and supplied metadata.")
        print(json.dumps(report["summary"], ensure_ascii=False))
    else:
        for error in report["errors"]:
            print("FAIL: " + error, file=sys.stderr)
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
