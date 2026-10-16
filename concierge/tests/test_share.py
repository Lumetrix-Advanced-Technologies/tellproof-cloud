"""C-4 tests: a verifier opens a share link and sees Verified."""
import base64
import csv
import functools
import http.server
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from pathlib import Path

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ec

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "concierge"))
sys.path.insert(0, str(ROOT / "concierge/tests"))

import share  # noqa: E402
import sign  # noqa: E402
from test_sign import ISSUED, SECRET, evaluation, leaves  # noqa: E402

CHROME = shutil.which("google-chrome") or shutil.which("chromium") or shutil.which("chromium-browser")


class Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass


def chrome_dom(url: str) -> str:
    with tempfile.TemporaryDirectory() as prof:
        r = subprocess.run([CHROME, "--headless=new", "--disable-gpu", "--no-sandbox", f"--user-data-dir={prof}",
                            "--virtual-time-budget=5000", "--dump-dom", url], capture_output=True, text=True, timeout=60)
    return r.stdout


class ShareTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.d = Path(tempfile.mkdtemp())
        key = ec.generate_private_key(ec.SECP256R1())
        pem = key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
        signer = sign.LocalSigner(pem, "wren-test-2026-10")
        ev = evaluation()
        p1 = sign.build_predicate(ev, leaves(), "WRN-RS-0001", ISSUED, None, "tr-123", SECRET)
        cls.pack1 = sign.sign_predicate(p1, signer)
        cls.pack2 = sign.sign_predicate(sign.build_predicate(ev, leaves(), "WRN-RS-0002", ISSUED, cls.pack1), signer)
        for name, env in (("1.json", cls.pack1), ("2.json", cls.pack2)):
            (cls.d / name).write_text(json.dumps(env))
        (cls.d / "jwks.json").write_text(json.dumps(sign.jwks(key.public_key(), "wren-test-2026-10")))
        cls.site = cls.d / "site"
        cls.url1, cls.row1 = share.share(str(cls.d / "1.json"), str(cls.d / "jwks.json"), str(cls.site), "http://127.0.0.1:PORT", "Bank X")
        cls.url2, cls.row2 = share.share(str(cls.d / "2.json"), str(cls.d / "jwks.json"), str(cls.site), "http://127.0.0.1:PORT", "Insurer Y",
                                         prev_path=str(cls.d / "1.json"))

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.d)

    def token(self, url):
        return url.split("?t=")[1]

    def test_site_layout_matches_the_api(self):
        for f in ("brand.css", "verify/index.html", "verify/page.js", "verify/verify.js", "verify/verify.css", ".well-known/jwks.json"):
            self.assertTrue((self.site / f).is_file(), f)
        self.assertFalse((self.site / "verify/verify.test.js").exists())
        body = json.loads((self.site / "v1/verify" / self.token(self.url2)).read_text())
        self.assertEqual(set(body), {"envelope", "prev_envelope_digest", "pack_meta"})
        self.assertEqual(body["prev_envelope_digest"], sign.pack_digest(self.pack1))
        self.assertIsNone(json.loads((self.site / "v1/verify" / self.token(self.url1)).read_text())["prev_envelope_digest"])

    def test_tokens_are_unguessable_and_never_logged(self):
        t1, t2 = self.token(self.url1), self.token(self.url2)
        self.assertNotEqual(t1, t2)
        self.assertEqual(len(base64.urlsafe_b64decode(t1 + "==")), 16)
        log = (self.d / "share-log.csv").read_text()
        self.assertNotIn(t1, log)
        self.assertNotIn(t2, log)
        rows = list(csv.DictReader(log.splitlines()))
        self.assertEqual([(r["pack_id"], r["verifier_org"], r["token_sha256"]) for r in rows],
                         [("WRN-RS-0001", "Bank X", share.token_hash(t1)), ("WRN-RS-0002", "Insurer Y", share.token_hash(t2))])
        self.assertFalse(any(p.name == "share-log.csv" for p in self.site.rglob("*")), "the log must not be published")

    def test_refuses_packs_that_do_not_verify(self):
        bad = dict(self.pack1, payload=base64.b64encode(base64.b64decode(self.pack1["payload"]).replace(b"Referral", b"Referal")).decode())
        (self.d / "bad.json").write_text(json.dumps(bad))
        with self.assertRaisesRegex(ValueError, "signature"):
            share.share(str(self.d / "bad.json"), str(self.d / "jwks.json"), str(self.d / "other-site"), "https://x", "Bank X")
        with self.assertRaisesRegex(ValueError, "--prev"):
            share.share(str(self.d / "2.json"), str(self.d / "jwks.json"), str(self.d / "other-site"), "https://x", "Bank X")
        with self.assertRaisesRegex(ValueError, "does not link"):
            share.share(str(self.d / "2.json"), str(self.d / "jwks.json"), str(self.d / "other-site"), "https://x", "Bank X", prev_path=str(self.d / "2.json"))
        other = sign.jwks(ec.generate_private_key(ec.SECP256R1()).public_key(), "wren-test-2026-10")
        (self.d / "other-jwks.json").write_text(json.dumps(other))
        with self.assertRaises(ValueError):  # the signature does not verify with another key under the same kid
            share.share(str(self.d / "1.json"), str(self.d / "other-jwks.json"), str(self.site), "https://x", "Bank X")

    def test_jwks_conflict(self):
        site = self.d / "conflict-site"
        share.share(str(self.d / "1.json"), str(self.d / "jwks.json"), str(site), "https://x", "Bank X", log_path=str(self.d / "c.csv"))
        other = sign.jwks(ec.generate_private_key(ec.SECP256R1()).public_key(), "wren-test-2026-10")
        with self.assertRaisesRegex(ValueError, "already published"):
            share._publish_jwks(site, other)

    @unittest.skipIf(CHROME is None, "no Chrome for the end-to-end check")
    def test_verifier_opens_the_link_and_sees_verified(self):
        # Acceptance: serve the site, open the links as a verifier would.
        handler = functools.partial(Quiet, directory=str(self.site))
        srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        try:
            base = f"http://127.0.0.1:{srv.server_address[1]}"
            dom1 = chrome_dom(self.url1.replace("http://127.0.0.1:PORT", base))
            self.assertIn("Verified, unaltered since signing", dom1)
            self.assertIn("Referral Summariser", dom1)
            dom2 = chrome_dom(self.url2.replace("http://127.0.0.1:PORT", base))
            self.assertIn("Verified, unaltered since signing", dom2)
            self.assertIn("Links to the previous pack", dom2)
            # Revoked: the link stops working.
            site_copy = self.d / "revoked-site"
            shutil.copytree(self.site, site_copy)
            share.revoke(str(site_copy), token_sha256=self.row1["token_sha256"])
            srv.RequestHandlerClass = functools.partial(Quiet, directory=str(site_copy))
            dom3 = chrome_dom(self.url1.replace("http://127.0.0.1:PORT", base))
            self.assertIn("No pack at this link", dom3)
        finally:
            srv.shutdown()
            srv.server_close()

    def test_cli_share_and_unshare(self):
        site = self.d / "cli-site"
        r = subprocess.run([sys.executable, str(ROOT / "concierge/evidencepack.py"), "share", "--pack", str(self.d / "1.json"), "--jwks", str(self.d / "jwks.json"),
                            "--site-dir", str(site), "--base-url", "https://verify.example", "--verifier", "Bank X", "--log", str(self.d / "cli-log.csv")],
                           capture_output=True, text=True, env=os.environ)
        self.assertEqual(r.returncode, 0, r.stderr)
        url = r.stdout.strip()
        self.assertTrue(url.startswith("https://verify.example/verify/?t="))
        token = url.split("?t=")[1]
        self.assertNotIn(token, r.stderr)
        self.assertTrue((site / "v1/verify" / token).exists())
        u = subprocess.run([sys.executable, str(ROOT / "concierge/evidencepack.py"), "unshare", "--site-dir", str(site), "--token=" + token],
                           capture_output=True, text=True, env=os.environ)
        self.assertEqual(u.returncode, 0, u.stderr)
        self.assertFalse((site / "v1/verify" / token).exists())


if __name__ == "__main__":
    unittest.main()
