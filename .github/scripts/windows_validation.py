"""Run T26 against a separate, immutable SDK checkout and preserve evidence.

The controller revision is not the tested SDK revision. Never edit, retry, or
weaken a failed SDK test here. Only explicit platform/disabled-live skips are
permitted; mandatory native tests and all started tests need actual terminals.
"""

import argparse
import collections
import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import time
import traceback

PREFIX = "github.com/agent-dance/agent-adaptor/"
MANDATORY = {
    # These native safety tests must PASS. They are never allowed skips.
    PREFIX + "internal/systemprompt:TestWindowsFilePrivateACLAndOwnership",
    PREFIX + "internal/systemprompt:TestWindowsFileRejectsHardlink",
    PREFIX + "internal/hostedprofile:TestWindowsPrivateDACLAndTamperedDACL",
    PREFIX + "internal/hostedprofile:TestWindowsReparseProfileRejected",
    PREFIX + "internal/hostedprofile:TestWindowsOwnershipLockForbidsRenameAndDelete",
    PREFIX + "internal/processx:TestPrepareCommandLaunchesBatchShim",
    PREFIX + "internal/processx:TestConfigureCancellationTerminatesWindowsProcessTree",
    PREFIX + "adaptertest:TestAlignmentWindowsNativeArgvRoundTrip",
    PREFIX + "adaptertest:TestAlignmentWindowsPowerShellArgvRoundTrip",
    PREFIX + "internal/clihelper:TestPrepareCommandWrapsBatchShimOnWindows",
    PREFIX + "internal/clihelper:TestPrepareCommandWrapsPowerShellScriptOnWindows",
    PREFIX + "internal/clihelper:TestMergeEnvSynthesizesWindowsRuntimeVariables",
}


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def output_of(args, cwd, env=None):
    return subprocess.check_output(args, cwd=cwd, env=env, text=True, encoding="utf-8").strip()


def snapshot(source, expected):
    head = output_of(["git", "rev-parse", "HEAD"], source)
    if head != expected:
        raise ValueError("SDK checkout HEAD differs from the frozen source")
    hashes = {}
    tree = subprocess.check_output(["git", "ls-tree", "-rz", "HEAD"], cwd=source)
    for entry in tree.split(b"\0"):
        if not entry:
            continue
        meta, raw_path = entry.split(b"\t", 1)
        mode, kind, oid = meta.split()
        if kind != b"blob" or mode == b"120000":
            raise ValueError("Unexpected non-file tracked source entry")
        relative = raw_path.decode("utf-8")
        content = (source / relative).read_bytes()
        blob = b"blob " + str(len(content)).encode("ascii") + b"\0" + content
        if hashlib.sha1(blob).hexdigest() != oid.decode("ascii"):
            raise ValueError("Working source differs from its committed blob: " + relative)
        hashes[relative] = hashlib.sha256(content).hexdigest()
    if output_of(["git", "status", "--porcelain", "--untracked-files=no"], source):
        raise ValueError("Tracked source is dirty")
    if subprocess.check_output(["git", "ls-files", "--others", "-z"], cwd=source):
        raise ValueError("Untracked or ignored files exist in the frozen SDK checkout")
    return {"head_sha": head, "tracked_file_count": len(hashes), "hashes": hashes}


def audit_events(records, required=(), allowed_skips=()):
    starts, ends = collections.Counter(), collections.Counter()
    passed, skipped, failures = set(), set(), set()
    counts = collections.Counter()
    for event in records:
        action, test = event.get("Action"), event.get("Test")
        identity = event.get("Package", "") + ":" + (test or "")
        if test and action == "run":
            starts[identity] += 1
        if test and action in ("pass", "skip", "fail"):
            ends[identity] += 1
            counts[action] += 1
            {"pass": passed, "skip": skipped, "fail": failures}[action].add(identity)
        if not test and action == "fail":
            failures.add(identity)
    missing = sorted(set(required) - passed)
    unexpected = sorted(skipped - set(allowed_skips))
    incomplete = sorted(key for key in starts.keys() | ends.keys() if starts[key] != ends[key])
    return {
        "named_pass_count": counts["pass"], "named_skip_count": counts["skip"],
        "failures": sorted(failures), "missing_required_pass": missing,
        "unexpected_skips": unexpected, "permitted_skips": sorted(skipped & set(allowed_skips)),
        "incomplete_tests": incomplete,
        "accepted": counts["pass"] > 0 and not (failures or missing or unexpected or incomplete),
    }


def read_events(log):
    records = []
    for line in log.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        # Ordinary Go dependency/build diagnostics are not test events. The
        # command exit code remains independently mandatory for acceptance.
        if not line.startswith("{"):
            continue
        record = json.loads(line)
        if not isinstance(record, dict) or "Action" not in record:
            raise ValueError("Malformed Go JSON event")
        records.append(record)
    return records


def audit_example(check_id, log):
    text = log.read_text(encoding="utf-8")
    if check_id == "T26-X01":
        data = json.loads(text)
        return (data["example"] == "threads/codec"
                and data["resume_id"] == data["display_id"] == "codex-session-42"
                and data["guard"]["fingerprint"] != data["guard"]["after_cwd_change"])
    expected = ['default: prompt="show channels" append="Use repo terminology.\\n"',
                'override: prompt="show channels" append="Answer in Chinese.\\n"',
                'clear: prompt="show channels" append=""',
                'default again: prompt="show channels" append="Use repo terminology.\\n"',
                "todo: revision=1 items=1", "todo: revision=2 items=0",
                "recorded capability facts: 2",
                'budget: active_execution_timeout partial="work started" raw=true']
    return all(item in text for item in expected)


def execute_check(check_id, command, limit, go, source, expected, env, out, initial):
    """Seal per-command evidence even if execution, cleanup or audit fails."""
    log = out / (check_id + ".jsonl")
    detail = {"id": check_id, "argv": ["go", *command], "head_sha": expected,
              "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "exit_code": None, "watchdog_seconds": limit, "watchdog_triggered": False,
              "source_unchanged": False, "log": log.name, "status": "failed"}
    start = time.monotonic()
    process = None
    try:
        if snapshot(source, expected) != initial:
            raise ValueError("Source drift before " + check_id)
        detail["source_before_unchanged"] = True
        with log.open("wb") as stream:
            process = subprocess.Popen([go, *command], cwd=source, env=env,
                                       stdout=stream, stderr=subprocess.STDOUT)
            try:
                detail["exit_code"] = process.wait(timeout=limit)
            except subprocess.TimeoutExpired:
                detail["watchdog_triggered"] = True
                try:
                    kill = subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"],
                                          capture_output=True, timeout=30)
                    detail["tree_cleanup"] = {"exit_code": kill.returncode,
                        "stdout": kill.stdout.decode("utf-8", errors="replace"),
                        "stderr": kill.stderr.decode("utf-8", errors="replace")}
                except Exception as error:
                    detail["tree_cleanup"] = {"exit_code": None, "error": str(error)}
                try:
                    detail["exit_code"] = process.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    # A failed tree kill is never acceptance; bound the local
                    # parent join and retain that descendant cleanup is unproven.
                    detail["parent_kill_fallback"] = True
                    process.kill()
                    detail["exit_code"] = process.wait(timeout=10)
    except Exception:
        detail["execution_error"] = traceback.format_exc()
    finally:
        if process is not None and detail["exit_code"] is None:
            detail["exit_code"] = process.poll()
        detail["elapsed_seconds"] = time.monotonic() - start
        try:
            after = snapshot(source, expected)
            write_json(out / (check_id + "-source-after.json"), after)
            detail["source_unchanged"] = after == initial
        except Exception:
            detail["source_audit_error"] = traceback.format_exc()
        observed = False
        try:
            if log.exists():
                detail["log_sha256"] = hashlib.sha256(log.read_bytes()).hexdigest()
                if command[0] == "test":
                    allowed = json.loads((out / "allowed-skips.json").read_text(encoding="utf-8"))
                    detail["test_audit"] = audit_events(read_events(log), MANDATORY if check_id == "T26-V01" else (), allowed)
                    observed = detail["test_audit"]["accepted"]
                else:
                    detail["example_observed"] = audit_example(check_id, log) if detail["exit_code"] == 0 else False
                    observed = detail["example_observed"]
        except Exception:
            detail["result_audit_error"] = traceback.format_exc()
        if (detail["exit_code"] == 0 and not detail["watchdog_triggered"]
                and detail["source_unchanged"] and observed
                and not any(key.endswith("_error") for key in detail)):
            detail["status"] = "passed"
        write_json(out / (check_id + "-command.json"), detail)
    return detail


def run(args):
    source, out = args.source.resolve(), args.output.resolve()
    private = args.private_root.resolve()
    out.mkdir(parents=True, exist_ok=True)
    private.mkdir(parents=True, exist_ok=True)
    checks = []
    try:
        if platform.system() != "Windows":
            raise ValueError("T26 requires native Windows execution")
        controller = Path(__file__).resolve().parents[2]
        go = shutil.which("go")
        if not go:
            raise ValueError("Go executable unavailable")
        env = dict(os.environ)
        for key in ("CODEX_HOME", "CLAUDE_CONFIG_DIR", "CODEBUDDY_CONFIG_DIR", "CURSOR_HOME"):
            env.pop(key, None)
        isolated = {name: str(private / folder) for name, folder in {
            "HOME": "home", "USERPROFILE": "home", "APPDATA": "appdata",
            "LOCALAPPDATA": "localappdata", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache",
            "TEMP": "tmp", "TMP": "tmp", "TMPDIR": "tmp", "GOPATH": "go", "GOCACHE": "gocache",
        }.items()}
        for value in isolated.values():
            Path(value).mkdir(parents=True, exist_ok=True)
        isolated.update(GOENV="off", GOTOOLCHAIN="local", GOTELEMETRY="off", GOFLAGS="", GOWORK="off",
                        AGENT_ADAPTOR_LIVE_CONFORMANCE="0", AGENT_ADAPTOR_E2E="0",
                        AGENT_ADAPTOR_UPDATE_API_GOLDEN="0")
        env.update(isolated)
        goenv = json.loads(output_of([go, "env", "-json", "GOOS", "GOARCH", "GOHOSTOS", "GOHOSTARCH",
                                      "GOROOT", "GOTOOLCHAIN", "GOFLAGS", "GOWORK", "GOMOD"], source, env))
        if (goenv["GOOS"] != "windows" or goenv["GOHOSTOS"] != "windows"
                or goenv["GOARCH"] != goenv["GOHOSTARCH"] or goenv["GOTOOLCHAIN"] != "local"
                or goenv["GOFLAGS"] != "" or goenv["GOWORK"] != "off"):
            raise ValueError("Native local Go toolchain with unmodified test selection required")
        metadata = {
            "tested_head": args.expected_head, "controller_head": output_of(["git", "rev-parse", "HEAD"], controller),
            "os": platform.platform(), "go_version": output_of([go, "version"], source, env),
            "go_env": goenv, "cwd": str(source), "environment": isolated,
            "powershell": output_of(["pwsh", "-NoProfile", "-Command", "$PSVersionTable | ConvertTo-Json -Depth 3"], source),
            "shell_paths": {name: shutil.which(name) for name in ("pwsh", "powershell", "cmd.exe", "taskkill.exe")},
            "powershell_execution_policy": output_of(["pwsh", "-NoProfile", "-Command", "Get-ExecutionPolicy -List | ConvertTo-Json"], source),
            "workflow_run_id": os.environ.get("GITHUB_RUN_ID"), "workflow_run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT"),
            "workflow_head": os.environ.get("GITHUB_SHA"), "runner_os": os.environ.get("RUNNER_OS"),
            "image_os": os.environ.get("ImageOS"), "image_version": os.environ.get("ImageVersion"),
            "provider_cli": "not invoked; live gates disabled and private profiles",
        }
        if metadata["controller_head"] != metadata["workflow_head"]:
            raise ValueError("Controller checkout does not match the workflow revision")
        write_json(out / "environment.json", metadata)
        shutil.copy2(Path(__file__), out / "controller.py")
        shutil.copy2(controller / ".github/workflows/windows-validation.yml", out / "workflow.yml")
        initial = snapshot(source, args.expected_head)
        write_json(out / "source-before.json", initial)
        skip_policy = json.loads(Path(__file__).with_name("windows_allowed_skips.json").read_text(encoding="utf-8"))
        allowed = set(skip_policy)
        if allowed & MANDATORY:
            raise ValueError("Mandatory native tests cannot be permitted skips")
        write_json(out / "allowed-skips.json", skip_policy)
        commands = [
            ("T26-V01", ["test", "-json", "-count=1", "./..."], 1800),
            ("T26-X01", ["run", "./examples/threads/codec"], 180),
            ("T26-X02", ["run", "./examples/offline"], 180),
            ("T26-X03", ["test", "-json", "-count=1", "./examples/..."], 600),
        ]
        for check_id, command, limit in commands:
            print("Running " + check_id + ": go " + " ".join(command), flush=True)
            detail = execute_check(check_id, command, limit, go, source, args.expected_head, env, out, initial)
            checks.append(detail)
            write_json(out / "checks.json", checks)
            print(json.dumps(detail, ensure_ascii=True), flush=True)
            if detail["status"] != "passed":
                raise ValueError("Required verification failed: " + check_id)
        write_json(out / "summary.json", {"status": "passed", "tested_head": args.expected_head,
                   "controller_head": metadata["controller_head"], "checks": len(checks),
                   "tracked_files": len(initial["hashes"]), "mandatory_native_passes": sorted(MANDATORY)})
        return 0
    except Exception:
        (out / "failure.txt").write_text(traceback.format_exc(), encoding="utf-8")
        write_json(out / "summary.json", {"status": "failed", "tested_head": args.expected_head,
                   "completed_checks": len(checks), "required_checks": 4})
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
