"""C-4 share: publish a pack behind an unguessable link on a static site.

The site holds the verify page (web/verify plus web/brand.css) and, at the same paths
and in the same shape as the future API (docs/architecture.md section 4.2), one file
per share at v1/verify/<token> and the issuer keys at .well-known/jwks.json. The link
is <base>/verify/?t=<token>. The token is shown once; the share log keeps only its
SHA-256, never the token (CLAUDE.md). Revoking deletes the file: the page then says
there is no pack at the link. A pack that does not verify is never published.
"""
import base64
import csv
import datetime as dt
import hashlib
import json
import secrets
import shutil
from pathlib import Path

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric import ec

import canon
import merkle
import sign

ROOT = Path(__file__).resolve().parent.parent
PAGE_FILES = ["index.html", "page.js", "verify.js", "verify.css"]
LOG_COLUMNS = ["created_at", "token_sha256", "pack_id", "seq_no", "pack_digest", "verifier_org", "note"]


def _b64url(b: bytes) -> str:
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def _b64url_decode(s: str) -> bytes:
    return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


def new_token() -> str:
    return _b64url(secrets.token_bytes(16))  # section 4.6: 16 random bytes, base64url


def token_hash(token: str) -> str:
    return hashlib.sha256(token.encode()).hexdigest()


def _jwk_key(jwk: dict):
    if jwk.get("kty") != "EC" or jwk.get("crv") != "P-256":
        raise ValueError("JWKS keys must be EC P-256")
    x, y = (int.from_bytes(_b64url_decode(jwk[c]), "big") for c in ("x", "y"))
    return ec.EllipticCurvePublicNumbers(x, y, ec.SECP256R1()).public_key()


def check_pack(env: dict, jwks: dict, prev_digest: str | None) -> dict:
    """Signature (by kid), subject digest, chain and disclosed proof. Returns the predicate."""
    keys = {k["kid"]: _jwk_key(k) for k in jwks["keys"]}
    if env.get("payloadType") != sign.PAYLOAD_TYPE:
        raise ValueError("not a pack envelope")
    payload = base64.b64decode(env["payload"], validate=True)
    to_sign = sign.pae(env["payloadType"], payload)
    ok = False
    for s in env["signatures"]:
        key = keys.get(s.get("keyid"))
        if key is None:
            continue
        try:
            key.verify(base64.b64decode(s["sig"], validate=True), to_sign, ec.ECDSA(hashes.SHA256()))
            ok = True
            break
        except InvalidSignature:
            pass
    if not ok:
        raise ValueError("the pack's signature does not verify with the JWKS being published")
    if canon.jcs(canon.loads(payload.decode("utf-8"))) != payload:
        raise ValueError("the pack payload is not canonical")
    st = json.loads(payload)
    p = st["predicate"]
    if st["subject"][0]["digest"]["sha256"] != hashlib.sha256(canon.jcs(p)).hexdigest():
        raise ValueError("the subject digest does not match the predicate")
    if prev_digest is not None and p["prev_pack_digest"] != prev_digest:
        raise ValueError("the pack does not link to the previous pack given")
    d = p["commitments"]["disclosed"]
    if d is not None:
        leaf = merkle.leaf_hash(bytes.fromhex(d["salt"]), d["trace_id"])
        if not merkle.verify(leaf, d["proof"], bytes.fromhex(p["commitments"]["trace_merkle_root"])):
            raise ValueError("the disclosed trace's proof does not reach the commitment root")
    return p


def _install_page(site: Path) -> None:
    (site / "verify").mkdir(parents=True, exist_ok=True)
    shutil.copyfile(ROOT / "web/brand.css", site / "brand.css")
    for f in PAGE_FILES:
        shutil.copyfile(ROOT / "web/verify" / f, site / "verify" / f)


def _publish_jwks(site: Path, jwks: dict) -> None:
    path = site / ".well-known/jwks.json"
    path.parent.mkdir(parents=True, exist_ok=True)
    keys = {k["kid"]: k for k in (json.loads(path.read_text())["keys"] if path.exists() else [])}
    for k in jwks["keys"]:
        if k["kid"] in keys and keys[k["kid"]] != k:
            raise ValueError(f"kid {k['kid']!r} is already published with a different key")
        keys[k["kid"]] = k
    path.write_text(json.dumps({"keys": list(keys.values())}, indent=2) + "\n")


def share(pack_path: str, jwks_path: str, site_dir: str, base_url: str, verifier_org: str,
          prev_path: str | None = None, log_path: str | None = None, note: str = "") -> tuple[str, dict]:
    env = json.loads(Path(pack_path).read_text())
    jwks = json.loads(Path(jwks_path).read_text())
    prev_digest = sign.pack_digest(json.loads(Path(prev_path).read_text())) if prev_path else None
    p = check_pack(env, jwks, prev_digest)
    if p["seq_no"] > 1 and prev_path is None:
        raise ValueError("this pack follows another: pass --prev so the verifier sees the chain checked")
    site = Path(site_dir)
    _install_page(site)
    _publish_jwks(site, jwks)
    token = new_token()
    target = site / "v1/verify" / token
    target.parent.mkdir(parents=True, exist_ok=True)
    body = {"envelope": env, "prev_envelope_digest": prev_digest, "pack_meta": {"pack_id": p["pack_id"], "seq_no": p["seq_no"]}}
    target.write_text(json.dumps(body) + "\n")
    row = {"created_at": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"), "token_sha256": token_hash(token),
           "pack_id": p["pack_id"], "seq_no": p["seq_no"], "pack_digest": sign.pack_digest(env), "verifier_org": verifier_org, "note": note}
    log = Path(log_path) if log_path else site.parent / "share-log.csv"
    new = not log.exists()
    with log.open("a", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=LOG_COLUMNS)
        if new:
            w.writeheader()
        w.writerow(row)
    return f"{base_url.rstrip('/')}/verify/?t={token}", row


def revoke(site_dir: str, token: str | None = None, token_sha256: str | None = None) -> str:
    """Remove a share by its token or the token's SHA-256 (as the share log records it)."""
    want = token_hash(token) if token else token_sha256
    for f in (Path(site_dir) / "v1/verify").iterdir():
        if token_hash(f.name) == want:
            f.unlink()
            return want
    raise ValueError("no share with that token on this site")
