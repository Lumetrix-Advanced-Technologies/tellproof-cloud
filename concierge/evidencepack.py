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


def cmd_sign(args) -> int:
    import sign

    ev = json.loads(Path(args.evaluation).read_text(encoding="utf-8"))
    leaves = Path(args.leaves).read_text().split()
    prev = json.loads(Path(args.prev).read_text()) if args.prev else None
    secret = salt_secret(args) if args.disclose else None
    if args.kms_key_id:
        signer = sign.KMSSigner(args.kms_key_id, args.kid, region=args.kms_region)
    else:
        signer = sign.LocalSigner(read_secret_file(args.key), args.kid)
    issued = args.issued_at or sign.issued_now(ev["period"]["timezone"])
    predicate = sign.build_predicate(ev, leaves, args.pack_id, issued, prev, args.disclose, secret, args.sample_notice)
    env = sign.sign_predicate(predicate, signer)
    Path(args.out).write_text(json.dumps(env, indent=2) + "\n")
    if args.jwks_out:
        Path(args.jwks_out).write_text(json.dumps(sign.jwks(signer.public_key(), args.kid), indent=2) + "\n")
    print(f"evidencepack: {predicate['pack_id']} #{predicate['seq_no']} {sign.pack_digest(env)} signed by {args.kid} -> {args.out}",
          file=sys.stderr)
    return 0


def cmd_share(args) -> int:
    import share

    url, row = share.share(args.pack, args.jwks, args.site_dir, args.base_url, args.verifier, args.prev, args.log, args.note or "")
    print(f"evidencepack: shared {row['pack_id']} #{row['seq_no']} with {row['verifier_org']}; logged token sha256 {row['token_sha256']}",
          file=sys.stderr)
    print("evidencepack: the link is shown once; copy it into the CRM now. Publish the site directory to make it live.", file=sys.stderr)
    print(url)
    return 0


def cmd_unshare(args) -> int:
    import share

    h = share.revoke(args.site_dir, token=args.token, token_sha256=args.token_sha256)
    print(f"evidencepack: removed share {h}; publish the site directory to take the link down", file=sys.stderr)
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
    s = sub.add_parser("sign", help="evaluation + leaves -> a signed pack chained to the previous one")
    s.add_argument("--evaluation", required=True)
    s.add_argument("--leaves", required=True, help="leaves file from ingest for the evaluated windows")
    s.add_argument("--pack-id", required=True, help="e.g. WRN-RS-0001")
    s.add_argument("--prev", help="the previous pack in this system's chain (sets seq_no and prev_pack_digest)")
    s.add_argument("--issued-at", help="RFC 3339 time; default now in the period's timezone")
    s.add_argument("--disclose", metavar="TRACE_ID", help="reveal one trace with its Merkle proof (needs the salt secret)")
    s.add_argument("--salt-secret-file")
    s.add_argument("--sample-notice", help="mark a sample pack (never on a real customer pack)")
    s.add_argument("--kid", required=True, help="key ID published in the JWKS")
    key = s.add_mutually_exclusive_group(required=True)
    key.add_argument("--key", help="local P-256 private key, PEM, mode 0600")
    key.add_argument("--kms-key-id", help="AWS KMS key ID or ARN (ECC_NIST_P256); needs boto3")
    s.add_argument("--kms-region", default="ap-southeast-2")
    s.add_argument("--jwks-out", help="also write the signer's public key as a JWKS")
    s.add_argument("--out", required=True)
    s.set_defaults(fn=cmd_sign)
    sh = sub.add_parser("share", help="publish a pack behind an unguessable link on the static verify site")
    sh.add_argument("--pack", required=True)
    sh.add_argument("--prev", help="the previous pack, so the verifier sees the chain checked")
    sh.add_argument("--jwks", required=True, help="issuer JWKS to publish at /.well-known/jwks.json")
    sh.add_argument("--site-dir", required=True, help="static site root to publish (page, keys, shares)")
    sh.add_argument("--base-url", required=True, help="where the site is served, e.g. https://verify.example")
    sh.add_argument("--verifier", required=True, help="the verifier organisation the link is for")
    sh.add_argument("--note", help="free text for the share log")
    sh.add_argument("--log", help="share log CSV (default: share-log.csv beside the site directory, never inside it)")
    sh.set_defaults(fn=cmd_share)
    un = sub.add_parser("unshare", help="revoke a share link")
    un.add_argument("--site-dir", required=True)
    who = un.add_mutually_exclusive_group(required=True)
    who.add_argument("--token")
    who.add_argument("--token-sha256", help="as recorded in the share log")
    un.set_defaults(fn=cmd_unshare)
    args = p.parse_args(argv)
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
