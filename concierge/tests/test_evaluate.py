"""C-2 tests: evaluate one design partner's month and compare with a hand calculation."""
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "concierge"))

import evaluate  # noqa: E402

M = ROOT / "concierge/testdata/partner-month"
ARGS = dict(system_path=str(M / "system.json"), aggregate_paths=[str(M / "aggregates.json")], usage_paths=[str(M / "usage.csv")],
            changes_path=str(M / "changes.csv"), incidents_path=str(M / "incidents.csv"), evals_path=str(M / "evals.csv"))

# Hand calculation for Wren Health, September 2026 (Australia/Sydney):
#  period            = 2026-08-31T14:00Z .. 2026-09-30T14:00Z; the 2026-08-31T10:00Z usage row falls before it
#  billed  Bedrock   = (610000+152000) + (410000+102000) + (530000+101000) = 1,905,000 tokens, 2,520 requests
#  traced  Bedrock   = 750000 + 125000 + 375000 + 600000                   = 1,850,000 tokens, 2,500 calls
#  billed  OpenAI    = 81,000 tokens, 800 requests; traced 80,000 tokens, 800 calls
#  EO-LOG-01  1,930,000 / 1,986,000 = 0.97180 -> 97.1%   >= 0.97 (0.98 - 0.01 band)      attention
#  EO-LIN-01  3 models observed, all 3 declared                                           pass
#  EO-CHG-01  model change 2026-09-10 logged; prompt 14->15 at 2026-09-28 08:10 Sydney
#             has no prompt entry within a day -> 1 unlogged; 2 register entries in period fail
#  EO-PII-01  350 / 350                                                                    pass
#  EO-CNT-01  2 content spans                                                              fail
#  EO-GRD-01  3,296 / 3,300 = 99.87% -> 99.8% covered; 10 / 3,300 = 0.30% blocked          pass
#  EO-EVAL-01 4.5 v14 carried (eval 2026-08-20, 0.95); 4.6 v14 eval 2 days before release;
#             4.6 v15 only eval is 0.85 < 0.90; live 2d16h < 7 days                        attention
#  EO-INC-01  guardrail spike 03:00-04:00Z: INC-0008 opened 05:30Z, after 04:00Z + 60 min;
#             error burst 22:00-23:00Z: INC-0009 opened 22:30Z, acknowledged in 20 min -> 1 fail
#  EO-AGT-01  29 / 2,500 = 1.16% <= 1.2% (1.0% + 0.2% band); escalations 24 / 900 = 2.6%  attention
#  EO-RES-01  regions ap-southeast-2, us-east-1; declared ap-southeast-2 only              fail
#  lineage    calls 4.6 1,300 (39.4%), 4.5 1,200 (36.4%), embeddings 800 (24.2%) of 3,300
HAND = {
    "EO-LOG-01": ("attention", "97.1% of billed tokens traced"),
    "EO-LIN-01": ("pass", "3 of 3 models observed are declared"),
    "EO-CHG-01": ("fail", "2 change(s) logged, 1 unlogged transition(s)"),
    "EO-PII-01": ("pass", "100.0% of tagged spans redacted"),
    "EO-CNT-01": ("fail", "2 spans with message content attributes"),
    "EO-GRD-01": ("pass", "99.8% covered; 0.30% blocked"),
    "EO-EVAL-01": ("attention", "No passing run on live anthropic.claude-sonnet-4-6 prompt v15 (live 2 days)"),
    "EO-INC-01": ("fail", "2 incident(s); 1 anomaly window(s) without an incident acknowledged within 60 min"),
    "EO-AGT-01": ("attention", "1.16% tool errors; 2.6% of runs escalated; 35 of 35 high-impact calls approved"),
    "EO-RES-01": ("fail", "Calls in undeclared region(s): us-east-1"),
}


class EvaluateTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.ev = evaluate.evaluate(**ARGS)
        cls.by_id = {o["id"]: o for o in cls.ev["objectives"]}

    def test_objectives_match_the_hand_calculation(self):
        self.assertEqual(list(self.by_id), list(HAND))  # all ten, in spec order
        for oid, (status, measured) in HAND.items():
            with self.subTest(oid):
                self.assertEqual((self.by_id[oid]["status"], self.by_id[oid]["measured"]), (status, measured))

    def test_coverage_and_lineage(self):
        cov = self.ev["coverage"]
        self.assertEqual(cov["total_pct"], 97.1)
        self.assertEqual(cov["providers"], [
            {"provider": "AWS Bedrock", "region": "ap-southeast-2", "billed_tokens": 1905000, "traced_tokens": 1850000, "billed_requests": 2520, "traced_requests": 2500},
            {"provider": "OpenAI", "region": "us-east-1", "billed_tokens": 81000, "traced_tokens": 80000, "billed_requests": 800, "traced_requests": 800},
        ])
        self.assertEqual([(l["model"], l["share_of_calls_pct"], l["role"]) for l in self.ev["lineage"]], [
            ("anthropic.claude-sonnet-4-6", 39.4, "summary drafting"),
            ("anthropic.claude-sonnet-4-5", 36.4, "summary drafting"),
            ("text-embedding-3-small", 24.2, "retrieval embeddings"),
        ])

    def test_period_registers_and_counts(self):
        p = self.ev["period"]
        self.assertEqual((p["utc_start"], p["utc_end"]), ("2026-08-31T14:00:00Z", "2026-09-30T14:00:00Z"))
        self.assertEqual([c["date"] for c in self.ev["changes"]], ["2026-09-10", "2026-09-15"])
        self.assertEqual([i["id"] for i in self.ev["incidents"]], ["INC-0008", "INC-0009"])
        self.assertEqual((self.ev["windows"], self.ev["leaf_count"]), (3, 900))

    def test_framework_maps_mark_unverified_refs(self):
        refs ={r["id"]: r for r in json.loads((ROOT / "spec/framework-refs.json").read_text())["refs"]}
        for o in self.ev["objectives"]:
            spec = next(x for x in json.loads((ROOT / "spec/objectives.json").read_text())["objectives"] if x["id"] == o["id"])
            for ref_id, label in zip(spec["maps_to"], o["maps"]):
                r = refs[ref_id]
                self.assertTrue(label.startswith(r["framework"]) and r["title"] in label, label)
                self.assertEqual(label.endswith("*"), not r["verified"], label)
        self.assertEqual(self.by_id["EO-LOG-01"]["maps"], ["EU AI Act Art. 12 Record-keeping (automatic logging)", "AIUC-1 E Accountability*",
                                                         "ISO/IEC 42001 A.6.2.8 AI system event logs*",
                                                         "APRA AI letter (30 Apr 2026): Continuous monitoring of AI*"])
        self.assertIn("APRA CPS 230: Operational risk: service providers", self.by_id["EO-INC-01"]["maps"])

    @unittest.skipIf(shutil.which("node") is None, "node not installed")
    def test_objective_set_hash_matches_the_browser_jcs(self):
        script = ("import('" + (ROOT / "web/verify/verify.js").as_uri() + "').then(async (m) => {"
                  "const fs = await import('node:fs'); const t = m.jcs(JSON.parse(fs.readFileSync(process.argv[1], 'utf8')));"
                  "console.log('sha256:' + m.hex(await m.sha256(new TextEncoder().encode(t)))); })")
        out = subprocess.run(["node", "-e", script, str(ROOT / "spec/objectives.json")], capture_output=True, text=True, check=True)
        self.assertEqual(self.ev["objective_set_hash"], out.stdout.strip())

    def test_status_rules(self):
        r = {"threshold": 0.98, "attention_band": 0.01}
        self.assertEqual([evaluate.status_ratio(n, 100, r) for n in (98, 97, 96.9)], ["pass", "attention", "fail"])
        self.assertEqual(evaluate.status_ratio(0, 0, r), "attention")  # denominator 0
        m = {"threshold": 0.01, "attention_band": 0.002}
        self.assertEqual([evaluate.status_max(v, m) for v in (0.01, 0.0115, 0.013)], ["pass", "attention", "fail"])
        self.assertEqual(evaluate.pct_floor(1999, 2000), 99.9)  # never rounds up to 100.0

    def test_eval_freshness_fails_after_max_age(self):
        # Same month, but prompt v15 went live on day 2: no passing eval for 28 days is a fail.
        with tempfile.TemporaryDirectory() as d:
            agg = json.loads((M / "aggregates.json").read_text())
            agg["windows"][2]["window"] = {"start": "2026-09-02T05:00:00Z", "end": "2026-09-02T06:00:00Z"}
            p = Path(d) / "agg.json"
            p.write_text(json.dumps(agg))
            ev = evaluate.evaluate(**{**ARGS, "aggregate_paths": [str(p)]})
            o = next(x for x in ev["objectives"] if x["id"] == "EO-EVAL-01")
            self.assertEqual(o["status"], "fail")
            self.assertIn("prompt v15 (live 28 days)", o["measured"])

    def test_bad_inputs(self):
        with tempfile.TemporaryDirectory() as d:
            bad = Path(d) / "usage.csv"
            bad.write_text("provider,model\nx,y\n")
            with self.assertRaises(ValueError):
                evaluate.evaluate(**{**ARGS, "usage_paths": [str(bad)]})
            sysf = Path(d) / "system.json"
            sysf.write_text(json.dumps({**json.loads((M / "system.json").read_text()), "colour": "blue"}))
            with self.assertRaises(ValueError):
                evaluate.evaluate(**{**ARGS, "system_path": str(sysf)})

    def test_cli(self):
        with tempfile.TemporaryDirectory() as d:
            out = Path(d) / "evaluation.json"
            r = subprocess.run([sys.executable, str(ROOT / "concierge/evidencepack.py"), "evaluate", "--system", str(M / "system.json"),
                                "--aggregates", str(M / "aggregates.json"), "--usage", str(M / "usage.csv"), "--changes", str(M / "changes.csv"),
                                "--incidents", str(M / "incidents.csv"), "--evals", str(M / "evals.csv"), "--out", str(out)],
                               capture_output=True, text=True, env=os.environ)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn("3 pass, 3 attention, 4 fail", r.stderr)
            self.assertEqual(json.loads(out.read_text())["objectives"], self.ev["objectives"])

    def test_inputs_are_fingerprinted(self):
        self.assertEqual(self.ev["inputs"]["usage"][0]["sha256"], hashlib.sha256((M / "usage.csv").read_bytes()).hexdigest())


if __name__ == "__main__":
    unittest.main()
