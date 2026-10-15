"""canon.py must produce the same canonical bytes as Go and the browser."""
import base64
import json
import struct
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "concierge"))

import canon  # noqa: E402


class CanonTest(unittest.TestCase):
    def test_jcs_vectors(self):
        v = json.loads((ROOT / "spec/jcs-vectors.json").read_text())
        for c in v["cases"]:
            with self.subTest(c["name"]):
                self.assertEqual(canon.jcs(canon.loads(c["input"])).decode(), c["expected"])
        for c in v["must_reject"]:
            with self.subTest(c["name"]):
                with self.assertRaises((ValueError, UnicodeEncodeError)):
                    canon.jcs(canon.loads(c["input"]))

    def test_numbers_match_ecmascript(self):
        # The same Node-generated table the Go tests use.
        pairs = json.loads((ROOT / "internal/jcs/testdata/es-numbers.json").read_text())
        self.assertGreater(len(pairs), 1000)
        for bits, want in pairs:
            x = struct.unpack(">d", bytes.fromhex(bits))[0]
            self.assertEqual(canon._number(x), want, f"bits {bits}")

    def test_pass_vector_payloads_reencode_identically(self):
        for f in sorted((ROOT / "spec/vectors/pass").glob("*.dsse.json")):
            payload = base64.b64decode(json.loads(f.read_text())["payload"])
            self.assertEqual(canon.jcs(canon.loads(payload.decode())), payload, f.name)

    def test_rejects(self):
        for bad in (float("nan"), float("inf"), 2**53, {1: "x"}, object(), "\ud800"):
            with self.assertRaises((ValueError, TypeError, UnicodeEncodeError)):
                canon.jcs(bad)
        self.assertEqual(canon.jcs(2**53 - 1), b"9007199254740991")
        self.assertEqual(canon.jcs({"b": [True, None], "a": -0.0}), b'{"a":0,"b":[true,null]}')


if __name__ == "__main__":
    unittest.main()
