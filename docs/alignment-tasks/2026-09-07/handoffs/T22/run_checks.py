#!/usr/bin/env python3
"""Run an ordinary T22 check with explicit, private subprocess environment.

Toolchain/cache paths are execution-harness inputs, never test assumptions.
The 600-second process timeout does not alter the task's Go test selection.
"""
import argparse
import json
import os
import pathlib
import platform
import subprocess
import sys
import time

parser = argparse.ArgumentParser(description=__doc__)
for name in ("go-root", "home", "cache", "mod-cache"):
    parser.add_argument("--" + name, required=True)
parser.add_argument("--log")
parser.add_argument("command", nargs=argparse.REMAINDER)
args = parser.parse_args()
command = args.command[1:] if args.command[:1] == ["--"] else args.command
if not command:
    parser.error("a command is required after --")
root = pathlib.Path(__file__).resolve().parents[5]
private_home = pathlib.Path(args.home).resolve()
private_home.mkdir(parents=True, exist_ok=True)
env = os.environ.copy()
for key in ("CODEX_HOME", "CLAUDE_CONFIG_DIR", "CODEBUDDY_CONFIG_DIR", "CURSOR_HOME"):
    env.pop(key, None)
overrides = dict(
    HOME=str(private_home), USERPROFILE=str(private_home),
    XDG_CONFIG_HOME=str(private_home / "config"), GOPATH=str(private_home / "go"),
    GOROOT=args.go_root, GOTOOLCHAIN="local", GOMODCACHE=args.mod_cache,
    GOCACHE=args.cache, PATH=str(pathlib.Path(args.go_root) / "bin") + os.pathsep + env.get("PATH", ""),
    AGENT_ADAPTOR_LIVE_CONFORMANCE="0", AGENT_ADAPTOR_E2E="0",
    AGENT_ADAPTOR_UPDATE_API_GOLDEN="0", PYTHONDONTWRITEBYTECODE="1",
)
env.update(overrides)
version = subprocess.check_output([str(pathlib.Path(args.go_root) / "bin" / "go"), "version"], env=env, text=True).strip()
if not version.startswith("go version go1.26.5 "):
    raise SystemExit("T22 requires the dispatched real Go 1.26.5 toolchain; observed " + version)
head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
meta = dict(command=command, cwd=str(root), head_sha=head, os=platform.platform(), go_version=version,
            cli_version="No real CLI: local Go formal protocol fixture only", environment=overrides,
            removed_environment=["CODEX_HOME", "CLAUDE_CONFIG_DIR", "CODEBUDDY_CONFIG_DIR", "CURSOR_HOME"],
            process_timeout_seconds=600)
print(json.dumps(meta), flush=True)
started = time.monotonic()
log = open(args.log, "w") if args.log else None
try:
    result = subprocess.run(command, cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT if log else None, timeout=600)
    code = result.returncode
except subprocess.TimeoutExpired:
    code = 124
finally:
    if log:
        log.close()
meta.update(exit_code=code, elapsed_seconds=time.monotonic() - started)
if args.log:
    pathlib.Path(args.log + ".meta.json").write_text(json.dumps(meta, ensure_ascii=False, indent=2) + "\n")
print(json.dumps(dict(exit_code=code, elapsed_seconds=meta["elapsed_seconds"])), flush=True)
sys.exit(code)
