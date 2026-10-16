# Engineering log

What was built, what went wrong, and how it was verified, one entry per task. Newest first. Decisions with lasting consequences go in `decisions.md`; the one-line status lives in `progress-tracker.md`. This log is for the next engineer (or reviewer) who wants to know why the code looks the way it does.

## 2026-10-16 · P4-3 · Jobs

**Built.** `internal/jobs` (`Enqueue`, `Worker.RunOnce`/`Run`, `EnsureScheduled`, `Prune`) and `plumbline-server work`, which schedules and runs `prune_jobs`; the other kinds get handlers in their own tasks. Choices in D-039.

**Problems.** My first draft passed Go duration strings (`15m0s`) to Postgres as intervals, which is not reliable interval syntax; seconds now go through `make_interval`. The prune handler would have scheduled a second successor if it was retried after scheduling one; it now skips itself when checking for a pending prune.

**Verified (Postgres 16).** Eight workers on 60 jobs, some slow, run every job exactly once (without `SKIP LOCKED` some run three times). A job that fails twice runs a third time and ends done with its error cleared; the default backoff puts the retry about 30 s out and it is not claimed early; a panicking handler is a failure and the job is dead after `MaxAttempts` with `panic: bug` recorded; a freshly locked job is left alone and one locked 16 minutes ago is reclaimed; future jobs and kinds without a handler are not claimed; an enqueue in a rolled-back transaction leaves nothing; prune deletes old finished and very old dead jobs, keeps recent ones and pending ones, and leaves exactly one prune scheduled a day out; `Run` stops with its context; `work` runs the first prune and stops cleanly.

## 2026-10-16 · P4-2 · Enrol and ingest endpoints

**Built.** `internal/server` (`Server.Handler`: `POST /v1/agents/enrol`, `POST /v1/batches`, `GET /healthz`) and `plumbline-server serve`. The server side of the formats the agent already speaks (D-030, D-031), with the choices in D-038.

**Problems.** None in the code; the tests passed on their first run, so I checked that they bite: removing the `prev` check makes the wrong-prev case return 201, and removing the agent row lock turns 11 of 20 concurrent resends into 500s (unique violations) instead of 200s.

**Verified (Postgres 16).** Acceptance: two chained batches return 201 with their digests; a resend of seq 1 returns 200 with the stored digest, and a different batch under seq 1 also returns 200 with the stored digest (so the agent sees the mismatch); a seq gap and a wrong `prev` return 409; a batch signed by another key under the agent's ID, and a tampered payload, return 401. Also: unknown and revoked agents 401; a non-canonical, invalid payload 400; garbage 400; over 1 MB 413; the chain continues after refusals; the stored envelope decodes back to its digest; rows carry the agent's tenant. Enrolment stores the agent's key under the token's system; used, expired, unknown and missing tokens 401; a non-key, an unknown field and a P-256 key 400, and refused requests never spend the token. Twenty concurrent sends of one batch store one row (one 201, nineteen 200). End to end, the real agent enrols, replays the sample export and delivers: both batches are stored with their metrics. `serve` answers `/healthz`, stops cleanly on cancel and never logs the database URL.

## 2026-10-15 · P4-1 · Migrate and store

**Built.** `migrations/embed.go`; `internal/store` (`LoadMigrations`, `Migrate`, `Open`, `WithTenant`, `Public`); `internal/store/storetest` (fresh database per test); `cmd/server` with `migrate` and `version`; `make test-db`; CI runs the database tests (`PLUMBLINE_TEST_DATABASE_URL`, `PLUMBLINE_REQUIRE_DB=1`). Added `github.com/jackc/pgx/v5` (allowed).

**Problems.** 0001 wraps itself in `BEGIN; … COMMIT;`, and its `COMMIT` would end the runner's transaction before the migration is recorded; the runner leaves out that outer pair at run time instead of editing an immutable file (D-037). A seed with several statements and parameters is not allowed in one extended-protocol `Exec`; split. govulncheck flagged two advisories in `golang.org/x/text` v0.29.0, which pgx pulled in; upgraded to v0.43.0 (and `x/sync` to v0.24.0). Two test runs silently skipped because an exported variable does not survive between my shell calls; I reran with it set and with `PLUMBLINE_REQUIRE_DB=1`, which turns a missing database into a failure, as CI does. `Pool.Config()` returns a copy, so my first attempt to force a single connection did nothing; the test now sets `pool_max_conns=1` in the URL, so the "tenant setting is transaction-local" check really reuses the connection.

**Verified (Postgres 16).** Acceptance: migrations apply to a fresh database (re-running applies nothing; an edited applied migration is refused); tenant A sees only its systems, packs and tenant row, tenant B does not see A's pack, and inserting a row for B inside A's scope fails row-level security; UPDATE, DELETE and TRUNCATE on evidence raise the append-only error for the owner, and the app role is refused UPDATE. Also: a failing migration leaves neither its table nor its record; without a tenant the app sees nothing, also on the same pooled connection right after a tenant transaction (making the setting session-wide makes that test fail); a malformed tenant ID is refused; `migrate` applies once then reports nothing to do, and never prints the database password.

## 2026-10-15 · S-4 · TypeScript auto-instrumentation

**Built.** `sdk/js/lib/instrument.cjs`: patches openai (`create` on chat completions, responses, embeddings, completions), @anthropic-ai/sdk (`create` on messages and beta messages) and @aws-sdk/client-bedrock-runtime (`send` on `BedrockRuntimeClient`, with a per-command middleware for InvokeModel's token headers); finds each library from the app's directory and patches both its CommonJS and ES module copies; `plumbline.ready()` and `plumbline.instrument()`. Tests: `test/fakes.mjs` writes fake packages into a temp app; `test/instrument.test.mjs`; `test/real-libs.test.mjs` with the CI job `sdk-js-libs`.

**Problems.** Unlike Python, the Node libraries return a lazily parsed `APIPromise` with extra methods, and wrapping it in an ordinary promise would break `withResponse()` and parse responses the app never asked for. Their own `_thenUnwrap` solves both. Their streams use `#private` fields, which a `Proxy` breaks, so only the instance's async iterator is wrapped. The first test run counted `parse()` calls twice: in Node, `parse()` calls `create()` (I confirmed this in openai-node's source), so only `create` is patched now. The real libraries are not installed locally, so `real-libs.test.mjs` only skipped here. Its first CI run (openai 7.34, @anthropic-ai/sdk 0.134, @aws-sdk/client-bedrock-runtime 3.1152) showed that my ES module finder was wrong for the AWS SDK: its `module` field points at a bundler-only build that Node ignores (Node serves `import` from the CommonJS build, so there is one copy, already patched). The finder now follows only the `exports` import condition, with a test. The other two failures were in my fake API: stream chunks without a `finish_reason`, which openai's stream helper requires, and an HTTP/1.1 server for a Bedrock client that now defaults to HTTP/2.

**Verified.** Both the CommonJS and the ES module copy of each fake library are instrumented and give the right span. `withResponse()` works and nothing is parsed until the app consumes the promise. Responses, embeddings and legacy completions report their usage. A streamed chat counts usage once, keeps its object and class, and the app's request body is untouched. Stream usage is opt-in and never overrides the app. The libraries' own `stream()` helpers are covered, and a stream left early is recorded once. A failed Responses stream is marked failed. Raw Anthropic streams, beta messages and `AnthropicBedrock` all work. Errors are recorded and rejected unchanged, with no error text in spans. Azure and compatible hosts get their provider names. For Bedrock: guardrail decisions, ConverseStream, InvokeModel header counts, non-model commands and callback-style sends ignored, the aggregated `BedrockRuntime` client, and a provider block beating an enclosing scope. A stream read after its scopes ended keeps their trace and attributes. Installing twice wraps once, explicit `instrument()` works, and a disabled SDK returns the library's own `APIPromise`. No content: markers in every message, response, stream event, body and error across both module copies of all three libraries; none appear in the 24 exported spans.

## 2026-10-15 · S-3 · TypeScript SDK core

**Built.** `sdk/js` (`@plumbline/sdk`, Node 18+, no dependencies): `lib/core.cjs` (frames over `AsyncLocalStorage`, `record`, the helpers as callback scopes, guardrail deferral and synthetic blocks, the deferred queue S-4's stream proxies need), `lib/export.cjs` (bounded buffer, unref'd timers, one request in flight, `fetch` transport), `lib/otlp.cjs`, `lib/index.cjs` with `lib/index.mjs` re-exporting it, `lib/index.d.ts`. `test/scenario.mjs` is the twin of the Python scenario and writes `sdk/testdata/js-export.jsonl`; `TestSDKScenarioExports` now checks every export, and the concierge cross-check runs for each one. `make sdk-test` runs the Node tests; CI job `sdk-js` on Node 18 and 24.

**Problems.** CI on Node 18 and 22 found a real bug that Node 24 hid locally: `flush()` waited only on unref'd timers, so a script or serverless handler that ends with `await plumbline.flush()` could exit before the spans were sent. A flush now drains at once and holds a ref'd timeout while the caller waits; the background timers stay unref'd. A regression test runs a child process with nothing else keeping it alive (it fails against the old exporter, on Node 24 too). `node --test <directory>` works on Node 18 but not on 22+, so the test file is named explicitly. My first mutation of the no-content test wrote error text into a frame the span never reads (the same mistake as in S-1); a mutation that put error text on tool spans was caught by both the fixture check and the randomised test.

**Verified.** Acceptance (as S-1): the JS export of the shared scenario aggregates in the Go agent to exactly `expected-windows.json`, the same windows as the Python export, and the concierge agrees; offering 20,000 spans to a stalled agent returns in under a second with the overflow dropped and counted; 300 randomised runs with marker text in arguments, return values, thrown errors and invalid decisions export more than 900 spans and none of the markers. Also: import and require share one instance; nested and deferred guardrails; async scopes end when their promise settles and keep the value or rejection; concurrent requests keep their own prompt and trace; disabled and `PLUMBLINE_DISABLED` run the app unchanged; misuse (a non-function) never throws; a failing agent is counted; flush batches and shutdown stops; `init()` posts OTLP JSON to a real HTTP endpoint. Tested on Node 24 locally; CI covers 18 and 24.

## 2026-10-15 · S-2 · Python auto-instrumentation

**Built.** `sdk/python/src/plumbline/_instrument.py`: patches for openai (chat completions, responses, embeddings, legacy completions; create and parse), anthropic (messages and beta messages; create and stream) and botocore's `_make_api_call` for Bedrock Runtime; sync and async; stream proxies that record the call once; `init(instrument=True, stream_usage=False)`; `stats()["instrumented"]`. The core gained a frame override (a call keeps the context it started in), a lock-free deferred queue for finalizers, and guardrails that stamp late spans after they end. `tests/fakes.py` reproduces the libraries' module paths, signatures, usage and stream event shapes; `tests/test_real_libs.py` runs the real libraries against a local fake API in the new CI job `sdk-python-libs`.

**Problems.** None of openai, anthropic or boto3 is installed locally and I did not install them, so the real-library tests ran only as skips here; CI runs them. I checked the shapes against the libraries' sources on GitHub and the AWS API reference instead. That check turned up three things the design had to handle: Anthropic's `messages.stream()` bypasses `create` (so it is patched separately and reads the stream's snapshot on exit); openai's `stream()` helpers pass omitted arguments as sentinel objects (so "has the app set `stream_options`" must ignore them); Bedrock's `inputTokens` excludes cache tokens, unlike Anthropic's direct API (each provider follows its own billing, D-034). Reading InvokeModel bodies for usage would mean reading completions, so the SDK uses Bedrock's token-count headers instead, and counts streamed InvokeModel calls without tokens. A test name claimed the app's guardrail decision wins when the provider's block does; renamed.

**Verified.** Every call type against the fakes gives the right span (provider, request and response model, region, operation, tokens, error) and returns the library's own result unchanged; streamed usage is counted once whether the stream is exhausted, closed, left early in a `with`, or abandoned (recorded on the next flush, never in the finalizer); `stream_usage` is opt-in and never overrides the app; async create and streams for all three; errors are recorded and re-raised unchanged; Azure, OpenAI-compatible hosts and AnthropicBedrock get their own provider names; Bedrock guardrail decisions from the stop reason; a stream consumed after its scopes ended keeps their trace, parent, prompt, agent and guardrail decision; double install wraps once; a disabled SDK passes calls straight through; an injected instrumentation bug is counted and the app still gets its result. No content: one run with a marker in every message, input, response, stream event, body and error across all three libraries exports 14 spans and no trace of the marker. `init()` instruments and `shutdown()` flushes.

## 2026-10-15 · S-1 · Python SDK core

**Built.** `sdk/python` (package `plumbline`, standard library only, Python 3.10+): `init`, `flush`, `shutdown`, `stats`, `is_enabled`; helpers `trace`, `prompt`, `personal_data`, `guardrail`, `tool`, `agent_run`, `escalation` and `llm_call`, each a context manager (sync and async) and a decorator over `contextvars`; a bounded background exporter posting OTLP JSON to the agent. `sdk/testdata/` holds the shared scenario: the Python export and the windows it must aggregate to. Make target `sdk-test`; CI job on Python 3.10 and 3.13.

**Problems.** The first fixture had the agent-run span in a different trace from its children (a root span recorded after its context was reset); root spans now keep the trace they started. The scenario exposed a real aggregation bug: a service calling a chat model and an embeddings model side by side logged a model transition on almost every call, which would fail EO-CHG-01 for most real apps. Model history is now per (slot, operation) in both the agent and the concierge (D-033), with a regression test. My first mutation for the no-content test (exception text written into a discarded frame) never reached a span, so it proved nothing; a mutation that put exception text on tool spans was caught. The repo has no licence yet, so `pyproject.toml` names none (a P6-2 decision).

**Verified.** Acceptance: the scenario's export parses in `internal/otlp` with no rejections and aggregates to `expected-windows.json`, which I checked number by number against the scenario (4 LLM calls, 3 guarded, 2 blocked including an input block with no call, 2 PII spans with 1 redacted, 3 tool calls with 1 error, 2 high-impact with 1 approved, 1 escalation, 4 traces; next hour 1 flagged call plus a model and a prompt transition). The concierge ingest gives the same windows. Overload: 10,000 spans offered to a stalled agent return in well under a second, with everything beyond the buffer dropped and counted. No content: 300 randomised runs put marker text in function arguments, return values, exception messages and invalid decisions; none appears in the more than 900 exported spans. Also: deferred guardrail decisions, nested guardrails, decorators (sync and async), concurrent asyncio tasks, a forked child starting clean, a failing agent counted not raised, flush and shutdown, `PLUMBLINE_DISABLED`, proxy variables ignored, and init sending to a real HTTP endpoint. The committed export must match a fresh run. I tested only Python 3.14 locally; CI covers 3.10 and 3.13.

## 2026-10-15 · P3-8 · Agent command, config and dry-run

**Built.** `internal/agent` (`LoadConfig`, `New`, `Receiver`, `Replay`, `Tick`, `Shutdown`, `Deliver`, `Run`) wiring the receiver, Langfuse puller, aggregator, usage pollers, commitments, batch signing and the queue; `cmd/agent` (`enrol`, `run`, `version`, `--dry-run`, `--replay`); Bedrock usage only in `-tags bedrock` builds (a clear error otherwise). Anthropic usage gained `workspace_ids`. `agent.example.json` lost its register files (now a web app upload) and gained the aggregation settings. `make agent-dry` replays the sample export.

**Problems.** The first end-to-end run failed because the test's provider-redirecting transport also redirected batch delivery to the fake OpenAI server; the transport now rewrites only the provider host. Emitting the open window at shutdown means an hour can arrive in two batches after a restart; recorded in D-032 as a requirement on the server (windows are additive). `config` from the AWS SDK brings in its own indirect modules (credentials, sso, sts); it is on the allowed list and only compiled with the bedrock tag.

**Verified.** Config: the example loads, env overrides apply, unknown fields, trailing data, plain http to a remote server, a non-loopback listener and an out-of-range grace are refused; an enabled source without its key names the variable. Dry-run replay of the sample prints two envelopes whose decoded payloads match the signed ones, contains no prompt text, PII sample or trace ID, makes no request and creates no data directory. End to end: enrol against a fake server, post the sample to the receiver, tick past the grace, deliver: the server verifies and stores seq 1 and 2 with the OpenAI usage for each hour, and the leaves files match each commit. CLI: dry-run replay exits 0 with two batches; six usage errors exit 2; `version` works.

## 2026-10-15 · P3-7 · Disk queue

**Built.** `internal/queue`: `Open` (recovers the chain state), `Enqueue` (durable write of batch, then state), `Pending`, `Envelope`, `Remove`, and `Sender.Drain` (seq order, one host, delete on 201 or matching 200, backoff on transient errors, stop on refusals).

**Problems.** The first run failed one test for a real reason: `MkdirAll` does not tighten an existing directory's mode, so a pre-existing queue directory stayed group-readable; `Open` now sets 0700 explicitly.

**Verified.** The acceptance test starts the test binary as a separate agent process against a server that stores batches by seq the way P4-2 must, kills it with SIGKILL three times at different points mid-send, then lets it finish: all 12 batches are stored exactly once and the queue ends empty (stable across repeated runs, with the race detector). Also: state recovered from the newest batch after a crash between the two writes; a response lost after the server stored a batch leads to a resend answered 200 and stored once; 409, 401 and 400 stop delivery and keep the batch; a different digest under the same seq is reported; backoff doubles to the cap with jitter; out-of-order enqueues are refused. Injected bugs (deleting refused batches, not recovering state) are caught.

## 2026-10-15 · P3-6 · Enrolment and batch signing

**Built.** `internal/enrol` (Ed25519 key generation, the enrol call, owner-only key and identity files written atomically, `Load` that refuses loose permissions) and `internal/batchsign` (`Builder.Build`: canonical v0.2 payload, DSSE signature keyed by agent ID, next seq/prev state; `Verify`: the server-side check; `Digest`).

**Problems.** The build plan gives no acceptance test for this task; I used the properties ingest will rely on (below). Crash safety of seq/prev is a queue concern, so `Build` returns the state rather than saving it (D-030). A leftover no-op line in a test was removed.

**Verified.** Enrolment sends the token as a bearer header and a PEM public key the server can parse; the key file is 0600 in a 0700 directory and loads back to the same key; re-enrolment is refused; failed enrolments (401, 500, a malformed agent ID) leave no key and never echo the token; plain http to a remote host is refused. Two batches from the sample chain correctly (seq 1 then 2, prev null then the first payload's digest), verify with the enrolled public key, are canonical, and validate against `spec/batch.schema.json` with the concierge's validator. `Verify` rejects a tampered payload, another agent's key, a keyid mismatch, unknown fields, the old schema version, non-canonical payloads and an agent_id that differs from the signer; `Build` refuses counts beyond 2^53.

## 2026-10-15 · P3-5 · Langfuse puller

**Built.** `internal/langfuse`: `Puller.Pull` over Observations API v2 and Scores API v3 with cursor paging, Basic auth and backoff; observations mapped through `otlp.Mapping.Build` (a small refactor of `internal/otlp` so both sources share one mapping); eval runs from configured scores. `aggregate.AddEvals` files eval runs into windows. `testdata/make_pages.py` writes recorded pages for the same synthetic traffic as the OTLP sample.

**Problems.** The Langfuse paths, field groups and the Scores v3 `subject` object were checked against the docs today; two things still need a partner to confirm (the stored metadata shape of OpenTelemetry-ingested spans, and whether `inputUsage` includes cached tokens). Counting content needs `input`/`output`; they are fetched but only tested for emptiness (D-029). The first draft kept a package-level map, against the standards; it is now a function. `max` does not order `time.Time`; replaced with an explicit comparison.

**Verified.** Acceptance: the recorded pages (800 observations newest first over three cursor pages, metadata in the nested OpenTelemetry form, content in `input`/`output` and in metadata) aggregate to exactly the same windows, observed versions and trace lists as the OTLP sample through the OTLP path. Eval runs: 21 eval scores across two pages give one run at 16/21; a categorical annotation is ignored. 429 then 502 are retried with the right waits; persistent 500s give up after five attempts without leaking the key; 401 fails at once; a cursor that never ends stops at the page limit. Injected bugs (ERROR level ignored, nested metadata ignored, paging stopped early) are caught.

## 2026-10-15 · P3-4 · Provider usage

**Built.** `internal/usage`: `OpenAI` and `Anthropic` pollers on `net/http`, a `Bedrock` CloudWatch poller behind `-tags bedrock`, merging into hourly `batch.Usage`, and `Reconcile` (coverage per provider and in total). `make check` now also vets, tests and vulnerability-scans the `bedrock` build.

**Problems.** The library notes marked both APIs "(verify)". I checked them against the live documentation today: the Anthropic API reference gave the full response schema; OpenAI's API reference page refused the fetch, so its fields were checked against the official SDK's published TypeScript types. Anthropic splits input into uncached, cache-creation and cache-read tokens, so billed input is their sum, and the SDKs must count cached input the same way (D-028). Neither API carries a cloud region, so coverage is matched per provider. My fake CloudWatch first returned its paged results in random map order, so pages overlapped; the fake now has a fixed order.

**Verified.** Recorded responses in the documented shapes (two OpenAI completions pages and an embeddings page; two Anthropic pages including an empty bucket; two CloudWatch pages): every hour and model maps as expected, including the request parameters and headers sent. Coverage against those responses matches a hand calculation written in the test within 0.1 point (OpenAI 99.0%, Anthropic 98.64%, total 98.85%), with a provider lacking usage listed but not counted. Errors never contain the key; bad windows and missing keys are refused; a server that pages forever stops at the limit.

## 2026-10-15 · P3-3 · Commitments

**Built.** `internal/commitment`: `LoadSecret` (env or owner-only file, at least 16 bytes, never echoed), `ForWindow`/`FromLeaves` (the batch `commit` plus the leaves kept for disclosure), `Window.Disclose`, and `ForPeriod`/`Period.Disclose` for a period root over window roots.

**Problems.** The server never receives leaves, so a pack cannot use the concierge's flat tree over every leaf. A tree over window roots keeps disclosure working with the existing verifiers: a proof to the window root followed by the window root's proof to the period root is an ordinary node-hash chain (D-027).

**Verified.** Every window of the sample: three traces (first, middle, last) disclose and verify against the window root; unknown traces and the wrong secret are refused. The window roots and leaf counts equal the Python concierge's for the same secret (the test runs it). A period proof through the window root verifies against the period root, and disclosing under the wrong window fails. Secret loading refuses missing, world-readable, short or doubly specified secrets without echoing them.

## 2026-10-15 · P3-2 · Hourly aggregator

**Built.** `internal/batch` (the v0.2 payload types, shared by signing and server ingest later) and `internal/aggregate`: UTC hours that close a 5-minute grace after the hour, late spans moved into the current window and counted, traces committed once in the window of their first span (in first-seen order), transitions tracked per slot and prompt name across windows, anomalies by the D-020 rule, empty lists encoded as `[]`, safe for concurrent `Add`.

**Problems.** None in the code. Two judgment calls: a late span whose "current" hour is itself already closed (a clock running behind) goes into the first open hour; trace IDs are remembered for two hours after their window closes, so a long trace is not committed twice without the agent's memory growing without bound.

**Verified.** The acceptance test runs the Python concierge ingest on the same sample in the test itself and compares window, metrics and observed field for field, plus trace counts per window: identical. Further tests: windows close exactly at the grace, never twice; late spans; first-window commitment; transitions across windows and slots; anomaly thresholds at and below the boundaries; empty arrays; 8 goroutines adding concurrently under `-race`. Injected bugs (grace off by one, late spans uncounted, transitions not per slot) are each caught.

## 2026-10-15 · P3-1 · OTLP JSON receiver and normaliser

**Built.** `internal/otlp`: `Mapping` (a validated semconv map), `Parse` (one OTLP JSON export to content-free `Span` facts) and `Receiver` (`POST /v1/traces`). Package `spec` embeds the spec files. `semconv-map.json` v2 adopts the D-020 names, adds an operation alias, the LLM operation list and content prefixes; `internal/spec` validates the new parts; the concierge reads the same map (D-026).

**Problems.** OpenLLMetry-style instrumentation writes content under indexed names (`gen_ai.prompt.0.content`) and marks calls with `llm.request.type` rather than `gen_ai.operation.name`, so exact-name content detection and operation matching both needed extending. My first fuzz seeds included strings equal to the span's legitimate mapped values ("openai"), a false positive in the test. staticcheck caught an unneeded `fmt.Sprint` in a test. A mutation that makes every attribute readable is not caught by tests, by design: values reach a span only through mapped-name lookups, and content is skipped before decoding, so the two guards back each other up.

**Verified.** The concierge's collector-style sample parses to the same counts and token totals the Python ingest produces (240 traces, 320 LLM calls, 54 of 60 PII spans redacted, and so on). A hand-written OpenLLMetry/JavaScript-style export (numbers as JSON numbers, upper-case hex IDs, old attribute names, indexed and array content, content in events) parses exactly to four expected spans with two malformed spans rejected, and no field holds any of its marker text. `FuzzNoContentInSpans`: 198k inputs, no content in any span. Receiver: JSON and gzip accepted, charset parameter allowed, protobuf 415, GET 405, oversized body and gzip bomb 413, bad gzip 400, unknown encoding 415, partial success reported, no error echoes content. Injected bugs (content prefixes ignored, enum status codes ignored) are caught.

## 2026-10-15 · P3-0 · Batch schema v0.2

**Built.** `spec/batch.schema.json` is now v0.2: a required `observed` object beside `metrics` with the models (`{provider, model}`) and model + prompt version pairs (`{model, prompt_version}`) live in the window. The v0.1 file is kept unchanged as `spec/batch-v0.1.schema.json`. The concierge emits and reads the object form.

**Problems.** The concierge had emitted `observed` as bare pairs (`[provider, model]`); JSON Schema describes objects more clearly than positional arrays, so the format moved to objects and the fixture month was converted.

**Verified.** Go: v0.2 differs from v0.1 only in the version const, `$id`, title and `observed` (every other property byte-identical). Python: every ingest window's `window`, `metrics`, `observed` and `commit` validate against v0.2; a v0.1 batch validates against the v0.1 schema and fails v0.2 until it carries the v0.2 const and `observed`. All 44 concierge tests pass, including the hand-calculated month.

## 2026-10-15 · C-4 · Concierge share

**Built.** `concierge/share.py` and `evidencepack.py share` / `unshare`: a self-contained static site (verify page, brand tokens, issuer JWKS, one file per share at `/v1/verify/<token>` in the future API's response shape), random 16-byte tokens, a share log with token hashes only, verification of every pack before it is published, and revocation by deleting the share file.

**Problems.** The verify page had no static mode; rather than add one, the concierge writes files at the API's paths so the existing share-link mode works and links survive the move to P5-1. A screenshot of a real concierge pack (the fixture month: mixed results) showed three presentation problems, fixed here: framework labels read "APRA AI letter (30 Apr 2026) theme" and "APRA CPS 230 CPS 230" (labels now use our short titles, D-023); the "Need attention" tile counted failures as well (now "Not passing: n attention · n fail"); status pills wrapped onto two lines.

**Verified.** 6 share tests (41 concierge tests in all): site layout matches the API; tokens are 16 random bytes, differ per share, and never appear in the log or on stderr, and the log is not inside the published site; tampered packs, a missing or wrong `--prev`, and a JWKS kid clash are refused. End to end with headless Chrome against a local server: both links render "Verified, unaltered since signing" (pack 2 with "Links to the previous pack"), and after `unshare` the link shows "No pack at this link". Looked at a full-page screenshot of the fixture month's pack.

## 2026-10-15 · C-3 · Concierge sign and chain

**Built.** `concierge/sign.py` and `evidencepack.py sign`: predicate from `evaluation.json`, period commitment from the leaves file with an optional disclosed trace, statement, JCS payload, PAE, ECDSA P-256 signature by a local key or KMS, chain fields from `--prev`, and `--jwks-out`. `concierge/schema.py`: the JSON Schema keywords the pack and batch schemas use, so every predicate is checked before signing. `evaluate` now carries `sources` and `assurance_level` from the system file.

**Problems.** boto3 is not installed on this machine and installing it means downloading packages, so the KMS path is exercised with a fake client that checks the call shape (DIGEST, 32-byte message, ECDSA_SHA_256); real KMS use needs boto3 and credentials. A leaves file could silently disagree with the evaluated windows, so its count must equal the evaluation's `leaf_count`.

**Verified.** 8 sign tests (35 concierge tests in all): a two-pack chain signed with a fresh key verifies in `concierge/verify.py`, in `go run ./cmd/verify --json` (signature, subject digest, chain and Merkle all true) and in the verify page's `verify.js` under Node, which is the acceptance test. The KMS signer sends the PAE digest with the right parameters; a signer whose public key differs is refused; bad pack IDs, timestamps, leaf counts, unknown traces and wrong salt secrets are refused; the CLI chains two packs and refuses a world-readable key file.

## 2026-10-15 · C-2 · Concierge evaluate

**Built.** `concierge/evaluate.py` and `evidencepack.py evaluate`: a generic engine over the five rule kinds in `spec/objectives.json`, coverage per provider and region, lineage with roles, registers filtered to the period, limitations, input fingerprints and `objective_set_hash`. `concierge/canon.py`: full RFC 8785 in Python (the existing `verify.py` subset only handles simple decimals), needed for the hash now and for signing in C-3. C-1 output gains a per-window `observed` record of live model and prompt versions.

**Problems.** The rules leave many semantics open (eval freshness above all, also transition and incident matching, bands, coverage scope); fixed in D-021 using the sample vectors' wording as the guide (pack 3 shows a new prompt version without an eval as `attention`). The batch schema cannot carry live versions, which the agent will need for EO-EVAL-01 (schema gap recorded in D-021). Python parses large integer literals exactly where JSON means doubles; `canon.loads` now converts beyond 2^53. My own tally of statuses in one test was wrong (3/3/4, not 4/3/3); the code was right.

**Verified.** 10 evaluate tests, 27 concierge tests in all. A fixture month for a fictional partner (`testdata/partner-month/`) with a hand calculation written out in the test: all ten objectives' status and measured text match, with 3 pass, 3 attention and 4 fail, including the Sydney-midnight period boundary that excludes one usage row. Coverage table and lineage shares match. The eval-freshness rule turns to `fail` once an unevaluated version has been live past `max_age`. `objective_set_hash` equals the browser verifier's JCS hash computed in Node. Python JCS matches all 3,119 Node number cases, the JCS vectors and the pass payloads byte for byte.

## 2026-10-15 · C-1 · Concierge ingest

**Built.** `concierge/evidencepack.py ingest` with `ingest.py` (OTLP JSON or JSONL; Langfuse JSON, JSONL or CSV) and `merkle.py` (§2.4 in Python). Hourly UTC windows in the batch `metrics` shape, a salted Merkle `commit` per window, and a leaves file for the period commitment in C-3. `testdata/make_samples.py` writes the same synthetic traffic in all three formats, with a content marker in every content field. `make concierge-test` added to `make check`.

**Problems.** The repo defines no attribute names for PII, guardrails, prompt versions, tool approval or escalation, and no anomaly rule; chose documented `plumbline.*` defaults with per-partner overrides (D-020). Naive model-transition detection would flip on every call when two models run side by side; transitions are tracked per agent slot. The Langfuse export format is unconfirmed; fields read are listed and marked (verify).

**Verified.** 12 unittest cases: totals match a count made directly from the generator's traces (not from ingest); Langfuse JSON, Langfuse CSV and OTLP give identical windows and leaves; no marker text, customer name, policy number or trace ID appears in any output (acceptance grep, also run by hand on the CLI output: 0 hits against 800 in the input); every window validates against `spec/batch.schema.json`; window roots and a disclosed trace's proof verify; the Python Merkle code rebuilds pass vector 1's root and exact proof; attribute overrides merge; the salt secret is required and a secret file must be mode 0600.

## 2026-10-15 · P1-6 · `web/verify/`

**Built.** `verify.js` (pure, Web Crypto: `jcs`, `pae`, `derToRaw`, `verifyEnvelope`, `verifyMerkle`, plus strict envelope, statement and JWKS decoding), `page.js` (share-link and offline loading, DOM rendering), `index.html`, `verify.css` with a print stylesheet. Shared extra vectors (`spec/vectors/extra/`, 14 cases) that Go and JS both run (D-018).

**Problems.** The reference page loads Google Fonts, builds most markup with `innerHTML` (some payload strings unescaped), renders contents before verifying, ignores `keyid`, accepts loose DER, and crashes on `disclosed: null`; rebuilt rather than ported line by line. Writing the extra vectors exposed two Go gaps, now fixed in `internal/pack`: a non-canonical payload was accepted, and `null` passed for required arrays (encoding/json decodes it to a nil slice that re-encodes as null). Added the safe-integer check so Go and JS read the same numbers. `concierge/verify.py` crashed on the no-disclosure vector; fixed. Found a bug in the P1-5 CLI while designing the page's multi-file flow: after a file that is not an envelope, the next pack's chain was "not checked" instead of failed as D-017 says; fixed with a test. UI copy: "Sample. SAMPLE." doubled (the signed notice already starts with SAMPLE); status word and text ran together for screen readers.

**Verified.** `node --test web/verify/verify.test.js`: 44 pass (8 manifest cases, 14 extra cases, JCS vectors and must-reject, DER, JWKS and envelope strictness). Go passes the same 14 extra cases. 3 injected JS bugs caught (canonical check off, kid ignored, null disclosure failing). In the browser pane: three pass vectors given out of order were sorted and verified with zero network requests; tampered, wrong-key, duplicate-key and no-disclosure packs render the right verdicts, with contents hidden when the signature fails. Share-link mode against a local mock API: chain checked against `prev_envelope_digest`, only first-party requests, clean console. Headless Chrome print to PDF: 5 pages, light theme, full digest and share URL on every page.

## 2026-10-15 · P1-5 · `cmd/verify`

**Built.** `plumbline-verify --jwks <file|https-url> [--json] pack...`, oldest pack first. Reuses `internal/pack.Verify`; the CLI only reads files, loads keys, chains packs and formats output. Exit codes 0/1/2 (D-017).

**Problems.** The first test run failed on layout (dots one short of `verify.py`'s column) and on a test comparing a sorted-key map with a struct; both fixed, the second in the test. Raw `dsse:` prefixes leaked into user-facing text; stripped. The untrusted-certificate test made `httptest` log a TLS handshake error; its error log now goes to `io.Discard`.

**Verified.** Acceptance: the pass chain prints VERIFIED and exits 0; each fail vector exits 1 and its output marks exactly the manifest's failed checks as FAILED (and no others). `--json` matches the manifest for all 8 cases. Usage errors exit 2 (8 cases). JWKS over https from a test server works; 404, oversized bodies, plain http and untrusted certificates are refused. Ran the built binary by hand on the vectors.

## 2026-10-15 · P1-4 · `internal/pack`

**Built.** Statement and predicate types, `Digest`, `CheckSubjectDigest`, `CheckChain`, `CheckDisclosure`, `Verify`, strict `DecodeStatement`, `ParseJWKS`.

**Problems.** Found a parser differential: `encoding/json` matches keys case-insensitively and lets later duplicates win, while JS reads exact keys, so `prev_pack_digest` plus `PREV_PACK_DIGEST` could read differently. Fixed by requiring the typed statement to re-encode to exactly the signed canonical bytes (D-016). The mutation for this guard first failed to compile (unused variable) and had to be written as a disabled condition. `disclosed: null` is schema-valid but crashes the JS reference; Go reports `merkle: null` (D-016).

**Verified.** All 8 manifest cases match exactly; tampered-payload and wrong-key now differ by subject digest (closing the D-014 gap); typed predicate round-trips byte-identically on all pass vectors; 15 malformed statements and 13 bad JWKS rejected; 4 injected bugs caught.

## 2026-10-15 · P1-3 · `internal/merkle`

**Built.** `Salt`, `LeafHash`, `NodeHash`, `Tree` (all levels kept), `Proof`, `VerifyProof`.

**Problems.** None in the code. Noted that side-based proofs do not bind `leaf_index` or `leaf_count` (D-015). The vector-rebuild test makes the package take ~9 s under `-race`; `-short` skips it.

**Verified.** Every manifest case's merkle result matches; Go rebuilt all three pass-vector trees (318k–401k leaves) from the generator's scheme and matched root, salt and disclosed proof exactly; every leaf of trees of size 1–1,000 proves; 3 injected bugs caught (including duplicating the odd node, the most common Merkle variant).

## 2026-10-15 · P1-2 · `internal/dsse`

**Built.** PAE, strict `Decode`, `Encode`, `Signer` with local P-256 (DER) and Ed25519 implementations, keyid-based `Verify`, `ParsePublicKeyPEM`.

**Problems.** The acceptance test "tampered-payload and wrong-key fail with distinct errors" cannot be met: both carry the trusted keyid and, with one key and one signature, are the same outcome. Both return `ErrBadSignature`; the pack layer separates them (D-014, raised on PR #5).

**Verified.** Pass vectors verify with the PEM key; every manifest case's signature result matches; DSSE spec PAE known answer; 14 error cases; envelope fuzzing (142k execs). A Go-signed copy of KST-CTA-0001 was verified by `concierge/verify.py` and the JS reference, proving the PAE and DER output match other implementations. 3 injected bugs caught.

## 2026-10-14 · P1-1 · `internal/jcs`

**Built.** RFC 8785 `Marshal`, strict `Parse` on `json.Decoder.Token`, `Canonicalize`.

**Problems.** `encoding/json` silently replaces invalid UTF-8 and lone surrogate escapes with U+FFFD, so Parse pre-checks the raw bytes (D-013). The JS reference never exercises `must_reject` (JSON.parse accepts duplicates and lone surrogates), so Go is the first implementation to enforce it. Miscounted one expected string length in a test by hand; the code was right.

**Verified.** 8/8 JCS vectors and 3/3 must-reject; pass payloads re-serialize byte-identically; number formatting matches Node's `String(x)` for 3,119 doubles (generated test data); 3.4M round-trip and 12.5M number fuzz execs; 3 injected bugs caught.

## 2026-10-13 · P0-2 · `internal/spec`

**Built.** Strict loading and cross-checking of `objectives.json`, `framework-refs.json`, `semconv-map.json`, with rule shapes, a ref title cap and presence-only exclusivity (D-012).

**Problems.** A stray `verify` binary from an earlier `go build ./...` was nearly committed; removed and confirmed absent from history.

**Verified.** Real files validate; 17 single-replacement breakages each fail with a message naming file, item and value.

## 2026-10-13 · P0-1 · Repo skeleton

**Built.** `cmd/verify` version stub, folder placeholders, module renamed to the GitHub org path.

**Problems.** The kit pinned Go 1.23 (out of support) and staticcheck 2025.1.1, which cannot read Go 1.27 export data; moved to Go 1.26 minimum, CI on 1.27, tools run via `go run tool@version` (D-011). Local Go is a snap stuck at 1.27.1 with 13 stdlib CVEs; switched to the 1.27.2 toolchain with `GOTOOLCHAIN`. CI's RLS step failed although the database behaved correctly: psql errors go to stderr and the step only captured stdout (fixed with `2>&1`). `make vectors` failed once `internal/` existed without packages; it now checks for packages.

**Verified.** `make check` green locally and in CI; the RLS and append-only SQL tests pass in CI.
