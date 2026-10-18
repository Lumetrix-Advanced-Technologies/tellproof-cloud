"""Acceptance (P8-3, D-065): the leak corpus, in every request and response
position the instrumented clients and the helpers see, never reaches an export.

The corpus is spec/leak-corpus.json, shared with the agent's and the Node SDK's
tests; the canary probes (spec/canaries.json) must not be exported either.
"""
import json
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "src"))
sys.path.insert(0, HERE)

import tellproof  # noqa: E402
from tellproof import _core, _instrument, _otlp  # noqa: E402
from tellproof._export import Exporter  # noqa: E402
import fakes  # noqa: E402

SPEC = os.path.join(HERE, "..", "..", "..", "spec")
with open(os.path.join(SPEC, "leak-corpus.json"), encoding="utf-8") as f:
    CORPUS = json.load(f)
with open(os.path.join(SPEC, "canaries.json"), encoding="utf-8") as f:
    PROBES = json.load(f)["probes"]
TEXT = " | ".join(m["text"] for m in CORPUS["markers"])
NEEDLES = [m["needle"] for m in CORPUS["markers"]] + [p["text"][:40] for p in PROBES]


def messages(text):
    """Every place a chat request can carry text."""
    return [
        {"role": "system", "content": text},
        {"role": "user", "content": [{"type": "text", "text": text}, {"type": "image_url", "image_url": {"url": "https://example.com/?q=" + text}}]},
        {"role": "assistant", "content": text, "tool_calls": [{"id": "call_1", "type": "function",
                                                                  "function": {"name": "lookup", "arguments": text}}]},
        {"role": "tool", "tool_call_id": "call_1", "content": text},
    ]


TOOLS = [{"type": "function", "function": {"name": "lookup", "description": TEXT, "parameters": {"type": "object", "description": TEXT}}}]


class LeakCorpusTest(unittest.TestCase):
    def test_nothing_of_the_corpus_reaches_an_export(self):
        exports = []
        exp = Exporter(exports.append, lambda spans: _otlp.encode(spans, {"service.name": "t"}, "t"), interval=0.01)
        with fakes.installed(fakes.make_openai(TEXT), fakes.make_anthropic(TEXT), fakes.make_botocore(TEXT)) as m:
            _instrument.install(stream_usage=True)
            old = _core.current_client()
            _core.set_client(_core.Client(exp, service="t"))
            try:
                self._calls(m)
                self.assertTrue(exp.flush(5))
            finally:
                _core.set_client(old)
                exp.shutdown(1)
        blob = b"".join(exports).decode("utf-8")
        self.assertGreaterEqual(blob.count('"spanId"'), 20)
        low = blob.lower()
        for n in NEEDLES:
            i = low.find(n.lower())
            if i >= 0:
                self.fail(f"{n!r} reached an export: ...{blob[max(0, i - 80):i + 80]}...")

    def _calls(self, m):
        o, a = m["openai"].OpenAI(), m["anthropic"].Anthropic()
        b = m["botocore.client"].BaseClient("bedrock-runtime", "us-east-1")
        msgs = messages(TEXT)
        o.chat.completions.create(model="gpt-4.1-mini", messages=msgs, tools=TOOLS, user=TEXT, metadata={"note": TEXT})
        list(o.chat.completions.create(model="gpt-4.1-mini", messages=msgs, stream=True))
        o.chat.completions.parse(model="gpt-4.1-mini", messages=msgs, response_format={"type": "json_schema", "json_schema": {"name": "x", "description": TEXT}})
        o.responses.create(model="gpt-5", input=TEXT, instructions=TEXT, tools=TOOLS)
        list(o.responses.create(model="gpt-5", input=[{"role": "user", "content": TEXT}], stream=True))
        o.embeddings.create(model="text-embedding-3-small", input=[TEXT, TEXT])
        o.completions.create(model="gpt-3.5-turbo-instruct", prompt=TEXT, suffix=TEXT)
        anth = [{"role": "user", "content": [{"type": "text", "text": TEXT}, {"type": "tool_result", "tool_use_id": "t1", "content": TEXT}]},
                {"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "lookup", "input": {"q": TEXT}}]}]
        a.messages.create(model="claude-sonnet-4-5", max_tokens=1, system=TEXT, messages=anth, tools=[{"name": "lookup", "description": TEXT, "input_schema": {}}],
                          metadata={"user_id": TEXT})
        list(a.messages.create(model="claude-sonnet-4-5", max_tokens=1, system=[{"type": "text", "text": TEXT}], messages=anth, stream=True))
        with a.messages.stream(model="claude-sonnet-4-5", max_tokens=1, messages=anth) as s:
            list(s.text_stream)
        conv = [{"role": "user", "content": [{"text": TEXT}, {"toolResult": {"toolUseId": "t1", "content": [{"text": TEXT}]}}]}]
        b.converse(modelId="anthropic.claude-sonnet-4-5", messages=conv, system=[{"text": TEXT}],
                   toolConfig={"tools": [{"toolSpec": {"name": "lookup", "description": TEXT}}]})
        list(b.converse_stream(modelId="anthropic.claude-sonnet-4-5", messages=conv)["stream"])
        b.invoke_model(modelId="anthropic.claude-sonnet-4-5", body=json.dumps({"prompt": TEXT}).encode())["body"].read()
        list(b.invoke_model_with_response_stream(modelId="anthropic.claude-sonnet-4-5", body=TEXT.encode())["body"])
        for bad in (lambda: o.chat.completions.create(model="boom", messages=msgs),
                    lambda: a.messages.create(model="boom", max_tokens=1, messages=anth),
                    lambda: b.converse(modelId="boom", messages=conv)):
            with self.assertRaises(Exception):
                bad()

        # The helpers: an app's own call that fails with content in the error,
        # a tool and an agent run that raise it, and the canaries' probes.
        with self.assertRaises(ValueError):
            with tellproof.trace(), tellproof.prompt("triage", "14"), tellproof.personal_data(redacted=True):
                with tellproof.guardrail("allow"), tellproof.llm_call("openai", "gpt-4.1-mini") as call:
                    call.usage(input_tokens=3, output_tokens=1)
                    raise ValueError(TEXT)
        with self.assertRaises(RuntimeError):
            with tellproof.agent_run("refund-agent"), tellproof.tool("lookup_policy"):
                raise RuntimeError(TEXT)
        seen = []
        result = tellproof.run_canaries(lambda probe: seen.append(probe) or True)
        self.assertEqual(len(seen), len(PROBES))
        self.assertTrue(all(result.values()))


if __name__ == "__main__":
    unittest.main()
