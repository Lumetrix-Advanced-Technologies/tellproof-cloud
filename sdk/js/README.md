# witnessmark (Node.js and TypeScript)

One line to send evidence telemetry from an AI product to the local Witnessmark evidence agent.

```bash
npm install witnessmark
```

```js
import * as witnessmark from 'witnessmark'; // or: const witnessmark = require('witnessmark')
witnessmark.init();
```

The SDK records one span per model call with provider, model, region, token counts, operation, timing and error status, and sends it as OTLP JSON to the agent on `http://127.0.0.1:4318` (or `WITNESSMARK_AGENT_URL`). The agent aggregates per hour and signs the aggregates: it keeps them in a local evidence log (`witnessmark-agent local`) or sends them to Witnessmark (`witnessmark-agent connect`). **The SDK never reads prompts, completions, function arguments, return values or error messages.**

- No dependencies: Node 18+ built-ins only (`fetch`, `AsyncLocalStorage`). Plain JavaScript with TypeScript types; no build step. `import` and `require` share one instance.
- Never slows or breaks the app: spans go to a bounded buffer; when it is full they are dropped and counted (`witnessmark.stats()`). SDK errors are swallowed and counted. Its timers never keep the process alive.
- `WITNESSMARK_DISABLED=1` turns it off. `init()` is safe to call more than once.
- Buffered spans are flushed when the event loop drains. In serverless handlers and short scripts, `await witnessmark.flush()` before returning.

## Automatic instrumentation

`init()` instruments the client libraries the app has installed, both their CommonJS and their ES module copies:

| Library | Calls |
| --- | --- |
| `openai` | `chat.completions.create` (and `parse`, `stream`, `runTools`, which go through it), `responses.create` (and `stream`, `parse`), `embeddings.create`, `completions.create`. Azure OpenAI is reported as `azure.ai.openai`; other OpenAI-compatible endpoints by their host name. |
| `@anthropic-ai/sdk` | `messages.create` (and `messages.stream`), `beta.messages.create`. `AnthropicBedrock` and `AnthropicVertex` are reported as `aws.bedrock` and `gcp.vertex_ai` with their region. |
| `@aws-sdk/client-bedrock-runtime` | `ConverseCommand`, `ConverseStreamCommand`, `InvokeModelCommand`, `InvokeModelWithResponseStreamCommand` sent with `client.send()` (also `BedrockRuntime`'s methods) |

Per call it reads the request's model and stream flag, and from the response only the model, the usage block, the stop reason and Bedrock's token-count headers. Input tokens follow what the provider bills (Anthropic: uncached + cache writes + cache reads; Bedrock: `inputTokens`, as CloudWatch counts them). You get the library's own objects back: `withResponse()` still works and streams are the same objects.

- **ES modules**: the CommonJS copies are instrumented inside `init()`, the ES module copies a moment later. If a module calls a model straight after `init()`, `await witnessmark.ready()` first.
- **Bundled apps or strict package managers** where the SDK cannot find the libraries: `witnessmark.instrument({ openai: OpenAI, anthropic: Anthropic, bedrock: await import('@aws-sdk/client-bedrock-runtime') })`.
- **Streams** are recorded once, when they end, error, or are left early (`break`). A stream dropped half-read is recorded after it is garbage-collected. Streams split with `tee()` or `toReadableStream()` are counted without tokens.
- **OpenAI streamed chat completions** report usage only if asked. `witnessmark.init({ streamUsage: true })` asks for it (`stream_options.include_usage`) when the app has not set `stream_options`; the stream then ends with one extra chunk with no `choices`. The app's own request object is never changed.
- **Bedrock guardrails**: a Converse call with `guardrailConfig` is a guarded call, `block` when the stop reason is `guardrail_intervened`, else `allow`. `InvokeModelWithResponseStream` reports token counts only inside the streamed body, which the SDK never reads, so those calls are counted without tokens. Callback-style `send(command, callback)` is not instrumented.
- `witnessmark.init({ instrument: false })` turns automatic instrumentation off; the helpers still work.

## What the helpers record

Each helper runs a function inside a scope and returns whatever it returns (a promise for async functions). Context follows `await`, timers and callbacks, so concurrent requests never mix.

| Helper | Records |
| --- | --- |
| `prompt(name, version, fn)` | the prompt (name and version, never its text) used by calls inside |
| `personalData({ redacted }, fn)` | spans inside handle personal data; `redacted`: removed or masked before the model saw it |
| `guardrail(decision?, fn)` | the guardrail decision (`allow`, `block`, `flag`) for calls inside; `fn` gets `g`: `g.allow()`, `g.block()`, `g.flag()` |
| `tool(name, { highImpact, approved }, fn)` | a tool call; a throw or rejection marks it failed; `fn` gets `t`: set `t.approved = true` once approved |
| `agentRun(name, fn)` | one agent run; model changes are tracked per agent |
| `escalation()` | the request was handed to a person |
| `trace(fn)` | groups everything inside into one trace (for example one web request) |
| `llmCall({ provider, model, operation, region }, fn)` | a model call the SDK does not instrument; `fn` gets `c`: `c.usage(inputTokens, outputTokens)` |

```js
app.post('/claims/:id/triage', (req, res) =>
  witnessmark.trace(() =>
    witnessmark.prompt('claims-triage', '14', () =>
      witnessmark.personalData({ redacted: true }, () =>
        witnessmark.guardrail(async (g) => {
          if (!inputPolicyOk(req.body)) {
            g.block(); // recorded as one blocked call; the model was never called
            return res.status(422).end();
          }
          const r = await witnessmark.llmCall({ provider: 'openai', model: 'gpt-4.1-mini' }, async (c) => {
            const r = await callModel(req.body);
            c.usage(r.usage.prompt_tokens, r.usage.completion_tokens);
            return r;
          });
          needsReview(r) ? g.flag() : g.allow();
          res.json(r);
        }),
      ),
    ),
  ),
);
```

Names and versions are capped at 128 characters; anything that is not a string or an integer is ignored. Calls inside a `guardrail` scope are held until it ends, so a decision made after the call still applies.

## Already on OpenTelemetry?

Point an OTLP/HTTP **JSON** exporter at the agent (`http://127.0.0.1:4318/v1/traces`) and set the `witnessmark.*` attributes yourself (`spec/semconv-map.json`). Do not run both for the same calls, or they are counted twice.

## Development

```bash
node --test sdk/js/test/sdk.test.mjs sdk/js/test/instrument.test.mjs
node sdk/js/test/scenario.mjs   # rewrite sdk/testdata/js-export.jsonl
```
