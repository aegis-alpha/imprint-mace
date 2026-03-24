#!/usr/bin/env python3
"""
imprint-bench: LoCoMo benchmark orchestrator for Imprint.

Triggered by Imprint's on_kept_command after Karpathy loop keeps a prompt.
Counts kept events; on every 5th, runs memorybench against a temporary
Imprint instance and sends results to Telegram.

Subcommands:
  notify-kept   Record a kept event, run benchmark if threshold reached
  run           Force-run the benchmark now (ignores counter)
  status        Show current counter and last run info

State file: ~/.imprint-bench/state.json
Config:     ~/.imprint-bench/config.json (or IMPRINT_BENCH_CONFIG env)

Usage:
  imprint-bench notify-kept
  imprint-bench run
  imprint-bench status
"""

import argparse
import json
import os
import signal
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

STATE_DIR = Path.home() / ".imprint-bench"
STATE_FILE = STATE_DIR / "state.json"
CONFIG_FILE = STATE_DIR / "config.json"
RESULTS_FILE = STATE_DIR / "results.md"
KEPT_THRESHOLD = 5


def load_state() -> dict:
    if STATE_FILE.exists():
        with open(STATE_FILE) as f:
            return json.load(f)
    return {
        "kept_count": 0,
        "last_run": None,
        "last_score": None,
        "total_runs": 0,
    }


def save_state(state: dict):
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    with open(STATE_FILE, "w") as f:
        json.dump(state, f, indent=2)


def load_config() -> dict:
    path = Path(os.environ.get("IMPRINT_BENCH_CONFIG", CONFIG_FILE))
    if not path.exists():
        print(f"Config not found: {path}", file=sys.stderr)
        print("Create it with at minimum:", file=sys.stderr)
        print(json.dumps({
            "imprint_binary": "/usr/local/bin/imprint",
            "imprint_config": "/path/to/config-bench.toml",
            "memorybench_dir": "/path/to/memorybench",
            "telegram_bot_token": "",
            "telegram_chat_id": "",
        }, indent=2), file=sys.stderr)
        sys.exit(1)
    with open(path) as f:
        return json.load(f)


def start_bench_imprint(cfg: dict) -> subprocess.Popen:
    binary = cfg["imprint_binary"]
    config_path = cfg["imprint_config"]
    cmd = [binary, "--config", config_path, "serve"]
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    time.sleep(3)
    if proc.poll() is not None:
        stderr = proc.stderr.read().decode() if proc.stderr else ""
        raise RuntimeError(f"Bench Imprint failed to start: {stderr}")
    print(f"[imprint-bench] started bench Imprint (pid={proc.pid})", file=sys.stderr)
    return proc


def stop_bench_imprint(proc: subprocess.Popen):
    proc.send_signal(signal.SIGTERM)
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
    print("[imprint-bench] stopped bench Imprint", file=sys.stderr)


def run_memorybench(cfg: dict) -> dict:
    memorybench_dir = cfg["memorybench_dir"]
    bench_url = cfg.get("bench_imprint_url", "http://127.0.0.1:8181")
    judge = cfg.get("judge_model", "gpt-4.1-mini")

    run_id = f"imprint-{datetime.now(timezone.utc).strftime('%Y%m%d-%H%M')}"

    cmd = [
        "bun", "run", "src/index.ts", "run",
        "-p", "imprint",
        "-b", "locomo",
        "-j", judge,
        "-r", run_id,
    ]

    env = os.environ.copy()
    env["IMPRINT_BASE_URL"] = bench_url

    print(f"[imprint-bench] running memorybench: {' '.join(cmd)}", file=sys.stderr)
    proc = subprocess.run(
        cmd,
        cwd=memorybench_dir,
        env=env,
        capture_output=True,
        text=True,
        timeout=7200,
    )

    if proc.returncode != 0:
        raise RuntimeError(
            f"memorybench failed (exit {proc.returncode}):\n{proc.stderr}"
        )

    report_path = Path(memorybench_dir) / "data" / "runs" / run_id / "report.json"
    if report_path.exists():
        with open(report_path) as f:
            return json.load(f)

    return {"run_id": run_id, "stdout": proc.stdout}


def send_telegram(cfg: dict, message: str):
    token = cfg.get("telegram_bot_token", "")
    chat_id = cfg.get("telegram_chat_id", "")
    if not token or not chat_id:
        print("[imprint-bench] no telegram config, skipping notification", file=sys.stderr)
        return

    import urllib.request
    import urllib.parse

    url = f"https://api.telegram.org/bot{token}/sendMessage"
    data = urllib.parse.urlencode({
        "chat_id": chat_id,
        "text": message,
        "parse_mode": "Markdown",
    }).encode()

    try:
        urllib.request.urlopen(url, data, timeout=10)
        print("[imprint-bench] telegram notification sent", file=sys.stderr)
    except Exception as e:
        print(f"[imprint-bench] telegram failed: {e}", file=sys.stderr)


def append_results(report: dict, trigger: str):
    RESULTS_FILE.parent.mkdir(parents=True, exist_ok=True)
    now = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    score = report.get("accuracy", report.get("score", "?"))
    run_id = report.get("run_id", "?")

    entry = f"\n## {now}\n\nTrigger: {trigger}\nRun: {run_id}\nScore: {score}\n"

    with open(RESULTS_FILE, "a") as f:
        f.write(entry)


def format_telegram_message(report: dict) -> str:
    score = report.get("accuracy", report.get("score", "?"))
    run_id = report.get("run_id", "?")
    memscore = report.get("memScore", "")

    lines = [
        f"*LoCoMo benchmark complete*",
        f"Run: `{run_id}`",
        f"Score: *{score}*",
    ]
    if memscore:
        lines.append(f"MemScore: {memscore}")

    return "\n".join(lines)


def do_run(cfg: dict, trigger: str):
    proc = None
    try:
        proc = start_bench_imprint(cfg)
        report = run_memorybench(cfg)
        append_results(report, trigger)
        msg = format_telegram_message(report)
        send_telegram(cfg, msg)
        return report
    finally:
        if proc:
            stop_bench_imprint(proc)


def cmd_notify_kept(args):
    state = load_state()
    state["kept_count"] = state.get("kept_count", 0) + 1

    score = os.environ.get("IMPRINT_KEPT_SCORE", "?")
    baseline = os.environ.get("IMPRINT_BASELINE_SCORE", "?")
    print(
        f"[imprint-bench] kept #{state['kept_count']}/{KEPT_THRESHOLD} "
        f"(score={score}, baseline={baseline})",
        file=sys.stderr,
    )

    if state["kept_count"] >= KEPT_THRESHOLD:
        cfg = load_config()
        trigger = f"kept #{state['kept_count']} (score={score})"
        try:
            report = do_run(cfg, trigger)
            state["kept_count"] = 0
            state["last_run"] = datetime.now(timezone.utc).isoformat()
            state["last_score"] = report.get("accuracy", report.get("score"))
            state["total_runs"] = state.get("total_runs", 0) + 1
        except Exception as e:
            print(f"[imprint-bench] benchmark failed: {e}", file=sys.stderr)
            send_telegram(
                cfg,
                f"*LoCoMo benchmark FAILED*\nTrigger: kept #{state['kept_count']}\nError: {e}",
            )

    save_state(state)


def cmd_run(args):
    cfg = load_config()
    state = load_state()
    trigger = "manual"
    try:
        report = do_run(cfg, trigger)
        state["kept_count"] = 0
        state["last_run"] = datetime.now(timezone.utc).isoformat()
        state["last_score"] = report.get("accuracy", report.get("score"))
        state["total_runs"] = state.get("total_runs", 0) + 1
        save_state(state)
    except Exception as e:
        print(f"[imprint-bench] benchmark failed: {e}", file=sys.stderr)
        sys.exit(1)


def cmd_status(args):
    state = load_state()
    print(json.dumps(state, indent=2))


def main():
    parser = argparse.ArgumentParser(description="Imprint LoCoMo benchmark orchestrator")
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("notify-kept", help="Record a kept event from Karpathy loop")
    sub.add_parser("run", help="Force-run benchmark now")
    sub.add_parser("status", help="Show counter and last run info")

    args = parser.parse_args()

    if args.command == "notify-kept":
        cmd_notify_kept(args)
    elif args.command == "run":
        cmd_run(args)
    elif args.command == "status":
        cmd_status(args)


if __name__ == "__main__":
    main()
