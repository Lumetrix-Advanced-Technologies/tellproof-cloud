"""Offline verifier for assurance packs (sample).

Usage: python verify.py sample-public-key.pem KST-CTA-0001.dsse.json KST-CTA-0002.dsse.json ...
Pass the packs oldest first so the chain can be checked.
Checks: DSSE signature, subject digest == sha256(JCS(predicate)), chain links, disclosed-trace Merkle proof.
"""
import base64, hashlib, json, sys
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.exceptions import InvalidSignature


def norm(o):
    if isinstance(o, float):
        return int(o) if o.is_integer() else o
    if isinstance(o, dict):
        return {k: norm(v) for k, v in o.items()}
    if isinstance(o, list):
        return [norm(v) for v in o]
    return o


def jcs(o):
    return json.dumps(norm(o), sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


def pae(t, body):
    t = t.encode()
    return b"DSSEv1 %d %s %d %s" % (len(t), t, len(body), body)


def check(pubkey, path, prev_digest):
    env = json.load(open(path))
    payload = base64.b64decode(env["payload"])
    sig = base64.b64decode(env["signatures"][0]["sig"])
    ok = True
    try:
        pubkey.verify(sig, pae(env["payloadType"], payload), ec.ECDSA(hashes.SHA256()))
        print("  signature ............ valid")
    except InvalidSignature:
        print("  signature ............ INVALID")
        return None, False
    st = json.loads(payload)
    pred = st["predicate"]
    d = hashlib.sha256(jcs(pred)).hexdigest()
    match = d == st["subject"][0]["digest"]["sha256"]
    print("  subject digest ....... " + ("matches" if match else "MISMATCH"))
    ok &= match
    if prev_digest is not None:
        link = pred["prev_pack_digest"] == f"sha256:{prev_digest}"
        print("  chain link ........... " + ("links to previous pack" if link else "BROKEN"))
        ok &= link
    c = pred["commitments"]
    disc = c["disclosed"]
    if disc is None:  # schema-valid: no trace disclosed, so there is no proof to check (D-016)
        print("  disclosed trace ...... none disclosed (not checked)")
        return hashlib.sha256(payload).hexdigest(), ok
    h = hashlib.sha256(b"\x00" + bytes.fromhex(disc["salt"]) + disc["trace_id"].encode()).digest()
    for step in disc["proof"]:
        s = bytes.fromhex(step["hash"])
        h = hashlib.sha256(b"\x01" + (s + h if step["side"] == "L" else h + s)).digest()
    inc = h.hex() == c["trace_merkle_root"]
    print("  disclosed trace ...... " + ("included in commitment" if inc else "NOT included"))
    ok &= inc
    return hashlib.sha256(payload).hexdigest(), ok


if __name__ == "__main__":
    key = serialization.load_pem_public_key(open(sys.argv[1], "rb").read())
    prev, all_ok = None, True
    for p in sys.argv[2:]:
        print(p)
        prev, ok = check(key, p, prev)
        all_ok &= ok
    print("RESULT:", "VERIFIED" if all_ok else "FAILED")
    sys.exit(0 if all_ok else 1)
