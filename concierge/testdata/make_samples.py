"""Writes the same synthetic traffic as an OTLP JSON-lines dump, a Langfuse JSON
export and a Langfuse CSV export, for the C-1 tests. Deterministic.

Every content field carries MARKER so a test can prove no content reaches the
output; trace IDs carry TRACE_PREFIX for the same reason.

  python3 concierge/testdata/make_samples.py
"""
import csv
import datetime as dt
import json
import random
from pathlib import Path

OUT = Path(__file__).resolve().parent
MARKER = "ZEBRA-7731 claim from Jane Citizen, policy HX-99812"
TRACE_PREFIX = "c0ffee"
START = dt.datetime(2026, 9, 1, 0, 0, tzinfo=dt.timezone.utc)


def build():
    rng = random.Random(7)
    traces = []
    for i in range(240):  # two hours, 120 traces each
        t0 = START + dt.timedelta(seconds=30 * i + rng.randint(0, 20))
        tid = f"{TRACE_PREFIX}{i:026x}"
        prompt_v = "13" if i < 150 else "14"  # prompt v13 -> v14 in the second hour
        model = "anthropic.claude-sonnet-4-5" if i < 200 else "anthropic.claude-sonnet-4-6"  # model change late on
        pii = i % 4 == 0
        blocked = i % 25 == 0
        obs = [
            {"kind": "invoke_agent", "t": t0, "error": False, "attrs": {"gen_ai.agent.name": "triage"}},
            {"kind": "chat", "t": t0 + dt.timedelta(seconds=1), "error": i % 60 == 0, "attrs": {
                "gen_ai.provider.name": "aws.bedrock", "gen_ai.request.model": model, "gen_ai.response.model": model,
                "cloud.region": "ap-southeast-2", "gen_ai.usage.input_tokens": 1200 + i, "gen_ai.usage.output_tokens": 300 + i % 7,
                "gen_ai.agent.name": "triage", "plumbline.prompt.name": "triage-reply", "plumbline.prompt.version": prompt_v,
                "plumbline.guardrail.decision": "block" if blocked else "allow",
                **({"plumbline.data_class": "personal", "plumbline.redaction.applied": i % 40 != 0} if pii else {}),
            }, "content": True},
            {"kind": "execute_tool", "t": t0 + dt.timedelta(seconds=2), "error": i % 30 == 0, "attrs": {
                "plumbline.tool.high_impact": i % 10 == 0, "plumbline.tool.approved": i % 20 == 0}},
        ]
        if i % 3 == 0:  # a small OpenAI classifier call, never guarded
            obs.append({"kind": "chat", "t": t0 + dt.timedelta(seconds=3), "error": False, "attrs": {
                "gen_ai.system": "openai", "gen_ai.request.model": "gpt-4.1-mini", "aws.region": "us-east-1",
                "gen_ai.usage.prompt_tokens": 90, "gen_ai.usage.completion_tokens": 4}, "content": False})
        if i == 77:
            obs[0]["attrs"]["plumbline.escalation"] = True
        traces.append((tid, obs))
    return traces


def nanos(t):
    return str(int(t.timestamp() * 1e9))


def otlp_attr(k, v):
    if isinstance(v, bool):
        return {"key": k, "value": {"boolValue": v}}
    if isinstance(v, int):
        return {"key": k, "value": {"intValue": str(v)}}
    return {"key": k, "value": {"stringValue": str(v)}}


def write_otlp(traces):
    lines = []
    for n, (tid, obs) in enumerate(traces):
        spans = []
        for j, o in enumerate(obs):
            attrs = [otlp_attr("gen_ai.operation.name", o["kind"])] + [otlp_attr(k, v) for k, v in o["attrs"].items()]
            events = []
            if o.get("content"):
                if n % 2:  # new style: content attributes on the span
                    attrs += [otlp_attr("gen_ai.input.messages", json.dumps([{"role": "user", "content": MARKER}])),
                              otlp_attr("gen_ai.output.messages", json.dumps([{"role": "assistant", "content": "Dear " + MARKER}]))]
                else:  # old style: content in span events
                    events = [{"name": "gen_ai.content.prompt", "attributes": [otlp_attr("gen_ai.prompt", MARKER)]}]
            spans.append({"traceId": tid, "spanId": f"{n:08x}{j:08x}", "name": f"{o['kind']} {MARKER[:6]}",
                          "startTimeUnixNano": nanos(o["t"]), "endTimeUnixNano": nanos(o["t"] + dt.timedelta(milliseconds=800)),
                          "attributes": attrs, "events": events, "status": {"code": 2 if o["error"] else 1}})
        lines.append(json.dumps({"resourceSpans": [{"resource": {"attributes": [otlp_attr("service.name", "claims-triage")]},
                                                    "scopeSpans": [{"scope": {"name": "sample"}, "spans": spans}]}]}))
    (OUT / "otlp-sample.jsonl").write_text("\n".join(lines) + "\n")


LF_TYPE = {"invoke_agent": "AGENT", "chat": "GENERATION", "execute_tool": "TOOL"}


def langfuse_obs(traces):
    rows = []
    for n, (tid, obs) in enumerate(traces):
        for j, o in enumerate(obs):
            a = dict(o["attrs"])
            ob = {"id": f"obs-{n}-{j}", "traceId": tid, "type": LF_TYPE[o["kind"]], "name": f"{o['kind']} {MARKER[:6]}",
                  "startTime": o["t"].isoformat().replace("+00:00", "Z"), "level": "ERROR" if o["error"] else "DEFAULT", "metadata": {}}
            if o["kind"] == "chat":
                ob["model"] = a.pop("gen_ai.response.model", a.pop("gen_ai.request.model", None))
                a.pop("gen_ai.request.model", None)
                ob["usageDetails"] = {"input": a.pop("gen_ai.usage.input_tokens", a.pop("gen_ai.usage.prompt_tokens", None)),
                                      "output": a.pop("gen_ai.usage.output_tokens", a.pop("gen_ai.usage.completion_tokens", None))}
                ob["metadata"]["provider"] = a.pop("gen_ai.provider.name", a.pop("gen_ai.system", None))
                ob["metadata"]["region"] = a.pop("cloud.region", a.pop("aws.region", None))
                if "plumbline.prompt.name" in a:
                    ob["promptName"] = a.pop("plumbline.prompt.name")
                    ob["promptVersion"] = int(a.pop("plumbline.prompt.version"))
                if o.get("content"):
                    ob["input"] = [{"role": "user", "content": MARKER}]
                    ob["output"] = {"role": "assistant", "content": "Dear " + MARKER}
            a.setdefault("service.name", "claims-triage")
            ob["metadata"].update(a)
            rows.append(ob)
    return rows


def write_langfuse(traces):
    rows = langfuse_obs(traces)
    (OUT / "langfuse-sample.json").write_text(json.dumps(rows, indent=1) + "\n")
    cols = ["id", "traceId", "type", "name", "startTime", "level", "model", "inputTokens", "outputTokens", "promptName",
            "promptVersion", "input", "output"]
    meta_keys = sorted({k for r in rows for k in r["metadata"]})
    with (OUT / "langfuse-sample.csv").open("w", newline="", encoding="utf-8") as f:
        w = csv.writer(f)
        w.writerow(cols + [f"metadata.{k}" for k in meta_keys])
        for r in rows:
            u = r.get("usageDetails") or {}
            vals = {**r, "inputTokens": u.get("input"), "outputTokens": u.get("output"),
                    "input": json.dumps(r["input"]) if "input" in r else "", "output": json.dumps(r["output"]) if "output" in r else ""}
            w.writerow([("" if vals.get(c) is None else vals.get(c)) for c in cols] +
                       [("" if r["metadata"].get(k) is None else str(r["metadata"][k]).lower() if isinstance(r["metadata"].get(k), bool) else r["metadata"][k])
                        for k in meta_keys])


if __name__ == "__main__":
    t = build()
    write_otlp(t)
    write_langfuse(t)
    print("wrote otlp-sample.jsonl, langfuse-sample.json, langfuse-sample.csv")
