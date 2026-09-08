"""Repeat the original T25 commands on the same candidate as native Windows."""
import argparse
import collections
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shlex
import shutil
import signal
import subprocess
import sys
import time
import traceback

from windows_validation import audit_events, audit_example, output_of, read_events, snapshot, write_json


def audit_test(check_id, records, allowed, argv):
    audit = audit_events(records, allowed_skips=allowed)
    passes = collections.Counter(e.get("Package", "") + ":" + e["Test"] for e in records
                                 if e.get("Test") and e["Action"] == "pass")
    roots = {name: count for name, count in passes.items() if "/" not in name.split(":", 1)[1]}
    if check_id == "T25-V04":
        audit["repeated_roots"] = roots
        audit["accepted"] &= bool(roots) and all(count == 20 for count in roots.values())
    if check_id == "T25-V05":
        audit["scenario_roots"] = roots
        audit["accepted"] &= len(roots) == 9
    if check_id == "T25-V06":
        audit["conformance_roots"] = roots
        audit["accepted"] &= len(roots) == 4
    if "-fuzz" in argv:
        counts = [int(m) for event in records for m in re.findall(r"execs: (\d+)", event.get("Output", ""))]
        audit["active_fuzz_samples"] = max(counts or [0])
        audit["accepted"] &= audit["active_fuzz_samples"] > 0
    return audit


def run(args):
    source, out, private = args.source.resolve(), args.output.resolve(), args.private_root.resolve()
    out.mkdir(parents=True, exist_ok=True)
    checks = []
    try:
        if platform.system() != "Linux":
            raise ValueError("Native Linux required")
        env = dict(os.environ)
        for key in ("CODEX_HOME", "CLAUDE_CONFIG_DIR", "CODEBUDDY_CONFIG_DIR", "CURSOR_HOME"):
            env.pop(key, None)
        isolated = {key: str(private / value) for key, value in {
            "HOME": "home", "USERPROFILE": "home", "XDG_CONFIG_HOME": "config",
            "XDG_CACHE_HOME": "cache", "TMPDIR": "tmp", "GOPATH": "go", "GOCACHE": "gocache"}.items()}
        for value in isolated.values():
            Path(value).mkdir(parents=True, exist_ok=True)
        isolated.update(GOENV="off", GOTOOLCHAIN="local", GOTELEMETRY="off", GOFLAGS="", GOWORK="off",
                        AGENT_ADAPTOR_LIVE_CONFORMANCE="0", AGENT_ADAPTOR_E2E="0",
                        AGENT_ADAPTOR_UPDATE_API_GOLDEN="0", CGO_ENABLED="1", GOMAXPROCS="4")
        env.update(isolated)
        go = shutil.which("go")
        controller = Path(__file__).resolve().parents[2]
        goenv = json.loads(output_of([go, "env", "-json", "GOOS", "GOARCH", "GOHOSTOS", "GOHOSTARCH",
                                      "GOROOT", "GOTOOLCHAIN", "GOFLAGS", "GOWORK", "CGO_ENABLED"], source, env))
        assert goenv["GOOS"] == goenv["GOHOSTOS"] == "linux"
        assert goenv["GOARCH"] == goenv["GOHOSTARCH"] and goenv["CGO_ENABLED"] == "1"
        assert goenv["GOFLAGS"] == "" and goenv["GOWORK"] == "off" and goenv["GOTOOLCHAIN"] == "local"
        controller_head = output_of(["git", "rev-parse", "HEAD"], controller)
        assert controller_head == os.environ["GITHUB_SHA"]
        write_json(out / "environment.json", {"tested_head": args.expected_head, "controller_head": controller_head,
                   "os": platform.platform(), "go_version": output_of([go, "version"], source, env),
                   "go_env": goenv, "environment": isolated, "cwd": str(source),
                   "workflow_run_id": os.environ.get("GITHUB_RUN_ID"),
                   "workflow_run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT"),
                   "provider_cli": "not invoked; live gates disabled and private profiles"})
        initial = snapshot(source, args.expected_head)
        write_json(out / "source-before.json", initial)
        for name in ("linux_validation.py", "windows_validation.py", "linux_allowed_skips.json"):
            shutil.copy2(Path(__file__).with_name(name), out / name)
        shutil.copy2(controller / ".github/workflows/windows-validation.yml", out / "workflow.yml")
        allowed = json.loads(Path(__file__).with_name("linux_allowed_skips.json").read_text())
        task = json.loads((source / "docs/alignment-tasks/2026-09-07/batches/06-platform-live/t25/task.json").read_text())
        commands = [(row["id"], shlex.split(row["command"])[1:]) for row in task["validation"]]
        commands += [("T25-X01", ["run", "./examples/threads/codec"]),
                     ("T25-X02", ["run", "./examples/offline"]),
                     ("T25-X03", ["test", "-count=1", "./examples/..."])]
        assert len(commands) == 18
        for check_id, command in commands:
            if command[0] == "test":
                command.insert(1, "-json")
            limit = 2400 if check_id == "T25-V04" else (1200 if check_id in ("T25-V01", "T25-V03") else 300)
            detail = {"id": check_id, "argv": ["go", *command], "head_sha": args.expected_head,
                      "exit_code": None, "watchdog_seconds": limit, "watchdog_triggered": False, "status": "failed"}
            log = out / (check_id + ".jsonl")
            started = time.monotonic()
            print("Running " + check_id + ": go " + shlex.join(command), flush=True)
            try:
                assert snapshot(source, args.expected_head) == initial
                with log.open("wb") as stream:
                    process = subprocess.Popen([go, *command], cwd=source, env=env,
                                               stdout=stream, stderr=subprocess.STDOUT, start_new_session=True)
                    try:
                        detail["exit_code"] = process.wait(timeout=limit)
                    except subprocess.TimeoutExpired:
                        detail["watchdog_triggered"] = True
                        os.killpg(process.pid, signal.SIGKILL)
                        detail["exit_code"] = process.wait(timeout=10)
                after = snapshot(source, args.expected_head)
                write_json(out / (check_id + "-source-after.json"), after)
                detail["source_unchanged"] = after == initial
                observed = command[0] == "vet"
                if command[0] == "test":
                    detail["test_audit"] = audit_test(check_id, read_events(log), allowed, command)
                    observed = detail["test_audit"]["accepted"]
                elif command[0] == "run":
                    detail["example_observed"] = audit_example(check_id.replace("T25", "T26"), log)
                    observed = detail["example_observed"]
                if detail["exit_code"] == 0 and not detail["watchdog_triggered"] and detail["source_unchanged"] and observed:
                    detail["status"] = "passed"
            except Exception:
                detail["error"] = traceback.format_exc()
            finally:
                detail["elapsed_seconds"] = time.monotonic() - started
                if log.exists():
                    detail["log_sha256"] = hashlib.sha256(log.read_bytes()).hexdigest()
                write_json(out / (check_id + "-command.json"), detail)
                checks.append(detail)
                write_json(out / "checks.json", checks)
                print(json.dumps({key: value for key, value in detail.items() if key != "test_audit"}), flush=True)
            if detail["status"] != "passed":
                raise ValueError("Required Linux verification failed: " + check_id)
        write_json(out / "summary.json", {"status": "passed", "tested_head": args.expected_head,
                   "controller_head": controller_head, "checks": len(checks), "tracked_files": len(initial["hashes"])})
        return 0
    except Exception:
        (out / "failure.txt").write_text(traceback.format_exc())
        write_json(out / "summary.json", {"status": "failed", "tested_head": args.expected_head, "completed_checks": len(checks)})
        traceback.print_exc()
        return 1
    finally:
        write_json(out / "artifacts.json", {p.name: hashlib.sha256(p.read_bytes()).hexdigest()
                   for p in sorted(out.iterdir()) if p.is_file() and p.name != "artifacts.json"})


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--expected-head", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--private-root", type=Path, required=True)
    sys.exit(run(parser.parse_args()))
