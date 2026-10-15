"""C-3 sign and chain: evaluation + leaves -> a signed, chained pack (DSSE envelope).

Predicate -> statement -> JCS payload -> PAE -> ECDSA P-256 / SHA-256 signature (DER),
by a local key or AWS KMS (MessageType DIGEST: the 32-byte SHA-256 of the PAE, D-006).
seq_no and prev_pack_digest come from the previous pack. Before writing, the predicate
is checked against spec/pack.schema.json and the signature is verified with the public
key, so a key mix-up never leaves the machine.
"""
import base64
import datetime as dt
import hashlib
import json
from pathlib import Path
from zoneinfo import ZoneInfo

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.utils import Prehashed

import canon
import merkle
import schema

SPEC = Path(__file__).resolve().parent.parent / "spec"
PAYLOAD_TYPE = "application/vnd.in-toto+json"
STATEMENT_TYPE = "https://in-toto.io/Statement/v1"
PREDICATE_TYPE = "https://lumetrix.example/plumbline/pack/v0.1"  # placeholder domain until D-008
PACK_SCHEMA = "assurance-pack/v0.1"
LEAF_RULE = "sha256(0x00 || salt16 || trace_id)"
NODE_RULE = "sha256(0x01 || left || right); odd node carried up"


def pae(ptype: str, body: bytes) -> bytes:
    t = ptype.encode()
    return b"DSSEv1 %d %s %d %s" % (len(t), t, len(body), body)


def pack_digest(envelope: dict) -> str:
    return "sha256:" + hashlib.sha256(base64.b64decode(envelope["payload"], validate=True)).hexdigest()


# ---------- signers ----------

class LocalSigner:
    """A P-256 key from a PEM file (tests and design-partner pilots before KMS)."""

    def __init__(self, pem: bytes, kid: str):
        key = serialization.load_pem_private_key(pem, password=None)
        if not isinstance(key, ec.EllipticCurvePrivateKey) or key.curve.name != "secp256r1":
            raise ValueError("signing key must be ECDSA P-256")
        self.key, self.kid = key, kid

    def sign(self, pae_bytes: bytes) -> bytes:
        return self.key.sign(pae_bytes, ec.ECDSA(hashes.SHA256()))

    def public_key(self):
        return self.key.public_key()


class KMSSigner:
    """AWS KMS asymmetric key (ECC_NIST_P256, SIGN_VERIFY). KMS signs at most 4,096 bytes,
    so it gets the SHA-256 of the PAE with MessageType DIGEST (D-006)."""

    def __init__(self, key_id: str, kid: str, region: str | None = None, client=None):
        if client is None:
            import boto3  # only needed for KMS signing

            client = boto3.client("kms", region_name=region)
        self.client, self.key_id, self.kid = client, key_id, kid

    def sign(self, pae_bytes: bytes) -> bytes:
        digest = hashlib.sha256(pae_bytes).digest()
        resp = self.client.sign(KeyId=self.key_id, Message=digest, MessageType="DIGEST", SigningAlgorithm="ECDSA_SHA_256")
        return resp["Signature"]

    def public_key(self):
        der = self.client.get_public_key(KeyId=self.key_id)["PublicKey"]
        key = serialization.load_der_public_key(der)
        if not isinstance(key, ec.EllipticCurvePublicKey) or key.curve.name != "secp256r1":
            raise ValueError("KMS key is not ECC_NIST_P256")
        return key


# ---------- predicate ----------

def _drop_empty(d: dict, keys) -> dict:
    """Optional fields are absent rather than empty (D-016, D-018)."""
    return {k: v for k, v in d.items() if not (k in keys and v in ("", None))}


def commitments(leaves_hex: list[str], disclose: str | None, salt_secret: bytes | None) -> dict:
    leaves = [bytes.fromhex(x) for x in leaves_hex]
    if not leaves:
        raise ValueError("no leaves: nothing was committed")
    tree = merkle.levels(leaves)
    c = {"trace_merkle_root": tree[-1][0].hex(), "leaf_count": len(leaves), "leaf": LEAF_RULE, "node": NODE_RULE, "disclosed": None}
    if disclose:
        if not salt_secret:
            raise ValueError("disclosing a trace needs the salt secret")
        salt = merkle.salt(salt_secret, disclose)
        leaf = merkle.leaf_hash(salt, disclose)
        try:
            index = leaves.index(leaf)
        except ValueError:
            raise ValueError(f"trace {disclose!r} is not in this commitment (or the salt secret differs)") from None
        steps = merkle.proof(tree, index)
        if not merkle.verify(leaf, steps, tree[-1][0]):
            raise AssertionError("own proof does not verify")
        c["disclosed"] = {"trace_id": disclose, "leaf_index": index, "salt": salt.hex(), "proof": steps}
    return c


def build_predicate(ev: dict, leaves_hex: list[str], pack_id: str, issued_at: str, prev: dict | None,
                    disclose: str | None = None, salt_secret: bytes | None = None, sample_notice: str | None = None) -> dict:
    if ev.get("schema") != "plumbline-concierge-evaluation/v0.1":
        raise ValueError("not a concierge evaluation file")
    if len(leaves_hex) != ev["leaf_count"]:
        raise ValueError(f"leaves file has {len(leaves_hex)} leaves but the evaluated windows committed {ev['leaf_count']}")
    seq_no, prev_digest = 1, None
    if prev is not None:
        prev_pred = json.loads(base64.b64decode(prev["payload"], validate=True))["predicate"]
        seq_no, prev_digest = prev_pred["seq_no"] + 1, pack_digest(prev)
    p = {
        "pack_schema": PACK_SCHEMA,
        "pack_id": pack_id,
        "seq_no": seq_no,
        "prev_pack_digest": prev_digest,
        "issued_at": issued_at,
        "period": {k: ev["period"][k] for k in ("start", "end", "timezone")},
        "sample_notice": sample_notice,
        "subject": _drop_empty(dict(ev["subject"]), {"description"}),
        "profiles": ev["profiles"],
        "assurance_level": ev["assurance_level"],
        "sources": ev["sources"],
        "coverage": ev["coverage"],
        "lineage": ev["lineage"],
        "changes": ev["changes"],
        "incidents": [_drop_empty(i, {"acknowledged", "resolved"}) for i in ev["incidents"]],
        "objectives": [{k: o[k] for k in ("id", "title", "status", "threshold", "measured", "basis", "maps")} for o in ev["objectives"]],
        "commitments": commitments(leaves_hex, disclose, salt_secret),
        "limitations": ev["limitations"],
        "objective_set_hash": ev["objective_set_hash"],
    }
    p = _drop_empty(p, {"sample_notice"})
    problems = schema.errors(p, json.loads((SPEC / "pack.schema.json").read_text()))
    if problems:
        raise ValueError("predicate does not match spec/pack.schema.json:\n  " + "\n  ".join(problems))
    return p


def statement(predicate: dict) -> dict:
    return {"_type": STATEMENT_TYPE,
            "subject": [{"name": predicate["pack_id"], "digest": {"sha256": hashlib.sha256(canon.jcs(predicate)).hexdigest()}}],
            "predicateType": PREDICATE_TYPE, "predicate": predicate}


def sign_predicate(predicate: dict, signer) -> dict:
    payload = canon.jcs(statement(predicate))
    if canon.jcs(canon.loads(payload.decode("utf-8"))) != payload:
        raise AssertionError("payload is not canonical")  # D-018: every verifier requires this
    to_sign = pae(PAYLOAD_TYPE, payload)
    sig = signer.sign(to_sign)
    # Verify before the pack leaves the machine (catches key mix-ups).
    try:
        signer.public_key().verify(sig, hashlib.sha256(to_sign).digest(), ec.ECDSA(Prehashed(hashes.SHA256())))
    except InvalidSignature:
        raise ValueError("the signature does not verify with the signer's public key") from None
    return {"payloadType": PAYLOAD_TYPE, "payload": base64.b64encode(payload).decode(),
            "signatures": [{"keyid": signer.kid, "sig": base64.b64encode(sig).decode()}]}


def issued_now(timezone: str) -> str:
    return dt.datetime.now(ZoneInfo(timezone)).replace(microsecond=0).isoformat()


def jwks(public_key, kid: str) -> dict:
    n = public_key.public_numbers()
    b64u = lambda i: base64.urlsafe_b64encode(i.to_bytes(32, "big")).rstrip(b"=").decode()  # noqa: E731
    return {"keys": [{"kty": "EC", "crv": "P-256", "kid": kid, "use": "sig", "alg": "ES256", "x": b64u(n.x), "y": b64u(n.y)}]}
