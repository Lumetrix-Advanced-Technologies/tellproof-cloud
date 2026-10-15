"""Concierge CLI: hand-build evidence packs for design partners (Phase C).

  python3 concierge/evidencepack.py ingest --source otlp|langfuse --out aggregates.json --leaves leaves.txt FILE...

The salt secret comes from PLUMBLINE_SALT_SECRET or --salt-secret-file (mode 0600).
Frozen after Milestone 1: bug fixes only, each with a matching vector.
"""
import argparse
import json
import os
import stat
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import ingest  # noqa: E402


def read_secret_file(path: str) -> bytes:
    p = Path(path)
    mode = stat.S_IMODE(p.stat().st_mode)
    if mode & 0o077:
        raise SystemExit(f"evidencepack: {path} must not be readable by group or others (chmod 600)")
    return p.read_bytes().strip()


def salt_secret(args) -> bytes:
    if args.salt_secret_file:
        return read_secret_file(args.salt_secret_file)
    secret = os.environ.get("PLUMBLINE_SALT_SECRET", "").encode()
    if not secret:
        raise SystemExit("evidencepack: set PLUMBLINE_SALT_SECRET or pass --salt-secret-file")
    return secret


def cmd_ingest(args) -> int:
    out, leaves = ingest.ingest(args.source, args.files, salt_secret(args), args.attributes)
    Path(args.out).write_text(json.dumps(out, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    Path(args.leaves).write_text("".join(l + "\n" for l in leaves), encoding="utf-8")
    calls = sum(w["metrics"]["guardrails"]["llm_calls"] for w in out["windows"])
    print(f"evidencepack: {len(out['windows'])} hourly windows, {len(leaves)} traces committed, {calls} LLM calls -> {args.out}, {args.leaves}",
          file=sys.stderr)
    return 0


def cmd_evaluate(args) -> int:
    import evaluate

    ev = evaluate.evaluate(args.system, args.aggregates, args.usage, args.changes, args.incidents, args.evals)
    Path(args.out).write_text(json.dumps(ev, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    counts = {s: sum(o["status"] == s for o in ev["objectives"]) for s in ("pass", "attention", "fail")}
    print(f"evidencepack: {counts['pass']} pass, {counts['attention']} attention, {counts['fail']} fail; "
          f"coverage {ev['coverage']['total_pct']}% over {ev['windows']} windows -> {args.out}", file=sys.stderr)
    for o in ev["objectives"]:
        print(f"  {o['id']:<11} {o['status']:<9} {o['measured']}", file=sys.stderr)
    return 0


def main(argv=None) -> int:
    p = argparse.ArgumentParser(prog="evidencepack", description=__doc__.splitlines()[0])
    sub = p.add_subparsers(dest="cmd", required=True)
    g = sub.add_parser("ingest", help="exports -> hourly aggregates (no content leaves)")
    g.add_argument("--source", choices=["otlp", "langfuse"], required=True)
    g.add_argument("--out", required=True, help="aggregates JSON to write")
    g.add_argument("--leaves", required=True, help="salted Merkle leaves to write, one hex hash per line")
    g.add_argument("--attributes", help="per-partner attribute names (JSON), overriding ingest.DEFAULT_ATTRIBUTES")
    g.add_argument("--salt-secret-file")
    g.add_argument("files", nargs="+")
    g.set_defaults(fn=cmd_ingest)
    e = sub.add_parser("evaluate", help="aggregates + provider usage + registers -> the ten objectives")
    e.add_argument("--system", required=True, help="the AI system's declarations (JSON)")
    e.add_argument("--aggregates", action="append", required=True, help="output of ingest (repeatable)")
    e.add_argument("--usage", action="append", required=True, help="provider usage CSV (repeatable)")
    e.add_argument("--changes", help="change register CSV")
    e.add_argument("--incidents", help="incident register CSV")
    e.add_argument("--evals", help="eval runs CSV")
    e.add_argument("--out", required=True)
    e.set_defaults(fn=cmd_evaluate)
    args = p.parse_args(argv)
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
