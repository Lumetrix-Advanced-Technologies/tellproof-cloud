"""C-3 tests: concierge packs must pass verify.py, cmd/verify and the verify page's verifier."""
import base64
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.utils import Prehashed

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "concierge"))

import evaluate  # noqa: E402
import merkle  # noqa: E402
import sign  # noqa: E402

M = ROOT / "concierge/testdata/partner-month"
SECRET = b"partner-salt-secret"
ISSUED = "2026-10-01T09:00:00+10:00"


def evaluation():
    return evaluate.evaluate(str(M / "system.json"), [str(M / "aggregates.json")], [str(M / "usage.csv")],
                             str(M / "changes.csv"), str(M / "incidents.csv"), str(M / "evals.csv"))


def leaves(n=900):
    return [merkle.leaf_hash(merkle.salt(SECRET, f"tr-{i}"), f"tr-{i}").hex() for i in range(n)]


class FakeKMS:
    """Stands in for boto3's KMS client: same call shape, a local key behind it."""

    def __init__(self, key):
        self.key, self.calls = key, []

    def sign(self, **kw):
        self.calls.append(kw)
        return {"Signature": self.key.sign(kw["Message"], ec.ECDSA(Prehashed(hashes.SHA256())))}

    def get_public_key(self, **kw):
        return {"PublicKey": self.key.public_key().public_bytes(serialization.Encoding.DER, serialization.PublicFormat.SubjectPublicKeyInfo)}


class SignTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = Path(tempfile.mkdtemp())
        cls.key = ec.generate_private_key(ec.SECP256R1())
        pem = cls.key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
        cls.signer = sign.LocalSigner(pem, "wren-test-2026-10")
        ev = evaluation()
        cls.ev = ev
        p1 = sign.build_predicate(ev, leaves(), "WRN-RS-0001", ISSUED, None, "tr-123", SECRET, "SAMPLE. Test pack.")
        cls.pack1 = sign.sign_predicate(p1, cls.signer)
        p2 = sign.build_predicate(ev, leaves(), "WRN-RS-0002", "2026-11-01T09:00:00+11:00", cls.pack1, "tr-899", SECRET)
        cls.pack2 = sign.sign_predicate(p2, cls.signer)
        cls.files = []
        for i, env in enumerate((cls.pack1, cls.pack2), 1):
            f = cls.dir / f"WRN-RS-000{i}.dsse.json"
            f.write_text(json.dumps(env, indent=2))
            cls.files.append(str(f))
        (cls.dir / "jwks.json").write_text(json.dumps(sign.jwks(cls.key.public_key(), "wren-test-2026-10")))
        (cls.dir / "pub.pem").write_bytes(cls.key.public_key().public_bytes(serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo))

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.dir)

    def predicate(self, env):
        return json.loads(base64.b64decode(env["payload"]))["predicate"]

    def test_chain_fields(self):
        p1, p2 = self.predicate(self.pack1), self.predicate(self.pack2)
        self.assertEqual((p1["seq_no"], p1["prev_pack_digest"]), (1, None))
        self.assertEqual(p2["seq_no"], 2)
        self.assertEqual(p2["prev_pack_digest"], "sha256:" + hashlib.sha256(base64.b64decode(self.pack1["payload"])).hexdigest())
        self.assertEqual(p1["sample_notice"], "SAMPLE. Test pack.")
        self.assertNotIn("sample_notice", p2)
        self.assertEqual(p1["commitments"]["leaf_count"], 900)
        self.assertEqual(p1["commitments"]["disclosed"]["trace_id"], "tr-123")
        self.assertEqual(p1["commitments"]["disclosed"]["leaf_index"], 123)
        self.assertEqual(p1["objective_set_hash"], self.ev["objective_set_hash"])
        self.assertEqual([o["status"] for o in p1["objectives"]], [o["status"] for o in self.ev["objectives"]])

    # Acceptance: verify.py, cmd/verify and the verify page all pass the output.
    def test_concierge_verify_py(self):
        r = subprocess.run([sys.executable, str(ROOT / "concierge/verify.py"), str(self.dir / "pub.pem"), *self.files], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertTrue(r.stdout.endswith("RESULT: VERIFIED\n"), r.stdout)
        self.assertIn("links to previous pack", r.stdout)

    @unittest.skipIf(shutil.which("go") is None, "go not installed")
    def test_go_cmd_verify(self):
        r = subprocess.run(["go", "run", "./cmd/verify", "--jwks", str(self.dir / "jwks.json"), "--json", *self.files],
                           cwd=ROOT, capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        report = json.loads(r.stdout)
        self.assertTrue(report["verified"])
        self.assertEqual([(p["signature"], p["subject_digest"], p["chain"], p["merkle"]) for p in report["packs"]],
                         [(True, True, None, True), (True, True, True, True)])

    @unittest.skipIf(shutil.which("node") is None, "node not installed")
    def test_verify_page_verifier(self):
        script = ("const m = await import(process.argv[1]); const fs = await import('node:fs');"
                  "const j = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));"
                  "const [a, b] = [process.argv[3], process.argv[4]].map((f) => JSON.parse(fs.readFileSync(f, 'utf8')));"
                  "const ra = await m.verifyEnvelope(a, j); const rb = await m.verifyEnvelope(b, j, a);"
                  "console.log(JSON.stringify([ra, rb].map((r) => [r.verified, r.chain, r.merkle])));")
        r = subprocess.run(["node", "--input-type=module", "-e", script, (ROOT / "web/verify/verify.js").as_uri(),
                            str(self.dir / "jwks.json"), *self.files], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(json.loads(r.stdout), [[True, None, True], [True, True, True]])

    def test_kms_signer_sends_a_digest(self):
        kms = FakeKMS(ec.generate_private_key(ec.SECP256R1()))
        signer = sign.KMSSigner("arn:aws:kms:ap-southeast-2:111122223333:key/test", "kms-test", client=kms)
        env = sign.sign_predicate(self.predicate(self.pack1), signer)
        (call,) = kms.calls
        self.assertEqual((call["MessageType"], call["SigningAlgorithm"], len(call["Message"])), ("DIGEST", "ECDSA_SHA_256", 32))
        self.assertEqual(call["Message"], hashlib.sha256(sign.pae(sign.PAYLOAD_TYPE, base64.b64decode(env["payload"]))).digest())
        self.assertEqual(env["signatures"][0]["keyid"], "kms-test")

    def test_signature_checked_against_the_public_key(self):
        class Mixup(sign.LocalSigner):
            def public_key(self):
                return ec.generate_private_key(ec.SECP256R1()).public_key()

        pem = self.key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
        with self.assertRaisesRegex(ValueError, "does not verify"):
            sign.sign_predicate(self.predicate(self.pack1), Mixup(pem, "x"))

    def test_refuses_bad_input(self):
        ev = self.ev
        with self.assertRaisesRegex(ValueError, "pack.schema.json"):
            sign.build_predicate(ev, leaves(), "lower case id", ISSUED, None)
        with self.assertRaisesRegex(ValueError, "leaves"):
            sign.build_predicate(ev, leaves(899), "WRN-RS-0001", ISSUED, None)
        with self.assertRaisesRegex(ValueError, "not in this commitment"):
            sign.build_predicate(ev, leaves(), "WRN-RS-0001", ISSUED, None, "tr-unknown", SECRET)
        with self.assertRaisesRegex(ValueError, "not in this commitment"):
            sign.build_predicate(ev, leaves(), "WRN-RS-0001", ISSUED, None, "tr-1", b"wrong-secret")
        with self.assertRaisesRegex(ValueError, "salt secret"):
            sign.build_predicate(ev, leaves(), "WRN-RS-0001", ISSUED, None, "tr-1", None)
        with self.assertRaisesRegex(ValueError, "date-time"):
            sign.build_predicate(ev, leaves(), "WRN-RS-0001", "yesterday", None)

    def test_cli_chain(self):
        d = Path(tempfile.mkdtemp())
        try:
            keyf = d / "key.pem"
            keyf.write_bytes(self.key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
            keyf.chmod(0o600)
            (d / "evaluation.json").write_text(json.dumps(self.ev))
            (d / "leaves.txt").write_text("\n".join(leaves()) + "\n")
            env = {**os.environ, "PLUMBLINE_SALT_SECRET": SECRET.decode()}
            base = [sys.executable, str(ROOT / "concierge/evidencepack.py"), "sign", "--evaluation", str(d / "evaluation.json"),
                    "--leaves", str(d / "leaves.txt"), "--kid", "cli-key", "--key", str(keyf)]
            r1 = subprocess.run(base + ["--pack-id", "WRN-RS-0001", "--disclose", "tr-7", "--out", str(d / "1.json"), "--jwks-out", str(d / "jwks.json")],
                                capture_output=True, text=True, env=env)
            self.assertEqual(r1.returncode, 0, r1.stderr)
            r2 = subprocess.run(base + ["--pack-id", "WRN-RS-0002", "--prev", str(d / "1.json"), "--out", str(d / "2.json")], capture_output=True, text=True, env=env)
            self.assertEqual(r2.returncode, 0, r2.stderr)
            self.assertIn("WRN-RS-0002 #2", r2.stderr)
            v = subprocess.run([sys.executable, str(ROOT / "concierge/verify.py"), "/dev/stdin", str(d / "1.json"), str(d / "2.json")],
                               input=self.key.public_key().public_bytes(serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo).decode(),
                               capture_output=True, text=True)
            self.assertTrue(v.stdout.endswith("RESULT: VERIFIED\n"), v.stdout + v.stderr)
            self.assertEqual(json.loads((d / "jwks.json").read_text())["keys"][0]["kid"], "cli-key")
            keyf.chmod(0o644)
            r3 = subprocess.run(base + ["--pack-id", "WRN-RS-0003", "--out", str(d / "3.json")], capture_output=True, text=True, env=env)
            self.assertNotEqual(r3.returncode, 0)
            self.assertIn("chmod 600", r3.stderr)
        finally:
            shutil.rmtree(d)


if __name__ == "__main__":
    unittest.main()
