"""C-1 tests. Run: python3 -m unittest discover -s concierge/tests"""
import base64
import json
import os
import subprocess
import sys
import tempfile
import unittest
from collections import Counter
from pathlib import Path

HERE = Path(__file__).resolve().parent
CONCIERGE = HERE.parent
ROOT = CONCIERGE.parent
sys.path.insert(0, str(CONCIERGE))
sys.path.insert(0, str(CONCIERGE / "testdata"))

import ingest  # noqa: E402
import make_samples  # noqa: E402
import merkle  # noqa: E402

DATA = CONCIERGE / "testdata"
SECRET = b"test-salt-secret"
OTLP = [str(DATA / "otlp-sample.jsonl")]
LF_JSON = [str(DATA / "langfuse-sample.json")]
LF_CSV = [str(DATA / "langfuse-sample.csv")]


def check_schema(value, schema, path="$"):
    """Validates the subset of JSON Schema that spec/batch.schema.json uses."""
    t = schema.get("type")
    types = t if isinstance(t, list) else [t] if t else []
    py = {"object": dict, "array": list, "string": str, "integer": int, "number": (int, float), "null": type(None)}
    if types and not any(isinstance(value, py[x]) and not (x in ("integer", "number") and isinstance(value, bool)) for x in types):
        raise AssertionError(f"{path}: {value!r} is not {types}")
    if "enum" in schema and value not in schema["enum"]:
        raise AssertionError(f"{path}: {value!r} not in {schema['enum']}")
    if "minimum" in schema and isinstance(value, (int, float)) and value < schema["minimum"]:
        raise AssertionError(f"{path}: below minimum")
    if "maximum" in schema and isinstance(value, (int, float)) and value > schema["maximum"]:
        raise AssertionError(f"{path}: above maximum")
    if isinstance(value, dict):
        for r in schema.get("required", []):
            if r not in value:
                raise AssertionError(f"{path}: missing {r}")
        props = schema.get("properties", {})
        if schema.get("additionalProperties") is False:
            extra = set(value) - set(props)
            if extra:
                raise AssertionError(f"{path}: unexpected {sorted(extra)}")
        for k, v in value.items():
            if k in props:
                check_schema(v, props[k], f"{path}.{k}")
    if isinstance(value, list) and "items" in schema:
        for i, v in enumerate(value):
            check_schema(v, schema["items"], f"{path}[{i}]")


class IngestTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.otlp, cls.otlp_leaves = ingest.ingest("otlp", OTLP, SECRET)
        cls.traces = make_samples.build()

    def totals(self, out):
        c = Counter()
        for w in out["windows"]:
            m = w["metrics"]
            c["windows"] += 1
            c["llm_calls"] += m["guardrails"]["llm_calls"]
            c["guarded"] += m["guardrails"]["guarded_calls"]
            c["blocked"] += m["guardrails"]["blocked"]
            c["content"] += m["content_present_spans"]
            c["pii"] += m["pii"]["pii_spans"]
            c["redacted"] += m["pii"]["pii_spans_redacted"]
            for k, v in m["agent"].items():
                c[k] += v
            for t in m["tokens"]:
                c[("in", t["provider"], t["model"])] += t["input_tokens"]
                c[("out", t["provider"], t["model"])] += t["output_tokens"]
                c[("calls", t["provider"], t["model"], t["region"])] += t["calls"]
            c["leaves"] += w["commit"]["leaves"]
        return c

    def test_matches_a_hand_count_of_the_sample(self):
        # Expected values computed directly from the generator's traces, not from ingest.
        obs = [o for _, os_ in self.traces for o in os_]
        chats = [o for o in obs if o["kind"] == "chat"]
        bedrock = [o for o in chats if o["attrs"].get("gen_ai.provider.name") == "aws.bedrock"]
        tools = [o for o in obs if o["kind"] == "execute_tool"]
        pii = [o for o in chats if o["attrs"].get("plumbline.data_class") == "personal"]
        got = self.totals(self.otlp)
        self.assertEqual(got["windows"], 2)
        self.assertEqual(got["llm_calls"], len(chats))
        self.assertEqual(got["guarded"], len(bedrock))
        self.assertEqual(got["blocked"], sum(o["attrs"]["plumbline.guardrail.decision"] == "block" for o in bedrock))
        self.assertEqual(got["content"], sum(bool(o.get("content")) for o in obs))
        self.assertEqual(got["pii"], len(pii))
        self.assertEqual(got["redacted"], sum(o["attrs"]["plumbline.redaction.applied"] for o in pii))
        self.assertEqual(got["runs"], sum(o["kind"] == "invoke_agent" for o in obs))
        self.assertEqual(got["tool_calls"], len(tools))
        self.assertEqual(got["tool_errors"], sum(o["error"] for o in tools))
        self.assertEqual(got["high_impact_calls"], sum(o["attrs"]["plumbline.tool.high_impact"] for o in tools))
        self.assertEqual(got["high_impact_approved"], sum(o["attrs"]["plumbline.tool.high_impact"] and o["attrs"]["plumbline.tool.approved"] for o in tools))
        self.assertEqual(got["escalations"], 1)
        self.assertEqual(got["leaves"], len(self.traces))
        for model in ("anthropic.claude-sonnet-4-5", "anthropic.claude-sonnet-4-6"):
            self.assertEqual(got[("in", "aws.bedrock", model)], sum(o["attrs"]["gen_ai.usage.input_tokens"] for o in bedrock if o["attrs"]["gen_ai.response.model"] == model))
        self.assertEqual(got[("calls", "openai", "gpt-4.1-mini", "us-east-1")], len(chats) - len(bedrock))  # old attribute names mapped

    def test_transitions_and_anomalies(self):
        ts = [t for w in self.otlp["windows"] for t in w["metrics"]["transitions"]]
        self.assertEqual([(t["kind"], t["from"], t["to"]) for t in ts],
                         [("prompt", "13", "14"), ("model", "anthropic.claude-sonnet-4-5", "anthropic.claude-sonnet-4-6")])
        # 5 of 160 calls blocked in each hour (3.1%) is over the 3% default; errors (2.5%) are under 5%.
        kinds = [a["kind"] for w in self.otlp["windows"] for a in w["metrics"]["anomalies"]]
        self.assertEqual(kinds, ["guardrail_rate", "guardrail_rate"])

    def test_langfuse_json_and_csv_match_otlp(self):
        for files in (LF_JSON, LF_CSV):
            out, leaves = ingest.ingest("langfuse", files, SECRET)
            self.assertEqual(out["windows"], self.otlp["windows"], files)
            self.assertEqual(leaves, self.otlp_leaves, files)

    def test_no_content_or_trace_ids_leave(self):
        # Acceptance: a grep for any prompt text in the output finds nothing.
        for kind, files in (("otlp", OTLP), ("langfuse", LF_JSON), ("langfuse", LF_CSV)):
            out, leaves = ingest.ingest(kind, files, SECRET)
            text = json.dumps(out, ensure_ascii=False) + "\n".join(leaves)
            for needle in ("ZEBRA", "Jane Citizen", "HX-99812", "Dear ", make_samples.TRACE_PREFIX):
                self.assertNotIn(needle, text, f"{kind} {files}: {needle!r} leaked")
            self.assertIn(make_samples.MARKER, Path(files[0]).read_text(encoding="utf-8"))  # the input really has it

    def test_metrics_match_the_batch_schema(self):
        schema = json.loads((ROOT / "spec" / "batch.schema.json").read_text())
        for w in self.otlp["windows"]:
            check_schema(w["metrics"], schema["properties"]["metrics"], "metrics")
            check_schema(w["commit"], schema["properties"]["commit"], "commit")
            check_schema(w["window"], schema["properties"]["window"], "window")

    def test_commitments(self):
        leaves = [bytes.fromhex(l) for l in self.otlp_leaves]
        offset = 0
        for w in self.otlp["windows"]:
            n = w["commit"]["leaves"]
            self.assertEqual(merkle.root(leaves[offset:offset + n]).hex(), w["commit"]["root"])
            offset += n
        # A disclosed trace proves to its window's root.
        tid = self.traces[5][0]
        leaf = merkle.leaf_hash(merkle.salt(SECRET, tid), tid)
        first = self.otlp["windows"][0]
        idx = self.otlp_leaves.index(leaf.hex())
        tree = merkle.levels(leaves[:first["commit"]["leaves"]])
        self.assertTrue(merkle.verify(leaf, merkle.proof(tree, idx), bytes.fromhex(first["commit"]["root"])))
        # A different secret gives different leaves.
        _, other = ingest.ingest("otlp", OTLP, b"another-secret")
        self.assertNotEqual(other, self.otlp_leaves)

    def test_attribute_overrides(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "a.json"
            p.write_text(json.dumps({"pii_marker": "nothing.matches", "anomaly": {"guardrail_block_rate": 0.5}}))
            out, _ = ingest.ingest("otlp", OTLP, SECRET, str(p))
            self.assertEqual(sum(w["metrics"]["pii"]["pii_spans"] for w in out["windows"]), 0)
            self.assertEqual([a for w in out["windows"] for a in w["metrics"]["anomalies"]], [])
            self.assertEqual(out["attributes"]["anomaly"]["min_calls"], 20)  # merged, not replaced
            p.write_text(json.dumps({"no_such_key": 1}))
            with self.assertRaises(ValueError):
                ingest.ingest("otlp", OTLP, SECRET, str(p))

    def test_salt_secret_required(self):
        with self.assertRaises(ValueError):
            ingest.ingest("otlp", OTLP, b"")


class MerkleParityTest(unittest.TestCase):
    def test_python_merkle_matches_pass_vector_1(self):
        # Rebuild KST-CTA-0001's commitment the way make_vectors.py did (sample secret).
        env = json.loads((ROOT / "spec/vectors/pass/KST-CTA-0001.dsse.json").read_text())
        c = json.loads(base64.b64decode(env["payload"]))["predicate"]["commitments"]
        secret = b"vendor-held-salt-secret-sample"
        ids = [f"tr_1_{i:07d}" for i in range(c["leaf_count"])]
        tree = merkle.levels([merkle.leaf_hash(merkle.salt(secret, t), t) for t in ids])
        self.assertEqual(tree[-1][0].hex(), c["trace_merkle_root"])
        self.assertEqual(merkle.proof(tree, c["disclosed"]["leaf_index"]), c["disclosed"]["proof"])


class CLITest(unittest.TestCase):
    def run_cli(self, *args, env=None):
        return subprocess.run([sys.executable, str(CONCIERGE / "evidencepack.py"), *args], capture_output=True, text=True,
                              env={**os.environ, **(env or {})})

    def test_ingest_command(self):
        with tempfile.TemporaryDirectory() as d:
            out, leaves = Path(d) / "agg.json", Path(d) / "leaves.txt"
            r = self.run_cli("ingest", "--source", "otlp", "--out", str(out), "--leaves", str(leaves), *OTLP,
                             env={"PLUMBLINE_SALT_SECRET": SECRET.decode()})
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn("2 hourly windows, 240 traces committed", r.stderr)
            self.assertEqual(json.loads(out.read_text())["windows"], ingest.ingest("otlp", OTLP, SECRET)[0]["windows"])
            self.assertEqual(len(leaves.read_text().split()), 240)
            self.assertNotIn("ZEBRA", out.read_text() + leaves.read_text() + r.stderr)

    def test_secret_file_must_be_private(self):
        with tempfile.TemporaryDirectory() as d:
            s = Path(d) / "salt"
            s.write_text("x")
            s.chmod(0o644)
            r = self.run_cli("ingest", "--source", "otlp", "--out", str(Path(d) / "a"), "--leaves", str(Path(d) / "l"),
                             "--salt-secret-file", str(s), *OTLP, env={"PLUMBLINE_SALT_SECRET": ""})
            self.assertNotEqual(r.returncode, 0)
            self.assertIn("chmod 600", r.stderr)

    def test_no_secret(self):
        with tempfile.TemporaryDirectory() as d:
            r = self.run_cli("ingest", "--source", "otlp", "--out", str(Path(d) / "a"), "--leaves", str(Path(d) / "l"), *OTLP,
                             env={"PLUMBLINE_SALT_SECRET": ""})
            self.assertNotEqual(r.returncode, 0)
            self.assertIn("PLUMBLINE_SALT_SECRET", r.stderr)


if __name__ == "__main__":
    unittest.main()


class ObservedTest(unittest.TestCase):
    def test_observed_versions_per_window(self):
        out, _ = ingest.ingest("otlp", OTLP, SECRET)
        first, second = out["windows"]
        v = lambda m, p: {"model": m, "prompt_version": p}  # noqa: E731
        self.assertEqual(first["observed"]["versions"], [v("anthropic.claude-sonnet-4-5", "13")])
        self.assertEqual(second["observed"]["versions"], [v("anthropic.claude-sonnet-4-5", "13"), v("anthropic.claude-sonnet-4-5", "14"), v("anthropic.claude-sonnet-4-6", "14")])
        self.assertIn({"provider": "openai", "model": "gpt-4.1-mini"}, first["observed"]["models"])

    def test_windows_match_batch_schema_v02(self):
        # P3-0: concierge windows validate against the v0.2 batch schema's window parts.
        import schema
        s = json.loads((ROOT / "spec/batch.schema.json").read_text())
        self.assertEqual(s["properties"]["schema"]["const"], "plumbline-batch/v0.2")
        out, _ = ingest.ingest("otlp", OTLP, SECRET)
        for w in out["windows"]:
            for part in ("window", "metrics", "observed", "commit"):
                self.assertEqual(schema.errors(w[part], s["properties"][part], part), [], part)

    def test_v01_batches_still_validate_against_the_v01_schema(self):
        import schema
        v01 = json.loads((ROOT / "spec/batch-v0.1.schema.json").read_text())
        v02 = json.loads((ROOT / "spec/batch.schema.json").read_text())
        out, _ = ingest.ingest("otlp", OTLP, SECRET)
        w = out["windows"][0]
        batch = {"schema": "plumbline-batch/v0.1", "agent_id": "agt_abcdef12", "seq": 1, "prev": None, "window": w["window"],
                 "semconv_map": "v1", "metrics": w["metrics"], "usage": [], "commit": w["commit"]}
        self.assertEqual(schema.errors(batch, v01), [])
        self.assertTrue(schema.errors(batch, v02))  # v0.2 needs its own const and observed
        batch.update(schema="plumbline-batch/v0.2", observed=w["observed"])
        self.assertEqual(schema.errors(batch, v02), [])
