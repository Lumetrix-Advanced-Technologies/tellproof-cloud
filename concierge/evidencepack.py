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
    args = p.parse_args(argv)
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
