# Decisions

Append-only log. New decisions go at the bottom with the next number. Superseding a decision means adding a new one that names the old.

## D-001 Go standard library, not C++ or Rust (11 Oct 2026)

The agent parses untrusted input inside customers' networks, often banks and insurers. C++ would make that memory-unsafe; CISA and the FBI list new critical-infrastructure products in memory-unsafe languages as a bad practice, and buyers' security reviews ask. Go's standard library covers HTTP, TLS, JSON, SHA-256, ECDSA, Ed25519 and templates, so Go needs fewer third-party modules than Rust (Tokio, Hyper, Rustls, Serde) for this job. Nothing in the product is CPU-bound.

## D-002 One Go binary for the control plane; no web framework, no ORM, no queue library (11 Oct 2026)

`net/http` routing (Go 1.22+), `html/template`, `pgx` with plain SQL, and a Postgres `jobs` table with `FOR UPDATE SKIP LOCKED`. Replaces the earlier TypeScript, Hono, Drizzle and pg-boss plan.

## D-003 Plain HTML, CSS and JavaScript in the browser; no build step (11 Oct 2026)

Server-rendered app pages with `go:embed`; the verify page is one static page with Web Crypto. No React, Vite, Tailwind or npm. JS tests run with `node --test` and no packages.

## D-004 Buy login and AWS request signing (11 Oct 2026)

Supabase Auth for magic links; the AWS SDK for KMS (and CloudWatch for Bedrock). Hand-written versions save a dependency and add the most common sources of security bugs.

## D-005 Aggregates and commitments only leave the customer (11 Oct 2026)

The agent sends counts, ratios and salted Merkle roots. No prompts, completions or raw trace IDs. This keeps us out of customer data, shrinks our SOC 2 scope and removes data-residency objections.

## D-006 Sign the digest with KMS (11 Oct 2026)

KMS `Sign` accepts at most 4,096 bytes and packs are about 12 KB, so we compute SHA-256 of the DSSE PAE locally and call `Sign` with `MessageType=DIGEST`, `ECDSA_SHA_256`, P-256.

## D-007 OPEN: Supabase session verification

Confirm the project signs sessions with asymmetric keys (JWKS) so the server verifies with `crypto/ecdsa` and no JWT library. Owner: build lead, by 29 Nov 2026.

## D-008 OPEN: predicateType domain

Packs use the placeholder `https://lumetrix.example/plumbline/pack/v0.1`. Buy the domain and switch before the first customer pack; regenerate vectors in the same PR.

## D-009 OPEN: Level 2 provider-usage keys

OpenAI and Anthropic usage APIs need organisation admin keys. Decide whether Level 2 reconciliation polls from the customer's agent (key stays with them) or from us. Owner: build lead, by 6 Dec 2026.

## D-010 OTLP JSON only; Bedrock behind a build tag (11 Oct 2026)

The agent accepts OTLP/HTTP with JSON encoding only, avoiding a protobuf dependency. Bedrock usage needs the AWS CloudWatch SDK, so it compiles only with `-tags bedrock`; default agent builds stay standard-library only.

## D-011 Go 1.26 minimum, CI on 1.27; lint tools pinned via `go run` (13 Oct 2026)

The kit pinned Go 1.23, which has been out of support since Aug 2025 (supported releases are now 1.26 and 1.27), so stdlib fixes stop reaching it and `govulncheck` would flag the verifier and agent. staticcheck 2025.1.1 also cannot read Go 1.27 export data, and current staticcheck (2026.2.2) and govulncheck (v1.9.0) need Go 1.26 to build. `go.mod` now says `go 1.26`, CI builds with the latest 1.27.x, and the Makefile runs both tools with `go run tool@version` so the pinned version is always used and `go.mod` gains no tool modules. Revisit at each Go release: keep the minimum at the oldest supported release.

## D-012 Spec checks go beyond references (13 Oct 2026)

`internal/spec` (P0-2) checks what the build plan asks (strict decoding, `maps_to` resolves, rule kinds are the five) and three more things, because each guards a rule the evaluator or CLAUDE.md depends on: (1) each rule kind has fixed required and optional fields (`ratio_at_least`: numerator, denominator, threshold, optional attention_band; `max_value`: metric, threshold, optional attention_band; `zero_count`: metric; `freshness_within`: event, versions_from, max_age, optional min_pass_rate; `all_declared`: observed, declared), so a rule cannot carry a field its kind ignores; (2) framework ref titles are at most 60 characters, a tripwire against pasted ISO/IEC 42001 or other licensed text; (3) in `semconv-map.json` every attribute appears once, and no presence-only (content) attribute can also be mapped to a value field. A new rule kind or field means changing `ruleShapes` in the same PR as the spec.

## D-013 JCS parser built on `json.Decoder.Token`, with I-JSON pre-checks and a depth cap (14 Oct 2026)

`internal/jcs.Parse` walks `json.Decoder.Token()` with `UseNumber()` (as §2.1 says), which keeps syntax checking in the standard library and lets us see every object key to reject duplicates. `encoding/json` silently turns invalid UTF-8 and unpaired `\uD800`–`\uDFFF` escapes into U+FFFD, so Parse first checks `utf8.Valid` and scans string escapes for unpaired surrogates; both fail with `ErrInvalidUnicode`. Numbers that overflow float64 fail with `ErrNonFinite`. Nesting is capped at 1,000 levels (`ErrTooDeep`) because the input is untrusted; packs and batches nest a few levels, so no real pack hits it, but a browser `JSON.parse` would accept such input, so this is the one place the Go verifier may reject what the JS one accepts. `Marshal` accepts only the decoded value types (nil, bool, float64, string, `[]any`, `map[string]any`); Go structs go through `encoding/json` then `Canonicalize`. Number formatting is cross-checked against Node's `String(x)` for 3,119 doubles (`internal/jcs/testdata/es-numbers.json`, generator alongside).

## D-014 DSSE verification: one error for any bad signature; keyid and payload type required (15 Oct 2026)

P1-2 asks that `fail/tampered-payload` and `fail/wrong-key` fail "with distinct errors". Both envelopes carry the trusted keyid; one is the trusted key's signature over other bytes, the other a different key's signature over these bytes. With one public key and one ECDSA signature the two are mathematically the same outcome (the signature does not verify), so `internal/dsse.Verify` returns `ErrBadSignature` for both. The manifest already separates them one layer up: only `tampered-payload` also fails `subject_digest`, which `internal/pack` (P1-4) reports. Failures that can be told apart get their own error: `ErrMalformed`, `ErrNoSignatures`, `ErrPayloadType`, `ErrUnknownKey`, `ErrUnsupportedKey`.

Other choices: `Verify` takes the expected payload type, so a batch envelope can never pass as a pack; it looks keys up by `keyid` (an empty or unknown keyid is `ErrUnknownKey`) and accepts the envelope if any signature from a trusted key verifies; it returns the payload so callers use the verified bytes. `Decode` is strict: unknown fields, trailing data, URL-safe or unpadded base64, and empty payloads are rejected. The reference JS verifier ignores `keyid` and uses the first JWKS key; P1-6 should look keys up by `kid` the same way.

## D-015 Merkle proofs prove membership, not position (15 Oct 2026)

`internal/merkle` keeps every level of the tree (`NewTree`, then `Root` and `Proof(i)` without rehashing; about 2n hashes of memory, 25 MB at 400k leaves) and verifies with `VerifyProof(leaf, proof, root)`, exactly the side-based rule the JS and Python verifiers use. Because levels where a node is carried up contribute no step, the proof does not determine `leaf_index`, and the root does not commit to `leaf_count`. A valid disclosure therefore shows that the trace is in the committed set, not where it sits or how many leaves there are. That is all a pack claims today. If a future check relies on position or count (for example ordering traces in time), every verifier must also check the proof's shape against `leaf_index` and `leaf_count`, which is a format change with new vectors.

## D-016 Pack verification semantics: null disclosure, strict statements, strict JWKS (15 Oct 2026)

`internal/pack.Verify` computes each check on its own (as the reference does), so a pack that fails its signature still reports its subject digest, chain and Merkle results. It returns an error, never `verified`, only when the payload is not a v0.1 pack statement. Three choices every verifier (P1-5, P1-6) must share:

1. **No disclosed trace.** The schema allows `commitments.disclosed: null`. Then `merkle` is `null` (not checked), like `chain` without a previous pack, and does not block `verified`. The JS reference would throw on such a pack; no vector covers it yet. P1-6 should match, and a pass vector with `disclosed: null` should be added when vectors are next generated.
2. **Strict statements.** The payload must be I-JSON (no duplicate keys, valid Unicode), have `_type` in-toto Statement v1, our `predicateType`, exactly one subject with a lowercase-hex sha256, `pack_schema` `assurance-pack/v0.1`, no fields outside the schema, and well-formed digests, salt and proof steps. Because `encoding/json` matches keys case-insensitively and lets later duplicates win, the typed statement must re-encode to exactly the canonical bytes that were signed; otherwise a payload with both `prev_pack_digest` and `PREV_PACK_DIGEST` could read differently in Go and JS. Packs we issue always satisfy this; a hand-made pack with an empty optional field (for example `"acknowledged": ""`) is rejected.
3. **Strict JWKS.** `ParseJWKS` accepts only EC P-256 keys with `kid`, optional `alg: ES256` and `use: sig`, unpadded base64url coordinates on the curve, and no unknown members, so a JWKS with `nbf`, `exp` or a revocation list fails until P4-6 gives those meaning instead of being ignored.

The subject digest is always computed over JCS of the predicate exactly as signed, never over a re-encoded struct.

## D-017 `plumbline-verify` contract: exit codes, JWKS sources, chain across files (15 Oct 2026)

Exit 0 only when every pack verifies; 1 when any pack fails, including a file that decodes as JSON but is not a valid pack; 2 for usage and input errors (bad flags, unreadable files, a JWKS that cannot be loaded or parsed). Scripts can therefore tell "the evidence failed" from "the check could not run". `--jwks` takes a file or an `https://` URL only: fetching the issuer's keys over plain HTTP would let anyone on the path substitute keys and make a forged pack verify. Fetches use a 10 s timeout and a 1 MB cap; pack files are capped at 16 MB. Packs are given oldest first and each is chain-checked against the previous file; if a file is not an envelope, the next pack's chain check fails rather than being skipped. Flags may come before or after file names. Human output follows `concierge/verify.py`'s layout, names every failed check on its line and in the pack's result line, and prints digests in full. `--json` prints one document `{verified, packs[]}` whose per-pack fields match `manifest.json`'s expected results.

## D-018 Every verifier enforces the same strictness, proven by shared extra vectors (15 Oct 2026)

The core manifest covers eight cases; the verifiers could still disagree on malformed packs (a browser's `JSON.parse` keeps the last duplicate key and accepts unpaired surrogates; Go's `encoding/json` matches keys case-insensitively and decodes `null` into an empty value). So:

1. **The payload must be exactly JCS(statement)**, which §2.3 already defines it as. It is the one rule a browser can enforce identically: decode UTF-8 strictly, `JSON.parse`, re-canonicalize and compare, which also exposes duplicate keys. Every pack we issue and every existing vector already satisfies it.
2. **Same types, same rejections.** `web/verify/verify.js` carries a field-by-field table of the Go v0.1 types; required strings may be empty, optional fields must be absent or non-empty, nullable fields must be present, required arrays may not be null. Go adds the missing null-array check. Integers must be safe JavaScript integers (|n| ≤ 2^53−1) in both, so no verifier reads a different number.
3. **Shared extra vectors.** `spec/vectors/make_extra_vectors.py` writes `spec/vectors/extra/` and `extra-manifest.json` (14 cases: a pass with no disclosure, a batch-typed envelope, an unknown kid, and 11 packs that must be refused). Go (`internal/pack`) and the browser verifier both run it; existing vectors are untouched and the reference test is unchanged. `concierge/verify.py` gained the null-disclosure fix with its matching vector.
4. **DER signatures are parsed as strictly as Go** in the browser (short-form lengths, minimal non-negative integers, no trailing bytes), so a re-encoded valid signature cannot pass in one verifier and fail in another.

## D-019 Verify page: render only issuer-signed contents; deploy `web/` as the site root (15 Oct 2026)

`web/verify/` is plain HTML, CSS and ES modules: `verify.js` (pure checks, tested with `node --test`), `page.js` (loading and DOM), `verify.css` (brand tokens only, print stylesheet). Pack contents are rendered only when the issuer's signature verifies; otherwise the page shows the failure and the raw envelope, because unsigned contents cannot be attributed to the issuer. All DOM is built with `createElement`/`textContent`; status classes come from fixed maps, never from payload strings. A strict CSP (`default-src 'none'`, scripts and styles from self only), `no-referrer`, no third-party fonts or scripts. Share links (`?t=` or `#t=`) call `GET {api}/v1/verify/{token}` and `{api}/.well-known/jwks.json`, where `{api}` comes from the `plumbline-api` meta tag (empty means the page's own origin); the chain is checked against `prev_envelope_digest`. Offline mode takes a JWKS file and pack files, orders them by claimed `seq_no` and uses no network. Printing switches to the light theme and repeats the pack ID, full digest and share URL (or source file) on every page. The page links `../brand.css`, so the static site publishes the whole `web/` directory with the page at `/verify/`; the P6-2 open-source split must carry `brand.css` with it.

## D-020 Concierge ingest: Plumbline attribute defaults, per-partner overrides, hourly anomaly rule (15 Oct 2026)

`spec/semconv-map.json` maps provider, models, tokens, operation and region, and lists the content attributes. The objectives also need facts no OTel convention defines yet: personal-data tagging, redaction, guardrail decisions, prompt name and version, high-impact tools and their approval, escalations. C-1 reads them from documented defaults (`plumbline.data_class` = `personal`, `plumbline.redaction.applied`, `plumbline.guardrail.decision` = allow|block|flag on the LLM call span, `plumbline.prompt.name`/`.version`, `plumbline.tool.high_impact`, `plumbline.tool.approved`, `plumbline.escalation`), overridable per design partner with `--attributes partner.json`, because each partner's telemetry will name these differently. Model transitions are tracked per slot (`gen_ai.agent.name`, falling back to `service.name`) so two models used side by side are not read as changes; prompt transitions per prompt name. An hour is an anomaly when it has at least 20 LLM calls and an error rate of at least 5% or a guardrail block rate of at least 3% (all configurable). Only these named attributes are read; every other attribute value, including content, is ignored at parse time, and trace IDs leave only as salted Merkle leaves. Langfuse export fields (`traceId`, `type`, `startTime`, `model`, `usageDetails`, `level`, `promptName`, `promptVersion`, `metadata`; `input`/`output` for presence only) are marked (verify) until checked against a real partner export. P3-1 should adopt these names in `semconv-map.json` (or replace them) rather than invent a second set.

## D-021 Evaluation semantics the objective rules leave open (15 Oct 2026)

`concierge/evaluate.py` (C-2) applies `spec/objectives.json` generically by rule kind; P4-4's Go evaluator must give the same results (parity test). Choices made here:

- **Period**: whole days in the system's timezone, as a half-open UTC interval. Aggregate windows and usage rows count when they start inside it.
- **Bands**: `ratio_at_least` passes at ≥ threshold, is `attention` down to threshold − band, else fails; denominator 0 is `attention`. `max_value` passes at ≤ threshold, `attention` up to threshold + band. `zero_count` and `all_declared` pass or fail.
- **Coverage (EO-LOG-01)**: tokens (input + output), per provider and region. Only providers with usage data count toward the total; a provider with traces but no usage is listed and named in the limitations. Percentages shown in `measured` and `total_pct` are rounded **down**, so a result is never displayed above a threshold it missed.
- **Transitions (EO-CHG-01)**: a model or prompt transition is logged when the change register has an entry of the same kind dated within one day either side (local dates).
- **Anomalies (EO-INC-01)**: an anomaly window is acknowledged when an incident opened within 60 min of the window was acknowledged within 60 min of opening.
- **Eval freshness (EO-EVAL-01)**: live versions are the (model, prompt version) pairs observed in the period, or models alone when no prompt versions are emitted. Each needs a passing eval (pass rate ≥ `min_pass_rate`) that ran before the version was first seen and at most `max_age` before it; versions already live in the first window need only one that ran before. A version without one is `attention` while it has been live less than `max_age` at period end (as pack 3 of the sample shows), `fail` after.
- **Agent safety (EO-AGT-01)**: status follows the rule (tool error rate); escalation share and high-impact approvals are reported in `measured`.
- **Framework maps** render as "framework ref", with an asterisk when the ref is `verified: false`.
- **Input formats** (concierge): `system.json` (declarations), usage CSV `provider,model,region,start,end,input_tokens,output_tokens,requests` (provider IDs as in telemetry; founders convert each provider's export), changes CSV `date,kind,what`, incidents CSV `id,opened,acknowledged,resolved,summary`, evals CSV `suite,model,prompt_version,ran_at,pass_rate`.
- **Schema gap**: eval freshness needs the versions live in each window, which the batch `metrics` schema cannot carry (`additionalProperties: false`). Concierge aggregates put an `observed` record beside `metrics`; P3/P4 need a batch schema change (a new version) before the agent can support EO-EVAL-01.
- `objective_set_hash` is `sha256:` + SHA-256 of JCS(`spec/objectives.json`); the test checks it against the browser verifier's JCS.

## D-022 Concierge signing: schema-checked, self-verified, KMS by digest (15 Oct 2026)

`evidencepack.py sign` (C-3) builds the predicate from `evaluation.json`, commits to the period's leaves and signs. Choices:

- **Signers.** A local P-256 PEM (mode 0600) for pilots and tests, or AWS KMS through boto3 with `MessageType=DIGEST` and the 32-byte SHA-256 of the PAE (D-006). boto3 is imported only for KMS, so the rest of the concierge needs only `cryptography`.
- **Checks before anything is written.** The predicate must pass `spec/pack.schema.json` (a small built-in validator for the keywords the schema uses); the payload must be exactly JCS(statement) (D-018); and the signature is verified with the signer's public key (from KMS `GetPublicKey` for KMS), so a key mix-up never produces a pack.
- **Chain.** `--prev` sets `seq_no` to the previous pack's plus one and `prev_pack_digest` to the SHA-256 of its payload. Pack IDs follow the schema pattern (for example `WRN-RS-0001`).
- **Commitment.** The leaves file must hold exactly the leaves of the evaluated windows (its count must equal the evaluation's `leaf_count`). `--disclose TRACE_ID` reveals one trace: the salt is re-derived from the vendor's secret and the leaf must already be in the commitment, so a wrong trace ID or secret fails instead of producing a proof that cannot verify.
- **Optional fields** are left out rather than written empty (`sample_notice`, a subject `description`, incident `acknowledged`/`resolved`), as the verifiers require. Concierge packs carry `objective_set_hash`; `batch_digests` is for production packs only.
- `--jwks-out` writes the signer's public key as the JWKS the verifiers take.

## D-023 Concierge sharing: a static site in the API's shape; tokens never logged (15 Oct 2026)

`evidencepack.py share` (C-4) writes a static site: the verify page (`web/verify` plus `web/brand.css`), the issuer keys at `/.well-known/jwks.json`, and one file per share at `/v1/verify/<token>` holding exactly what `GET /v1/verify/{token}` will return (`{envelope, prev_envelope_digest, pack_meta}`). The verify page's share-link mode therefore works unchanged, and when P5-1 moves sharing to the server, links keep their form (`<base>/verify/?t=<token>`). Choices:

- **Tokens**: 16 random bytes, base64url (§4.6). The token is printed once for the founders to paste into the CRM; the share log (`share-log.csv`, written beside the site directory, never inside it) records only `sha256(token)`, the pack, its digest and the verifier organisation, because CLAUDE.md forbids logging share tokens.
- **Nothing unverified is published**: `share` checks the signature against the JWKS it publishes, canonical form, subject digest, the chain against `--prev` (required for any pack after the first) and the disclosed proof. A kid already published with a different key is refused.
- **Revoking** deletes the share file (`unshare --token` or `--token-sha256` from the log); the page then reports no pack at the link. Static hosting cannot return 410 or log views; both arrive with P5-1.
- **Framework map labels** (supersedes the format in D-021): "framework ref title" using our own short titles (for example "AIUC-1 E Accountability"), or "framework: title" where the ref adds nothing (APRA letter themes, CPS 230), asterisked when unverified.

## D-024 Build the whole product before trial runs; the M1 gate no longer blocks Phases 3–6 (15 Oct 2026)

Decided by a founder on 15 Oct 2026, to be confirmed by the other founder in review. The plan was to validate demand with concierge pilots before building the platform: Phases 3–6 were blocked until `M1: PASSED` (≥15 qualified calls, ≥40% with a deal blocked in AI review, ≥3 paid pilots at ≥A$3K, ≥60% choosing the telemetry pack). The founders now want a complete, self-serve product to pitch, so trials run on the real product rather than on hand-built packs.

- Phases 3–6 and the new Phase S (D-025) are unblocked, in the order in `build-plan.md`.
- M1's criteria are unchanged and become the goal of the trial runs. The tracker never says `M1: PASSED` until they are actually met.
- The concierge CLI stays usable for early pilots until the platform issues packs; it is frozen (bug fixes with vectors only) from then.
- Risk accepted: building weeks of platform before demand is proven. Mitigations: keep tasks one PR each, ship the SDK and agent early so a design partner can try them before the web app exists, and keep the open-source verifier and agent as the first public artefacts.

Supersedes the gate in CLAUDE.md ("Respect the gate") and in `build-plan.md`.

## D-025 One-line integration: zero-dependency SDKs that feed the local evidence agent (15 Oct 2026)

Adoption must feel like Sentry: install a package, add one line. Chosen model (over an SDK that signs and uploads from inside the app, which is weak in serverless and multi-worker apps and puts signing keys in every process):

- **SDKs** `plumbline` (Python, `sdk/python`) and `@plumbline/sdk` (TypeScript, `sdk/js`). `plumbline.init()` patches the LLM clients the app already uses (OpenAI, Anthropic, AWS Bedrock Runtime; sync, async and streaming) and exports spans as OTLP JSON to the agent at `http://127.0.0.1:4318` (override with `PLUMBLINE_AGENT_URL`). Manual helpers set the facts no client knows: prompt name and version, guardrail decisions, personal-data tags and redaction, tool calls (high impact, approved), escalations, agent runs, using the attribute names of D-020.
- **No content, by construction.** The SDKs record only provider, model, region, token usage, operation, duration, error status and the helper attributes. They never read message arguments or responses beyond the usage block, so there is nothing to drop. Tests prove argument text never reaches an exported span.
- **No dependencies.** Python standard library only; TypeScript uses only `fetch` and Node built-ins, shipped as plain `.js` with `.d.ts` (no bundler, no build step). The SDKs have no runtime dependencies to audit; tests use fakes shaped like the real client libraries, plus the real libraries when installed. This extends the "standard library first" rule to the SDKs; publishing to PyPI and npm is a release step (P6-2).
- **Never break the app.** Instrumentation errors are swallowed and counted; the exporter is a bounded background queue that drops (and counts drops) rather than block a request; `init()` is idempotent and a no-op when disabled (`PLUMBLINE_DISABLED=1`).
- **The agent stays the trust boundary.** It aggregates, salts, signs (Ed25519) and queues batches to one host, exactly as §3 describes, and also accepts spans from any OpenTelemetry SDK or collector. The one-command install (S-5) is a `docker run` line or a static binary with `plumbline-agent connect <token>`; the web app shows both lines with the token filled in.

## D-026 Agent span intake: semconv map v2, embedded spec, JSON-only receiver (15 Oct 2026)

`internal/otlp` (P3-1) is the agent's intake. Choices:

- **semconv map v2.** The D-020 concierge attribute names (`plumbline.data_class`, `plumbline.redaction.applied`, `plumbline.guardrail.decision`, `plumbline.prompt.name` and `.version`, `plumbline.tool.high_impact` and `.approved`, `plumbline.escalation`, and `gen_ai.agent.name` as the slot) now live in `spec/semconv-map.json` as fields, so the agent, the SDKs and the concierge read the same names. `llm.request.type` (OpenLLMetry) is an alias of the operation, and `llm_operations` lists the values that make a span an LLM call. `presence_only_prefixes` (`gen_ai.prompt.`, `gen_ai.completion.`, `llm.prompts.` and others) catches indexed content attributes such as `gen_ai.prompt.0.content`. `internal/spec` refuses a map that would read any attribute under a content prefix.
- **Embedded spec.** Package `spec` (`specdata`) embeds the spec JSON files as immutable strings, so a binary carries exactly the mappings and rules it was tested with.
- **Two guards against content.** Content attributes (exact names or prefixes, on spans and on span events) are counted and never decoded; other unmapped attributes are skipped, and span facts are filled only by lookups of mapped names. Span names, status messages and event names are never read.
- **Receiver.** `POST /v1/traces`, `Content-Type: application/json` only (protobuf gets 415 with a message to switch the exporter to JSON, D-010), optional gzip, 8 MB on the wire and 32 MB after decompression (a gzip-bomb guard), OTLP partial success for spans without a valid trace ID or start time. Error messages are fixed text and never echo the request. Trace IDs are normalised to lower-case hex.

## D-027 Period commitment = Merkle tree over window roots (15 Oct 2026)

The agent commits each hourly window to a salted root and sends only that root and its leaf count; the server never sees leaves. A pack's `trace_merkle_root` (P4-5) is therefore the Merkle root over the period's non-empty window roots, in time order, combined with the ordinary node hash (`sha256(0x01 ‖ left ‖ right)`, odd node carried up), and `leaf_count` is the sum of the windows' leaves. A disclosure proof is the trace's proof to its window root followed by the window root's proof to the period root; every verifier checks it with the existing side/hash rule, unchanged. The agent keeps each window's leaves locally (`internal/commitment`) so a vendor can disclose one trace later; the server supplies the upper half of the proof. Concierge packs, built from all leaves at once, use a flat tree; both forms verify identically, and a proof still shows membership, not position (D-015). The salt secret comes from `PLUMBLINE_SALT_SECRET` or a file readable only by its owner, and must be at least 16 bytes.

## D-028 Provider usage and coverage reconciliation (15 Oct 2026)

`internal/usage` (P3-4) polls billed usage per hour and model: OpenAI (completions and embeddings, admin key), Anthropic (messages usage report, Admin API key), and Amazon Bedrock (CloudWatch `AWS/Bedrock` by `ModelId`, built only with `-tags bedrock`). Rules the evaluator (P4-4) and the SDKs (S-2, S-4) must follow:

- **Tokens are input + output, and input includes cached tokens.** OpenAI's `input_tokens` already includes cached tokens; for Anthropic, billed input is uncached + cache creation (5 min and 1 h) + cache reads. The SDKs therefore record Anthropic input the same way, or coverage would read low for any app that uses prompt caching.
- **Coverage is matched per provider.** OpenAI and Anthropic usage carries no cloud region (Anthropic's `inference_geo` is `global`, `us` or not available), so traced and billed tokens are compared per provider across regions; Bedrock usage is per region. Only providers with usage data count toward total coverage; a provider with traces but no usage is listed (and named in the pack's limitations).
- **Never leak keys.** Errors name the endpoint and HTTP status only; response bodies are discarded on error; keys are never logged.
- Each response is capped at 16 MB and a poll at 50 pages, so a misbehaving API cannot loop the agent.

## D-029 Langfuse puller: same facts as OTLP; content fetched only as presence; evals from scores (15 Oct 2026)

`internal/langfuse` (P3-5) pulls Observations v2 and Scores v3 and maps each observation into an `otlp.Span` through the same semconv mapping (`otlp.Mapping.Build`), so a Langfuse source and an OTLP source aggregate identically.

- **Mapping.** `type` gives the operation (`GENERATION` → chat, `EMBEDDING` → embeddings, `AGENT` → invoke_agent, `TOOL` → execute_tool), `level: ERROR` the error flag, `providedModelName` the model, `inputUsage`/`outputUsage` the tokens, `promptName`/`promptVersion` the prompt. Metadata is read as OpenTelemetry ingestion stores it (`attributes`, `resourceAttributes`) or as flat SDK metadata (`provider`, `region` and attribute names); only mapped names are read. Langfuse trace IDs may be any string up to 256 characters (lower-cased), not only 32-hex.
- **Content.** Langfuse stores prompts and completions, so content-present must be counted from them, as the concierge does. The puller requests the `io` group but decodes `input` and `output` only as raw JSON to test whether they are empty; the text is never parsed, stored or sent. This is the same exposure as the OTLP receiver, whose request bodies also carry content that is dropped at parse.
- **Evals.** Configured score names become eval runs: for each suite, model and prompt version (from the scored observation, or the trace's earliest generation for a trace score), the pass rate is the share of `BOOLEAN` scores that are true and `NUMERIC` scores at or above the configured threshold; other data types are ignored. `ran_at` is the latest score's time. The aggregator files runs into the hour of `ran_at` (late runs into the current hour). To verify with a design partner how they record eval runs.
- **Reliability.** 429 and 5xx are retried up to five times (honouring `Retry-After`, otherwise 1 s doubling, at most 60 s); other errors fail at once with the status only. Pulls stop at 1,000 pages.

## D-030 Agent identity and the enrolment and batch wire formats (15 Oct 2026)

`internal/enrol` and `internal/batchsign` (P3-6) fix what the server's enrol and ingest endpoints (P4-2) must accept:

- **Enrolment.** `POST /v1/agents/enrol` with `Authorization: Bearer <one-time token>` and `{"public_key": "<PEM SubjectPublicKeyInfo, Ed25519>", "hostname": "<display only>"}`; the reply (200 or 201) is `{"agent_id": "agt_<8-40 alphanumerics>"}`. The server URL must be https, except http for localhost. The token is never stored, logged or included in an error.
- **On disk.** The data directory is 0700. `agent.key` is the Ed25519 private key as PKCS#8 PEM, 0600, and the agent refuses to start if anyone else can read it. `agent.json` (agent ID, server) is written last, so its presence means enrolment finished; a failed enrolment leaves no key behind, and enrolling again over an existing identity is refused.
- **Batches.** The payload is the canonical (JCS) `plumbline-batch/v0.2` JSON; the DSSE envelope has payload type `application/vnd.lumetrix.plumbline.batch+json` and one Ed25519 signature whose keyid is the agent ID. `seq` starts at 1 and increases by one; `prev` is null for the first batch, then `sha256:` + hex of the previous payload. A batch's digest (what ingest returns, and what the next `prev` names) is computed the same way. Verification requires the signature by the enrolled key, the canonical form, no unknown fields, schema v0.2 and `agent_id` equal to the signer. Counts must be exact in JSON (at most 2^53).
- **State.** `Build` returns the next chain state but does not persist it; the queue (P3-7) persists it together with the envelope so a crash can neither skip nor reuse a `seq`.

## D-031 Agent outbox: one durable file per batch, delete only when stored (15 Oct 2026)

`internal/queue` (P3-7): each signed batch is written to its own file (`<seq>.batch.json`, the envelope plus the chain state after it) by temp file, fsync, rename and directory fsync, before `state.json` is updated; on start the state is the newer of `state.json` and the newest queued batch, so a crash can neither skip nor reuse a `seq`. Batches are sent in `seq` order to the one configured server and deleted only on 201, or on 200 when the server reports the same digest for a seq it already holds; that is how a batch re-sent after a crash is accepted without being stored twice. Network errors, 429 and 5xx are retried with exponential backoff (1 s doubling, jittered to between half and the full wait, capped at 15 minutes); 400, 401, 409, 413 and a different digest under the same seq stop delivery and keep the batch, because retrying cannot fix them and the operator must see them. The queue directory is 0700 and its files 0600.

## D-032 Agent config, dry-run and replay (15 Oct 2026)

`internal/agent` and `cmd/agent` (P3-8) wire the agent together:

- **Config.** One JSON file (`agent.example.json`), decoded strictly (unknown fields and trailing data are errors, so a misspelt key or a pasted secret fails loudly). It never holds secrets: it names the environment variables (or the salt file) that do, and an enabled source whose variable is unset fails at start with the variable's name, never a value. `PLUMBLINE_SERVER`, `PLUMBLINE_DATA_DIR` and `PLUMBLINE_OTLP_LISTEN` override the file. The server must be https (http only for localhost). The OTLP receiver has no authentication, so it listens on loopback only unless `otlp_allow_remote` is set. `grace_minutes` is 1–55. Register files (change, incident) left the agent config: registers are uploaded in the web app (P5-3), so the agent only sends telemetry aggregates.
- **Commands.** `plumbline-agent enrol` (token from `PLUMBLINE_ENROL_TOKEN`, or `--token`, which would show in the process list), `run` (the default) and `version`. Exit 2 for usage and config errors, 1 for runtime errors.
- **`--dry-run`** prints each batch exactly as it would be sent (the envelope) followed by its decoded payload, and sends, queues and stores nothing; it does not create the data directory. Without an enrolment it signs with an ephemeral key under `agt_dryrun0000000000`, so the output is the real shape.
- **`--replay <file>`** feeds an OTLP JSON export (one request per line, or one document) through the same pipeline, emits every window and exits; with `--dry-run` it is the offline check a customer runs before connecting anything (`make agent-dry`).
- **Leaves.** Each window's salted leaves are kept in `data_dir/leaves/<seq>.json` (0600, directory 0700) so a trace can be disclosed later (D-027); they are hashes, never IDs.
- **Restarts.** On shutdown the agent emits the open window as a partial batch rather than losing it, so after a restart the same hour can arrive in two batches. Windows are additive: the server and evaluator (P4-2, P4-4) must sum batches that share a window, not reject or overwrite them.
- **One HTTP client** for usage, Langfuse and delivery; delivery still goes to the one configured server only.

## D-033 Python SDK semantics, and model history per slot and operation (15 Oct 2026)

`sdk/python` (S-1) implements D-025. Choices the TypeScript SDK (S-3) must match:

- **Scopes, not globals.** Every helper is a context manager and a decorator over `contextvars`, so attributes follow threads and `asyncio` tasks and never leak from one request to the next. A decorated function gets a fresh scope per call. Without `init()` (or with `PLUMBLINE_DISABLED=1`) every helper is a no-op that still runs the app's code unchanged.
- **Traces.** Spans inside one scope share a trace; a span outside any scope gets its own. `plumbline.trace()` groups a unit of work (a web request). The agent commits each trace once (salted), so grouping decides the leaf count, not any metric.
- **Guardrail decisions** apply only to LLM calls (as the agent counts them). LLM spans inside a `guardrail` scope wait for the decision until the scope ends (at most 1,000 per scope; beyond that they go undecided), so output checks work. A `block` with no call inside is recorded as one blocked LLM call (operation `chat`, provider and model from the arguments, else `unknown`, no tokens): an input guardrail that stops a request still counts. An inner decision wins; an outer one fills calls the inner left undecided.
- **What a value can be.** Helper names and versions are strings or integers, capped at 128 characters; anything else is ignored and counted as an instrumentation error. Token counts must be non-negative integers. The SDK never reads messages, arguments, return values or exception text; a randomised test proves none reach an export.
- **Exporter.** A bounded buffer (2,048 spans), batches of 512 or every second, one background daemon thread started lazily (so preforking servers fork first) and restarted clean in a forked child. A full buffer drops the new span and counts it; a failed export drops the batch and counts it (no retries: the agent is local, and evidence of missing data shows up as lower coverage, not as a blocked app). HTTP proxies are never used. `flush()` and `shutdown()` wait with a timeout; `shutdown` runs at exit.
- **Python 3.10+, standard library only**; CI tests 3.10 and 3.13.
- **Shared scenario.** `sdk/testdata/` holds each SDK's export of one scripted app and the windows they must aggregate to in the agent and the concierge (README there).
- **Model history per slot and operation.** The scenario showed that one service calling a chat model and an embeddings model side by side logged a "model transition" on almost every call (the slot was the agent or service name alone), which would fail EO-CHG-01 for most real apps. Both the agent (`internal/aggregate`) and the concierge now track the last model per (slot, operation). Two chat models used side by side in one service still read as transitions; name the agents (`agent_run`, `gen_ai.agent.name`) to separate them. Prompt names were considered as part of the key and rejected: renaming a prompt along with a model change would hide a real transition.

## D-034 Python auto-instrumentation: what is patched and what is read (15 Oct 2026)

`plumbline.init()` (S-2) patches, when installed, the resource classes of `openai` (`chat.completions` create and parse, `responses` create and parse, `embeddings`, legacy `completions`; the libraries' own `stream()` helpers call `create` and are covered), `anthropic` (`messages` and `beta.messages`, `create` and `stream`, which does not go through `create`) and botocore's `BaseClient._make_api_call` for `bedrock-runtime` (Converse, ConverseStream, InvokeModel, InvokeModelWithResponseStream), sync and async. The TypeScript SDK (S-4) follows the same rules:

- **Read only these.** Request: `model` / `modelId`, `stream`, and whether a Bedrock guardrail is configured (the key's presence). Response: model, the usage block, the stop reason, Bedrock's `x-amzn-bedrock-*-token-count` headers, and for streams the event `type` and usage events. Never messages, inputs, outputs, tool arguments, response bodies or error text.
- **Billed input, per provider** (D-028): OpenAI `prompt_tokens` / `input_tokens` (cached tokens are already included); Anthropic uncached + cache writes + cache reads, each field taking its latest value because stream deltas are cumulative and may repeat only some fields; Bedrock `inputTokens`, which excludes cache reads and writes as CloudWatch's `InputTokenCount` does. Cache tokens on Bedrock are therefore compared on neither side for now.
- **Streams** are proxied (iteration, context manager, `close`, attribute access, and `isinstance` still work) and record the call once, when they end, raise, close or leave a `with`. A stream dropped unfinished is queued from its finalizer without taking locks and recorded on the next call or flush. A call keeps the context it started in, so a stream consumed after its scopes have ended still carries their attributes; a guardrail that has already ended stamps its decision at once.
- **OpenAI stream usage is opt-in** (`init(stream_usage=True)` adds `stream_options.include_usage` when the app has not set it, treating the library's omitted-argument sentinels as unset), because it adds a final chunk with no `choices` that some apps would not expect. Without it, streamed chat calls are counted without tokens and coverage reads lower, which is the honest result.
- **Provider names** follow the OTel GenAI values: `openai`, `azure.ai.openai` (Azure clients or `*.openai.azure.com`), `anthropic`, `aws.bedrock` (also `AnthropicBedrock`, with its region), `gcp.vertex_ai`. An OpenAI-compatible endpoint is named by its host, so it is never mistaken for OpenAI usage.
- **Bedrock guardrails.** Converse with `guardrailConfig` is a guarded call: `block` when `stopReason` is `guardrail_intervened`, else `allow`; a decision on the span beats an enclosing `guardrail` scope. InvokeModel reports intervention only inside the body, so it gets no decision. InvokeModelWithResponseStream carries its token counts only inside the streamed body and is counted without tokens.
- **Never break the app.** Patching is idempotent; a disabled SDK passes calls straight through and returns the library's own objects; an instrumentation error is counted and the call is recorded without the missing facts. Tests run fakes with the libraries' real shapes, and the real libraries against a local fake API in CI (`sdk-python-libs`, test-only installs).

## D-035 TypeScript SDK: callback scopes, one CommonJS core (15 Oct 2026)

`sdk/js` (S-3) implements D-025 and D-033 with the same semantics as the Python SDK; the shared scenario's JS export aggregates to the same windows in the agent and the concierge. JavaScript-specific choices:

- **Scopes take a function** (`prompt(name, version, fn)`, `guardrail(decision, fn)`), run it under `AsyncLocalStorage` and return its result. When it returns a promise, the scope ends when the promise settles and a new promise with the same value or rejection is returned (custom promise methods are not preserved; the auto-instrumentation in S-4 returns the library's own objects). Handles (`g`, `t`, `c`) are passed to the function.
- **One instance.** The implementation is CommonJS (`lib/*.cjs`); `lib/index.mjs` re-exports it, so an app that both imports and requires the package gets one buffer and one configuration (no dual-package hazard). Plain JavaScript with a hand-written `index.d.ts`; no build step, no dependencies, Node 18+ (CI tests 18 and 24).
- **Exporter.** Same limits as Python (2,048 spans, batches of 512 or every second, one request in flight, drop and count when full or when the agent fails). Timers are unref'd so the SDK never keeps a process alive; it flushes once on `beforeExit` and installs no signal handlers. Serverless handlers call `await plumbline.flush()`.
- **Times** are nanoseconds as `BigInt` from `performance.timeOrigin + performance.now()`, encoded as decimal strings.

## D-036 TypeScript auto-instrumentation: patch create, keep the library's objects (15 Oct 2026)

`init()` (S-4) follows D-034's rules on what is read and how billed input is counted. Node-specific choices:

- **Where to patch.** openai: `create` on `OpenAI.Chat.Completions`, `OpenAI.Responses`, `OpenAI.Embeddings`, `OpenAI.Completions` (in Node, `parse()`, `stream()` and `runTools()` call `create`, so patching them too would count calls twice; Python's `parse` posts directly, so there both are patched). @anthropic-ai/sdk: `create` on `Anthropic.Messages` and `Anthropic.Beta.Messages` (`messages.stream()` calls `create(...).withResponse()`). Bedrock: `send` on `BedrockRuntimeClient.prototype` (an own property over the inherited smithy `send`, so other AWS clients are untouched); command type by `instanceof` the module's command classes; InvokeModel's token headers through a per-command `deserialize` middleware, since the SDK does not expose response headers in the output. Callback-style `send` passes through.
- **Return the library's own objects.** Stainless clients return a lazily parsed `APIPromise`; the wrapper returns `apiPromise._thenUnwrap(observe)` (the library's own mechanism for its helpers), so `withResponse()`, `asResponse()` and lazy parsing keep working and nothing is parsed before the app asks. Failures are observed on the request's `responsePromise` without consuming the body. Streams keep their identity and class: only the instance's `Symbol.asyncIterator` is wrapped (a `Proxy` would break their `#private` fields). A stream dropped unfinished is finished from a `FinalizationRegistry` callback via the deferred queue.
- **Both module copies.** The libraries ship separate CommonJS and ES module builds, which are separate class instances. The finder resolves each library from the app's directory (`process.cwd()`, then the SDK's own location), patches the CommonJS copy synchronously in `init()`, and imports the ES module entry named by the package's `exports["."].import` (or `module`) to patch that copy; `plumbline.ready()` resolves when both are done. `plumbline.instrument({...})` instruments what an app passes, for bundlers and strict package managers.
- **Opt-in OpenAI stream usage** copies the request body before adding `stream_options`; the app's object is never changed.
- **Tests**: fake packages written into a temporary app (CommonJS and ES module copies of each, with Stainless's `APIPromise`, `#private` stream fields and a smithy middleware stack), plus the real libraries against a local fake API in CI (`sdk-js-libs`, test-only installs).
